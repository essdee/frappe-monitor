package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// fakePusher captures the most recent body and lets tests inject errors.
type fakePusher struct {
	mu       sync.Mutex
	body     string
	pushErr  error
	calls    int
}

func (f *fakePusher) Push(_ context.Context, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.body = body
	return f.pushErr
}

func (f *fakePusher) Body() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.body
}

func (f *fakePusher) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func newTestPipeline(t *testing.T) (*Pipeline, *fakePusher, *sshpkg.FakeExecutor, storage.Store) {
	t.Helper()
	store, err := storage.OpenEntStore(context.Background(),
		"file:pipeline?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	exec := sshpkg.NewFakeExecutor()
	push := &fakePusher{}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	p := &Pipeline{Store: store, Exec: exec, Push: push, Logger: logger}
	return p, push, exec, store
}

func makeServer(t *testing.T, store storage.Store, name, host string) *storage.Server {
	t.Helper()
	srv, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: name, Hostname: host, SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	return srv
}

func loadGoldenCollectorOutput(t *testing.T) string {
	t.Helper()
	// Reuse the parser's captured fixture so the pipeline test exercises
	// real collector output.
	raw, err := os.ReadFile("../parser/testdata/server-good.txt")
	require.NoError(t, err)
	return string(raw)
}

func TestPullOnce_HappyPath(t *testing.T) {
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-1", "host-a")

	exec.SetResponse("host-a", loadGoldenCollectorOutput(t), nil)

	err := p.PullOnce(context.Background(), srv.ID)
	require.NoError(t, err)

	require.Equal(t, 1, push.Calls(), "VM client called exactly once")
	body := push.Body()
	require.NotEmpty(t, body)

	// Spot-check: at least 5 of the expected metric families are present,
	// each tagged with server=prod-1.
	for _, name := range []string{
		"frappe_server_load_1m,server=prod-1",
		"frappe_server_cpu_user,server=prod-1",
		"frappe_server_mem_total_bytes,server=prod-1",
		"frappe_server_disk_used_bytes,server=prod-1",
		"frappe_server_net_rx_bytes,server=prod-1",
	} {
		require.Contains(t, body, name)
	}

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "reachable", refreshed.Status)
	require.NotNil(t, refreshed.LastPingedAt)
	require.Empty(t, refreshed.LastError)
}

func TestPullOnce_SSHFailure(t *testing.T) {
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-2", "host-b")

	exec.SetResponse("host-b", "", errors.New("connection refused"))

	err := p.PullOnce(context.Background(), srv.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssh:")
	require.Contains(t, err.Error(), "connection refused")
	require.Equal(t, 0, push.Calls(), "no push on SSH failure")

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", refreshed.Status)
	require.Contains(t, refreshed.LastError, "ssh:")
	require.Contains(t, refreshed.LastError, "connection refused")
}

func TestPullOnce_ParseFailure(t *testing.T) {
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-3", "host-c")

	// SSH succeeds but stdout doesn't have ###END.
	exec.SetResponse("host-c", "###META\nversion=1\n###SERVER\ncpu_user=1\n", nil)

	err := p.PullOnce(context.Background(), srv.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse:")
	require.Equal(t, 0, push.Calls(), "no push on parse failure")

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", refreshed.Status)
	require.Contains(t, refreshed.LastError, "parse:")
}

func TestPullOnce_PushFailure_StillReachable(t *testing.T) {
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-4", "host-d")

	exec.SetResponse("host-d", loadGoldenCollectorOutput(t), nil)
	push.pushErr = errors.New("VM is down")

	err := p.PullOnce(context.Background(), srv.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "push:")
	require.Contains(t, err.Error(), "VM is down")
	require.Equal(t, 1, push.Calls(), "push attempted")

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "reachable", refreshed.Status,
		"VM failure must NOT mark server unreachable — server probe succeeded")
}

func TestPullOnce_ServerNotFound(t *testing.T) {
	p, _, _, _ := newTestPipeline(t)
	err := p.PullOnce(context.Background(), 99999)
	require.Error(t, err)
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestPullOnce_EmptyDisksRejected(t *testing.T) {
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-5", "host-e")

	// Construct collector output with no disk_* lines (simulates a torn read
	// or a host with no real filesystems mounted — both anomalies).
	body := []string{
		"###META", "version=1.0.0", "hostname=h", "timestamp=1", "",
		"###SERVER",
		"cpu_user=1", "cpu_nice=1", "cpu_system=1", "cpu_idle=1",
		"cpu_iowait=1", "cpu_irq=0", "cpu_softirq=0", "cpu_steal=0",
		"mem_total_kb=1000", "mem_available_kb=500",
		"mem_free_kb=500", "mem_buffers_kb=0", "mem_cached_kb=0",
		"swap_total_kb=0", "swap_free_kb=0",
		"load_1m=0", "load_5m=0", "load_15m=0",
		"uptime_seconds=42",
		`net_rx_bytes{iface="eth0"}=1`, `net_tx_bytes{iface="eth0"}=1`,
		"###END",
	}
	exec.SetResponse("host-e", strings.Join(body, "\n")+"\n", nil)

	err := p.PullOnce(context.Background(), srv.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse:")
	require.Contains(t, err.Error(), "empty disks")
	require.Equal(t, 0, push.Calls())

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", refreshed.Status)
}

func TestPullOnce_BodyContainsSSHTargetData(t *testing.T) {
	// Use srv.Name as the influx server label, NOT m.Hostname.
	// This guards against a regression where the body uses the OS-reported
	// hostname (which can drift) instead of the user-supplied stable name.
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "stable-name-123", "host-f")
	exec.SetResponse("host-f", loadGoldenCollectorOutput(t), nil)

	require.NoError(t, p.PullOnce(context.Background(), srv.ID))

	body := push.Body()
	require.Contains(t, body, "server=stable-name-123",
		"body must use srv.Name (user-supplied) as the server label, not OS hostname")
	// The captured fixture has hostname=fixture-host; it should NOT appear in any line.
	require.NotContains(t, body, "server=fixture-host")
}
