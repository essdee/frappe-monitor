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

type PoolConfig struct {
	DialTimeout        time.Duration
	CommandTimeout     time.Duration
	KnownHostsPath     string
	MaxSessionsPerHost int
}

type Pool struct {
	cfg      PoolConfig
	mu       sync.Mutex
	conn     map[string]*ssh.Client // key: host:port|user|keypath
	limits   map[string]chan struct{}
	keyCache map[string]cachedKey
}

type cachedKey struct {
	modTime time.Time
	size    int64
	auth    ssh.AuthMethod
}

const maxCommandOutputBytes = 8 << 20

// cappedBuffer drains all remote output while retaining only a bounded
// prefix. Reporting the full write length keeps SSH flowing; callers reject
// truncated output instead of parsing an incomplete collector document.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newCappedBuffer(limit int) *cappedBuffer { return &cappedBuffer{limit: limit} }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	if originalLen > remaining {
		b.truncated = true
	}
	return originalLen, nil
}

func (b *cappedBuffer) String() string { return b.buf.String() }

func NewPool(cfg PoolConfig) *Pool {
	if cfg.DialTimeout == 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.CommandTimeout == 0 {
		cfg.CommandTimeout = 30 * time.Second
	}
	if cfg.MaxSessionsPerHost <= 0 {
		cfg.MaxSessionsPerHost = 2
	}
	if strings.TrimSpace(cfg.KnownHostsPath) == "" {
		cfg.KnownHostsPath = "~/.ssh/known_hosts"
	}
	return &Pool{
		cfg:      cfg,
		conn:     map[string]*ssh.Client{},
		limits:   map[string]chan struct{}{},
		keyCache: map[string]cachedKey{},
	}
}

func (p *Pool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, c := range p.conn {
		_ = c.Close()
		delete(p.conn, k)
	}
}

func (p *Pool) Run(ctx context.Context, tgt Target, cmd string) (string, error) {
	return p.RunWithInput(ctx, tgt, cmd, "")
}

func (p *Pool) RunWithInput(ctx context.Context, tgt Target, cmd, stdin string) (string, error) {
	release, err := p.acquire(ctx, tgt)
	if err != nil {
		return "", err
	}
	defer release()

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

	out := newCappedBuffer(maxCommandOutputBytes)
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

	timeout := p.cfg.CommandTimeout
	select {
	case err := <-done:
		if out.truncated {
			return out.String(), fmt.Errorf("ssh run %q on %s: output exceeded %d bytes", cmd, tgt.addr(), maxCommandOutputBytes)
		}
		if err != nil {
			return out.String(), fmt.Errorf("ssh run %q on %s: %w: %s", cmd, tgt.addr(), err, truncateOutput(out.String(), 4096))
		}
		return out.String(), nil
	case <-ctx.Done():
		_ = sess.Signal(ssh.SIGKILL)
		return out.String(), fmt.Errorf("ssh run %q on %s: %w", cmd, tgt.addr(), ctx.Err())
	case <-time.After(timeout):
		_ = sess.Signal(ssh.SIGKILL)
		return out.String(), fmt.Errorf("ssh run %q on %s: %w after %s", cmd, tgt.addr(), ErrTimeout, timeout)
	}
}

func truncateOutput(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}

// Stream opens a session, starts cmd, and returns its stdout as a
// StreamHandle. Unlike Run, it does NOT wait for cmd to finish — the
// caller drives the stream by reading until EOF (clean exit) or a
// non-EOF error (network drop, host reboot, etc.). The CommandTimeout
// from PoolConfig is intentionally NOT applied here; long-lived
// streams (the log protocol session is long-lived) would otherwise
// be killed at the timeout. Callers manage their own deadlines.
func (p *Pool) Stream(ctx context.Context, tgt Target, cmd string) (StreamHandle, error) {
	return p.StreamWithInput(ctx, tgt, cmd, "")
}

// StreamWithInput is Stream with a caller-owned stdin payload. Production
// uses it to execute embedded read-only scripts via `bash -s`, avoiding any
// file deployment on monitored servers.
func (p *Pool) StreamWithInput(ctx context.Context, tgt Target, cmd, stdin string) (StreamHandle, error) {
	release, err := p.acquire(ctx, tgt)
	if err != nil {
		return nil, err
	}
	keepSlot := false
	defer func() {
		if !keepSlot {
			release()
		}
	}()

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
	if stdin != "" {
		sess.Stdin = strings.NewReader(stdin)
	}
	if err := sess.Start(cmd); err != nil {
		_ = sess.Close()
		return nil, fmt.Errorf("ssh start %q on %s: %w", cmd, tgt.addr(), err)
	}
	h := &poolStream{
		sess:    sess,
		stdout:  stdout,
		addr:    tgt.addr(),
		closed:  make(chan struct{}),
		release: release,
	}
	keepSlot = true
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
	release   func()
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
		if s.release != nil {
			s.release()
		}
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

func (p *Pool) acquire(ctx context.Context, tgt Target) (func(), error) {
	p.mu.Lock()
	sem, ok := p.limits[tgt.addr()]
	if !ok {
		sem = make(chan struct{}, p.cfg.MaxSessionsPerHost)
		p.limits[tgt.addr()] = sem
	}
	p.mu.Unlock()

	select {
	case sem <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-sem }) }, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("ssh session slot %s: %w", tgt.addr(), ctx.Err())
	}
}

func (p *Pool) getOrDial(ctx context.Context, tgt Target) (*ssh.Client, error) {
	k := poolKey(tgt)
	p.mu.Lock()
	if c, ok := p.conn[k]; ok {
		p.mu.Unlock()
		return c, nil
	}
	p.mu.Unlock()

	auth, err := p.loadKey(tgt.KeyPath)
	if err != nil {
		return nil, err
	}
	hostKeyCallback, err := knownHostsCallback(p.cfg.KnownHostsPath)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            tgt.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: hostKeyCallback,
	}
	c, err := dialSSHContext(ctx, "tcp", tgt.addr(), cfg, p.cfg.DialTimeout)
	if err != nil {
		return nil, classifyDialError(tgt.addr(), err)
	}

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
	var hostKeyErr *knownhosts.KeyError
	if errors.As(err, &hostKeyErr) {
		return fmt.Errorf("ssh dial %s: %w: %v", addr, ErrHostKey, err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("ssh dial %s: %w: %v", addr, ErrTimeout, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("ssh dial %s: %w: %v", addr, ErrTimeout, err)
	}
	s := err.Error()
	if strings.Contains(s, "unable to authenticate") ||
		strings.Contains(s, "no supported methods remain") ||
		strings.Contains(s, "permission denied") {
		return fmt.Errorf("ssh dial %s: %w: %v", addr, ErrAuth, err)
	}
	return fmt.Errorf("ssh dial %s: %w: %v", addr, ErrDial, err)
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

func knownHostsCallback(path string) (ssh.HostKeyCallback, error) {
	resolved, err := expandHome(path)
	if err != nil {
		return nil, fmt.Errorf("known_hosts path: %w", err)
	}
	callback, err := knownhosts.New(resolved)
	if err != nil {
		return nil, fmt.Errorf("%w: load known_hosts %q: %v", ErrHostKey, resolved, err)
	}
	return callback, nil
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func dialSSHContext(ctx context.Context, network, addr string, cfg *ssh.ClientConfig, timeout time.Duration) (*ssh.Client, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			_ = conn.Close()
		}
	}()

	deadline := time.Now().Add(timeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	_ = conn.SetDeadline(deadline)
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-done:
		}
	}()
	cc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	close(done)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	success = true
	return ssh.NewClient(cc, chans, reqs), nil
}

func (p *Pool) loadKey(path string) (ssh.AuthMethod, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("%w: stat key %q: %v", ErrKey, path, err)
	}
	p.mu.Lock()
	cached, ok := p.keyCache[path]
	if ok && cached.size == stat.Size() && cached.modTime.Equal(stat.ModTime()) {
		p.mu.Unlock()
		return cached.auth, nil
	}
	p.mu.Unlock()

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: read key %q: %v", ErrKey, path, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: parse key %q: %v", ErrKey, path, err)
	}
	auth := ssh.PublicKeys(signer)
	p.mu.Lock()
	p.keyCache[path] = cachedKey{modTime: stat.ModTime(), size: stat.Size(), auth: auth}
	p.mu.Unlock()
	return auth, nil
}
