package streamer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/logs"
	monitorMetrics "frappe-monitor/internal/metrics"
	"frappe-monitor/internal/storage"
)

// fakeLoki captures every Push call.
type fakeLoki struct {
	mu      sync.Mutex
	pushes  []logs.Stream
	failNxt error
	started chan struct{}
	release chan struct{}
}

type outageLoki struct {
	mu   sync.Mutex
	down bool
}

func (f *outageLoki) Push(_ context.Context, _ []logs.Stream) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return errors.New("loki unavailable")
	}
	return nil
}

func (f *outageLoki) setDown(down bool) {
	f.mu.Lock()
	f.down = down
	f.mu.Unlock()
}

func (f *fakeLoki) Push(_ context.Context, streams []logs.Stream) error {
	if f.started != nil {
		f.started <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNxt != nil {
		err := f.failNxt
		f.failNxt = nil
		return err
	}
	for _, s := range streams {
		// Make a deep copy so later mutations on the original
		// buffer don't surprise assertions.
		entries := append([]logs.Entry(nil), s.Entries...)
		labels := map[string]string{}
		for k, v := range s.Labels {
			labels[k] = v
		}
		f.pushes = append(f.pushes, logs.Stream{Labels: labels, Entries: entries})
	}
	return nil
}

func (f *fakeLoki) snapshot() []logs.Stream {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]logs.Stream, len(f.pushes))
	copy(out, f.pushes)
	return out
}

// fakeVM captures every Push body.
type fakeVM struct {
	mu      sync.Mutex
	bodies  []string
	failNxt error
}

func (f *fakeVM) Push(_ context.Context, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNxt != nil {
		err := f.failNxt
		f.failNxt = nil
		return err
	}
	f.bodies = append(f.bodies, body)
	return nil
}

func (f *fakeVM) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.bodies))
	copy(out, f.bodies)
	return out
}

// memCursors implements CursorStore in memory.
type memCursors struct {
	mu      sync.Mutex
	cursors map[string]storage.LogCursor // key: serverID|path
}

func newMemCursors() *memCursors {
	return &memCursors{cursors: map[string]storage.LogCursor{}}
}

func (m *memCursors) key(serverID int, path string) string {
	return fmt.Sprintf("%d|%s", serverID, path)
}

func (m *memCursors) GetLogCursor(_ context.Context, serverID int, path string) (*storage.LogCursor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.cursors[m.key(serverID, path)]
	if !ok {
		return nil, storage.ErrNotFound
	}
	out := c
	return &out, nil
}

func (m *memCursors) UpsertLogCursor(_ context.Context, c storage.LogCursor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cursors[m.key(c.ServerID, c.LogPath)] = c
	return nil
}

func staticResolver(pairs map[string]string) FilePathResolver {
	return func(_ int, fileID string) (string, bool) {
		p, ok := pairs[fileID]
		return p, ok
	}
}

func newSinkForTest(t *testing.T) (*LokiSink, *fakeLoki, *fakeVM, *memCursors) {
	t.Helper()
	loki := &fakeLoki{}
	vm := &fakeVM{}
	store := newMemCursors()
	resolver := staticResolver(map[string]string{
		"web":     "/home/frappe/frappe-bench/logs/web.log",
		"err":     "/home/frappe/frappe-bench/logs/web.error.log",
		"slowq":   "/var/log/mysql/mariadb-slow.log",
		"monitor": "/home/frappe/frappe-bench/logs/monitor.json.log",
	})
	sink := NewLokiSink(SinkConfig{
		MonitorID:     "mon-test",
		FlushInterval: 500 * time.Millisecond,
		MaxBatchLines: 5,
		PushTimeout:   2 * time.Second,
	}, loki, vm, store, resolver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sink, loki, vm, store
}

func TestSink_NativeFrappeMonitorLineProducesSearchableLogAndSiteMetrics(t *testing.T) {
	loki := &fakeLoki{}
	vm := &fakeVM{}
	store := newMemCursors()
	sink := NewLokiSink(SinkConfig{LabelResolver: func(_ int, _ string) map[string]string {
		return map[string]string{"server": "prod-a", "bench": "frappe-15", "log_type": "monitor"}
	}}, loki, vm, store, staticResolver(map[string]string{
		"monitor": "/home/frappe/frappe-15/logs/monitor.json.log",
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx := context.Background()
	sink.OnOffset(ctx, 4, "monitor", "10-20", 0)
	line := `{"site":"shop.example","timestamp":"2026-08-13 09:30:12.123456+00:00","transaction_type":"request","duration":125000,"request":{"method":"POST","path":"/api/method/shop.checkout","status_code":200,"response_length":42}}`
	sink.OnLine(ctx, 4, "monitor", "10-20", 281, line, time.Date(2026, 8, 13, 9, 31, 0, 0, time.UTC))
	require.NoError(t, sink.Flush(ctx))

	pushes := loki.snapshot()
	require.Len(t, pushes, 1)
	require.Equal(t, line, pushes[0].Entries[0].Line)
	require.Equal(t, time.Date(2026, 8, 13, 9, 30, 12, 123456281, time.UTC), pushes[0].Entries[0].Time)

	bodies := strings.Join(vm.snapshot(), "")
	require.Contains(t, bodies, "frappe_site_request_duration_seconds,server=prod-a,bench=frappe-15,site=shop.example")
	require.Contains(t, bodies, "route=/api/method/shop.checkout")
	cursor := store.cursors[store.key(4, "/home/frappe/frappe-15/logs/monitor.json.log")]
	require.Equal(t, int64(281), cursor.ByteOffset)
}

func TestSink_NativeMetricFailureRetainsCursorAndRetriesIdempotently(t *testing.T) {
	loki := &fakeLoki{}
	vm := &fakeVM{failNxt: errors.New("vm unavailable")}
	store := newMemCursors()
	sink := NewLokiSink(SinkConfig{LabelResolver: func(_ int, _ string) map[string]string {
		return map[string]string{"server": "prod-a", "bench": "frappe-16", "log_type": "monitor"}
	}}, loki, vm, store, staticResolver(map[string]string{"monitor": "/logs/monitor.json.log"}),
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx := context.Background()
	sink.OnOffset(ctx, 1, "monitor", "1-1", 0)
	sink.OnLine(ctx, 1, "monitor", "1-1", 180, `{"site":"erp.example","transaction_type":"job","duration":1000,"job":{"method":"erp.run"}}`, time.Unix(100, 0).UTC())
	require.ErrorContains(t, sink.Flush(ctx), "derived stream metrics push")
	require.Equal(t, int64(0), store.cursors[store.key(1, "/logs/monitor.json.log")].ByteOffset)
	require.NoError(t, sink.Flush(ctx))
	require.Equal(t, int64(180), store.cursors[store.key(1, "/logs/monitor.json.log")].ByteOffset)
	require.Len(t, loki.snapshot(), 2, "Loki retry uses the same timestamp and is idempotent")
	require.Equal(t, loki.snapshot()[0].Entries[0].Time, loki.snapshot()[1].Entries[0].Time)
}

func TestSink_SlowQueryLinesProduceSafeDatabaseMetrics(t *testing.T) {
	loki := &fakeLoki{}
	vm := &fakeVM{}
	store := newMemCursors()
	sink := NewLokiSink(SinkConfig{LabelResolver: func(_ int, _ string) map[string]string {
		return map[string]string{"server": "local", "log_type": "slow-query"}
	}}, loki, vm, store, staticResolver(map[string]string{"slow": "/var/log/mysql/slow.log"}),
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx := context.Background()
	sink.OnOffset(ctx, 1, "slow", "9-9", 0)
	lines := []string{
		"# Time: 2026-08-13T18:15:31.123456Z",
		"# User@Host: site_user[site_user] @ localhost []",
		"# Query_time: 4.25 Lock_time: 0.1 Rows_sent: 1 Rows_examined: 99",
		"use `site_db`;",
		"SELECT name FROM tabTask /* FRAPPE_TRACE_ID: trace-secret */;",
	}
	var offset int64
	for _, line := range lines {
		offset += int64(len(line) + 1)
		sink.OnLine(ctx, 1, "slow", "9-9", offset, line, time.Now().UTC())
	}
	require.NoError(t, sink.Flush(ctx))

	body := strings.Join(vm.snapshot(), "")
	require.Contains(t, body, "frappe_database_slow_query_duration_seconds,server=local,engine=mysql")
	require.Contains(t, body, "identity_hash="+monitorMetrics.DatabaseIdentityHash("site_db"))
	require.NotContains(t, body, "site_db")
	require.NotContains(t, body, "trace-secret")
	streams := loki.snapshot()
	require.Len(t, streams, 2)
	byType := map[string]logs.Stream{}
	for _, stream := range streams {
		byType[stream.Labels["log_type"]] = stream
	}
	require.Contains(t, byType["slow-query"].Entries[4].Line, "trace-secret", "raw trace remains searchable only in Loki")
	correlated := byType["slow-query-event"]
	require.Equal(t, monitorMetrics.DatabaseIdentityHash("site_db"), correlated.Labels["identity_hash"])
	require.Equal(t, "database", correlated.Labels["identity_kind"])
	require.Equal(t, "select", correlated.Labels["operation"])
	require.Len(t, correlated.Entries, 1)
	require.JSONEq(t, `{"engine":"mysql","operation":"select","duration_seconds":4.25,"lock_seconds":0.1,"rows_sent":1,"rows_examined":99,"trace_id":"trace-secret"}`, correlated.Entries[0].Line)
	require.NotContains(t, correlated.Entries[0].Line, "site_db")
	require.NotContains(t, correlated.Entries[0].Line, "SELECT name")
	require.Equal(t, offset, store.cursors[store.key(1, "/var/log/mysql/slow.log")].ByteOffset)
}

func TestSink_FlushPushesBufferedLinesWithCorrectLabels(t *testing.T) {
	sink, loki, _, _ := newSinkForTest(t)
	now := time.Date(2026, 5, 7, 14, 21, 3, 0, time.UTC)

	ctx := context.Background()
	sink.OnOffset(ctx, 1, "web", "2049-100", 100)
	sink.OnOffset(ctx, 1, "err", "2049-200", 200)
	sink.OnLine(ctx, 1, "web", "2049-100", 120, "GET /api/method/foo", now)
	sink.OnLine(ctx, 1, "web", "2049-100", 140, "GET /api/method/bar", now.Add(time.Second))
	sink.OnLine(ctx, 1, "err", "2049-200", 219, "ValidationError: x", now.Add(2*time.Second))

	require.NoError(t, sink.Flush(context.Background()))

	pushes := loki.snapshot()
	require.Len(t, pushes, 2, "one Loki stream per (server, file_id)")

	byLogType := map[string]logs.Stream{}
	for _, s := range pushes {
		byLogType[s.Labels["log_type"]] = s
	}

	web, ok := byLogType["web"]
	require.True(t, ok)
	require.Equal(t, "id-1", web.Labels["server"])
	require.Equal(t, "mon-test", web.Labels["monitor"])
	require.Len(t, web.Entries, 2)
	require.Equal(t, "GET /api/method/foo", web.Entries[0].Line)

	err, ok := byLogType["err"]
	require.True(t, ok)
	require.Len(t, err.Entries, 1)
	require.Equal(t, "ValidationError: x", err.Entries[0].Line)
}

func TestSink_UsesHierarchyLabelsAndRefreshesThemAtBatchBoundary(t *testing.T) {
	loki := &fakeLoki{}
	store := newMemCursors()
	serverName := "prod-a"
	sink := NewLokiSink(SinkConfig{LabelResolver: func(serverID int, fileID string) map[string]string {
		return map[string]string{
			"server": serverName, "server_id": serverIDLabel(serverID),
			"bench": "frappe-bench", "site": "alpha.test", "log_type": "error",
		}
	}}, loki, nil, store, staticResolver(map[string]string{"err": "/logs/error.log"}),
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx := context.Background()
	sink.OnOffset(ctx, 4, "err", "1-1", 0)
	sink.OnLine(ctx, 4, "err", "1-1", 2, "a", time.Now())
	require.NoError(t, sink.Flush(ctx))
	first := loki.snapshot()[0].Labels
	require.Equal(t, "prod-a", first["server"])
	require.Equal(t, "id-4", first["server_id"])
	require.Equal(t, "frappe-bench", first["bench"])
	require.Equal(t, "alpha.test", first["site"])
	require.Equal(t, "error", first["log_type"])

	serverName = "prod-renamed"
	sink.OnLine(ctx, 4, "err", "1-1", 4, "b", time.Now())
	require.NoError(t, sink.Flush(ctx))
	pushes := loki.snapshot()
	require.Equal(t, "prod-renamed", pushes[1].Labels["server"])
}

func TestSink_AdvancesCursorOnSuccessfulFlush(t *testing.T) {
	sink, _, _, store := newSinkForTest(t)
	ctx := context.Background()
	now := time.Now()

	// Pre-existing cursor at byte 100. New lines add 24 bytes total
	// (each line is 23 chars + 1 LF = 24 bytes; two lines = 48). Wait,
	// let me make this clean: two 23-byte content lines + LF each =
	// 24 bytes each = 48 bytes total.
	require.NoError(t, store.UpsertLogCursor(ctx, storage.LogCursor{
		ServerID: 7, LogPath: "/home/frappe/frappe-bench/logs/web.log",
		ByteOffset: 100, LastSeenAt: now,
	}))

	sink.OnOffset(ctx, 7, "web", "2049-700", 100)
	line1 := "GET /api/method/aaaaaa"
	line2 := "GET /api/method/bbbbbb"
	sink.OnLine(ctx, 7, "web", "2049-700", 123, line1, now)
	sink.OnLine(ctx, 7, "web", "2049-700", 146, line2, now)

	require.NoError(t, sink.Flush(ctx))

	cur, err := store.GetLogCursor(ctx, 7, "/home/frappe/frappe-bench/logs/web.log")
	require.NoError(t, err)
	require.Equal(t, "2049-700", cur.FileIdentity)
	require.Equal(t, int64(146), cur.ByteOffset,
		"cursor should use the protocol's absolute end offset")
}

func TestSink_DoesNotAdvanceCursorOnLokiPushFailure(t *testing.T) {
	sink, loki, _, store := newSinkForTest(t)
	ctx := context.Background()

	loki.failNxt = errors.New("loki simulated outage")

	sink.OnOffset(ctx, 7, "web", "2049-700", 0)
	sink.OnLine(ctx, 7, "web", "2049-700", 7, "line A", time.Now())
	err := sink.Flush(ctx)
	require.Error(t, err, "flush should propagate the loki error")

	cur, err := store.GetLogCursor(ctx, 7, "/home/frappe/frappe-bench/logs/web.log")
	require.NoError(t, err)
	require.Equal(t, int64(0), cur.ByteOffset,
		"failed flush must leave the persisted cursor at its baseline")

	// On the next flush (now without the configured failure) the
	// previously-buffered bytes must be re-attempted, not lost.
	require.NoError(t, sink.Flush(ctx))
	cur, err = store.GetLogCursor(ctx, 7, "/home/frappe/frappe-bench/logs/web.log")
	require.NoError(t, err)
	require.Equal(t, int64(len("line A")+1), cur.ByteOffset,
		"retry after failure must advance cursor by the original byte count")
}

func TestSink_OnSessionStateEmitsHealthMetricOnTransitionsOnly(t *testing.T) {
	sink, _, vm, _ := newSinkForTest(t)
	ctx := context.Background()

	sink.OnSessionState(ctx, 3, true, "")
	sink.OnSessionState(ctx, 3, true, "") // duplicate; should NOT emit
	sink.OnSessionState(ctx, 3, false, "ssh blip")
	sink.OnSessionState(ctx, 3, true, "") // back up; should emit

	bodies := vm.snapshot()
	require.Len(t, bodies, 3, "three transitions, three pushes (idempotent duplicates suppressed)")
	require.Contains(t, bodies[0], "frappe_stream_connected")
	require.Contains(t, bodies[0], "value=1")
	require.Contains(t, bodies[1], "value=0")
	require.Contains(t, bodies[2], "value=1")
	require.Contains(t, bodies[0], "server=id-3")
}

func TestSink_FlushIsNoopWhenEmpty(t *testing.T) {
	sink, loki, _, _ := newSinkForTest(t)
	require.NoError(t, sink.Flush(context.Background()))
	require.Empty(t, loki.snapshot(), "no buffered lines means no push")
}

func TestSink_OverflowFlushesEarly(t *testing.T) {
	sink, loki, _, _ := newSinkForTest(t)
	// MaxBatchLines is 5 in newSinkForTest; sixth line should trigger flush.
	now := time.Now()
	sink.OnOffset(context.Background(), 1, "web", "2049-100", 0)
	var offset int64
	for i := 0; i < 6; i++ {
		offset += int64(len("line-X") + 1)
		sink.OnLine(context.Background(), 1, "web", "2049-100", offset,
			fmt.Sprintf("line-%d", i), now)
	}
	// Give the synchronous flush a beat (it's invoked from OnLine).
	require.Eventually(t, func() bool {
		return len(loki.snapshot()) > 0
	}, time.Second, 20*time.Millisecond)
}

func TestSink_OutageAppliesBackpressureAtHardBufferLimit(t *testing.T) {
	loki := &outageLoki{down: true}
	sink := NewLokiSink(SinkConfig{MaxBatchLines: 2, PushTimeout: time.Second},
		loki, nil, newMemCursors(), staticResolver(map[string]string{"web": "/logs/web.log"}),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	sink.OnOffset(ctx, 1, "web", "1-1", 0)
	sink.OnLine(ctx, 1, "web", "1-1", 2, "a", time.Now())
	sink.OnLine(ctx, 1, "web", "1-1", 4, "b", time.Now())

	done := make(chan struct{})
	go func() {
		sink.OnLine(ctx, 1, "web", "1-1", 6, "c", time.Now())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("third line must block while the full batch cannot be pushed")
	case <-time.After(100 * time.Millisecond):
	}
	sink.mu.Lock()
	require.Len(t, sink.streams[streamKey{serverID: 1, fileID: "web"}].entries, 2)
	sink.mu.Unlock()

	loki.setDown(false)
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
}

func TestSink_OutageAppliesGlobalBackpressureAcrossStreams(t *testing.T) {
	loki := &outageLoki{down: true}
	sink := NewLokiSink(SinkConfig{
		MaxBatchLines: 2, MaxBufferedLines: 2, MaxBufferedBytes: 1024,
		PushTimeout: time.Second,
	}, loki, nil, newMemCursors(), staticResolver(map[string]string{
		"a": "/logs/a.log", "b": "/logs/b.log", "c": "/logs/c.log",
	}), slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()
	for _, id := range []string{"a", "b", "c"} {
		sink.OnOffset(ctx, 1, id, "1-"+id, 0)
	}
	sink.OnLine(ctx, 1, "a", "1-a", 2, "a", time.Now())
	sink.OnLine(ctx, 1, "b", "1-b", 2, "b", time.Now())

	done := make(chan struct{})
	go func() {
		sink.OnLine(ctx, 1, "c", "1-c", 2, "c", time.Now())
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("global third line must block even though no stream reached its per-stream limit")
	case <-time.After(100 * time.Millisecond):
	}
	sink.mu.Lock()
	require.Equal(t, 2, sink.bufferedLines)
	require.Equal(t, int64(2), sink.bufferedBytes)
	sink.mu.Unlock()

	loki.setDown(false)
	require.Eventually(t, func() bool {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
}

func TestSink_LineWithEmbeddedPipeIsPreserved(t *testing.T) {
	sink, loki, _, _ := newSinkForTest(t)
	content := "WHERE name = 'foo|bar'"
	sink.OnOffset(context.Background(), 1, "slowq", "2049-300", 10)
	sink.OnLine(context.Background(), 1, "slowq", "2049-300", 34, content, time.Now())
	require.NoError(t, sink.Flush(context.Background()))
	pushes := loki.snapshot()
	require.Len(t, pushes, 1)
	require.Len(t, pushes[0].Entries, 1)
	require.Equal(t, content, pushes[0].Entries[0].Line,
		"sink must not mangle line content even when it contains the protocol pipe")
}

func TestSink_ConcurrentFlushSerializesSnapshotAndCursorCommit(t *testing.T) {
	sink, loki, _, store := newSinkForTest(t)
	loki.started = make(chan struct{}, 2)
	loki.release = make(chan struct{})
	ctx := context.Background()
	sink.OnOffset(ctx, 11, "web", "2049-1100", 100)
	sink.OnLine(ctx, 11, "web", "2049-1100", 106, "line1", time.Now())

	firstDone := make(chan error, 1)
	secondDone := make(chan error, 1)
	go func() { firstDone <- sink.Flush(ctx) }()
	select {
	case <-loki.started:
	case <-time.After(time.Second):
		t.Fatal("first flush did not reach Loki")
	}

	go func() { secondDone <- sink.Flush(ctx) }()
	select {
	case <-loki.started:
		t.Fatal("second flush reached Loki while the first was in flight")
	case <-time.After(50 * time.Millisecond):
	}

	close(loki.release)
	require.NoError(t, <-firstDone)
	require.NoError(t, <-secondDone)
	require.Len(t, loki.snapshot(), 1, "the buffered prefix must be pushed exactly once")

	cur, err := store.GetLogCursor(ctx, 11, "/home/frappe/frappe-bench/logs/web.log")
	require.NoError(t, err)
	require.Equal(t, "2049-1100", cur.FileIdentity)
	require.Equal(t, int64(106), cur.ByteOffset)
}

func TestSink_ResumePositionsPreferUnflushedMemoryAndDetectRotation(t *testing.T) {
	sink, _, _, store := newSinkForTest(t)
	ctx := context.Background()
	require.NoError(t, store.UpsertLogCursor(ctx, storage.LogCursor{
		ServerID: 12, LogPath: "/home/frappe/frappe-bench/logs/web.log",
		FileIdentity: "2049-old", ByteOffset: 500,
	}))

	sink.OnOffset(ctx, 12, "web", "2049-new", 0)
	sink.OnLine(ctx, 12, "web", "2049-new", 8, "rotated", time.Now())
	positions, err := sink.ResumePositions(ctx, 12, []FileSpec{{
		ID: "web", Path: "/home/frappe/frappe-bench/logs/web.log",
	}})
	require.NoError(t, err)
	require.Equal(t, ResumePosition{FileIdentity: "2049-new", ByteOffset: 8}, positions["web"])

	cur, err := store.GetLogCursor(ctx, 12, "/home/frappe/frappe-bench/logs/web.log")
	require.NoError(t, err)
	require.Equal(t, int64(0), cur.ByteOffset,
		"the new baseline is persisted, while its unflushed line remains memory-only")
}

func TestSink_NoVMEmitsHealthIfVMIsNil(t *testing.T) {
	store := newMemCursors()
	loki := &fakeLoki{}
	sink := NewLokiSink(SinkConfig{}, loki, nil, store,
		staticResolver(map[string]string{}),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Should not panic when vm is nil.
	sink.OnSessionState(context.Background(), 1, true, "")
}

func TestEscapeTag(t *testing.T) {
	require.Equal(t, `id-1`, escapeTag("id-1"))
	require.Equal(t, `with\,comma`, escapeTag("with,comma"))
	require.Equal(t, `with\ space`, escapeTag("with space"))
	require.Equal(t, `with\=equals`, escapeTag("with=equals"))
}

// guard against unused-import noise.
var _ = strings.Join
