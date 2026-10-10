package ssh

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	gossh "golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestFakeExecutor_Success(t *testing.T) {
	f := NewFakeExecutor()
	f.SetResponse("host-a", "ok\n", nil)

	out, err := f.Run(context.Background(), Target{Host: "host-a"}, "echo ok")
	require.NoError(t, err)
	require.Equal(t, "ok\n", out)
	require.Equal(t, 1, f.CallCount("host-a"))
}

func TestKnownHostsCallback_VerifiesPinnedKey(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	key, err := gossh.NewPublicKey(pub)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "known_hosts")
	require.NoError(t, os.WriteFile(path,
		[]byte(knownhosts.Line([]string{"example.test"}, key)+"\n"), 0o600))

	callback, err := knownHostsCallback(path)
	require.NoError(t, err)
	require.NoError(t, callback("example.test:22", &net.TCPAddr{}, key))

	otherPub, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	otherKey, err := gossh.NewPublicKey(otherPub)
	require.NoError(t, err)
	err = callback("example.test:22", &net.TCPAddr{}, otherKey)
	require.Error(t, err)
	require.ErrorIs(t, classifyDialError("example.test:22", err), ErrHostKey)
}

func TestKnownHostsCallback_MissingFileIsHostKeyError(t *testing.T) {
	_, err := knownHostsCallback(filepath.Join(t.TempDir(), "missing"))
	require.ErrorIs(t, err, ErrHostKey)
}

func TestLoadKey_MissingFileIsKeyError(t *testing.T) {
	p := NewPool(PoolConfig{})
	_, err := p.loadKey(filepath.Join(t.TempDir(), "missing-key"))
	require.ErrorIs(t, err, ErrKey)
	require.ErrorContains(t, err, "stat key")
}

func TestCappedBufferDrainsButBoundsMemory(t *testing.T) {
	buf := newCappedBuffer(4)
	n, err := buf.Write([]byte("abcdef"))
	require.NoError(t, err)
	require.Equal(t, 6, n, "writer must report the full input drained")
	require.Equal(t, "abcd", buf.String())
	require.True(t, buf.truncated)
}

func TestPoolAcquire_EnforcesPerHostLimitAndContext(t *testing.T) {
	p := NewPool(PoolConfig{MaxSessionsPerHost: 1})
	target := Target{Host: "example.test", Port: 22}
	release, err := p.acquire(context.Background(), target)
	require.NoError(t, err)
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = p.acquire(ctx, target)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestTargetAddr_SupportsIPv6(t *testing.T) {
	require.Equal(t, "[2001:db8::1]:2222", Target{Host: "2001:db8::1", Port: 2222}.addr())
}

func TestFakeExecutor_Failure(t *testing.T) {
	f := NewFakeExecutor()
	boom := errors.New("connection refused")
	f.SetResponse("host-b", "", boom)

	_, err := f.Run(context.Background(), Target{Host: "host-b"}, "echo ok")
	require.ErrorIs(t, err, boom)
}

func TestPing_UsesExecutor(t *testing.T) {
	f := NewFakeExecutor()
	f.SetResponse("host-c", "ok\n", nil)

	lat, err := Ping(context.Background(), f, Target{Host: "host-c"})
	require.NoError(t, err)
	require.Greater(t, lat, time.Duration(0))
}

func TestPing_WrapsExecutorError(t *testing.T) {
	f := NewFakeExecutor()
	f.SetResponse("host-d", "", errors.New("auth failed"))

	_, err := Ping(context.Background(), f, Target{Host: "host-d"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "auth failed")
}

func TestFakeExecutor_RunWithInput_CapturesStdin(t *testing.T) {
	f := NewFakeExecutor()
	f.SetResponse("host-e", "ok\n", nil)

	out, err := f.RunWithInput(context.Background(), Target{Host: "host-e"},
		"cat > /tmp/x", "hello world")
	require.NoError(t, err)
	require.Equal(t, "ok\n", out)
	require.Equal(t, 1, f.CallCount("host-e"))
	require.Equal(t, "hello world", f.LastStdin("host-e"))
	require.Equal(t, "cat > /tmp/x", f.LastCmd("host-e"))
}

func TestFakeExecutor_Run_RecordsEmptyStdin(t *testing.T) {
	f := NewFakeExecutor()
	f.SetResponse("host-f", "", nil)

	_, err := f.Run(context.Background(), Target{Host: "host-f"}, "uptime")
	require.NoError(t, err)
	require.Equal(t, "", f.LastStdin("host-f"), "Run should record empty stdin")
	require.Equal(t, "uptime", f.LastCmd("host-f"))
}
