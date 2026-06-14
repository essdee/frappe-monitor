package ssh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// syncBuffer is a bytes.Buffer guarded by a mutex. crypto/ssh writes
// stdout/stderr from internal goroutines that keep running after a
// timeout/cancel select branch returns; reading the buffer on that
// branch would otherwise race those writes. The lock makes concurrent
// Write (from ssh) and String (our snapshot) safe.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type PoolConfig struct {
	DialTimeout    time.Duration
	CommandTimeout time.Duration

	// KnownHostsPath is the OpenSSH known_hosts file used to verify remote
	// host keys. Empty defaults to ~/.ssh/known_hosts of the user the monitor
	// runs as.
	KnownHostsPath string
	// InsecureSkipHostKeyCheck disables host-key verification entirely. It is
	// an explicit, dangerous opt-out (MITM-able) intended for first-run / dev;
	// the caller should log a warning when it is set. Default false = verify.
	InsecureSkipHostKeyCheck bool
}

type Pool struct {
	cfg  PoolConfig
	mu   sync.Mutex
	conn map[string]*ssh.Client // key: host:port|user|keypath
	khMu sync.Mutex             // serializes trust-on-first-use known_hosts appends
}

func NewPool(cfg PoolConfig) *Pool {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.CommandTimeout == 0 {
		cfg.CommandTimeout = 30 * time.Second
	}
	return &Pool{cfg: cfg, conn: map[string]*ssh.Client{}}
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, c := range p.conn {
		_ = c.Close()
		delete(p.conn, k)
	}
}

// noCommandCapKey marks a context as opting out of the pool's per-command
// CommandTimeout ceiling (see WithoutCommandCap).
type noCommandCapKey struct{}

// WithoutCommandCap returns a context that tells Pool.Run/RunWithInput to bound
// the command only by the context's own deadline, not by the pool's
// CommandTimeout. The control panel uses it for bench actions (migrate/update)
// that legitimately run far longer than a metrics command; everything else
// keeps the min(ctx, CommandTimeout) default so ssh.command_timeout_seconds
// still bounds normal commands.
func WithoutCommandCap(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCommandCapKey{}, struct{}{})
}

func hasNoCommandCap(ctx context.Context) bool {
	return ctx.Value(noCommandCapKey{}) != nil
}

func (p *Pool) Run(ctx context.Context, tgt Target, cmd string) (string, error) {
	return p.RunWithInput(ctx, tgt, cmd, "")
}

func (p *Pool) RunWithInput(ctx context.Context, tgt Target, cmd, stdin string) (string, error) {
	client, err := p.getOrDial(ctx, tgt)
	if err != nil {
		return "", err
	}
	sess, err := client.NewSession()
	if err != nil {
		// Connection may be stale; drop and retry once.
		p.dropClient(tgt)
		client, err = p.getOrDial(ctx, tgt)
		if err != nil {
			return "", err
		}
		sess, err = client.NewSession()
		if err != nil {
			return "", fmt.Errorf("ssh new session %s: %w", tgt.addr(), err)
		}
	}
	defer sess.Close()

	out := &syncBuffer{}
	sess.Stdout = out
	sess.Stderr = out
	if stdin != "" {
		sess.Stdin = strings.NewReader(stdin)
	}

	// Buffer 1: the goroutine can always write and exit even after a
	// timeout/cancel select branch returns. Combined with defer sess.Close()
	// (which causes sess.Run to return as the channel closes), this avoids
	// the goroutine surviving after this function returns.
	done := make(chan error, 1)
	go func() { done <- sess.Run(cmd) }()

	// CommandTimeout is the default per-command ceiling. For normal callers the
	// effective bound is min(ctx deadline, CommandTimeout) — so a scheduled
	// collector/dbmonitor command is still capped by ssh.command_timeout_seconds
	// AND by the caller's per-job deadline, whichever is sooner. A caller that
	// legitimately needs longer — the control panel's bench update/migrate, which
	// can run for minutes — opts out with WithoutCommandCap, leaving ONLY its own
	// ctx deadline (5m/30m) as the bound so it isn't SIGKILLed mid-flight.
	var timeoutCh <-chan time.Time
	if !hasNoCommandCap(ctx) {
		t := time.NewTimer(p.cfg.CommandTimeout)
		defer t.Stop()
		timeoutCh = t.C
	}
	select {
	case err := <-done:
		if err != nil {
			return out.String(), fmt.Errorf("ssh run %q on %s: %w: %s", cmd, tgt.addr(), err, out.String())
		}
		return out.String(), nil
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return out.String(), fmt.Errorf("ssh run %q on %s: %w", cmd, tgt.addr(), ctx.Err())
	case <-timeoutCh:
		_ = sess.Signal(ssh.SIGKILL)
		return out.String(), fmt.Errorf("ssh run %q on %s: %w after %s", cmd, tgt.addr(), ErrTimeout, p.cfg.CommandTimeout)
	}
}

// Stream opens a session, starts cmd, and returns its stdout as a
// StreamHandle. Unlike Run, it does NOT wait for cmd to finish — the
// caller drives the stream by reading until EOF (clean exit) or a
// non-EOF error (network drop, host reboot, etc.). The CommandTimeout
// from PoolConfig is intentionally NOT applied here; long-lived
// streams (the streamer's tail -F session is forever) would otherwise
// be killed at the timeout. Callers manage their own deadlines.
func (p *Pool) Stream(ctx context.Context, tgt Target, cmd string) (StreamHandle, error) {
	client, err := p.getOrDial(ctx, tgt)
	if err != nil {
		return nil, err
	}
	sess, err := client.NewSession()
	if err != nil {
		// Same stale-connection retry the synchronous path uses.
		p.dropClient(tgt)
		client, err = p.getOrDial(ctx, tgt)
		if err != nil {
			return nil, err
		}
		sess, err = client.NewSession()
		if err != nil {
			return nil, fmt.Errorf("ssh new session %s: %w", tgt.addr(), err)
		}
	}
	stdout, err := sess.StdoutPipe()
	if err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("ssh stdout pipe %s: %w", tgt.addr(), err)
	}
	// Drain stderr into a small bounded buffer so the remote isn't
	// stalled by a full pipe; we don't surface stderr from streams
	// today, but we MUST consume it to avoid wedging the session.
	if stderr, err := sess.StderrPipe(); err == nil {
		go io.Copy(io.Discard, stderr)
	}
	if err := sess.Start(cmd); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("ssh start %q on %s: %w", cmd, tgt.addr(), err)
	}
	h := &poolStream{
		sess:   sess,
		stdout: stdout,
		addr:   tgt.addr(),
		closed: make(chan struct{}),
	}
	go h.watchCtx(ctx)
	return h, nil
}

// poolStream is the StreamHandle returned by Pool.Stream. The Close
// path is the load-bearing one: it sends SIGTERM (so the remote
// streamer's trap fires and cleans up tail children) and closes the
// session. Idempotent via sync.Once so a defer + ctx-watcher don't
// double-close.
type poolStream struct {
	sess   *ssh.Session
	stdout io.Reader
	addr   string

	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

func (s *poolStream) Read(p []byte) (int, error) {
	return s.stdout.Read(p)
}

func (s *poolStream) Close() error {
	s.closeOnce.Do(func() {
		// Best-effort SIGTERM to give the remote bash trap a chance
		// to clean up child tail processes. crypto/ssh accepts the
		// POSIX signal name; some servers ignore it, that's fine.
		_ = s.sess.Signal(ssh.SIGTERM)
		s.closeErr = s.sess.Close()
		close(s.closed)
	})
	return s.closeErr
}

// watchCtx forwards ctx cancellation to the session — when the parent
// context (usually the monitor's root signal-aware ctx) is canceled,
// in-flight streams should tear down promptly, not block on Read. The
// goroutine exits cleanly when Close is called explicitly.
func (s *poolStream) watchCtx(ctx context.Context) {
	select {
	case <-ctx.Done():
		_ = s.Close()
	case <-s.closed:
	}
}

func poolKey(tgt Target) string {
	return fmt.Sprintf("%s|%s|%s", tgt.addr(), tgt.User, tgt.KeyPath)
}

func (p *Pool) getOrDial(ctx context.Context, tgt Target) (*ssh.Client, error) {
	k := poolKey(tgt)
	p.mu.Lock()
	if c, ok := p.conn[k]; ok {
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()

	auth, err := loadKey(tgt.KeyPath)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            tgt.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: p.hostKeyCallback(),
		Timeout:         p.cfg.DialTimeout,
	}

	// Dial with the caller's context so a per-job timeout can interrupt
	// an in-progress TCP connect, instead of blocking for the full
	// DialTimeout while holding a scheduler semaphore slot.
	d := net.Dialer{Timeout: p.cfg.DialTimeout}
	conn, err := d.DialContext(ctx, "tcp", tgt.addr())
	if err != nil {
		return nil, classifyDialError(tgt.addr(), err)
	}
	// Bound the SSH handshake by the smaller of the ctx deadline and
	// DialTimeout, then clear it (per-session I/O manages its own).
	hsDeadline := time.Now().Add(p.cfg.DialTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(hsDeadline) {
		hsDeadline = dl
	}
	_ = conn.SetDeadline(hsDeadline)
	sc, chans, reqs, err := ssh.NewClientConn(conn, tgt.addr(), cfg)
	if err != nil {
		_ = conn.Close()
		return nil, classifyDialError(tgt.addr(), err)
	}
	_ = conn.SetDeadline(time.Time{})
	c := ssh.NewClient(sc, chans, reqs)

	p.mu.Lock()
	// Someone may have raced ahead of us; prefer whatever is stored.
	if existing, ok := p.conn[k]; ok {
		p.mu.Unlock()
		_ = c.Close()
		return existing, nil
	}
	p.conn[k] = c
	p.mu.Unlock()
	return c, nil
}

// classifyDialError wraps a crypto/ssh dial error with the appropriate
// sentinel (ErrAuth or ErrDial) so callers can distinguish the two without
// inspecting messages themselves.
func classifyDialError(addr string, err error) error {
	s := err.Error()
	if strings.Contains(s, "unable to authenticate") ||
		strings.Contains(s, "no supported methods remain") ||
		strings.Contains(s, "permission denied") {
		return fmt.Errorf("ssh dial %s: %w: %v", addr, ErrAuth, err)
	}
	return fmt.Errorf("ssh dial %s: %w: %v", addr, ErrDial, err)
}

// hostKeyCallback builds the host-key verifier for a dial. Modes:
//   - InsecureSkipHostKeyCheck: no verification at all (accept any key). Logged
//     at startup; intended for dev only — connections are MITM-able.
//   - default: trust-on-first-use (TOFU). An UNKNOWN host's key is pinned to
//     known_hosts on first connect and accepted; a host whose presented key
//     DIFFERS from the pinned one is rejected as a possible MITM. This auto-
//     onboards a whole fleet without a manual ssh-keyscan step (so a fresh
//     install and an upgrade-from-insecure both just work) while still catching
//     the real threat — a host key that changes under us.
//
// known_hosts is read fresh on each dial (dials only happen on a pool cache
// miss, so this is rare and cheap), and the file + its parent dir are created
// if absent so a fresh service user works out of the box.
func (p *Pool) hostKeyCallback() ssh.HostKeyCallback {
	if p.cfg.InsecureSkipHostKeyCheck {
		return ssh.InsecureIgnoreHostKey()
	}
	path := p.cfg.KnownHostsPath
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".ssh", "known_hosts")
		}
	} else {
		path = expandHomePath(path)
	}
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if err := ensureKnownHosts(path); err != nil {
			return fmt.Errorf("ssh: host-key store unavailable (%q): %w "+
				"(set ssh.insecure_skip_host_key_check: true to disable verification)", path, err)
		}
		cb, err := knownhosts.New(path)
		if err != nil {
			return fmt.Errorf("ssh: cannot read known_hosts %q: %w "+
				"(set ssh.insecure_skip_host_key_check: true to disable verification)", path, err)
		}
		verr := cb(hostname, remote, key)
		if verr == nil {
			return nil // already trusted
		}
		var keyErr *knownhosts.KeyError
		if errors.As(verr, &keyErr) && len(keyErr.Want) == 0 {
			// Unknown host (no key pinned yet) → trust-on-first-use: pin + accept.
			if perr := p.pinHostKey(path, hostname, key); perr != nil {
				return fmt.Errorf("ssh: could not pin host key for %s to %q: %w", hostname, path, perr)
			}
			return nil
		}
		if errors.As(verr, &keyErr) {
			// A DIFFERENT key is pinned → the host key changed under us.
			return fmt.Errorf("ssh: host key for %s CHANGED from the pinned value — possible man-in-the-middle; "+
				"if the host was legitimately rebuilt, remove its line from %s and reconnect", hostname, path)
		}
		return fmt.Errorf("ssh: host-key verification failed for %s: %w", hostname, verr)
	}
}

// pinHostKey appends hostname's key to the known_hosts file (trust-on-first-
// use). Serialized so concurrent first-dials don't interleave file writes; a
// duplicate line from a benign race is harmless (knownhosts dedupes on lookup).
func (p *Pool) pinHostKey(path, hostname string, key ssh.PublicKey) error {
	p.khMu.Lock()
	defer p.khMu.Unlock()
	line := knownhosts.Line([]string{knownhosts.Normalize(hostname)}, key)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(line + "\n")
	return err
}

// ensureKnownHosts makes sure the known_hosts file (and its parent ~/.ssh) exist
// with owner-only permissions, so the TOFU callback can both read and append.
func ensureKnownHosts(path string) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

func (p *Pool) dropClient(tgt Target) {
	k := poolKey(tgt)
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.conn[k]; ok {
		_ = c.Close()
		delete(p.conn, k)
	}
}

func loadKey(path string) (ssh.AuthMethod, error) {
	raw, err := os.ReadFile(expandHomePath(path))
	if err != nil {
		return nil, fmt.Errorf("read key %q: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		var passErr *ssh.PassphraseMissingError
		if errors.As(err, &passErr) {
			return nil, fmt.Errorf("parse key %q: key is passphrase-protected, which is not supported — "+
				"point the server at an unencrypted key dedicated to the monitor: %w", path, err)
		}
		return nil, fmt.Errorf("parse key %q: %w", path, err)
	}
	return ssh.PublicKeys(signer), nil
}

// expandHomePath expands a leading "~" / "~/" to the home directory of the user
// the monitor runs as. SSH key paths come from the dashboard/config where
// operators reasonably write ~/.ssh/...; Go's os.ReadFile does NOT expand "~"
// the way a shell does, so without this it looks for a literal "~" directory.
func expandHomePath(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
