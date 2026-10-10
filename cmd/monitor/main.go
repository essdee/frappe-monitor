package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"frappe-monitor/internal/alerts"
	"frappe-monitor/internal/api"
	"frappe-monitor/internal/collector"
	"frappe-monitor/internal/config"
	"frappe-monitor/internal/logs"
	"frappe-monitor/internal/metrics"
	"frappe-monitor/internal/probes"
	"frappe-monitor/internal/scheduler"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
	"frappe-monitor/internal/streamer"
	"frappe-monitor/scripts"
)

func main() {
	cfgPath := flag.String("config", "./config/monitor.yaml", "path to config yaml")
	checkConfig := flag.Bool("check-config", false, "validate configuration and exit without starting services")
	flag.Parse()

	if *checkConfig {
		if err := checkConfiguration(*cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
		fmt.Println("configuration valid")
		return
	}
	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func checkConfiguration(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	_, _, err = runtimeConfigs(cfg)
	return err
}

// runtimeConfigs builds and validates every subsystem configuration before run
// creates a database, starts the scheduler, or opens any network connection.
// The installer uses the same path through --check-config, preventing an
// invalid first-run prompt answer from installing a restart-looping service.
func runtimeConfigs(cfg *config.Config) (alerts.Config, streamer.Config, error) {
	alertsCfg := alerts.Config{
		Enabled:                   cfg.Alerts.Enabled,
		EvaluationIntervalSeconds: cfg.Alerts.EvaluationIntervalSeconds,
		NotifyRepeatSeconds:       cfg.Alerts.NotifyRepeatSeconds,
		VMQueryTimeoutSeconds:     cfg.Alerts.VMQueryTimeoutSeconds,
		Telegram: alerts.TelegramConfig{
			BotToken:           cfg.Alerts.Telegram.BotToken,
			ChatIDs:            cfg.Alerts.Telegram.ChatIDs,
			SendTimeoutSeconds: cfg.Alerts.Telegram.SendTimeoutSeconds,
		},
		DisableDefaults: cfg.Alerts.DisableDefaults,
		Timezone:        cfg.Display.Timezone,
	}
	for _, rule := range cfg.Alerts.Rules {
		alertsCfg.Rules = append(alertsCfg.Rules, alerts.Rule{
			Name:              rule.Name,
			Expr:              rule.Expr,
			Severity:          rule.Severity,
			Message:           rule.Message,
			FingerprintLabels: rule.FingerprintLabels,
		})
	}
	if err := alertsCfg.Validate(); err != nil {
		return alerts.Config{}, streamer.Config{}, fmt.Errorf("alerts: %w", err)
	}

	streamCfg := streamer.Config{
		Enabled:              cfg.Streaming.Enabled,
		MonitorID:            cfg.Streaming.MonitorID,
		ScriptPath:           cfg.Streaming.ScriptPath,
		FlushIntervalSeconds: cfg.Streaming.FlushIntervalSeconds,
		MaxBatchLines:        cfg.Streaming.MaxBatchLines,
		MaxBufferedLines:     cfg.Streaming.MaxBufferedLines,
		MaxBufferedBytes:     cfg.Streaming.MaxBufferedBytes,
		PushTimeoutSeconds:   cfg.Streaming.PushTimeoutSeconds,
		MinBackoffSeconds:    cfg.Streaming.MinBackoffSeconds,
		MaxBackoffSeconds:    cfg.Streaming.MaxBackoffSeconds,
	}
	for _, file := range cfg.Streaming.Files {
		streamCfg.Files = append(streamCfg.Files, streamer.FileSpecConfig{
			ServerID: file.ServerID, ID: file.ID, Path: file.Path,
			LogType: file.LogType, Bench: file.Bench, Site: file.Site,
		})
	}
	if err := streamCfg.Validate(); err != nil {
		return alerts.Config{}, streamer.Config{}, fmt.Errorf("streamer: %w", err)
	}
	return alertsCfg, streamCfg, nil
}

func run(cfgPath string) error {
	startedAt := time.Now().UTC()
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	alertsCfg, streamCfg, err := runtimeConfigs(cfg)
	if err != nil {
		return err
	}

	logger := newLogger(cfg.Log.Level, cfg.Log.Format)

	if err := os.MkdirAll(filepath.Dir(cfg.Database.Path), 0o750); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}

	// Signal-aware root context: cancellation here propagates to the HTTP
	// server's BaseContext, which is the parent of every request context,
	// which is what handlers pass to sshpkg.Ping. So SIGTERM cancels
	// in-flight SSH probes too. The scheduler also uses its own internal
	// parent context — we cancel it explicitly via Stop below.
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	store, err := storage.OpenEntStore(ctx, buildSQLiteDSN(cfg.Database.Path))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			logger.Error("store close", "err", err)
		}
	}()

	pool := sshpkg.NewPool(sshpkg.PoolConfig{
		DialTimeout:        time.Duration(cfg.SSH.DialTimeoutSeconds) * time.Second,
		CommandTimeout:     time.Duration(cfg.SSH.CommandTimeoutSeconds) * time.Second,
		KnownHostsPath:     cfg.SSH.KnownHostsPath,
		MaxSessionsPerHost: cfg.SSH.MaxConnectionsPerHost,
	})
	defer pool.Close()

	// Phase 2: metrics push + scheduler.
	vmClient := metrics.NewVMClient(
		cfg.Metrics.VMURL,
		time.Duration(cfg.Metrics.PushTimeoutSeconds)*time.Second,
	)
	pipeline := &collector.Pipeline{
		Store:  store,
		Exec:   pool,
		Push:   vmClient,
		Public: probes.NewPublicProber(cfg.PublicProbes),
		Logger: logger,
	}
	metricsLookback := 3*time.Duration(cfg.Scheduler.DefaultIntervalSeconds)*time.Second +
		time.Duration(cfg.Scheduler.PerJobTimeoutSeconds)*time.Second
	if metricsLookback < 5*time.Minute {
		metricsLookback = 5 * time.Minute
	}
	sched := scheduler.New(
		cfg.Scheduler.MaxParallel,
		time.Duration(cfg.Scheduler.PerJobTimeoutSeconds)*time.Second,
		logger,
	)
	if err := registerScheduledPulls(ctx, sched, store, pipeline, cfg.Scheduler.DefaultIntervalSeconds, logger); err != nil {
		return fmt.Errorf("register pulls: %w", err)
	}
	sched.Start()
	logger.Info("scheduler started",
		"default_interval_seconds", cfg.Scheduler.DefaultIntervalSeconds,
		"max_parallel", cfg.Scheduler.MaxParallel)

	// Phase 6: alerts service. New() returns nil when alerts.enabled=false,
	// which the rest of the wiring treats as "no-op" — no goroutine, no
	// telegram traffic, no extra rows in SQLite. Validation surfaces
	// missing telegram config etc. before we ever start.
	alertsCfg.QueryLookback = metricsLookback
	alertsSvc, err := alerts.New(alertsCfg, cfg.Metrics.VMURL, store, logger)
	if err != nil {
		return fmt.Errorf("alerts: %w", err)
	}
	if alertsSvc != nil {
		alertsSvc.Start(ctx)
		logger.Info("alerts service started",
			"interval_seconds", cfg.Alerts.EvaluationIntervalSeconds,
			"chat_ids", len(cfg.Alerts.Telegram.ChatIDs),
			"rule_count_default", len(alerts.DefaultRules()),
			"rule_count_user", len(cfg.Alerts.Rules))
	}

	// Phase 8: streamer (per-event capture). Disabled by default;
	// when enabled, opens a long-lived SSH session per server to
	// tail Frappe + MariaDB logs and push them to Loki. Health
	// metrics (frappe_stream_connected, frappe_stream_lag_seconds)
	// land in VM. NewManager returns nil + nil when disabled.
	lokiClient := logs.NewLokiClient(
		cfg.Logs.LokiURL,
		time.Duration(cfg.Logs.PushTimeoutSeconds)*time.Second,
	)
	streamMgr, err := streamer.NewManager(streamCfg, pool, store, lokiClient, vmClient, logger)
	if err != nil {
		return fmt.Errorf("streamer: %w", err)
	}
	if streamMgr != nil {
		if err := streamMgr.Start(ctx); err != nil {
			return fmt.Errorf("streamer start: %w", err)
		}
		logger.Info("streamer manager started",
			"configured_file_count", len(cfg.Streaming.Files),
			"auto_discovery", len(cfg.Streaming.Files) == 0)
	}
	streamStatus := func() api.StreamRuntimeStatus {
		status := api.StreamRuntimeStatus{
			Enabled:          cfg.Streaming.Enabled,
			MaxBufferedLines: cfg.Streaming.MaxBufferedLines,
			MaxBufferedBytes: cfg.Streaming.MaxBufferedBytes,
		}
		if streamMgr == nil {
			return status
		}
		stats := streamMgr.Stats()
		status.Sessions = stats.Sessions
		status.Connected = stats.Connected
		status.Launching = stats.Launching
		status.BufferedLines = stats.BufferedLines
		status.BufferedBytes = stats.BufferedBytes
		status.MaxBufferedLines = stats.MaxBufferedLines
		status.MaxBufferedBytes = stats.MaxBufferedBytes
		return status
	}

	// Lifecycle hooks: when a server is added/removed via the API,
	// register/deregister its scheduler entry so it picks up (or
	// stops) on the next tick — no process restart required.
	scheduleSpec := fmt.Sprintf("@every %ds", cfg.Scheduler.DefaultIntervalSeconds)
	jobForServer := func(serverID int) scheduler.Job {
		return scheduler.Job{
			ServerID: serverID,
			Run: func(ctx context.Context) error {
				return pipeline.PullOnce(ctx, serverID)
			},
		}
	}
	onServerCreated := func(serverID int) {
		job := jobForServer(serverID)
		err := sched.AddKeyed(serverID, scheduleSpec, job)
		if err != nil {
			logger.Warn("scheduler: hot-add failed", "server_id", serverID, "err", err)
			return
		}
		logger.Info("scheduler: hot-added server", "server_id", serverID, "spec", scheduleSpec)
		sched.RunNow(job)

		// Phase 8: hot-launch the streamer for the new server. No-op
		// when streaming is disabled (streamMgr is nil).
		if streamMgr != nil {
			go func() {
				launchCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if err := streamMgr.LaunchServer(launchCtx, serverID); err != nil {
					logger.Warn("streamer: hot-launch failed", "server_id", serverID, "err", err)
				}
			}()
		}

		// Best-effort: capture a system snapshot in the background.
		// Failures land in last_error on the SystemSnapshot row; the
		// dashboard's refresh button can retry. Don't block server
		// creation on this.
		go func() {
			snapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			srv, err := store.GetServer(snapCtx, serverID)
			if err != nil {
				logger.Warn("snapshot: bootstrap get-server failed", "server_id", serverID, "err", err)
				return
			}
			tgt := sshpkg.Target{
				Host: srv.Hostname, Port: srv.SSHPort,
				User: srv.SSHUser, KeyPath: srv.SSHKeyPath,
			}
			out, runErr := pool.RunWithInput(snapCtx, tgt, "bash -s", scripts.SystemScript)
			snap := storage.SystemSnapshot{ServerID: serverID}
			if runErr != nil {
				snap.LastError = runErr.Error()
			} else {
				snap.Payload = []byte(out)
			}
			if err := store.UpsertSystemSnapshot(snapCtx, snap); err != nil {
				logger.Warn("snapshot: bootstrap upsert failed", "server_id", serverID, "err", err)
				return
			}
			logger.Info("snapshot: bootstrap captured", "server_id", serverID,
				"ssh_failed", runErr != nil)
		}()
	}
	onServerDeleted := func(serverID int) {
		sched.RemoveKeyed(serverID)
		if streamMgr != nil {
			streamMgr.RemoveServer(serverID)
		}
		logger.Info("scheduler: hot-removed server", "server_id", serverID)
	}
	onServerUpdated := func(serverID int) {
		if streamMgr == nil {
			return
		}
		go func() {
			streamMgr.RemoveServer(serverID)
			launchCtx, launchCancel := context.WithTimeout(ctx, 10*time.Second)
			defer launchCancel()
			if err := streamMgr.LaunchServer(launchCtx, serverID); err != nil {
				logger.Warn("streamer: restart after server update failed", "server_id", serverID, "err", err)
			}
		}()
	}

	router := api.NewRouter(api.Deps{
		Store:      store,
		Executor:   pool,
		Logger:     logger,
		ConfigPath: cfgPath,
		Config:     cfg,

		// Phase 4: query proxies for the dashboard.
		MetricsBaseURL:      cfg.Metrics.VMURL,
		LogsBaseURL:         cfg.Logs.LokiURL,
		MetricsQueryTimeout: time.Duration(cfg.Metrics.QueryTimeoutSeconds) * time.Second,
		MetricsLookback:     metricsLookback,
		LogsQueryTimeout:    time.Duration(cfg.Logs.QueryTimeoutSeconds) * time.Second,

		// Single-tenant cookie auth with HTTP basic compatibility for API clients.
		AuthPassword:      cfg.Auth.Password,
		AuthRealm:         cfg.Auth.Realm,
		DisplayTimezone:   cfg.Display.Timezone,
		TrustedProxyCIDRs: cfg.Server.TrustedProxyCIDRs,

		// Hot register/deregister scheduler entries on server CRUD.
		OnServerCreated: onServerCreated,
		OnServerUpdated: onServerUpdated,
		OnServerDeleted: onServerDeleted,

		// Surface effective rules even while delivery is disabled so the
		// monitoring UI can review the policy before Telegram is enabled.
		AlertsRules: func() []alerts.Rule {
			if alertsSvc != nil {
				return alertsSvc.Rules()
			}
			return alerts.EffectiveRules(alertsCfg)
		}(),
		AlertsEnabled: cfg.Alerts.Enabled,
		StartedAt:     startedAt,
		StreamStatus:  streamStatus,
	})

	srv := &http.Server{
		Addr:              cfg.Server.ListenAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       time.Duration(cfg.Server.ReadTimeoutSeconds) * time.Second,
		WriteTimeout:      time.Duration(cfg.Server.WriteTimeoutSeconds) * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    1 << 20,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	listener, err := net.Listen("tcp", cfg.Server.ListenAddr)
	if err != nil {
		// Background services have already started by this point. Cancel
		// their shared work and drain them before returning the bind error.
		cancel()
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = sched.Stop(stopCtx)
		if alertsSvc != nil {
			alertsSvc.Stop()
		}
		if streamMgr != nil {
			streamMgr.Stop(5 * time.Second)
		}
		return fmt.Errorf("http listen %s: %w", cfg.Server.ListenAddr, err)
	}
	logger.Info("http listening", "addr", listener.Addr().String())

	serverErr := make(chan error, 1)
	go func() {
		err := srv.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-serverErr:
		if err != nil {
			return err
		}
		return nil
	}

	// Stop the scheduler FIRST so in-flight pulls get a chance to finish
	// before we tear down dependent resources (store, pool, HTTP server).
	// Scheduler jobs go SSH→VM directly — they don't flow through the
	// HTTP server — but they DO use the store and pool, which are
	// closed via deferreds when run() returns.
	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := sched.Stop(shutdownCtx); err != nil {
		logger.Error("scheduler stop", "err", err)
	}
	if alertsSvc != nil {
		alertsSvc.Stop()
	}
	if streamMgr != nil {
		// Stop the streamer BEFORE shutting down the store/pool —
		// the manager's final flush touches both. 5s timeout matches
		// the per-session shutdown grace period.
		streamMgr.Stop(5 * time.Second)
	}
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

// registerScheduledPulls lists every server in the store and adds a
// scheduler entry per server using the global default interval. API lifecycle
// hooks add and remove keyed jobs after boot, so the registry stays live
// without a process restart.
func registerScheduledPulls(
	ctx context.Context,
	sched *scheduler.Scheduler,
	store storage.Store,
	pipeline *collector.Pipeline,
	defaultIntervalSeconds int,
	logger *slog.Logger,
) error {
	servers, err := store.ListServers(ctx)
	if err != nil {
		return err
	}
	spec := fmt.Sprintf("@every %ds", defaultIntervalSeconds)
	for _, s := range servers {
		serverID := s.ID // capture per iteration
		// AddKeyed (not plain Add) so a later API delete can remove
		// this entry without process restart. Boot-time and runtime
		// scheduling end up sharing the same registry.
		job := scheduler.Job{
			ServerID: serverID,
			Run: func(ctx context.Context) error {
				return pipeline.PullOnce(ctx, serverID)
			},
		}
		err := sched.AddKeyed(serverID, spec, job)
		if err != nil {
			return fmt.Errorf("schedule server %d: %w", serverID, err)
		}
		sched.RunNow(job)
	}
	logger.Info("scheduled pulls registered",
		"server_count", len(servers),
		"spec", spec)
	return nil
}

func newLogger(level, format string) *slog.Logger {
	var lvl slog.Level
	switch level {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	if format == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	} else {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}

// buildSQLiteDSN constructs the modernc.org/sqlite DSN with the PRAGMAs
// recommended in master plan §4. Note foreign_keys(on) — required because
// any future cascading deletes (servers → benches → sites) depend on
// foreign-key enforcement, and the test setup in OpenEntStore uses
// foreign_keys(1) which is equivalent.
func buildSQLiteDSN(path string) string {
	v := url.Values{}
	v.Add("_pragma", "journal_mode(wal)")
	v.Add("_pragma", "synchronous(normal)")
	v.Add("_pragma", "busy_timeout(5000)")
	v.Add("_pragma", "foreign_keys(on)")
	v.Add("_pragma", "temp_store(memory)")
	v.Add("_pragma", "mmap_size(134217728)")
	v.Add("_pragma", "cache_size(-64000)")
	return "file:" + path + "?" + v.Encode()
}
