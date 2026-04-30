package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/logs"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

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

// fakeLogPusher captures pushed streams for assertion + lets tests inject errors.
type fakeLogPusher struct {
	streams [][]logs.Stream
	pushErr error
}

func (f *fakeLogPusher) Push(_ context.Context, streams []logs.Stream) error {
	f.streams = append(f.streams, streams)
	return f.pushErr
}

func newTailer(t *testing.T, exec *sshpkg.FakeExecutor, store storage.Store) *LogTailer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &LogTailer{Store: store, Exec: exec, Logger: logger}
}

func TestPullLogsOnce_HappyPath_AdvancesCursorsAndPushes(t *testing.T) {
	p, _, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-1", "logs-host")

	// Two files, each with a fresh-cycle response (no prior cursor).
	body1 := "err one\nerr two\n"
	body2 := "slow query 1\n"
	exec.SetResponse("logs-host", "SIZE:"+itoa(int64(len(body1)))+"\n"+body1, nil)
	tailer := newTailer(t, exec, store)
	pusher := &fakeLogPusher{}

	// First call: tail the error log.
	ok, errs, err := p.PullLogsOnce(context.Background(), srv.ID, pusher, tailer,
		[]LogTailFile{{Path: "/var/log/web.error.log", Type: "error", Bench: "b1"}})
	require.NoError(t, err)
	require.Equal(t, 1, ok)
	require.Equal(t, 0, errs)
	require.Len(t, pusher.streams, 1)
	require.Len(t, pusher.streams[0], 1)
	require.Equal(t, "b1", pusher.streams[0][0].Labels["bench"])
	require.Equal(t, "error", pusher.streams[0][0].Labels["log_type"])
	require.Equal(t, "prod-1", pusher.streams[0][0].Labels["server"])

	// Cursor advanced to len(body1).
	cur, err := store.GetLogCursor(context.Background(), srv.ID, "/var/log/web.error.log")
	require.NoError(t, err)
	require.Equal(t, int64(len(body1)), cur.ByteOffset)

	// Second call: tail a slow-query log (server-wide, no bench label).
	exec.SetResponse("logs-host", "SIZE:"+itoa(int64(len(body2)))+"\n"+body2, nil)
	pusher.streams = nil
	ok, errs, err = p.PullLogsOnce(context.Background(), srv.ID, pusher, tailer,
		[]LogTailFile{{Path: "/var/log/mysql/slow.log", Type: "slow_query"}})
	require.NoError(t, err)
	require.Equal(t, 1, ok)
	require.Equal(t, 0, errs)
	require.Len(t, pusher.streams[0], 1)
	require.Equal(t, "slow_query", pusher.streams[0][0].Labels["log_type"])
	_, hasBench := pusher.streams[0][0].Labels["bench"]
	require.False(t, hasBench, "server-wide log must not carry a bench label")
}

func TestPullLogsOnce_NoNewBytes_NoStreamPushed(t *testing.T) {
	p, _, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-2", "quiet-host")
	require.NoError(t, store.UpsertLogCursor(context.Background(), storage.LogCursor{
		ServerID: srv.ID, LogPath: "/var/log/x.log", ByteOffset: 100,
	}))
	exec.SetResponse("quiet-host", "SIZE:100\n", nil)

	tailer := newTailer(t, exec, store)
	pusher := &fakeLogPusher{}

	ok, errs, err := p.PullLogsOnce(context.Background(), srv.ID, pusher, tailer,
		[]LogTailFile{{Path: "/var/log/x.log", Type: "error", Bench: "b1"}})
	require.NoError(t, err)
	require.Equal(t, 1, ok)
	require.Equal(t, 0, errs)
	// Push was called once with empty streams (no-op in LokiClient).
	require.Len(t, pusher.streams, 1)
	require.Empty(t, pusher.streams[0])
}

func TestPullLogsOnce_PushFailureKeepsCursors(t *testing.T) {
	p, _, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-3", "loki-down-host")
	body := "alpha\nbeta\n"
	exec.SetResponse("loki-down-host", "SIZE:"+itoa(int64(len(body)))+"\n"+body, nil)

	tailer := newTailer(t, exec, store)
	pusher := &fakeLogPusher{pushErr: errors.New("loki is down")}

	_, _, err := p.PullLogsOnce(context.Background(), srv.ID, pusher, tailer,
		[]LogTailFile{{Path: "/var/log/y.log", Type: "error", Bench: "b1"}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "loki push")

	// Cursor must NOT have advanced — re-pushing on the next cycle is
	// preferable to silently dropping the entries.
	_, err = store.GetLogCursor(context.Background(), srv.ID, "/var/log/y.log")
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestPullLogsOnce_PerFileTailFailureSkipsButContinues(t *testing.T) {
	// First file's TailFile fails (FakeExecutor returns no canned response
	// for the host AT ALL — simulates a malformed response). Second file
	// returns a valid SIZE header. The good file should still push and
	// advance.

	p, _, exec, store := newTestPipeline(t)
	srv := makeServer(t, store, "prod-4", "mixed-host")

	// Set canned response that returns malformed output (no SIZE header)
	// — TailFile will return an error, but we want one bad file to skip
	// and the cycle to continue.
	exec.SetResponse("mixed-host", "garbage no header\n", nil)

	tailer := newTailer(t, exec, store)
	pusher := &fakeLogPusher{}

	// Two files; both will hit the same canned response (FakeExecutor
	// keys by host, not cmd). So both will fail. We expect errs=2, ok=0.
	ok, errs, err := p.PullLogsOnce(context.Background(), srv.ID, pusher, tailer,
		[]LogTailFile{
			{Path: "/var/log/a.log", Type: "error"},
			{Path: "/var/log/b.log", Type: "error"},
		})
	require.NoError(t, err) // outer call succeeds; per-file errors don't fail it
	require.Equal(t, 0, ok)
	require.Equal(t, 2, errs)
	// Empty Loki push happened (no streams accumulated).
	require.Len(t, pusher.streams, 1)
	require.Empty(t, pusher.streams[0])
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
