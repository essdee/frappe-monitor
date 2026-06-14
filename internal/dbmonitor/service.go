package dbmonitor

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"frappe-monitor/internal/realtime"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// MetricsPusher writes Influx line-protocol to VictoriaMetrics.
// metrics.VMClient implements it (same interface as the collector's).
type MetricsPusher interface {
	Push(ctx context.Context, body string) error
}

// Service periodically checks every enabled DB target's replication
// health, persists the result, pushes metrics to VM, and broadcasts live
// updates over WebSocket. It runs as its own goroutine.
type Service struct {
	store   storage.Store
	checker *Checker
	vm      MetricsPusher
	hub     realtime.Broadcaster
	logger  *slog.Logger

	interval    time.Duration
	maxParallel int
	cmdTimeout  time.Duration

	stop chan struct{}
	wg   sync.WaitGroup
	once sync.Once
}

// Config tunes the dbmonitor Service. Any zero field falls back to the default,
// so Config{} preserves the historical behavior.
type Config struct {
	Interval    time.Duration // sweep cadence (default 60s)
	MaxParallel int           // concurrent checks per sweep (default 4)
	CmdTimeout  time.Duration // per status query (default 15s)
}

// New builds a dbmonitor Service. hub may be nil (no live push); vm may be
// nil (no metrics). Zero fields in cfg take sensible defaults.
func New(store storage.Store, exec sshpkg.Executor, vm MetricsPusher, hub realtime.Broadcaster, logger *slog.Logger, cfg Config) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 60 * time.Second
	}
	if cfg.MaxParallel <= 0 {
		cfg.MaxParallel = 4
	}
	if cfg.CmdTimeout <= 0 {
		cfg.CmdTimeout = 15 * time.Second
	}
	return &Service{
		store:       store,
		checker:     &Checker{Exec: exec},
		vm:          vm,
		hub:         hub,
		logger:      logger,
		interval:    cfg.Interval,
		maxParallel: cfg.MaxParallel,
		cmdTimeout:  cfg.CmdTimeout,
		stop:        make(chan struct{}),
	}
}

// Start launches the check loop (non-blocking). It runs an immediate
// sweep, then every interval. Cheap and idle when there are no targets.
func (s *Service) Start(ctx context.Context) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.checkAll(ctx)
		t := time.NewTicker(s.interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-t.C:
				s.checkAll(ctx)
			}
		}
	}()
}

// Stop signals the loop and waits for it. Idempotent.
func (s *Service) Stop() {
	s.once.Do(func() { close(s.stop) })
	s.wg.Wait()
}

// CheckOnce runs a single immediate check of one target — used by the
// API's "check now" endpoint. Returns the persisted status.
func (s *Service) CheckOnce(ctx context.Context, id int) (*storage.DBTarget, error) {
	t, err := s.store.GetDBTarget(ctx, id)
	if err != nil {
		return nil, err
	}
	s.checkOne(ctx, t, nil, nil)
	return s.store.GetDBTarget(ctx, id)
}

func (s *Service) checkAll(ctx context.Context) {
	targets, err := s.store.ListDBTargets(ctx)
	if err != nil {
		s.logger.Warn("dbmonitor: list targets failed", "err", err)
		return
	}
	sem := make(chan struct{}, s.maxParallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var body strings.Builder

	for _, t := range targets {
		if !t.Enabled {
			continue
		}
		wg.Add(1)
		go func(t *storage.DBTarget) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			s.checkOne(ctx, t, &mu, &body)
		}(t)
	}
	wg.Wait()

	if s.vm != nil {
		if b := body.String(); b != "" {
			pctx, cancel := context.WithTimeout(ctx, s.cmdTimeout)
			if err := s.vm.Push(pctx, b); err != nil {
				s.logger.Error("dbmonitor: vm push failed", "err", err)
			}
			cancel()
		}
	}
}

// checkOne checks a single target, persists its status, appends its
// metrics to body (when body!=nil), and broadcasts a live update.
func (s *Service) checkOne(ctx context.Context, t *storage.DBTarget, mu *sync.Mutex, body *strings.Builder) {
	srv, err := s.store.GetServer(ctx, t.ServerID)
	if err != nil {
		st := storage.DBTargetStatus{Status: "unreachable", LastError: "server not found: " + err.Error()}
		_ = s.store.SetDBTargetStatus(ctx, t.ID, st)
		s.broadcast(t, "", st)
		return
	}

	tgt := sshpkg.Target{Host: srv.Hostname, Port: srv.SSHPort, User: srv.SSHUser, KeyPath: srv.SSHKeyPath}
	cctx, cancel := context.WithTimeout(ctx, s.cmdTimeout)
	res := s.checker.Check(cctx, tgt, t)
	cancel()

	status, lastErr := deriveStatus(res, t.LagThresholdSeconds)
	st := storage.DBTargetStatus{
		Status:              status,
		IORunning:           res.IORunning,
		SQLRunning:          res.SQLRunning,
		LagSeconds:          res.LagSeconds,
		HeartbeatLagSeconds: res.HeartbeatLagSeconds,
		LastError:           lastErr,
	}
	if err := s.store.SetDBTargetStatus(ctx, t.ID, st); err != nil {
		s.logger.Warn("dbmonitor: set status failed", "id", t.ID, "err", err)
	}

	if body != nil {
		// Stamp at the moment THIS target was checked, not a single
		// sweep-start time — a slow multi-target sweep would otherwise
		// bunch every point at the start instant.
		line := buildMetrics(t.Name, srv.Name, res, status, time.Now())
		mu.Lock()
		body.WriteString(line)
		mu.Unlock()
	}
	s.broadcast(t, srv.Name, st)
	s.logger.Info("dbmonitor: checked", "db", t.Name, "server", srv.Name, "status", status)
}

func (s *Service) broadcast(t *storage.DBTarget, serverName string, st storage.DBTargetStatus) {
	if s.hub == nil {
		return
	}
	s.hub.Broadcast(realtime.Event{
		Type:  realtime.TypeDBStatus,
		Topic: realtime.TopicDatabases(),
		Data:  dbStatusData(t, serverName, st),
	})
}

// dbStatusData is the JSON the dashboard merges into its DB list.
func dbStatusData(t *storage.DBTarget, serverName string, st storage.DBTargetStatus) map[string]any {
	d := map[string]any{
		"id":              t.ID,
		"server_id":       t.ServerID,
		"server":          serverName,
		"name":            t.Name,
		"status":          st.Status,
		"io_running":      st.IORunning,
		"sql_running":     st.SQLRunning,
		"last_error":      st.LastError,
		"last_checked_at": time.Now().UTC().Format(time.RFC3339),
	}
	if st.LagSeconds != nil {
		d["lag_seconds"] = *st.LagSeconds
	}
	if st.HeartbeatLagSeconds != nil {
		d["heartbeat_lag_seconds"] = *st.HeartbeatLagSeconds
	}
	return d
}

// buildMetrics emits one target's replication metrics in Influx line
// protocol (nanosecond timestamp shared across the sweep).
func buildMetrics(dbName, serverName string, res Result, status string, now time.Time) string {
	ts := now.UnixNano()
	tags := fmt.Sprintf("db=%s,server=%s", escapeTag(dbName), escapeTag(serverName))
	var b strings.Builder
	line := func(metric string, val string) {
		b.WriteString(fmt.Sprintf("%s,%s value=%s %d\n", metric, tags, val, ts))
	}
	line("frappe_db_up", b01(res.Reachable))
	line("frappe_db_replication_io_running", b01(res.IORunning))
	line("frappe_db_replication_sql_running", b01(res.SQLRunning))
	line("frappe_db_replication_healthy", b01(status == "healthy"))
	// 1 when lag exceeds THIS target's own threshold — lets a single alert
	// rule fire on per-target thresholds without encoding them in PromQL.
	line("frappe_db_replication_lagging", b01(status == "lagging"))
	if res.LagSeconds != nil {
		line("frappe_db_replication_lag_seconds", strconv.FormatInt(*res.LagSeconds, 10))
	}
	if res.HeartbeatLagSeconds != nil {
		line("frappe_db_heartbeat_lag_seconds", strconv.FormatFloat(*res.HeartbeatLagSeconds, 'f', -1, 64))
	}
	return b.String()
}

func b01(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

var tagEscaper = strings.NewReplacer(`\`, `\\`, `,`, `\,`, `=`, `\=`, ` `, `\ `, "\n", `\n`, "\r", `\r`)

func escapeTag(s string) string { return tagEscaper.Replace(s) }
