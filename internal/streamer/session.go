package streamer

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	sshpkg "frappe-monitor/internal/ssh"
)

// FileSpec is one (id, path) pair the streamer should tail on a
// particular server. The id appears in the protocol's `##F=<id>`
// prefix; the path is the absolute path on the bench.
type FileSpec struct {
	ID   string
	Path string
}

// SessionConfig captures everything one session goroutine needs to
// run a single server's stream end-to-end. The Manager builds these
// from each server's row.
type SessionConfig struct {
	ServerID int
	Target   sshpkg.Target

	// Files is the list the bench-side streamer will tail. The
	// session passes them through to --files=<id>:<path>.
	Files []FileSpec

	// RemoteCommand is the full bash command the session will exec.
	// Typically: bash -s -- --files=... --resume=... ; the manager
	// is responsible for deploying the streamer script and computing
	// the resume offsets.
	RemoteCommand string

	// ResumeOffsets maps fileID → starting byte offset (read from
	// log_cursors at session boot). The session interpolates these
	// into RemoteCommand via --resume=<id>:<offset>,...
	ResumeOffsets map[string]int64

	// MinBackoff / MaxBackoff govern reconnect cadence. Defaults
	// applied by NewSession when zero.
	MinBackoff time.Duration
	MaxBackoff time.Duration

	// MaxLineBytes caps a single scanned line. Default 1 MiB —
	// higher than any plausible Frappe access-log line, but bounded
	// so a misbehaving bench can't OOM the monitor.
	MaxLineBytes int
}

// Session owns one server's stream. Run() is the long-lived
// goroutine; Stop() signals it to wind down.
type Session struct {
	cfg     SessionConfig
	exec    sshpkg.Executor
	handler Handler
	logger  *slog.Logger

	stopOnce sync.Once
	stop     chan struct{}
	done     chan struct{}
}

// NewSession constructs a Session. Run is non-blocking — it starts
// the loop in a new goroutine. Stop returns when the loop exits.
func NewSession(cfg SessionConfig, exec sshpkg.Executor, h Handler, logger *slog.Logger) *Session {
	if cfg.MinBackoff <= 0 {
		cfg.MinBackoff = 1 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 60 * time.Second
	}
	if cfg.MaxLineBytes <= 0 {
		cfg.MaxLineBytes = 1 << 20
	}
	return &Session{
		cfg:     cfg,
		exec:    exec,
		handler: h,
		logger:  logger,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Run starts the connect → scan → reconnect loop in a goroutine. The
// returned channel closes when the loop has fully exited.
func (s *Session) Run(ctx context.Context) <-chan struct{} {
	go s.loop(ctx)
	return s.done
}

// Stop signals the loop and waits up to timeout for it to finish.
// Returns false if the loop didn't exit in time (best to log and
// move on; the goroutine will exit when ctx is canceled).
func (s *Session) Stop(timeout time.Duration) bool {
	s.stopOnce.Do(func() { close(s.stop) })
	select {
	case <-s.done:
		return true
	case <-time.After(timeout):
		return false
	}
}

func (s *Session) loop(ctx context.Context) {
	defer close(s.done)

	backoff := s.cfg.MinBackoff
	for {
		// Honor stop/ctx before each attempt so we don't dial after
		// being told to wind down.
		select {
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		default:
		}

		err := s.runOnce(ctx)
		if err == nil {
			// Clean EOF — remote command exited normally. Treat as a
			// reconnect-worthy event since the streamer is supposed
			// to run forever; an EOF likely means the bench killed
			// the session externally (e.g. shell timed out), not a
			// graceful shutdown.
			s.logger.Info("streamer: session ended cleanly, reconnecting",
				"server_id", s.cfg.ServerID)
		} else if errors.Is(err, context.Canceled) ||
			errors.Is(err, context.DeadlineExceeded) {
			// Context canceled = process is shutting down; drop out.
			return
		} else {
			s.logger.Warn("streamer: session error",
				"server_id", s.cfg.ServerID, "err", err)
		}

		s.handler.OnSessionState(ctx, s.cfg.ServerID, false, fmt.Sprintf("reconnect after: %v", err))

		// Sleep with jitter, but wake on stop/ctx so shutdown is
		// snappy even when we'd otherwise be backing off for 60s.
		select {
		case <-time.After(backoff):
		case <-s.stop:
			return
		case <-ctx.Done():
			return
		}
		backoff = nextBackoff(backoff, s.cfg.MaxBackoff)
	}
}

// runOnce opens a stream, scans until EOF/error, calls the handler.
// Returns nil on clean EOF, the underlying error otherwise. Backoff
// advancement is the caller's job.
func (s *Session) runOnce(ctx context.Context) error {
	cmd := s.cfg.RemoteCommand
	if cmd == "" {
		return errors.New("streamer: empty remote command")
	}

	stream, err := s.exec.Stream(ctx, s.cfg.Target, cmd)
	if err != nil {
		return fmt.Errorf("ssh stream: %w", err)
	}
	defer stream.Close()

	s.handler.OnSessionState(ctx, s.cfg.ServerID, true, "")

	// Watch for stop concurrently with reading. We can't cancel a
	// blocked Read on a generic io.Reader, but Closing the handle
	// will unblock it (the real impl signals the remote and tears
	// down the session; the fake's blocked Read returns EOF).
	stopWatcher := make(chan struct{})
	go func() {
		select {
		case <-s.stop:
			_ = stream.Close()
		case <-ctx.Done():
			_ = stream.Close()
		case <-stopWatcher:
		}
	}()
	defer close(stopWatcher)

	scanner := bufio.NewScanner(stream)
	scanner.Buffer(make([]byte, 0, 64*1024), s.cfg.MaxLineBytes)
	versionSeen := false

	for scanner.Scan() {
		line := scanner.Text()
		ev := ParseLine(line)
		now := time.Now()

		switch ev.Type {
		case EventVersion:
			versionSeen = true
			s.handler.OnVersion(ctx, s.cfg.ServerID, ev.Version)
		case EventLine:
			s.handler.OnLine(ctx, s.cfg.ServerID, ev.FileID, ev.Content, now, ev.SourceByteLen())
		case EventFileError:
			s.handler.OnFileError(ctx, s.cfg.ServerID, ev.FileID, ev.ErrorToken)
		case EventUnknown:
			// Don't flood logs on every unknown line — log once per
			// session at info, then shut up. Unknown lines are
			// almost always a forward-compat-newer-streamer thing.
			if versionSeen {
				s.logger.Debug("streamer: unknown line",
					"server_id", s.cfg.ServerID, "line", truncate(line, 200))
			}
		}
	}
	if err := scanner.Err(); err != nil {
		// Distinguish "remote closed cleanly" (EOF, scanner.Err==nil)
		// from a network error. bufio.Scanner returns nil for EOF.
		return fmt.Errorf("scan: %w", err)
	}
	return nil
}

// nextBackoff doubles up to max. Simple exponential, no jitter for
// now — the cardinality of "how often does this server's stream
// flap" is low enough that thundering-herd isn't a real concern.
func nextBackoff(cur, max time.Duration) time.Duration {
	cur *= 2
	if cur > max {
		cur = max
	}
	return cur
}

// truncate caps a string at n runes (approximately) for log output.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// BuildRemoteCommand assembles the bash invocation that runs the
// streamer script via stdin (heredoc-style). The script content is
// piped in by the caller via RunWithInput-shaped semantics — but
// since Stream doesn't accept stdin in this iteration, we instead
// expect the script to already exist at $HOME/.frappe-monitor/
// stream.sh on the bench. The Manager handles deployment.
//
// Format: `bash -lc 'PATH/stream.sh --files=... --resume=...'`. We
// quote the inner string with single quotes and refuse any file id /
// path / offset that contains a single quote — none of those should
// ever appear in well-formed config.
func BuildRemoteCommand(scriptPath string, files []FileSpec, resume map[string]int64) (string, error) {
	if scriptPath == "" {
		return "", errors.New("streamer: empty script path")
	}
	if len(files) == 0 {
		return "", errors.New("streamer: no files specified")
	}
	for _, f := range files {
		if strings.ContainsAny(f.ID, "',=:|\n") {
			return "", fmt.Errorf("streamer: file id %q contains a forbidden character", f.ID)
		}
		if strings.ContainsAny(f.Path, "',\n") {
			return "", fmt.Errorf("streamer: file path %q contains a forbidden character", f.Path)
		}
	}
	if strings.ContainsAny(scriptPath, "',\n") {
		return "", fmt.Errorf("streamer: script path %q contains a forbidden character", scriptPath)
	}

	var fileParts []string
	for _, f := range files {
		fileParts = append(fileParts, fmt.Sprintf("%s:%s", f.ID, f.Path))
	}
	args := []string{
		"--files=" + strings.Join(fileParts, ","),
	}
	if len(resume) > 0 {
		var rparts []string
		for _, f := range files {
			if off, ok := resume[f.ID]; ok && off > 0 {
				rparts = append(rparts, fmt.Sprintf("%s:%d", f.ID, off))
			}
		}
		if len(rparts) > 0 {
			args = append(args, "--resume="+strings.Join(rparts, ","))
		}
	}
	// Single-quoted inner: no shell interpretation of $/`/\\, only
	// single-quote terminates. We've already rejected single-quotes
	// above, so this is safe.
	cmd := fmt.Sprintf("bash -lc '%s %s'", scriptPath, strings.Join(args, " "))
	return cmd, nil
}

// guard against unused-import noise if io ever becomes unreferenced.
var _ io.Reader = (*strings.Reader)(nil)
