package streamer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"frappe-monitor/internal/logs"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
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

	// ScriptPath is the absolute path on each bench where the
	// frappe-monitor-stream.sh script lives. Phase 8b.1 expects the
	// operator (or a separate deploy step) to drop the script in
	// place; auto-deploy from the monitor lives in 8b.2.
	ScriptPath string `koanf:"script_path"`

	// Files is the global list of (id, path) pairs. Today every
	// streamed server tails the same set; per-server overrides are a
	// post-v1 backlog item.
	Files []FileSpecConfig `koanf:"files"`

	// FlushIntervalSeconds is the LokiSink flush cadence. Default 1.
	FlushIntervalSeconds int `koanf:"flush_interval_seconds"`

	// MaxBatchLines forces a flush when any per-stream buffer
	// reaches this many entries. Default 500.
	MaxBatchLines int `koanf:"max_batch_lines"`

	// PushTimeoutSeconds caps each Loki / VM push. Default 10.
	PushTimeoutSeconds int `koanf:"push_timeout_seconds"`

	// MinBackoffSeconds / MaxBackoffSeconds shape the per-session
	// reconnect cadence. Defaults 1 / 60.
	MinBackoffSeconds int `koanf:"min_backoff_seconds"`
	MaxBackoffSeconds int `koanf:"max_backoff_seconds"`
}

// FileSpecConfig is the YAML-friendly form of FileSpec.
type FileSpecConfig struct {
	ID   string `koanf:"id"`
	Path string `koanf:"path"`
}

// Validate returns an error if the streamer config is unusable. When
// disabled, no validation runs — defaults are fine.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	if strings.TrimSpace(c.ScriptPath) == "" {
		return fmt.Errorf("streaming.script_path is required when streaming.enabled is true")
	}
	if len(c.Files) == 0 {
		return fmt.Errorf("streaming.files must contain at least one entry when streaming.enabled is true")
	}
	for i, f := range c.Files {
		if strings.TrimSpace(f.ID) == "" {
			return fmt.Errorf("streaming.files[%d].id is required", i)
		}
		if strings.TrimSpace(f.Path) == "" {
			return fmt.Errorf("streaming.files[%d].path is required", i)
		}
	}
	return nil
}

// Manager owns one Session per streaming-enabled server. Lifecycle:
// Start enumerates servers, spawns sessions, returns; Stop signals
// every session and waits for them to finish.
type Manager struct {
	cfg     Config
	exec    sshpkg.Executor
	store   storage.Store
	loki    LokiPusher
	vm      VMPusher
	logger  *slog.Logger

	sink     *LokiSink
	sessions map[int]*Session

	mu       sync.Mutex
	stopped  bool
	flushDone chan struct{}
	flushTick *time.Ticker
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

	// Build the path resolver up-front from the config — every
	// server tails the same files in 8b.1, so the resolver is just
	// a static map lookup.
	pathByID := map[string]string{}
	for _, f := range cfg.Files {
		pathByID[f.ID] = f.Path
	}
	resolver := func(_ int, fileID string) (string, bool) {
		p, ok := pathByID[fileID]
		return p, ok
	}

	sink := NewLokiSink(SinkConfig{
		MonitorID:     cfg.MonitorID,
		FlushInterval: flushInterval,
		MaxBatchLines: maxBatch,
		PushTimeout:   pushTimeout,
	}, loki, vm, store, resolver, logger)

	return &Manager{
		cfg:       cfg,
		exec:      exec,
		store:     store,
		loki:      loki,
		vm:        vm,
		logger:    logger,
		sink:      sink,
		sessions:  map[int]*Session{},
		flushDone: make(chan struct{}),
	}, nil
}

// Start opens a Session per server returned by store.ListServers and
// kicks them off. Returns once every session has been launched (the
// sessions themselves run forever in goroutines). The flush ticker
// also starts here.
func (m *Manager) Start(ctx context.Context) error {
	servers, err := m.store.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("streamer: list servers: %w", err)
	}

	files := make([]FileSpec, 0, len(m.cfg.Files))
	for _, f := range m.cfg.Files {
		files = append(files, FileSpec{ID: f.ID, Path: f.Path})
	}

	launched := 0
	for _, s := range servers {
		if err := m.launchOne(ctx, s.ID, files); err != nil {
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
	go m.flushLoop(ctx)

	m.logger.Info("streamer: manager started",
		"servers_launched", launched,
		"servers_total", len(servers),
		"flush_interval_seconds", m.cfg.FlushIntervalSeconds,
		"max_batch_lines", m.cfg.MaxBatchLines)
	return nil
}

// LaunchServer adds a session for a newly-created server. Used by
// the server-CRUD hot-add path. Idempotent: re-launching an
// already-running server is a no-op.
func (m *Manager) LaunchServer(ctx context.Context, serverID int) error {
	files := make([]FileSpec, 0, len(m.cfg.Files))
	for _, f := range m.cfg.Files {
		files = append(files, FileSpec{ID: f.ID, Path: f.Path})
	}
	return m.launchOne(ctx, serverID, files)
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
	if ok {
		sess.Stop(5 * time.Second)
		m.logger.Info("streamer: removed session", "server_id", serverID)
	}
}

func (m *Manager) launchOne(ctx context.Context, serverID int, files []FileSpec) error {
	m.mu.Lock()
	if _, exists := m.sessions[serverID]; exists {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	srv, err := m.store.GetServer(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get server: %w", err)
	}

	// Resolve resume offsets fresh from the cursor store on EVERY
	// connect attempt — including reconnects — so a flap doesn't replay
	// every line ingested since the session first started. The sink
	// advances these cursors as it flushes to Loki, so each reconnect
	// resumes from the last durably-shipped byte.
	buildCmd := func(ctx context.Context) (string, error) {
		resume := map[string]int64{}
		for _, f := range files {
			if cur, err := m.store.GetLogCursor(ctx, serverID, f.Path); err == nil && cur != nil {
				resume[f.ID] = cur.ByteOffset
			}
		}
		return BuildRemoteCommand(m.cfg.ScriptPath, files, resume)
	}

	// Build once up front to fail fast on bad config (script path /
	// file ids) before we spawn the session goroutine.
	if _, err := buildCmd(ctx); err != nil {
		return fmt.Errorf("build remote command: %w", err)
	}

	cfg := SessionConfig{
		ServerID: serverID,
		Target: sshpkg.Target{
			Host: srv.Hostname, Port: srv.SSHPort,
			User: srv.SSHUser, KeyPath: srv.SSHKeyPath,
		},
		Files:        files,
		BuildCommand: buildCmd,
		MinBackoff:   time.Duration(m.cfg.MinBackoffSeconds) * time.Second,
		MaxBackoff:   time.Duration(m.cfg.MaxBackoffSeconds) * time.Second,
	}

	sess := NewSession(cfg, m.exec, m.sink, m.logger)
	sess.Run(ctx)

	m.mu.Lock()
	m.sessions[serverID] = sess
	m.mu.Unlock()
	m.logger.Info("streamer: launched session",
		"server_id", serverID, "host", srv.Hostname, "files", len(files))
	return nil
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
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.sessions = nil
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
