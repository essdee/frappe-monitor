package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

// Sentinel errors at the package boundary. Callers (HTTP handlers, alert
// engine) should errors.Is on these instead of string-matching the wrapped
// crypto/ssh errors. Pool wraps each path with the appropriate sentinel:
//   - ErrDial: TCP/DNS-level failures (host unreachable, connection refused).
//   - ErrAuth: SSH handshake/auth failures (bad key, key not authorized).
//   - ErrTimeout: command exceeded the per-call timeout.
//
// Auth-vs-dial detection is best-effort string-matching against crypto/ssh
// error messages; crypto/ssh does not expose typed errors for this fork.
var (
	ErrDial    = errors.New("ssh: dial failed")
	ErrAuth    = errors.New("ssh: authentication failed")
	ErrTimeout = errors.New("ssh: command timeout")
)

// Target identifies an SSH endpoint.
type Target struct {
	Host    string // hostname or IP
	Port    int    // 0 → 22
	User    string
	KeyPath string
}

func (t Target) addr() string {
	p := t.Port
	if p == 0 {
		p = 22
	}
	// net.JoinHostPort brackets IPv6 literals ("::1" → "[::1]:22"); a plain
	// "%s:%d" would produce the unparseable "::1:22".
	return net.JoinHostPort(t.Host, strconv.Itoa(p))
}

// Executor runs a command on a remote host and returns its combined stdout/stderr.
//
// Run is for commands that take no stdin. RunWithInput pipes the supplied
// stdin string to the command's stdin and returns its combined stdout/stderr;
// passing "" for stdin is equivalent to Run.
//
// Stream is for long-running commands whose stdout should be consumed
// incrementally — e.g. the Phase 8 log streamer's `tail -F` pipeline.
// The returned StreamHandle is an io.ReadCloser over stdout; Read
// returns new bytes as they arrive on the wire and io.EOF when the
// remote command exits cleanly. Always Close the handle — that signals
// the remote process and tears down the SSH session. Close is safe to
// call multiple times and from a different goroutine than Read.
type Executor interface {
	Run(ctx context.Context, tgt Target, cmd string) (string, error)
	RunWithInput(ctx context.Context, tgt Target, cmd, stdin string) (string, error)
	Stream(ctx context.Context, tgt Target, cmd string) (StreamHandle, error)
}

// StreamHandle is the handle returned by Executor.Stream. Reads are
// blocking until new bytes arrive or the session ends; Close is
// idempotent and concurrent-safe with Read.
type StreamHandle interface {
	io.ReadCloser
}

// Ping executes a no-op command and returns the round-trip latency.
func Ping(ctx context.Context, e Executor, tgt Target) (time.Duration, error) {
	start := time.Now()
	if _, err := e.Run(ctx, tgt, "echo ok"); err != nil {
		return 0, fmt.Errorf("ssh ping %s: %w", tgt.addr(), err)
	}
	return time.Since(start), nil
}
