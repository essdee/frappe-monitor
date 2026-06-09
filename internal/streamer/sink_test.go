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
	"frappe-monitor/internal/storage"
)

// fakeLoki captures every Push call.
type fakeLoki struct {
	mu      sync.Mutex
	pushes  []logs.Stream
	failNxt error
}

func (f *fakeLoki) Push(_ context.Context, streams []logs.Stream) error {
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
	mu     sync.Mutex
	bodies []string
}

func (f *fakeVM) Push(_ context.Context, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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
		"web":   "/home/frappe/frappe-bench/logs/web.log",
		"err":   "/home/frappe/frappe-bench/logs/web.error.log",
		"slowq": "/var/log/mysql/mariadb-slow.log",
	})
	sink := NewLokiSink(SinkConfig{
		MonitorID:     "mon-test",
		FlushInterval: 500 * time.Millisecond,
		MaxBatchLines: 5,
		PushTimeout:   2 * time.Second,
	}, loki, vm, store, resolver, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return sink, loki, vm, store
}

func TestSink_FlushPushesBufferedLinesWithCorrectLabels(t *testing.T) {
	sink, loki, _, _ := newSinkForTest(t)
	now := time.Date(2026, 5, 7, 14, 21, 3, 0, time.UTC)

	sink.OnLine(context.Background(), 1, "web", "GET /api/method/foo", now, len("GET /api/method/foo")+1)
	sink.OnLine(context.Background(), 1, "web", "GET /api/method/bar", now.Add(time.Second), len("GET /api/method/bar")+1)
	sink.OnLine(context.Background(), 1, "err", "ValidationError: x", now.Add(2*time.Second), len("ValidationError: x")+1)

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

	line1 := "GET /api/method/aaaaaa"  // 22 bytes content
	line2 := "GET /api/method/bbbbbb"  // 22 bytes content
	sink.OnLine(ctx, 7, "web", line1, now, len(line1)+1) // contributes 23 source bytes
	sink.OnLine(ctx, 7, "web", line2, now, len(line2)+1) // contributes 23 source bytes

	require.NoError(t, sink.Flush(ctx))

	cur, err := store.GetLogCursor(ctx, 7, "/home/frappe/frappe-bench/logs/web.log")
	require.NoError(t, err)
	require.Equal(t, int64(100+23+23), cur.ByteOffset,
		"cursor should advance by sum of source byte lengths")
}

func TestSink_DoesNotAdvanceCursorOnLokiPushFailure(t *testing.T) {
	sink, loki, _, store := newSinkForTest(t)
	ctx := context.Background()

	loki.failNxt = errors.New("loki simulated outage")

	sink.OnLine(ctx, 7, "web", "line A", time.Now(), len("line A")+1)
	err := sink.Flush(ctx)
	require.Error(t, err, "flush should propagate the loki error")

	_, err = store.GetLogCursor(ctx, 7, "/home/frappe/frappe-bench/logs/web.log")
	require.ErrorIs(t, err, storage.ErrNotFound,
		"failed flush must not create or advance cursors — we'd lose the in-flight bytes")

	// On the next flush (now without the configured failure) the
	// previously-buffered bytes must be re-attempted, not lost.
	require.NoError(t, sink.Flush(ctx))
	cur, err := store.GetLogCursor(ctx, 7, "/home/frappe/frappe-bench/logs/web.log")
	require.NoError(t, err)
	require.Equal(t, int64(len("line A")+1), cur.ByteOffset,
		"retry after failure must advance cursor by the original byte count")
}

func TestSink_OnSessionStateEmitsHealthMetricOnTransitionsOnly(t *testing.T) {
	sink, _, vm, _ := newSinkForTest(t)
	ctx := context.Background()

	sink.OnSessionState(ctx, 3, true, "")
	sink.OnSessionState(ctx, 3, true, "")  // duplicate; should NOT emit
	sink.OnSessionState(ctx, 3, false, "ssh blip")
	sink.OnSessionState(ctx, 3, true, "")  // back up; should emit

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
	for i := 0; i < 6; i++ {
		sink.OnLine(context.Background(), 1, "web",
			fmt.Sprintf("line-%d", i), now, len("line-X")+1)
	}
	// Give the synchronous flush a beat (it's invoked from OnLine).
	require.Eventually(t, func() bool {
		return len(loki.snapshot()) > 0
	}, time.Second, 20*time.Millisecond)
}

func TestSink_LineWithEmbeddedPipeIsPreserved(t *testing.T) {
	sink, loki, _, _ := newSinkForTest(t)
	content := "WHERE name = 'foo|bar'"
	sink.OnLine(context.Background(), 1, "slowq", content, time.Now(), len(content)+1)
	require.NoError(t, sink.Flush(context.Background()))
	pushes := loki.snapshot()
	require.Len(t, pushes, 1)
	require.Len(t, pushes[0].Entries, 1)
	require.Equal(t, content, pushes[0].Entries[0].Line,
		"sink must not mangle line content even when it contains the protocol pipe")
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
