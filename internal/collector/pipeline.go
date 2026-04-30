// Package collector glues the per-server pull together: SSH run the
// collector script, tokenize + convert its output, push to VictoriaMetrics,
// and update the server's status row in storage.
//
// The scheduler invokes one Pipeline.PullOnce per server per tick; each
// invocation owns its own ctx so Stop or per-job timeouts cut across the
// whole pipeline.
package collector

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"frappe-monitor/internal/parser"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// Pusher abstracts the metrics-backend write so the pipeline can be tested
// without standing up VictoriaMetrics. metrics.VMClient implements it.
type Pusher interface {
	Push(ctx context.Context, body string) error
}

// Pipeline owns the per-server pull. The exec, push, and store
// dependencies are all interfaces so tests can swap fakes in.
type Pipeline struct {
	Store  storage.Store
	Exec   sshpkg.Executor
	Push   Pusher
	Logger *slog.Logger
}

// CollectorPath is the location of the deployed bash collector on each
// bench server. Phase 2 hardcodes this; Phase 3+ may move it to config.
const CollectorPath = "/usr/local/bin/frappe-monitor-collect.sh"

// PullOnce runs one full cycle for the server with the given id:
//
//	store.GetServer  → ssh.Run(CollectorPath)
//	→ parser.Tokenize → parser.ServerFromSections
//	→ metrics.ServerMetrics.LineProtocol → push.Push
//	→ store.SetServerStatus("reachable" | "unreachable", err)
//
// Status persistence rules:
//   - SSH errors and parse errors mark the server "unreachable" with the
//     wrapped error in last_error (the prefix indicates which stage failed:
//     "ssh:", "parse:").
//   - Push (VM) errors are logged but do NOT mark the server unreachable
//     — the server probe succeeded; the metrics backend is the failure
//     domain. Server status is still updated to "reachable".
//   - All paths return the underlying error (or nil) so the scheduler can
//     log + count outcomes.
func (p *Pipeline) PullOnce(ctx context.Context, serverID int) error {
	srv, err := p.Store.GetServer(ctx, serverID)
	if err != nil {
		// Don't write status; if we can't even find the row, there's nothing to update.
		return fmt.Errorf("collector: get server %d: %w", serverID, err)
	}

	target := sshpkg.Target{
		Host: srv.Hostname, Port: srv.SSHPort, User: srv.SSHUser, KeyPath: srv.SSHKeyPath,
	}
	stdout, runErr := p.Exec.Run(ctx, target, CollectorPath)
	if runErr != nil {
		wrapped := fmt.Errorf("ssh: %w", runErr)
		_ = p.markUnreachable(ctx, serverID, wrapped)
		return wrapped
	}

	out, parseErr := parser.Tokenize(stdout)
	if parseErr != nil {
		wrapped := fmt.Errorf("parse: %w", parseErr)
		_ = p.markUnreachable(ctx, serverID, wrapped)
		return wrapped
	}

	m, convErr := parser.ServerFromSections(out)
	if convErr != nil {
		wrapped := fmt.Errorf("parse: %w", convErr)
		_ = p.markUnreachable(ctx, serverID, wrapped)
		return wrapped
	}

	// Defense against torn upstream output: a real Linux host always has
	// at least `/` mounted and one non-`lo` interface. Empty here means
	// the collector emitted a structurally valid but semantically empty
	// section (e.g. df returned nothing). Treat as parse failure.
	if len(m.Disks) == 0 || len(m.Net) == 0 {
		wrapped := fmt.Errorf("parse: empty disks or net (Disks=%d Net=%d)",
			len(m.Disks), len(m.Net))
		_ = p.markUnreachable(ctx, serverID, wrapped)
		return wrapped
	}

	// Phase 3: build the consolidated line-protocol body across the full
	// hierarchy. Server section is required (we already validated it
	// above); per-bench / per-site parse failures are logged but don't
	// fail the whole cycle — a single misbehaving site shouldn't drop
	// the rest of the pull.
	var body strings.Builder
	body.WriteString(m.LineProtocol(srv.Name))

	benchOK, benchErr := 0, 0
	for _, benchName := range parser.BenchNames(out) {
		bm, err := parser.BenchFromSections(out, benchName)
		if err != nil {
			benchErr++
			p.Logger.Warn("collector: bench parse failed (skipped)",
				"server_id", serverID, "bench", benchName, "err", err)
			continue
		}
		bm.Timestamp = m.Timestamp
		bm.Server = srv.Name
		body.WriteString(bm.LineProtocol(srv.Name))
		benchOK++
	}

	siteOK, siteErr := 0, 0
	for _, pair := range parser.SiteNamesFor(out) {
		benchName, siteName := pair[0], pair[1]
		sm, err := parser.SiteFromSections(out, benchName, siteName)
		if err != nil {
			siteErr++
			p.Logger.Warn("collector: site parse failed (skipped)",
				"server_id", serverID, "bench", benchName, "site", siteName, "err", err)
			continue
		}
		sm.Timestamp = m.Timestamp
		sm.Server = srv.Name
		body.WriteString(sm.LineProtocol(srv.Name))
		siteOK++
	}

	bodyStr := body.String()
	if pushErr := p.Push.Push(ctx, bodyStr); pushErr != nil {
		// Server is reachable; the metrics backend failed. Mark reachable
		// (the probe succeeded) and log the push error loudly so operators
		// know to look at VM, not the bench server.
		p.Logger.Error("collector: vm push failed",
			"server_id", serverID,
			"server_name", srv.Name,
			"err", pushErr)
		if err := p.Store.SetServerStatus(ctx, serverID, "reachable", ""); err != nil {
			p.Logger.Error("collector: set status reachable", "server_id", serverID, "err", err)
		}
		return fmt.Errorf("push: %w", pushErr)
	}

	if err := p.Store.SetServerStatus(ctx, serverID, "reachable", ""); err != nil {
		p.Logger.Error("collector: set status reachable", "server_id", serverID, "err", err)
		return fmt.Errorf("status: %w", err)
	}
	p.Logger.Info("collector: pull ok",
		"server_id", serverID,
		"server_name", srv.Name,
		"body_lines", strings.Count(bodyStr, "\n"),
		"benches_ok", benchOK,
		"benches_err", benchErr,
		"sites_ok", siteOK,
		"sites_err", siteErr)
	return nil
}

// markUnreachable updates the server's status row, capping the recorded
// last_error so a multi-MB SSH stderr doesn't bloat the SQLite row.
func (p *Pipeline) markUnreachable(ctx context.Context, serverID int, cause error) error {
	const maxErrLen = 4096
	msg := cause.Error()
	if len(msg) > maxErrLen {
		msg = msg[:maxErrLen]
	}
	if err := p.Store.SetServerStatus(ctx, serverID, "unreachable", msg); err != nil {
		p.Logger.Error("collector: set status unreachable",
			"server_id", serverID, "err", err)
		return err
	}
	return nil
}
