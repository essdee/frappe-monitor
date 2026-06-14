package streamer

import (
	"bufio"
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/realtime"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// fakeManagerStore is a minimal storage.Store for Manager tests. It
// embeds the interface (nil) so any method the Manager doesn't touch
// will panic loudly if it's ever called — only the four methods the
// Manager + sink actually exercise are overridden. Servers are keyed by
// ID; GetServer/ListServers read from the same map.
type fakeManagerStore struct {
	storage.Store // nil — unimplemented methods panic if called

	servers map[int]*storage.Server
	cursors map[string]storage.LogCursor // key: serverID|path
}

func newFakeManagerStore(servers ...*storage.Server) *fakeManagerStore {
	m := &fakeManagerStore{
		servers: map[int]*storage.Server{},
		cursors: map[string]storage.LogCursor{},
	}
	for _, s := range servers {
		m.servers[s.ID] = s
	}
	return m
}

func (m *fakeManagerStore) cursorKey(serverID int, path string) string {
	return string(rune(serverID)) + "|" + path
}

func (m *fakeManagerStore) ListServers(_ context.Context) ([]*storage.Server, error) {
	out := make([]*storage.Server, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, s)
	}
	return out, nil
}

func (m *fakeManagerStore) GetServer(_ context.Context, id int) (*storage.Server, error) {
	s, ok := m.servers[id]
	if !ok {
		return nil, storage.ErrNotFound
	}
	return s, nil
}

func (m *fakeManagerStore) GetLogCursor(_ context.Context, serverID int, path string) (*storage.LogCursor, error) {
	c, ok := m.cursors[m.cursorKey(serverID, path)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	out := c
	return &out, nil
}

func (m *fakeManagerStore) UpsertLogCursor(_ context.Context, c storage.LogCursor) error {
	m.cursors[m.cursorKey(c.ServerID, c.LogPath)] = c
	return nil
}

// newManagerForTest builds a Manager wired to the supplied executor and
// store via the public constructor, so config defaults + the sink are
// the real ones. files defaults to a single valid web.log spec when nil.
func newManagerForTest(t *testing.T, exec sshpkg.Executor, store storage.Store, files []FileSpecConfig) *Manager {
	t.Helper()
	if files == nil {
		files = []FileSpecConfig{{ID: "web", Path: "/home/frappe/web.log"}}
	}
	cfg := Config{
		Enabled:    true,
		MonitorID:  "mon-test",
		ScriptPath: "/opt/frappe-monitor/stream.sh",
		Files:      files,
		// Keep backoff tiny so reconnect-after-EOF loops don't hammer.
		MinBackoffSeconds: 0, // → session default 1s; we Stop fast anyway
		MaxBackoffSeconds: 0,
		// Start() builds its flush ticker from this BEFORE the <=0 guard,
		// so a positive value is required to avoid a NewTicker panic.
		FlushIntervalSeconds: 1,
		MaxBatchLines:        0,
		PushTimeoutSeconds:   0,
	}
	mgr, err := NewManager(cfg, exec, store, &fakeLoki{}, &fakeVM{}, quietLogger(), realtime.NopBroadcaster{})
	require.NoError(t, err)
	require.NotNil(t, mgr, "manager must be constructed when streaming is enabled")
	return mgr
}

func srv(id int, host string) *storage.Server {
	return &storage.Server{ID: id, Name: host, Hostname: host, SSHUser: "u", SSHPort: 22, SSHKeyPath: "/k"}
}

// (1) Start launches exactly one session per server; LaunchServer is a
// no-op for an already-running server; RemoveServer stops + removes.
func TestManager_StartLaunchesOneSessionPerServer(t *testing.T) {
	exec := sshpkg.NewFakeExecutor()
	// Block the stream forever so the session stays connected (one open
	// handle per server) and the loop doesn't EOF-reconnect mid-assert.
	prA, pwA := io.Pipe()
	prB, pwB := io.Pipe()
	defer pwA.Close()
	defer pwB.Close()
	exec.SetStreamReader("hostA", prA)
	exec.SetStreamReader("hostB", prB)

	store := newFakeManagerStore(srv(1, "hostA"), srv(2, "hostB"))
	mgr := newManagerForTest(t, exec, store, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, mgr.Start(ctx))
	defer mgr.Stop(2 * time.Second)

	// Exactly one Stream per server.
	require.Eventually(t, func() bool {
		return exec.StreamCallCount("hostA") >= 1 && exec.StreamCallCount("hostB") >= 1
	}, time.Second, 10*time.Millisecond, "each server should get a stream opened")
	require.Equal(t, 1, exec.StreamCallCount("hostA"))
	require.Equal(t, 1, exec.StreamCallCount("hostB"))
	require.Equal(t, 2, exec.OpenStreamCount(), "two live sessions = two open handles")
}

func TestManager_LaunchServerIsIdempotent(t *testing.T) {
	exec := sshpkg.NewFakeExecutor()
	pr, pw := io.Pipe()
	defer pw.Close()
	exec.SetStreamReader("hostA", pr)

	store := newFakeManagerStore(srv(1, "hostA"))
	mgr := newManagerForTest(t, exec, store, nil)
	defer mgr.Stop(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	require.NoError(t, mgr.LaunchServer(ctx, 1))
	require.Eventually(t, func() bool {
		return exec.StreamCallCount("hostA") >= 1
	}, time.Second, 10*time.Millisecond)

	// Second launch of the same serverID must be a no-op: no new Stream.
	require.NoError(t, mgr.LaunchServer(ctx, 1))

	// Give a relaunch a chance to (wrongly) fire, then assert the count
	// never moved past 1.
	require.Never(t, func() bool {
		return exec.StreamCallCount("hostA") > 1
	}, 200*time.Millisecond, 20*time.Millisecond,
		"re-launching an already-running server must not open a second stream")
	require.Equal(t, 1, exec.StreamCallCount("hostA"))
	require.Equal(t, 1, exec.OpenStreamCount())
}

func TestManager_RemoveServerStopsAndRemoves(t *testing.T) {
	exec := sshpkg.NewFakeExecutor()
	pr, pw := io.Pipe()
	defer pw.Close()
	exec.SetStreamReader("hostA", pr)

	store := newFakeManagerStore(srv(1, "hostA"))
	mgr := newManagerForTest(t, exec, store, nil)
	defer mgr.Stop(2 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, mgr.LaunchServer(ctx, 1))
	require.Eventually(t, func() bool {
		return exec.OpenStreamCount() == 1
	}, time.Second, 10*time.Millisecond, "session should be live before removal")

	mgr.RemoveServer(1)

	// Stream handle closed by the session shutdown.
	require.Eventually(t, func() bool {
		return exec.OpenStreamCount() == 0
	}, 2*time.Second, 20*time.Millisecond, "RemoveServer must stop the session (handle closed)")

	// And the session is gone from the map, so re-launching opens a
	// fresh stream rather than no-op'ing.
	require.NoError(t, mgr.LaunchServer(ctx, 1))
	require.Eventually(t, func() bool {
		return exec.StreamCallCount("hostA") >= 2
	}, time.Second, 10*time.Millisecond,
		"after removal a re-launch must open a new stream (entry was deleted)")
}

// (2) launchOne fails fast on a bad config (build error from
// BuildRemoteCommand) — here, a file id containing a forbidden char.
func TestManager_LaunchServerFailsFastOnBadConfig(t *testing.T) {
	exec := sshpkg.NewFakeExecutor()
	store := newFakeManagerStore(srv(1, "hostA"))
	// A forbidden char in the file id makes BuildRemoteCommand error,
	// which launchOne surfaces before spawning the session goroutine.
	mgr := newManagerForTest(t, exec, store, []FileSpecConfig{
		{ID: "we'rd", Path: "/home/frappe/web.log"},
	})
	defer mgr.Stop(time.Second)

	err := mgr.LaunchServer(context.Background(), 1)
	require.Error(t, err, "bad file id must fail fast on build")
	require.Contains(t, err.Error(), "build remote command")
	require.Equal(t, 0, exec.StreamCallCount("hostA"),
		"fail-fast means no stream is ever opened")
}

func TestManager_LaunchServerFailsFastOnEmptyScriptPath(t *testing.T) {
	exec := sshpkg.NewFakeExecutor()
	store := newFakeManagerStore(srv(1, "hostA"))
	mgr := newManagerForTest(t, exec, store, nil)
	// Reach in and blank the script path so BuildRemoteCommand errors.
	mgr.cfg.ScriptPath = ""
	defer mgr.Stop(time.Second)

	err := mgr.LaunchServer(context.Background(), 1)
	require.Error(t, err, "empty script path must fail fast on build")
	require.Contains(t, err.Error(), "build remote command")
	require.Equal(t, 0, exec.StreamCallCount("hostA"))
}

// (3) Stop returns promptly, stops all sessions, and leaves no leaked
// streams AND no lingering flush goroutine — the leak-fix verification.
func TestManager_StopReleasesSessionsAndFlushGoroutine(t *testing.T) {
	before := runtime.NumGoroutine()

	exec := sshpkg.NewFakeExecutor()
	prA, pwA := io.Pipe()
	prB, pwB := io.Pipe()
	defer pwA.Close()
	defer pwB.Close()
	exec.SetStreamReader("hostA", prA)
	exec.SetStreamReader("hostB", prB)

	store := newFakeManagerStore(srv(1, "hostA"), srv(2, "hostB"))
	mgr := newManagerForTest(t, exec, store, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	require.NoError(t, mgr.Start(ctx))

	require.Eventually(t, func() bool {
		return exec.OpenStreamCount() == 2
	}, time.Second, 10*time.Millisecond, "both sessions live before Stop")

	// Stop must return promptly (well under its own timeout).
	stopDone := make(chan struct{})
	go func() { mgr.Stop(5 * time.Second); close(stopDone) }()
	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Manager.Stop did not return promptly")
	}

	// All sessions wound down: no open stream handles.
	require.Equal(t, 0, exec.OpenStreamCount(),
		"Stop must close every session's stream handle")

	// The flush goroutine exited: flushDone is closed (Stop blocks on it
	// after cancelling flushCtx). A read must not block.
	select {
	case <-mgr.flushDone:
	default:
		t.Fatal("flush goroutine still running after Stop (flushDone not closed)")
	}

	// Goroutine count returns to baseline — the ticker+flush goroutine
	// did not leak. Allow scheduler slack via Eventually.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= before+1
	}, 2*time.Second, 20*time.Millisecond,
		"flush + session goroutines should be released after Stop")

	// Idempotent: a second Stop returns immediately without blocking.
	mgr.Stop(time.Second)
}

func TestManager_StopWithoutStartDoesNotBlock(t *testing.T) {
	exec := sshpkg.NewFakeExecutor()
	store := newFakeManagerStore()
	mgr := newManagerForTest(t, exec, store, nil)

	// flushCancel is nil because Start never ran; Stop must not block on
	// flushDone.
	done := make(chan struct{})
	go func() { mgr.Stop(time.Second); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Stop without Start blocked")
	}
}

// (4) Direct unit tests for readBoundedLine.
func TestReadBoundedLine_NormalTwoLines(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("a\nb\n"))

	line, physLen, trunc, err := readBoundedLine(r, 64)
	require.NoError(t, err)
	require.Equal(t, "a", string(line))
	require.False(t, trunc)
	require.Equal(t, 2, physLen, "physLen includes the '\\n'")

	line, physLen, trunc, err = readBoundedLine(r, 64)
	require.NoError(t, err)
	require.Equal(t, "b", string(line))
	require.False(t, trunc)
	require.Equal(t, 2, physLen)
}

func TestReadBoundedLine_TruncatesOverLongLine(t *testing.T) {
	const maxLine = 8
	// 20 'x' chars + '\n' = 21 physical bytes; only first 8 kept.
	body := strings.Repeat("x", 20) + "\n"
	r := bufio.NewReader(strings.NewReader(body))

	line, physLen, trunc, err := readBoundedLine(r, maxLine)
	require.NoError(t, err)
	require.True(t, trunc, "an over-long line must be flagged truncated")
	require.Equal(t, maxLine, len(line), "only the first maxLine bytes are kept")
	require.Equal(t, strings.Repeat("x", maxLine), string(line))
	require.Equal(t, len(body), physLen,
		"physLen must be the FULL physical length including the discarded tail and the '\\n'")
}

func TestReadBoundedLine_ReassemblesAcrossInternalChunksUpToCap(t *testing.T) {
	// A line far larger than the reader's internal buffer forces multiple
	// ReadSlice/ErrBufferFull iterations. The cap still bounds the kept
	// bytes; physLen still counts the whole physical line.
	const maxLine = 100
	const physBody = 5000 // > the small bufio buffer below
	body := strings.Repeat("y", physBody) + "\n"
	// Tiny internal buffer (16) guarantees ErrBufferFull reassembly path.
	r := bufio.NewReaderSize(strings.NewReader(body), 16)

	line, physLen, trunc, err := readBoundedLine(r, maxLine)
	require.NoError(t, err)
	require.True(t, trunc)
	require.Equal(t, maxLine, len(line), "kept bytes capped at maxLine across chunks")
	require.Equal(t, strings.Repeat("y", maxLine), string(line))
	require.Equal(t, len(body), physLen,
		"physical length reassembled across all internal chunks + '\\n'")
}

func TestReadBoundedLine_UnterminatedFinalLineReturnsEOF(t *testing.T) {
	// Final line has no trailing '\n'.
	r := bufio.NewReader(strings.NewReader("first\nlast-no-newline"))

	line, _, _, err := readBoundedLine(r, 64)
	require.NoError(t, err)
	require.Equal(t, "first", string(line))

	line, physLen, trunc, err := readBoundedLine(r, 64)
	require.ErrorIs(t, err, io.EOF, "a final unterminated line surfaces io.EOF")
	require.Equal(t, "last-no-newline", string(line),
		"the unterminated bytes are still returned alongside EOF")
	require.False(t, trunc)
	require.Equal(t, len("last-no-newline"), physLen)
}
