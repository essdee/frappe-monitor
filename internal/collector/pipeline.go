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
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"frappe-monitor/internal/metrics"
	"frappe-monitor/internal/parser"
	"frappe-monitor/internal/probes"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
	"frappe-monitor/scripts"
)

// Pusher abstracts the metrics-backend write so the pipeline can be tested
// without standing up VictoriaMetrics. metrics.VMClient implements it.
type Pusher interface {
	Push(ctx context.Context, body string) error
}

type PublicProber interface {
	ProbeSites(ctx context.Context, sites []probes.Site) []probes.Result
}

// Pipeline owns the per-server pull. The exec, push, and store
// dependencies are all interfaces so tests can swap fakes in.
type Pipeline struct {
	Store  storage.Store
	Exec   sshpkg.Executor
	Push   Pusher
	Public PublicProber
	Logger *slog.Logger
}

// buildCollectCmd returns the shell command to run the collector,
// optionally with BENCH_PATHS preset. The embedded collector is piped over
// SSH stdin and executed with bash; nothing is installed on the target.
//
// Paths are joined with ':' (matching the bash collector's parser)
// and shell-escaped via single-quote wrapping so paths with spaces
// or quotes don't break the command line.
func buildCollectCmd(benchPaths []string) string {
	if len(benchPaths) == 0 {
		return "bash -s"
	}
	joined := strings.Join(benchPaths, ":")
	// Single-quote escape: ' → '\'' (close, escape, reopen).
	escaped := "'" + strings.ReplaceAll(joined, "'", `'\''`) + "'"
	return "BENCH_PATHS=" + escaped + " bash -s"
}

// PullOnce runs one full cycle for the server with the given id:
//
//	store.GetServer  → ssh.RunWithInput("bash -s", embedded collector)
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
	startedAt := time.Now()
	srv, err := p.Store.GetServer(ctx, serverID)
	if err != nil {
		// Don't write status; if we can't even find the row, there's nothing to update.
		return fmt.Errorf("collector: get server %d: %w", serverID, err)
	}

	target := sshpkg.Target{
		Host: srv.Hostname, Port: srv.SSHPort, User: srv.SSHUser, KeyPath: srv.SSHKeyPath,
	}
	stdout, runErr := p.Exec.RunWithInput(ctx, target, buildCollectCmd(srv.BenchPaths), scripts.CollectorScript)
	if runErr != nil {
		wrapped := fmt.Errorf("ssh: %w", runErr)
		_ = p.markUnreachable(ctx, srv, wrapped)
		return wrapped
	}
	sshFinishedAt := time.Now()

	out, parseErr := parser.Tokenize(stdout)
	if parseErr != nil {
		wrapped := fmt.Errorf("parse: %w", parseErr)
		_ = p.markUnreachable(ctx, srv, wrapped)
		return wrapped
	}

	// If the collector's trap fired, ERROR section carries exit_code +
	// line. Surface it loud — generic "missing META" is useless when
	// the operator wants to know the bash script's actual failure.
	if errSec, ok := out.Section("ERROR"); ok {
		exitCode := errSec.KVs["exit_code"]
		line := errSec.KVs["line"]
		wrapped := fmt.Errorf("collector script error (exit=%s line=%s)", exitCode, line)
		_ = p.markUnreachable(ctx, srv, wrapped)
		return wrapped
	}

	m, convErr := parser.ServerFromSections(out)
	if convErr != nil {
		wrapped := fmt.Errorf("parse: %w", convErr)
		_ = p.markUnreachable(ctx, srv, wrapped)
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
		_ = p.markUnreachable(ctx, srv, wrapped)
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
	body.WriteString(metrics.ReachabilityLineProtocol(srv.Name, true, time.Now().UTC()))

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
	publicSites := make([]probes.Site, 0)
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
		publicSites = append(publicSites, probes.Site{Server: srv.Name, Bench: benchName, Site: siteName})
		siteOK++
	}

	bodyStr := body.String()
	pushStartedAt := time.Now()
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
	internalPushDuration := time.Since(pushStartedAt)

	if err := p.Store.SetServerStatus(ctx, serverID, "reachable", ""); err != nil {
		p.Logger.Error("collector: set status reachable", "server_id", serverID, "err", err)
		return fmt.Errorf("status: %w", err)
	}

	// Public checks are deliberately a second, non-fatal phase. The SSH and
	// internal site metrics above are already durable, so DNS/TLS latency or a
	// public-probe timeout can never make a reachable server appear offline.
	publicOK, publicFailed := 0, 0
	var publicProbeDuration time.Duration
	if p.Public != nil && len(publicSites) > 0 && ctx.Err() == nil {
		publicStartedAt := time.Now()
		results := p.Public.ProbeSites(ctx, publicSites)
		var publicBody strings.Builder
		for _, result := range results {
			pm := metrics.PublicSiteMetrics{
				Timestamp: result.Timestamp, Server: result.Server, Bench: result.Bench, Site: result.Site,
				HTTPStatusCode: result.HTTPStatusCode, HTTPResponseMs: result.HTTPResponseMs,
				IsHealthy: result.IsHealthy, FailureReasonCode: result.FailureReasonCode,
				TLSCertExpiryTimestamp: result.TLSCertExpiryTimestamp,
			}
			publicBody.WriteString(pm.LineProtocol())
			if result.IsHealthy == 1 {
				publicOK++
			} else {
				publicFailed++
			}
		}
		if publicBody.Len() > 0 {
			if err := p.Push.Push(ctx, publicBody.String()); err != nil {
				p.Logger.Warn("collector: public probe metrics push failed", "server_id", serverID, "err", err)
			}
		}
		publicProbeDuration = time.Since(publicStartedAt)
	}
	p.Logger.Info("collector: pull ok",
		"server_id", serverID,
		"server_name", srv.Name,
		"body_lines", strings.Count(bodyStr, "\n"),
		"benches_ok", benchOK,
		"benches_err", benchErr,
		"sites_ok", siteOK,
		"sites_err", siteErr,
		"public_sites_ok", publicOK,
		"public_sites_failed", publicFailed,
		"ssh_ms", sshFinishedAt.Sub(startedAt).Milliseconds(),
		"parse_ms", pushStartedAt.Sub(sshFinishedAt).Milliseconds(),
		"push_ms", internalPushDuration.Milliseconds(),
		"public_probe_ms", publicProbeDuration.Milliseconds(),
		"total_ms", time.Since(startedAt).Milliseconds())
	return nil
}

// markUnreachable updates the server's status row, capping the recorded
// last_error so a multi-MB SSH stderr doesn't bloat the SQLite row.
func (p *Pipeline) markUnreachable(ctx context.Context, srv *storage.Server, cause error) error {
	// A caller cancellation (normally process shutdown) is not evidence that
	// the remote server failed. A per-job deadline, however, is a real failed
	// collection and must be persisted/reported using a short cleanup context.
	if errors.Is(ctx.Err(), context.Canceled) {
		return ctx.Err()
	}
	statusCtx := ctx
	cancel := func() {}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		statusCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	}
	defer cancel()

	const maxErrLen = 4096
	msg := cause.Error()
	if len(msg) > maxErrLen {
		msg = msg[:maxErrLen]
	}
	if err := p.Store.SetServerStatus(statusCtx, srv.ID, "unreachable", msg); err != nil {
		p.Logger.Error("collector: set status unreachable",
			"server_id", srv.ID, "err", err)
		return err
	}
	if err := p.Push.Push(statusCtx,
		metrics.ReachabilityLineProtocol(srv.Name, false, time.Now().UTC())); err != nil {
		p.Logger.Error("collector: push unreachable status failed",
			"server_id", srv.ID, "server_name", srv.Name, "err", err)
	}
	return nil
}
