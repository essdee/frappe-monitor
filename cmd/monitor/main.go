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

	"frappe-monitor/internal/api"
	"frappe-monitor/internal/config"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

func main() {
	cfgPath := flag.String("config", "./config/monitor.yaml", "path to config yaml")
	flag.Parse()

	if err := run(*cfgPath); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	logger := newLogger(cfg.Log.Level, cfg.Log.Format)

	if err := os.MkdirAll(filepath.Dir(cfg.Database.Path), 0o750); err != nil {
		return fmt.Errorf("mkdir data dir: %w", err)
	}

	// Signal-aware root context: cancellation here propagates to the HTTP
	// server's BaseContext, which is the parent of every request context,
	// which is what handlers pass to sshpkg.Ping. So SIGTERM cancels
	// in-flight SSH probes too.
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
		DialTimeout:    time.Duration(cfg.SSH.DialTimeoutSeconds) * time.Second,
		CommandTimeout: time.Duration(cfg.SSH.CommandTimeoutSeconds) * time.Second,
	})
	defer pool.Close()

	router := api.NewRouter(api.Deps{
		Store:    store,
		Executor: pool,
		Logger:   logger,
	})

	srv := &http.Server{
		Addr:         cfg.Server.ListenAddr,
		Handler:      router,
		ReadTimeout:  time.Duration(cfg.Server.ReadTimeoutSeconds) * time.Second,
		WriteTimeout: time.Duration(cfg.Server.WriteTimeoutSeconds) * time.Second,
		BaseContext:  func(net.Listener) context.Context { return ctx },
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("http listening", "addr", cfg.Server.ListenAddr)
		err := srv.ListenAndServe()
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

	shutdownCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
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
