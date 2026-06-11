package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"frappe-monitor/internal/realtime"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// Sentinel errors so the API layer can map failures to 400/404.
var (
	ErrUnknownAction = errors.New("unknown action")
	ErrInvalidParams = errors.New("invalid parameters")
)

// ActionView is the JSON shape of an audit record, shared by the REST handlers
// and the realtime broadcasts so both speak exactly one schema.
type ActionView struct {
	ID          int        `json:"id"`
	ServerID    int        `json:"server_id"`
	Server      string     `json:"server,omitempty"`
	Action      string     `json:"action"`
	BenchPath   string     `json:"bench_path,omitempty"`
	Site        string     `json:"site,omitempty"`
	Command     string     `json:"command,omitempty"`
	RequestedBy string     `json:"requested_by,omitempty"`
	Status      string     `json:"status"`
	ExitOK      bool       `json:"exit_ok"`
	Output      string     `json:"output,omitempty"`
	Error       string     `json:"error,omitempty"`
	DurationMs  int        `json:"duration_ms"`
	CreatedAt   time.Time  `json:"created_at"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`
}

// ViewOf builds the wire view of an audit record, attaching the server name
// when known.
func ViewOf(a *storage.ControlAction, serverName string) ActionView {
	return ActionView{
		ID: a.ID, ServerID: a.ServerID, Server: serverName,
		Action: a.Action, BenchPath: a.BenchPath, Site: a.Site,
		Command: a.Command, RequestedBy: a.RequestedBy,
		Status: a.Status, ExitOK: a.ExitOK, Output: a.Output, Error: a.Error,
		DurationMs: a.DurationMs, CreatedAt: a.CreatedAt, FinishedAt: a.FinishedAt,
	}
}

// Service executes allowlisted control actions over SSH and records each run.
type Service struct {
	store  storage.Store
	exec   sshpkg.Executor
	hub    realtime.Broadcaster
	logger *slog.Logger

	timeout        time.Duration // normal action ceiling
	dangerTimeout  time.Duration // for Dangerous actions (bench update)
	readTimeout    time.Duration // synchronous reads (site-config fetch)
	maxOutputBytes int
	sem            chan struct{} // caps concurrent in-flight runs
}

// New builds a control Service with production-safe defaults.
func New(store storage.Store, exec sshpkg.Executor, hub realtime.Broadcaster, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:          store,
		exec:           exec,
		hub:            hub,
		logger:         logger,
		timeout:        5 * time.Minute,
		dangerTimeout:  30 * time.Minute,
		readTimeout:    20 * time.Second,
		maxOutputBytes: 64 * 1024,
		sem:            make(chan struct{}, 4),
	}
}

// RunRequest asks to run one allowlisted action.
type RunRequest struct {
	ServerID    int
	ActionKey   string
	BenchPath   string
	Site        string
	RequestedBy string
}

// Run validates the request, records a pending audit row, and executes the
// command asynchronously. It returns the pending record immediately; progress
// and the final result arrive over the realtime control topic.
func (s *Service) Run(ctx context.Context, req RunRequest) (*storage.ControlAction, error) {
	action, ok := Lookup(req.ActionKey)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownAction, req.ActionKey)
	}
	cmd, err := action.Resolve(Params{BenchPath: req.BenchPath, Site: req.Site})
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	srv, err := s.store.GetServer(ctx, req.ServerID)
	if err != nil {
		return nil, err
	}
	if action.Scope == ScopeBench || action.Scope == ScopeSite {
		if !benchAllowed(srv, req.BenchPath) {
			return nil, fmt.Errorf("%w: bench path is not registered on this server", ErrInvalidParams)
		}
	}
	act, err := s.store.CreateControlAction(ctx, storage.NewControlAction{
		ServerID: req.ServerID, Action: req.ActionKey,
		BenchPath: req.BenchPath, Site: req.Site,
		Command: cmd, RequestedBy: req.RequestedBy,
	})
	if err != nil {
		return nil, err
	}
	s.broadcast(realtime.TypeControlStarted, act, srv.Name)
	// Hand the goroutine its OWN copy: Run returns `act` to the HTTP handler,
	// which reads it via ViewOf; mutating the same pointer in execAudited
	// would be a data race. The copy carries the same ID for the DB updates.
	runCopy := *act
	go s.execAudited(&runCopy, srv, action.Dangerous, func(ctx context.Context, tgt sshpkg.Target) (string, error) {
		return s.exec.Run(ctx, tgt, cmd)
	})
	return act, nil
}

// ReadSiteConfig fetches a site's site_config.json over SSH (synchronous).
func (s *Service) ReadSiteConfig(ctx context.Context, serverID int, bench, site string) (string, error) {
	if err := ValidateBenchPath(bench); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	if err := ValidateSite(site); err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	srv, err := s.store.GetServer(ctx, serverID)
	if err != nil {
		return "", err
	}
	if !benchAllowed(srv, bench) {
		return "", fmt.Errorf("%w: bench path is not registered on this server", ErrInvalidParams)
	}
	cctx, cancel := context.WithTimeout(ctx, s.readTimeout)
	defer cancel()
	tgt := sshpkg.Target{Host: srv.Hostname, Port: srv.SSHPort, User: srv.SSHUser, KeyPath: srv.SSHKeyPath}
	path := bench + "/sites/" + site + "/site_config.json"
	out, err := s.exec.Run(cctx, tgt, "cat "+shellQuote(path))
	if err != nil {
		return "", err
	}
	return out, nil
}

// WriteConfigRequest edits a site's site_config.json, optionally restarting the
// bench afterward. The previous config is backed up (.bak) on the host and also
// captured in the audit output.
type WriteConfigRequest struct {
	ServerID    int
	BenchPath   string
	Site        string
	Content     string // new site_config.json — must be valid JSON
	Restart     bool
	RequestedBy string
}

// WriteSiteConfig validates and applies a site-config edit asynchronously,
// recording the change (with the prior config) in the audit log.
func (s *Service) WriteSiteConfig(ctx context.Context, req WriteConfigRequest) (*storage.ControlAction, error) {
	if err := ValidateBenchPath(req.BenchPath); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	if err := ValidateSite(req.Site); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	if !json.Valid([]byte(req.Content)) {
		return nil, fmt.Errorf("%w: content is not valid JSON", ErrInvalidParams)
	}
	srv, err := s.store.GetServer(ctx, req.ServerID)
	if err != nil {
		return nil, err
	}
	if !benchAllowed(srv, req.BenchPath) {
		return nil, fmt.Errorf("%w: bench path is not registered on this server", ErrInvalidParams)
	}
	desc := "edit " + req.Site + "/site_config.json"
	if req.Restart {
		desc += " + bench restart"
	}
	act, err := s.store.CreateControlAction(ctx, storage.NewControlAction{
		ServerID: req.ServerID, Action: "site.config-edit",
		BenchPath: req.BenchPath, Site: req.Site,
		Command: desc, RequestedBy: req.RequestedBy,
	})
	if err != nil {
		return nil, err
	}
	s.broadcast(realtime.TypeControlStarted, act, srv.Name)

	path := req.BenchPath + "/sites/" + req.Site + "/site_config.json"
	content, restart, bench, site := req.Content, req.Restart, req.BenchPath, req.Site
	writeCopy := *act // goroutine-owned copy; see Run for the rationale
	go s.execAudited(&writeCopy, srv, restart, func(ctx context.Context, tgt sshpkg.Target) (string, error) {
		qp := shellQuote(path)
		old, _ := s.exec.Run(ctx, tgt, "cat "+qp+" 2>/dev/null")
		writeCmd := "cp -f " + qp + " " + qp + ".bak 2>/dev/null; cat > " + qp
		if _, werr := s.exec.RunWithInput(ctx, tgt, writeCmd, content); werr != nil {
			return "previous config:\n" + old, werr
		}
		var b strings.Builder
		b.WriteString("previous config:\n")
		b.WriteString(old)
		b.WriteString("\n--- wrote new site_config.json (backup: site_config.json.bak) ---\n")
		if restart {
			r, rerr := s.exec.Run(ctx, tgt,
				"cd "+shellQuote(bench)+" && bench --site "+shellQuote(site)+" clear-cache && bench restart")
			b.WriteString("\nrestart:\n")
			b.WriteString(r)
			if rerr != nil {
				return b.String(), rerr
			}
		}
		return b.String(), nil
	})
	return act, nil
}

// execAudited runs fn under a bounded-concurrency slot, flipping the audit row
// to running, then to its terminal state, broadcasting each transition.
func (s *Service) execAudited(act *storage.ControlAction, srv *storage.Server, danger bool, fn func(ctx context.Context, tgt sshpkg.Target) (string, error)) {
	s.sem <- struct{}{}
	defer func() { <-s.sem }()

	bg := context.Background()
	if err := s.store.MarkControlActionRunning(bg, act.ID); err != nil {
		s.logger.Error("control: mark running", "id", act.ID, "err", err)
	}
	act.Status = "running"
	s.broadcast(realtime.TypeControlUpdated, act, srv.Name)

	timeout := s.timeout
	if danger {
		timeout = s.dangerTimeout
	}
	cctx, cancel := context.WithTimeout(bg, timeout)
	defer cancel()

	tgt := sshpkg.Target{Host: srv.Hostname, Port: srv.SSHPort, User: srv.SSHUser, KeyPath: srv.SSHKeyPath}
	start := time.Now()
	out, runErr := fn(cctx, tgt)

	res := storage.ControlActionResult{
		Output:     truncate(out, s.maxOutputBytes),
		DurationMs: int(time.Since(start).Milliseconds()),
	}
	if runErr != nil {
		res.Status = "failed"
		res.Error = cleanErr(runErr.Error())
	} else {
		res.Status = "success"
		res.ExitOK = true
	}
	final, err := s.store.FinishControlAction(bg, act.ID, res)
	if err != nil {
		s.logger.Error("control: finish", "id", act.ID, "err", err)
		// The DB write failed, but the command DID run. Still broadcast a
		// terminal state from the in-memory copy so a watching dashboard
		// doesn't hang on "running" forever; the row itself is reconciled
		// to failed on the next monitor restart (FailStaleControlActions).
		act.Status = res.Status
		act.ExitOK = res.ExitOK
		act.Output = res.Output
		act.Error = res.Error
		act.DurationMs = res.DurationMs
		s.broadcast(realtime.TypeControlUpdated, act, srv.Name)
		return
	}
	s.broadcast(realtime.TypeControlUpdated, final, srv.Name)
}

// benchAllowed enforces, as defense-in-depth on top of the path validation,
// that a bench-scoped operation targets one of the server's registered bench
// paths. When the server has no explicit bench_paths (auto-discovery host)
// there is nothing to check against, so any validated path is allowed.
func benchAllowed(srv *storage.Server, bench string) bool {
	if len(srv.BenchPaths) == 0 {
		return true
	}
	for _, b := range srv.BenchPaths {
		if b == bench {
			return true
		}
	}
	return false
}

func (s *Service) broadcast(typ string, a *storage.ControlAction, serverName string) {
	if s.hub == nil {
		return
	}
	s.hub.Broadcast(realtime.Event{Type: typ, Topic: realtime.TopicControl(), Data: ViewOf(a, serverName)})
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:runeBoundary(s, max)] + "\n…(truncated)"
}

func cleanErr(s string) string {
	s = strings.TrimSpace(s)
	const lim = 2000
	if len(s) > lim {
		s = s[:runeBoundary(s, lim)] + "…"
	}
	return s
}

// runeBoundary returns the largest offset <= max that falls on a UTF-8 rune
// start, so slicing s[:offset] never cuts a multi-byte rune in half (which
// would corrupt the trailing character into U+FFFD once JSON-encoded).
func runeBoundary(s string, max int) int {
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return max
}
