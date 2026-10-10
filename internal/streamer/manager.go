package streamer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"frappe-monitor/internal/logs"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
	"frappe-monitor/scripts"
)

// Config is the operator-facing settings for the streamer subsystem.
// Mirrors cfg.streaming.* in monitor.yaml.
type Config struct {
	// Enabled gates the whole subsystem. When false, no goroutines
	// run and no SSH sessions are opened. Default off — operators
	// must opt in.
	Enabled bool `koanf:"enabled"`

	// MonitorID surfaces in Loki labels and metrics so multi-monitor
	// deployments don't collide. Free-form short string; empty is OK.
	MonitorID string `koanf:"monitor_id"`

	// ScriptPath is retained for configuration compatibility but ignored by
	// production. The embedded streamer is executed through SSH stdin.
	ScriptPath string `koanf:"script_path"`

	// Files optionally overrides read-only standard-path discovery. Entries
	// with server_id 0 apply to every server; positive IDs target one row.
	Files []FileSpecConfig `koanf:"files"`

	// FlushIntervalSeconds is the LokiSink flush cadence. Default 1.
	FlushIntervalSeconds int `koanf:"flush_interval_seconds"`

	// MaxBatchLines forces a flush when any per-stream buffer
	// reaches this many entries. Default 500.
	MaxBatchLines int `koanf:"max_batch_lines"`
	// Global outage bounds across all discovered files.
	MaxBufferedLines int   `koanf:"max_buffered_lines"`
	MaxBufferedBytes int64 `koanf:"max_buffered_bytes"`

	// PushTimeoutSeconds caps each Loki / VM push. Default 10.
	PushTimeoutSeconds int `koanf:"push_timeout_seconds"`

	// MinBackoffSeconds / MaxBackoffSeconds shape the per-session
	// reconnect cadence. Defaults 1 / 60.
	MinBackoffSeconds int `koanf:"min_backoff_seconds"`
	MaxBackoffSeconds int `koanf:"max_backoff_seconds"`
}

// FileSpecConfig is the YAML-friendly form of FileSpec.
type FileSpecConfig struct {
	ServerID int    `koanf:"server_id"`
	ID       string `koanf:"id"`
	Path     string `koanf:"path"`
	LogType  string `koanf:"log_type"`
	Bench    string `koanf:"bench"`
	Site     string `koanf:"site"`
}

var safeFileID = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Validate returns an error if the streamer config is unusable. When
// disabled, no validation runs — defaults are fine.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	for i, f := range c.Files {
		if f.ServerID < 0 {
			return fmt.Errorf("streaming.files[%d].server_id must be >= 0", i)
		}
		if strings.TrimSpace(f.ID) == "" {
			return fmt.Errorf("streaming.files[%d].id is required", i)
		}
		if strings.TrimSpace(f.Path) == "" {
			return fmt.Errorf("streaming.files[%d].path is required", i)
		}
		if !safeFileID.MatchString(f.ID) {
			return fmt.Errorf("streaming.files[%d].id %q must contain only letters, digits, underscore, or hyphen", i, f.ID)
		}
		if strings.ContainsAny(f.Path, "'\r\n\x00") {
			return fmt.Errorf("streaming.files[%d].path contains a character unsafe for the remote shell", i)
		}
		if strings.ContainsAny(f.LogType, "\r\n\x00") || strings.ContainsAny(f.Bench, "\r\n\x00") || strings.ContainsAny(f.Site, "\r\n\x00") {
			return fmt.Errorf("streaming.files[%d] labels contain an invalid control character", i)
		}
		if f.Site != "" && f.Bench == "" {
			return fmt.Errorf("streaming.files[%d].bench is required when site is set", i)
		}
	}
	for i, f := range c.Files {
		for j := 0; j < i; j++ {
			other := c.Files[j]
			// Two entries overlap when either is global or both target the
			// same server. IDs and paths only need to be unique inside the
			// set actually sent to one remote process.
			overlap := f.ServerID == 0 || other.ServerID == 0 || f.ServerID == other.ServerID
			if !overlap {
				continue
			}
			if f.ID == other.ID {
				return fmt.Errorf("streaming.files[%d].id %q conflicts with entry %d", i, f.ID, j)
			}
			if f.Path == other.Path {
				return fmt.Errorf("streaming.files[%d].path %q conflicts with entry %d", i, f.Path, j)
			}
		}
	}
	return nil
}

// Manager owns one Session per streaming-enabled server. Lifecycle:
// Start enumerates servers, spawns sessions, returns; Stop signals
// every session and waits for them to finish.
type Manager struct {
	cfg    Config
	exec   sshpkg.Executor
	store  storage.Store
	loki   LokiPusher
	vm     VMPusher
	logger *slog.Logger

	sink      *LokiSink
	sessions  map[int]*Session
	launching map[int]struct{}

	mu           sync.Mutex
	stopped      bool
	runCtx       context.Context
	runCancel    context.CancelFunc
	flushDone    chan struct{}
	flushTick    *time.Ticker
	serverLabels sync.Map // server id -> operator-facing server name
	discovered   sync.Map // server id -> []FileSpec found through read-only SSH
}

// ManagerStats is safe to expose through the local authenticated status API.
// It intentionally contains only counts and configured memory bounds.
type ManagerStats struct {
	Sessions         int
	Connected        int
	Launching        int
	BufferedLines    int
	BufferedBytes    int64
	MaxBufferedLines int
	MaxBufferedBytes int64
}

func (m *Manager) Stats() ManagerStats {
	m.mu.Lock()
	sessions := len(m.sessions)
	launching := len(m.launching)
	m.mu.Unlock()
	sink := m.sink.Stats()
	return ManagerStats{
		Sessions: sessions, Connected: sink.Connected, Launching: launching,
		BufferedLines: sink.BufferedLines, BufferedBytes: sink.BufferedBytes,
		MaxBufferedLines: m.cfg.MaxBufferedLines, MaxBufferedBytes: m.cfg.MaxBufferedBytes,
	}
}

// NewManager constructs the Manager. Returns nil + nil when
// streaming is disabled — callers treat nil as "no-op".
func NewManager(
	cfg Config,
	exec sshpkg.Executor,
	store storage.Store,
	loki LokiPusher,
	vm VMPusher,
	logger *slog.Logger,
) (*Manager, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if !cfg.Enabled {
		return nil, nil
	}

	flushInterval := time.Duration(cfg.FlushIntervalSeconds) * time.Second
	if cfg.FlushIntervalSeconds <= 0 {
		flushInterval = 1 * time.Second
	}
	pushTimeout := time.Duration(cfg.PushTimeoutSeconds) * time.Second
	if cfg.PushTimeoutSeconds <= 0 {
		pushTimeout = 10 * time.Second
	}
	maxBatch := cfg.MaxBatchLines
	if maxBatch <= 0 {
		maxBatch = 500
	}

	m := &Manager{
		cfg:       cfg,
		exec:      exec,
		store:     store,
		loki:      loki,
		vm:        vm,
		logger:    logger,
		sessions:  map[int]*Session{},
		launching: map[int]struct{}{},
		flushDone: make(chan struct{}),
	}
	resolver := func(serverID int, fileID string) (string, bool) {
		file, ok := m.lookupFile(serverID, fileID)
		return file.Path, ok
	}
	sink := NewLokiSink(SinkConfig{
		MonitorID:        cfg.MonitorID,
		FlushInterval:    flushInterval,
		MaxBatchLines:    maxBatch,
		MaxBufferedLines: cfg.MaxBufferedLines,
		MaxBufferedBytes: cfg.MaxBufferedBytes,
		PushTimeout:      pushTimeout,
		LabelResolver: func(serverID int, fileID string) map[string]string {
			out := map[string]string{"server_id": serverIDLabel(serverID)}
			if name, ok := m.serverLabels.Load(serverID); ok {
				out["server"] = name.(string)
			} else {
				out["server"] = serverIDLabel(serverID)
			}
			if f, ok := m.lookupFile(serverID, fileID); ok {
				logType := strings.TrimSpace(f.LogType)
				if logType == "" {
					logType = f.ID
				}
				out["log_type"] = logType
				if f.Bench != "" {
					out["bench"] = f.Bench
				}
				if f.Site != "" {
					out["site"] = f.Site
				}
			}
			return out
		},
	}, loki, vm, store, resolver, logger)
	m.sink = sink
	return m, nil
}

// Start opens a Session per server returned by store.ListServers and
// kicks them off. Returns once every session has been launched (the
// sessions themselves run forever in goroutines). The flush ticker
// also starts here.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.runCtx != nil {
		m.mu.Unlock()
		return errors.New("streamer: manager already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	m.runCtx = runCtx
	m.runCancel = cancel
	m.mu.Unlock()

	servers, err := m.store.ListServers(runCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("streamer: list servers: %w", err)
	}

	launched := 0
	for _, s := range servers {
		if err := m.launchOne(runCtx, s.ID); err != nil {
			m.logger.Warn("streamer: failed to launch session",
				"server_id", s.ID, "err", err)
			continue
		}
		launched++
	}

	// Periodic flush tick. The session's OnLine path also flushes on
	// MaxBatchLines, but the ticker covers slow-volume servers that
	// would otherwise hold lines for minutes.
	m.flushTick = time.NewTicker(time.Duration(m.cfg.FlushIntervalSeconds) * time.Second)
	if m.cfg.FlushIntervalSeconds <= 0 {
		m.flushTick = time.NewTicker(1 * time.Second)
	}
	go m.flushLoop(runCtx)

	m.logger.Info("streamer: manager started",
		"servers_launched", launched,
		"servers_total", len(servers),
		"flush_interval_seconds", m.cfg.FlushIntervalSeconds,
		"max_batch_lines", m.cfg.MaxBatchLines,
		"max_buffered_lines", m.cfg.MaxBufferedLines,
		"max_buffered_bytes", m.cfg.MaxBufferedBytes)
	return nil
}

// LaunchServer adds a session for a newly-created server. Used by
// the server-CRUD hot-add path. Idempotent: re-launching an
// already-running server is a no-op.
func (m *Manager) LaunchServer(ctx context.Context, serverID int) error {
	return m.launchOne(ctx, serverID)
}

// RemoveServer signals a server's session to stop. Used by the
// server-CRUD hot-delete path.
func (m *Manager) RemoveServer(serverID int) {
	m.mu.Lock()
	sess, ok := m.sessions[serverID]
	if ok {
		delete(m.sessions, serverID)
	}
	m.mu.Unlock()
	m.serverLabels.Delete(serverID)
	m.discovered.Delete(serverID)
	if ok {
		sess.Stop(5 * time.Second)
		m.logger.Info("streamer: removed session", "server_id", serverID)
	}
}

func (m *Manager) launchOne(ctx context.Context, serverID int) error {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return errors.New("streamer: manager stopped")
	}
	if _, exists := m.sessions[serverID]; exists {
		m.mu.Unlock()
		return nil
	}
	if _, exists := m.launching[serverID]; exists {
		m.mu.Unlock()
		return nil
	}
	runCtx := m.runCtx
	if runCtx == nil {
		m.mu.Unlock()
		return errors.New("streamer: manager not started")
	}
	m.launching[serverID] = struct{}{}
	m.mu.Unlock()
	launched := false
	defer func() {
		if launched {
			return
		}
		m.mu.Lock()
		delete(m.launching, serverID)
		m.mu.Unlock()
	}()

	srv, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}
	files := filesForServer(m.cfg.Files, serverID)
	if len(files) == 0 {
		files, err = m.discoverFiles(ctx, srv)
		if err != nil {
			return fmt.Errorf("discover logs: %w", err)
		}
		if len(files) == 0 {
			return fmt.Errorf("no readable Frappe log files discovered for server %d", serverID)
		}
	}
	m.discovered.Store(serverID, files)
	m.serverLabels.Store(serverID, srv.Name)

	// Validate the static part at launch. The resume positions themselves
	// are rebuilt before every connection attempt below.
	_, err = BuildInlineCommand(files, nil)
	if err != nil {
		return fmt.Errorf("build remote command: %w", err)
	}

	cfg := SessionConfig{
		ServerID: serverID,
		Target: sshpkg.Target{
			Host: srv.Hostname, Port: srv.SSHPort,
			User: srv.SSHUser, KeyPath: srv.SSHKeyPath,
		},
		Files: files,
		Stdin: scripts.StreamerScript,
		CommandBuilder: func(commandCtx context.Context) (string, error) {
			resume, err := m.sink.ResumePositions(commandCtx, serverID, files)
			if err != nil {
				return "", err
			}
			return BuildInlineCommand(files, resume)
		},
		MinBackoff: time.Duration(m.cfg.MinBackoffSeconds) * time.Second,
		MaxBackoff: time.Duration(m.cfg.MaxBackoffSeconds) * time.Second,
	}

	sess := NewSession(cfg, m.exec, m.sink, m.logger)

	m.mu.Lock()
	if m.stopped {
		delete(m.launching, serverID)
		m.mu.Unlock()
		return errors.New("streamer: manager stopped")
	}
	delete(m.launching, serverID)
	m.sessions[serverID] = sess
	m.mu.Unlock()
	launched = true
	sess.Run(runCtx)
	m.logger.Info("streamer: launched session",
		"server_id", serverID, "host", srv.Hostname, "files", len(files))
	return nil
}

func (m *Manager) discoverFiles(ctx context.Context, srv *storage.Server) ([]FileSpec, error) {
	cmd := "bash -s"
	if len(srv.BenchPaths) > 0 {
		joined := strings.Join(srv.BenchPaths, ":")
		escaped := "'" + strings.ReplaceAll(joined, "'", `'\''`) + "'"
		cmd = "BENCH_PATHS=" + escaped + " bash -s"
	}
	out, err := m.exec.RunWithInput(ctx, sshpkg.Target{Host: srv.Hostname, Port: srv.SSHPort, User: srv.SSHUser, KeyPath: srv.SSHKeyPath}, cmd, scripts.LogDiscoveryScript)
	if err != nil {
		return nil, err
	}
	files := make([]FileSpec, 0)
	seenID, seenPath := map[string]bool{}, map[string]bool{}
	for lineNo, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 5 {
			return nil, fmt.Errorf("line %d: invalid discovery record", lineNo+1)
		}
		file := FileSpec{ID: parts[0], Path: parts[1], LogType: parts[2], Bench: parts[3], Site: parts[4]}
		if !safeFileID.MatchString(file.ID) || !strings.HasPrefix(file.Path, "/") {
			return nil, fmt.Errorf("line %d: unsafe file identity", lineNo+1)
		}
		if seenID[file.ID] || seenPath[file.Path] {
			continue
		}
		seenID[file.ID], seenPath[file.Path] = true, true
		files = append(files, file)
	}
	return files, nil
}

func (m *Manager) lookupFile(serverID int, fileID string) (FileSpec, bool) {
	for _, file := range filesForServer(m.cfg.Files, serverID) {
		if file.ID == fileID {
			return file, true
		}
	}
	if value, ok := m.discovered.Load(serverID); ok {
		for _, file := range value.([]FileSpec) {
			if file.ID == fileID {
				return file, true
			}
		}
	}
	return FileSpec{}, false
}

func filesForServer(configured []FileSpecConfig, serverID int) []FileSpec {
	files := make([]FileSpec, 0, len(configured))
	for _, f := range configured {
		if f.ServerID != 0 && f.ServerID != serverID {
			continue
		}
		files = append(files, FileSpec{
			ID: f.ID, Path: f.Path, LogType: f.LogType, Bench: f.Bench, Site: f.Site,
		})
	}
	return files
}

func matchingFileConfig(configured []FileSpecConfig, serverID int, fileID string) (FileSpecConfig, bool) {
	for _, f := range configured {
		if f.ID == fileID && (f.ServerID == 0 || f.ServerID == serverID) {
			return f, true
		}
	}
	return FileSpecConfig{}, false
}

// flushLoop drives periodic Sink.Flush. Runs until Stop is called or
// ctx is canceled. We don't return errors — Flush itself logs and
// the loop must keep ticking.
func (m *Manager) flushLoop(ctx context.Context) {
	defer close(m.flushDone)
	defer m.flushTick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.flushTick.C:
			_ = m.sink.Flush(ctx)
		}
	}
}

// Stop signals every session to wind down, waits up to timeout for
// each, and does a final flush. Idempotent.
func (m *Manager) Stop(timeout time.Duration) {
	m.mu.Lock()
	if m.stopped {
		m.mu.Unlock()
		return
	}
	m.stopped = true
	if m.runCancel != nil {
		m.runCancel()
	}
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = map[int]*Session{}
	m.mu.Unlock()

	deadline := time.Now().Add(timeout)
	for _, s := range sessions {
		remain := time.Until(deadline)
		if remain < 100*time.Millisecond {
			remain = 100 * time.Millisecond
		}
		if !s.Stop(remain) {
			m.logger.Warn("streamer: session did not stop in time")
		}
	}

	if m.flushTick != nil {
		// Drain the ticker and run one last flush so anything queued
		// makes it to Loki + the cursor table.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = m.sink.Flush(ctx)
		cancel()
	}
	m.logger.Info("streamer: manager stopped")
}

// suppress unused-import noise on slim builds where logs.Stream
// might not be referenced through a code path we kept.
var _ = logs.Stream{}
