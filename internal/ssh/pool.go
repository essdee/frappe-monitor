package ssh

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type PoolConfig struct {
	DialTimeout    time.Duration
	CommandTimeout time.Duration
}

type Pool struct {
	cfg  PoolConfig
	mu   sync.Mutex
	conn map[string]*ssh.Client // key: host:port|user|keypath
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

	var out bytes.Buffer
	sess.Stdout = &out
	sess.Stderr = &out
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
		if err != nil {
			return out.String(), fmt.Errorf("ssh run %q on %s: %w: %s", cmd, tgt.addr(), err, out.String())
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

func poolKey(tgt Target) string {
	return fmt.Sprintf("%s|%s|%s", tgt.addr(), tgt.User, tgt.KeyPath)
}

func (p *Pool) getOrDial(_ context.Context, tgt Target) (*ssh.Client, error) {
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
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // Phase 7 will add a known_hosts check.
		Timeout:         p.cfg.DialTimeout,
	}
	c, err := ssh.Dial("tcp", tgt.addr(), cfg)
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

func loadKey(path string) (ssh.AuthMethod, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key %q: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("parse key %q: %w", path, err)
	}
	return ssh.PublicKeys(signer), nil
}
