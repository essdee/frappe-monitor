package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestWithoutCommandCap_MarksContext(t *testing.T) {
	ctx := context.Background()
	if hasNoCommandCap(ctx) {
		t.Fatal("a plain context must not be marked no-command-cap")
	}
	if !hasNoCommandCap(WithoutCommandCap(ctx)) {
		t.Fatal("WithoutCommandCap must mark the context")
	}
	// The marker must survive derivation (e.g. WithTimeout wrapping it).
	derived, cancel := context.WithCancel(WithoutCommandCap(ctx))
	defer cancel()
	if !hasNoCommandCap(derived) {
		t.Fatal("the marker must propagate to derived contexts")
	}
}

// dummyAddr is a minimal net.Addr for invoking host-key callbacks directly.
type dummyAddr struct{}

func (dummyAddr) Network() string { return "tcp" }
func (dummyAddr) String() string  { return "203.0.113.7:22" }

// newTestPublicKey generates a fresh ed25519 key and returns its
// ssh.PublicKey form for feeding to host-key callbacks.
func newTestPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 GenerateKey: %v", err)
	}
	pk, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh.NewPublicKey: %v", err)
	}
	return pk
}

func TestClassifyDialError_Auth(t *testing.T) {
	for _, m := range []string{
		"ssh: unable to authenticate, attempted methods [none], no supported methods remain",
		"x no supported methods remain y",
		"server says: permission denied",
	} {
		got := classifyDialError("h:22", errors.New(m))
		if !errors.Is(got, ErrAuth) {
			t.Errorf("classifyDialError(%q) = %v, want ErrAuth", m, got)
		}
		if errors.Is(got, ErrDial) {
			t.Errorf("classifyDialError(%q) also matched ErrDial", m)
		}
		// The wrapped message must still carry the address and original text.
		if !strings.Contains(got.Error(), "h:22") || !strings.Contains(got.Error(), m) {
			t.Errorf("classifyDialError(%q) lost context: %v", m, got)
		}
	}
}

func TestClassifyDialError_Dial(t *testing.T) {
	for _, m := range []string{
		"dial tcp 203.0.113.7:22: connect: connection refused",
		"dial tcp 203.0.113.7:22: i/o timeout",
		"dial tcp: lookup nope.invalid: no such host",
	} {
		got := classifyDialError("h:22", errors.New(m))
		if !errors.Is(got, ErrDial) {
			t.Errorf("classifyDialError(%q) = %v, want ErrDial", m, got)
		}
		if errors.Is(got, ErrAuth) {
			t.Errorf("classifyDialError(%q) wrongly matched ErrAuth", m)
		}
	}
}

func TestLoadKey_ReadError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist", "id_ed25519")
	_, err := loadKey(missing)
	if err == nil {
		t.Fatal("loadKey on missing path: want error, got nil")
	}
	if !strings.Contains(err.Error(), "read key") {
		t.Errorf("loadKey error = %q, want it to mention \"read key\"", err.Error())
	}
}

func TestLoadKey_ParseError(t *testing.T) {
	dir := t.TempDir()
	junk := filepath.Join(dir, "junk.key")
	if err := os.WriteFile(junk, []byte("this is not a private key at all\n"), 0o600); err != nil {
		t.Fatalf("write junk key: %v", err)
	}
	_, err := loadKey(junk)
	if err == nil {
		t.Fatal("loadKey on junk bytes: want error, got nil")
	}
	if !strings.Contains(err.Error(), "parse key") {
		t.Errorf("loadKey error = %q, want it to mention \"parse key\"", err.Error())
	}
	// Should NOT be misclassified as a read error.
	if strings.Contains(err.Error(), "read key") {
		t.Errorf("loadKey junk error wrongly mentions \"read key\": %q", err.Error())
	}
}

func TestLoadKey_PassphraseProtected(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "encrypted.key")

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 GenerateKey: %v", err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(priv, "monitor-test", []byte("hunter2"))
	if err != nil {
		t.Fatalf("MarshalPrivateKeyWithPassphrase: %v", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write encrypted key: %v", err)
	}

	_, err = loadKey(keyPath)
	if err == nil {
		t.Fatal("loadKey on encrypted key: want error, got nil")
	}
	if !strings.Contains(err.Error(), "passphrase-protected") {
		t.Errorf("loadKey encrypted error = %q, want it to mention \"passphrase-protected\"", err.Error())
	}
}

func TestLoadKey_ExpandsHome(t *testing.T) {
	// expandHomePath is applied inside loadKey: a "~/" path for a file that
	// does not exist should surface a read error referencing the original
	// (unexpanded) path in the message, proving expansion ran without panic.
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	// A nested non-existent file under home so we never read a real key.
	rel := "~/.frappe-monitor-test-nonexistent/" + filepath.Base(t.TempDir()) + "/id_ed25519"
	_, lerr := loadKey(rel)
	if lerr == nil {
		t.Fatalf("loadKey(%q): want read error, got nil", rel)
	}
	if !strings.Contains(lerr.Error(), "read key") {
		t.Errorf("loadKey(%q) = %q, want \"read key\"", rel, lerr.Error())
	}
	// Sanity: expandHomePath would have pointed under the real home dir.
	if got := expandHomePath(rel); !strings.HasPrefix(got, home) {
		t.Errorf("expandHomePath(%q) = %q, want prefix %q", rel, got, home)
	}
}

func TestHostKeyCallback_InsecureAcceptsAny(t *testing.T) {
	p := NewPool(PoolConfig{InsecureSkipHostKeyCheck: true})
	cb := p.hostKeyCallback()
	if cb == nil {
		t.Fatal("hostKeyCallback returned nil")
	}
	key := newTestPublicKey(t)
	if err := cb("anyhost", dummyAddr{}, key); err != nil {
		t.Errorf("insecure callback rejected key: %v", err)
	}
}

func TestHostKeyCallback_TOFUPinsUnknownThenAccepts(t *testing.T) {
	khPath := filepath.Join(t.TempDir(), "sub", "known_hosts") // neither file nor dir exists yet
	p := NewPool(PoolConfig{KnownHostsPath: khPath})
	cb := p.hostKeyCallback()
	key := newTestPublicKey(t)
	const host = "203.0.113.7:22"

	// First connect to an unknown host: trust-on-first-use pins the key + accepts.
	if err := cb(host, dummyAddr{}, key); err != nil {
		t.Fatalf("TOFU first connect should accept+pin, got: %v", err)
	}
	data, err := os.ReadFile(khPath)
	if err != nil || len(data) == 0 {
		t.Fatalf("expected known_hosts to be written by TOFU, err=%v len=%d", err, len(data))
	}
	// Reconnect with the SAME key: now trusted, still accepted.
	if err := cb(host, dummyAddr{}, key); err != nil {
		t.Errorf("reconnect with the pinned key should accept, got: %v", err)
	}
}

func TestHostKeyCallback_TOFURejectsChangedKey(t *testing.T) {
	khPath := filepath.Join(t.TempDir(), "known_hosts")
	p := NewPool(PoolConfig{KnownHostsPath: khPath})
	cb := p.hostKeyCallback()
	const host = "203.0.113.7:22"

	if err := cb(host, dummyAddr{}, newTestPublicKey(t)); err != nil {
		t.Fatalf("first pin should accept, got: %v", err)
	}
	// A DIFFERENT key for the same host = possible MITM → must be rejected.
	err := cb(host, dummyAddr{}, newTestPublicKey(t))
	if err == nil {
		t.Fatal("a changed host key should be rejected, got nil")
	}
	if !strings.Contains(err.Error(), "CHANGED") {
		t.Errorf("changed-key error = %q, want it to mention CHANGED", err.Error())
	}
}

func TestHostKeyCallback_FailsClosedOnUnusableStore(t *testing.T) {
	// A known_hosts path whose parent can't be created (a regular file sits
	// where the dir should be) folds into a fail-closed error rather than
	// silently disabling verification.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := NewPool(PoolConfig{KnownHostsPath: filepath.Join(blocker, "known_hosts")})
	cb := p.hostKeyCallback()
	if err := cb("203.0.113.7:22", dummyAddr{}, newTestPublicKey(t)); err == nil {
		t.Fatal("unusable known_hosts store should fail closed, got nil")
	}
}

func TestSyncBuffer_ConcurrentWriteAndString(t *testing.T) {
	b := &syncBuffer{}

	const writers = 8
	const perWriter = 200

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Reader goroutine: hammer String() while writers run.
	var rwg sync.WaitGroup
	rwg.Add(1)
	go func() {
		defer rwg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_ = b.String()
			}
		}
	}()

	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := []byte(strings.Repeat("x", 16))
			for j := 0; j < perWriter; j++ {
				if _, err := b.Write(payload); err != nil {
					t.Errorf("Write: %v", err)
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(stop)
	rwg.Wait()

	// All writes must be durably present.
	got := b.String()
	want := writers * perWriter * 16
	if len(got) != want {
		t.Errorf("buffer length = %d, want %d", len(got), want)
	}
}

func TestLoadKey_PublicKeyGivesClearError(t *testing.T) {
	dir := t.TempDir()
	// A .pub file (the common mistake) — content is an OpenSSH public key.
	pub := filepath.Join(dir, "id_ed25519.pub")
	if err := os.WriteFile(pub, []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5 monitor\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadKey(pub)
	if err == nil || !strings.Contains(err.Error(), "PUBLIC key") {
		t.Fatalf("want a clear PUBLIC-key error, got: %v", err)
	}
}
