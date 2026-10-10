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
	"time"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/probes"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// fakePusher captures the most recent body and lets tests inject errors.
type fakePusher struct {
	mu      sync.Mutex
	body    string
	pushErr error
	calls   int
	bodies  []string
}

func (f *fakePusher) Push(_ context.Context, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.body = body
	f.bodies = append(f.bodies, body)
	return f.pushErr
}

type fakePublicProber struct{ results []probes.Result }

func (f fakePublicProber) ProbeSites(_ context.Context, _ []probes.Site) []probes.Result {
	return f.results
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
		"frappe_server_reachable,server=prod-1 value=1",
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

func TestPullOnce_PublicProbeIsSeparateNonFatalPhase(t *testing.T) {
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-public", "host-public")
	full, err := os.ReadFile("../parser/testdata/full-hierarchy.txt")
	require.NoError(t, err)
	exec.SetResponse("host-public", string(full), nil)
	p.Public = fakePublicProber{results: []probes.Result{{
		Server: "prod-public", Bench: "production", Site: "client-a.com",
		Timestamp: time.Unix(1714363200, 0).UTC(), HTTPStatusCode: 200,
		HTTPResponseMs: 42.5, IsHealthy: 1, FailureReasonCode: probes.ReasonOK,
	}}}

	require.NoError(t, p.PullOnce(context.Background(), srv.ID))
	require.Equal(t, 2, push.Calls(), "internal metrics must be durable before the public phase")
	require.Contains(t, push.bodies[0], "frappe_server_reachable,server=prod-public value=1")
	require.NotContains(t, push.bodies[0], "frappe_site_public_")
	require.Contains(t, push.bodies[1], "frappe_site_public_is_healthy,server=prod-public,bench=production,site=client-a.com value=1")
}

func TestPullOnce_SSHFailure(t *testing.T) {
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-2", "host-b")

	exec.SetResponse("host-b", "", errors.New("connection refused"))

	err := p.PullOnce(context.Background(), srv.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssh:")
	require.Contains(t, err.Error(), "connection refused")
	require.Equal(t, 1, push.Calls(), "SSH failure must push an explicit unreachable sample")
	require.Contains(t, push.Body(), "frappe_server_reachable,server=prod-2 value=0")

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
	require.Equal(t, 1, push.Calls(), "parse failure must push an explicit unreachable sample")
	require.Contains(t, push.Body(), "frappe_server_reachable,server=prod-3 value=0")

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", refreshed.Status)
	require.Contains(t, refreshed.LastError, "parse:")
}

func TestPullOnce_ScriptErrorSectionSurfacesExitCode(t *testing.T) {
	// When the bash collector's ERR trap fires it appends an ###ERROR
	// section with exit_code + line, then ###END. Pipeline must surface
	// those values in the wrapped error so operators see the actual
	// failure instead of a downstream "missing X" message.
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-3b", "host-c2")

	body := []string{
		"###META", "version=1.0.0", "hostname=h", "timestamp=1", "",
		"###ERROR", "exit_code=42", "line=137", "",
		"###END",
	}
	exec.SetResponse("host-c2", strings.Join(body, "\n")+"\n", nil)

	err := p.PullOnce(context.Background(), srv.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "collector script error")
	require.Contains(t, err.Error(), "exit=42")
	require.Contains(t, err.Error(), "line=137")
	require.Equal(t, 1, push.Calls())
	require.Contains(t, push.Body(), "frappe_server_reachable,server=prod-3b value=0")

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", refreshed.Status)
	require.Contains(t, refreshed.LastError, "exit=42")
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

func TestPullOnce_EmptyDisksAcceptedWhenNetPresent(t *testing.T) {
	// Was previously TestPullOnce_EmptyDisksRejected. Operators flagged
	// the old rule as too aggressive — a fully-healthy bench (CPU, mem,
	// load, network all populated) was marked "unreachable" because
	// `df` returned nothing on overlay-only / minimal containers. The
	// new rule fails ONLY when BOTH disks and net are empty (real torn
	// output). Either-empty just logs a warning and pushes whatever's
	// available.
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-5", "host-e")

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
	require.NoError(t, err, "empty disks alone is not torn output")
	require.Equal(t, 1, push.Calls(), "should still push CPU + mem + net metrics")

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "reachable", refreshed.Status)
}

func TestPullOnce_EmptyDisksAndNetRejected(t *testing.T) {
	// BOTH disks and net empty means the SERVER section is torn — no
	// real Linux host has zero mounts AND zero non-`lo` interfaces.
	// This is the actual safety check; mark unreachable so the
	// dashboard surfaces the parse failure.
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-6", "host-f")

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
		"###END",
	}
	exec.SetResponse("host-f", strings.Join(body, "\n")+"\n", nil)

	err := p.PullOnce(context.Background(), srv.ID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse:")
	require.Contains(t, err.Error(), "empty disks AND net")
	require.Equal(t, 1, push.Calls())
	require.Contains(t, push.Body(), "frappe_server_reachable,server=prod-6 value=0")

	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", refreshed.Status)
}

func loadFullHierarchyOutput(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../parser/testdata/full-hierarchy.txt")
	require.NoError(t, err)
	return string(raw)
}

func TestPullOnce_FullHierarchy_PushesBenchAndSite(t *testing.T) {
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-1", "host-h")

	exec.SetResponse("host-h", loadFullHierarchyOutput(t), nil)

	err := p.PullOnce(context.Background(), srv.ID)
	require.NoError(t, err)

	body := push.Body()
	require.NotEmpty(t, body)

	// Server-level lines still present.
	require.Contains(t, body, "frappe_server_load_1m,server=prod-1")

	// Bench-level lines present, with the right tag set.
	require.Contains(t, body,
		"frappe_bench_apps_count,server=prod-1,bench=frappe-bench")
	require.Contains(t, body,
		"frappe_bench_supervisor_running,server=prod-1,bench=frappe-bench")
	require.Contains(t, body,
		"frappe_bench_redis_queue_depth,server=prod-1,bench=frappe-bench,queue=short")

	// Site-level lines present (at least one of the *.site dirs is in
	// the captured fixture).
	require.Contains(t, body, "frappe_site_http_status_code,server=prod-1,bench=frappe-bench,site=")
	require.Contains(t, body, "frappe_site_http_response_ms,server=prod-1,bench=frappe-bench,site=")
	require.Contains(t, body, "frappe_site_is_healthy,server=prod-1,bench=frappe-bench,site=")

	// Server status persisted to reachable.
	refreshed, err := store.GetServer(context.Background(), srv.ID)
	require.NoError(t, err)
	require.Equal(t, "reachable", refreshed.Status)
}

func TestPullOnce_FullHierarchy_PerSectionParseErrorsLogged(t *testing.T) {
	// Construct an output with a valid SERVER section, a valid BENCH,
	// and a malformed BENCH (missing apps_count). The valid BENCH should
	// still appear in the push body; the malformed one should be
	// skipped without failing the whole cycle.
	p, push, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-1", "host-i")

	// Start from a known-valid full-hierarchy fixture, then surgically
	// break one of its bench sections by removing apps_count.
	good := loadFullHierarchyOutput(t)
	// Inject a second BENCH section with malformed apps_count just before ###END.
	bad := "###BENCH:malformed\napps_count=not-a-number\nsupervisor_running=0\nsupervisor_total=0\n"
	combined := strings.Replace(good, "###END", bad+"###END", 1)
	exec.SetResponse("host-i", combined, nil)

	err := p.PullOnce(context.Background(), srv.ID)
	require.NoError(t, err, "one bad bench should not fail the whole pull")

	body := push.Body()
	require.Contains(t, body, "bench=frappe-bench") // good bench landed
	require.NotContains(t, body, "bench=malformed") // bad bench skipped
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
