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

	"frappe-monitor/internal/logs"
	"frappe-monitor/internal/parser"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// Pusher abstracts the metrics-backend write so the pipeline can be tested
// without standing up VictoriaMetrics. metrics.VMClient implements it.
type Pusher interface {
	Push(ctx context.Context, body string) error
}

// LogPusher abstracts the Loki-side write so the pipeline can be tested
// without standing up Loki. logs.LokiClient implements it.
type LogPusher interface {
	Push(ctx context.Context, streams []logs.Stream) error
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
// bench server. Lives in the SSH user's home directory so deploy +
// invoke don't need root. The literal "$HOME" survives interpolation:
// every shell we exec under (bash, dash, zsh) expands it for the
// remote user before the collector is invoked.
const CollectorPath = "$HOME/.frappe-monitor/frappe-monitor-collect.sh"

// buildCollectCmd returns the shell command to run the collector,
// optionally with BENCH_PATHS preset so the bash side skips its
// auto-discovery and uses the operator-supplied paths instead. When
// benchPaths is empty, the bash side falls back to scanning standard
// locations.
//
// Paths are joined with ':' (matching the bash collector's parser)
// and shell-escaped via single-quote wrapping so paths with spaces
// or quotes don't break the command line.
func buildCollectCmd(benchPaths []string) string {
	if len(benchPaths) == 0 {
		return CollectorPath
	}
	joined := strings.Join(benchPaths, ":")
	// Single-quote escape: ' → '\'' (close, escape, reopen).
	escaped := "'" + strings.ReplaceAll(joined, "'", `'\''`) + "'"
	return "BENCH_PATHS=" + escaped + " " + CollectorPath
}

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
	stdout, runErr := p.Exec.Run(ctx, target, buildCollectCmd(srv.BenchPaths))
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

	// If the collector's trap fired, ERROR section carries exit_code +
	// line. Surface it loud — generic "missing META" is useless when
	// the operator wants to know the bash script's actual failure.
	if errSec, ok := out.Section("ERROR"); ok {
		exitCode := errSec.KVs["exit_code"]
		line := errSec.KVs["line"]
		wrapped := fmt.Errorf("collector script error (exit=%s line=%s)", exitCode, line)
		_ = p.markUnreachable(ctx, serverID, wrapped)
		return wrapped
	}

	m, convErr := parser.ServerFromSections(out)
	if convErr != nil {
		wrapped := fmt.Errorf("parse: %w", convErr)
		_ = p.markUnreachable(ctx, serverID, wrapped)
		return wrapped
	}

	// Defense against torn upstream output: a real Linux host always
	// has at least `/` mounted and one non-`lo` interface. Treating
	// either-empty as a fatal parse error was too aggressive — minimal
	// containers / overlay-only mounts and hosts with all interfaces
	// filtered (LXC profiles) trip it even when CPU + memory are
	// perfectly populated, so the dashboard would mark a fully healthy
	// server "unreachable". Only fail when BOTH are empty (real torn
	// output); log a warning otherwise so operators see degraded
	// inventory without losing reachability state.
	if len(m.Disks) == 0 && len(m.Net) == 0 {
		wrapped := fmt.Errorf("parse: empty disks AND net — collector output looks torn")
		_ = p.markUnreachable(ctx, serverID, wrapped)
		return wrapped
	}
	if len(m.Disks) == 0 || len(m.Net) == 0 {
		p.Logger.Warn("collector: partial server section",
			"server_id", serverID, "server_name", srv.Name,
			"disks", len(m.Disks), "net", len(m.Net))
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

// PullLogsOnce tails the supplied log files for the given server and
// pushes any new entries to the configured LogPusher (Loki). For each
// file it advances the per-(server, path) cursor in storage on success.
//
// Per-file failures are logged at warn-level and skipped — one bad file
// shouldn't block the rest. Returns the number of files that succeeded
// (advanced their cursor) and the number that errored. A single Loki
// push covers all files in this cycle (one HTTP request).
//
// Caller supplies the LogFile list — typically derived from the bench
// list reported by the most recent metrics pull, plus any server-wide
// files like the MariaDB slow query log. Phase 3 v1 doesn't auto-
// discover log paths server-side.
func (p *Pipeline) PullLogsOnce(
	ctx context.Context,
	serverID int,
	logPusher LogPusher,
	tailer *LogTailer,
	files []LogTailFile,
) (ok int, errs int, err error) {
	srv, err := p.Store.GetServer(ctx, serverID)
	if err != nil {
		return 0, 0, fmt.Errorf("collector logs: get server %d: %w", serverID, err)
	}
	target := sshpkg.Target{
		Host: srv.Hostname, Port: srv.SSHPort, User: srv.SSHUser, KeyPath: srv.SSHKeyPath,
	}

	// Tail every file; collect (TailResult, file) pairs for the ones
	// that yielded entries so we can push in one shot at the end.
	var streams []logs.Stream
	type pendingCursor struct {
		path   string
		offset int64
	}
	var pending []pendingCursor

	for _, f := range files {
		labels := map[string]string{
			"server":   srv.Name,
			"log_type": f.Type,
		}
		if f.Bench != "" {
			labels["bench"] = f.Bench
		}

		res, terr := tailer.TailFile(ctx, target, serverID, labels, LogFile{
			Path: f.Path, Type: f.Type,
		})
		if terr != nil {
			errs++
			p.Logger.Warn("collector: log tail failed (skipped)",
				"server_id", serverID, "path", f.Path, "err", terr)
			continue
		}

		if len(res.Stream.Entries) > 0 {
			streams = append(streams, res.Stream)
		}
		pending = append(pending, pendingCursor{path: f.Path, offset: res.NewOffset})
		ok++
	}

	// One Loki push covers all files in this cycle. Empty input is a
	// no-op in LokiClient.Push, so the call is safe regardless.
	if pushErr := logPusher.Push(ctx, streams); pushErr != nil {
		// Don't advance any cursors on push failure — re-pushing the
		// same entries on the next cycle is preferable to losing them.
		p.Logger.Error("collector: loki push failed",
			"server_id", serverID, "files", len(pending), "err", pushErr)
		return ok, errs, fmt.Errorf("loki push: %w", pushErr)
	}

	// Push succeeded — advance every cursor.
	for _, pc := range pending {
		if cerr := p.Store.UpsertLogCursor(ctx, storage.LogCursor{
			ServerID:   serverID,
			LogPath:    pc.path,
			ByteOffset: pc.offset,
		}); cerr != nil {
			p.Logger.Error("collector: cursor update failed",
				"server_id", serverID, "path", pc.path, "err", cerr)
			// Don't return — best-effort cursor advance.
		}
	}

	p.Logger.Info("collector: logs pulled",
		"server_id", serverID,
		"server_name", srv.Name,
		"files_ok", ok,
		"files_err", errs,
		"streams_pushed", len(streams))
	return ok, errs, nil
}

// LogTailFile is one entry in the list passed to PullLogsOnce. Bench is
// optional — server-wide files (like MariaDB's slow query log) leave it
// empty so the resulting Loki stream has no `bench` label.
type LogTailFile struct {
	Path  string
	Type  string // "error" | "slow_query" | etc.
	Bench string // "" for server-wide files
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
