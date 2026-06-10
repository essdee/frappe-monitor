package streamer

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"frappe-monitor/internal/logs"
	"frappe-monitor/internal/realtime"
	"frappe-monitor/internal/storage"
)

// FilePathResolver answers "what's the absolute file path on the bench
// for (serverID, fileID)?". The Manager builds this from each server's
// configured --files=id:path so the sink can store cursors keyed by
// the path (matching the existing log_cursors schema).
type FilePathResolver func(serverID int, fileID string) (path string, ok bool)

// LokiPusher is the subset of *logs.LokiClient that the sink needs.
// Defined as an interface so tests can plug in a fake without the
// real HTTP client.
type LokiPusher interface {
	Push(ctx context.Context, streams []logs.Stream) error
}

// VMPusher is the subset of *metrics.VMClient that the sink needs.
// The sink emits stream-health metrics (connected gauge, lag).
type VMPusher interface {
	Push(ctx context.Context, body string) error
}

// CursorStore is the subset of storage.Store the sink needs for
// resume + cursor advancement. Decoupled from the real Store so the
// sink can be tested without sqlite.
type CursorStore interface {
	GetLogCursor(ctx context.Context, serverID int, logPath string) (*storage.LogCursor, error)
	UpsertLogCursor(ctx context.Context, c storage.LogCursor) error
}

// SinkConfig are the knobs the operator (or the manager) tunes per
// instance. All fields have safe defaults applied by NewLokiSink.
type SinkConfig struct {
	// Identifier the operator gave this monitor instance. Surfaces
	// as a Loki label and as a metrics label.
	MonitorID string

	// FlushInterval forces a Loki push at least this often, even if
	// MaxBatchLines hasn't been hit. Default 1s.
	FlushInterval time.Duration

	// MaxBatchLines flushes early when any per-stream buffer reaches
	// this many entries. Default 500.
	MaxBatchLines int

	// PushTimeout caps each Loki / VM HTTP push. Default 10s.
	PushTimeout time.Duration
}

// NewLokiSink builds a sink that pushes lines to Loki with stream
// labels {server, log_type} and advances the (server, file_path)
// cursor on each successful flush. Phase 8b.1 — bench/site labels and
// VM metric emission for content-derived counters arrive in 8b.2.
func NewLokiSink(
	cfg SinkConfig,
	loki LokiPusher,
	vm VMPusher,
	store CursorStore,
	resolver FilePathResolver,
	logger *slog.Logger,
) *LokiSink {
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = 1 * time.Second
	}
	if cfg.MaxBatchLines <= 0 {
		cfg.MaxBatchLines = 500
	}
	if cfg.PushTimeout <= 0 {
		cfg.PushTimeout = 10 * time.Second
	}
	return &LokiSink{
		cfg:      cfg,
		loki:     loki,
		vm:       vm,
		store:    store,
		resolver: resolver,
		logger:   logger,
		streams:  map[streamKey]*streamBuf{},
		bytes:    map[streamKey]int64{},
		lastSeen: map[int]time.Time{},
		health:   map[int]bool{},
	}
}

// LokiSink is the production Handler. Buffers per-(server, file_id)
// stream, flushes on a ticker or on backpressure, advances cursors
// transactionally per flush, emits health gauges to VM.
type LokiSink struct {
	cfg      SinkConfig
	loki     LokiPusher
	vm       VMPusher
	store    CursorStore
	resolver FilePathResolver
	logger   *slog.Logger

	// broadcaster, when set, pushes each tailed line to the dashboard's
	// live log feed for that server over WebSocket. Nil = no-op.
	broadcaster realtime.Broadcaster

	mu      sync.Mutex
	streams map[streamKey]*streamBuf
	// bytes: cumulative source-file bytes consumed since the last
	// successful cursor write, per stream. On flush we add these to
	// the stored cursor and reset to 0.
	bytes map[streamKey]int64
	// lastSeen: monitor wall-clock time of the most recent line
	// observed per server. Drives frappe_stream_lag_seconds.
	lastSeen map[int]time.Time
	// health: most recently reported connected state per server.
	// Drives frappe_stream_connected. Only emitted on transitions
	// (and from FlushHealth) to keep VM ingest noise low.
	health map[int]bool
}

// streamKey identifies a per-(server, file) ingest channel. The
// LokiSink's per-stream buffers and per-stream cursor-byte counters
// are both keyed by this — they always advance together, since one
// successful Loki push covers a contiguous run of bytes on disk.
type streamKey struct {
	serverID int
	fileID   string
}

type streamBuf struct {
	labels  map[string]string
	entries []logs.Entry
}

// OnVersion is currently informational only — the manager handles
// redeploy on mismatch. We log so version drift is visible.
func (s *LokiSink) OnVersion(_ context.Context, serverID int, version string) {
	s.logger.Info("streamer: bench version", "server_id", serverID, "version", version)
}

// OnLine appends to the per-(server, file_id) buffer. If we cross
// MaxBatchLines, we synchronously flush this stream's buffer so the
// session loop doesn't grow unbounded under a burst.
func (s *LokiSink) OnLine(ctx context.Context, serverID int, fileID, content string, observedAt time.Time, byteLen int) {
	s.mu.Lock()
	key := streamKey{serverID, fileID}
	buf, ok := s.streams[key]
	if !ok {
		buf = &streamBuf{
			labels:  s.labelsFor(serverID, fileID),
			entries: make([]logs.Entry, 0, s.cfg.MaxBatchLines),
		}
		s.streams[key] = buf
	}
	buf.entries = append(buf.entries, logs.Entry{Time: observedAt, Line: content})
	s.bytes[key] += int64(byteLen)
	s.lastSeen[serverID] = observedAt
	overflow := len(buf.entries) >= s.cfg.MaxBatchLines
	s.mu.Unlock()

	// Push the line to the dashboard's live feed for this server — but only
	// when someone is actually watching. Building the event for every
	// tailed line when nobody has the live tail open (the common case)
	// would burn CPU/GC on the hot ingest path; HasSubscribers short-
	// circuits that. The hub drops slow clients, so a chatty log can't
	// back-pressure ingest either.
	if s.broadcaster != nil {
		topic := realtime.TopicLogs(serverID)
		if s.broadcaster.HasSubscribers(topic) {
			s.broadcaster.Broadcast(realtime.Event{
				Type:  realtime.TypeLogLine,
				Topic: topic,
				Data: map[string]any{
					"server_id": serverID,
					"file_id":   fileID,
					"line":      content,
					"ts":        observedAt.UnixMilli(),
				},
			})
		}
	}

	if overflow {
		// Flush is best-effort; failures are logged inside Flush. We
		// don't propagate up because the session loop's job is to
		// keep reading — a Loki blip should not stall ingest.
		_ = s.Flush(ctx)
	}
}

func (s *LokiSink) OnFileError(_ context.Context, serverID int, fileID, errorToken string) {
	s.logger.Warn("streamer: per-file error",
		"server_id", serverID, "file_id", fileID, "token", errorToken)
}

func (s *LokiSink) OnSessionState(ctx context.Context, serverID int, connected bool, reason string) {
	s.mu.Lock()
	prev, hadPrev := s.health[serverID]
	s.health[serverID] = connected
	s.mu.Unlock()
	if hadPrev && prev == connected {
		// Idempotent: don't log/emit on every reconnect attempt for
		// an already-down server.
		return
	}
	if connected {
		s.logger.Info("streamer: session connected", "server_id", serverID)
	} else {
		s.logger.Warn("streamer: session disconnected",
			"server_id", serverID, "reason", reason)
	}
	s.emitHealthMetric(ctx, serverID, connected)
}

// Flush pushes every non-empty buffered stream to Loki, advances the
// per-(server, file_id) cursors on success, and emits a lag gauge
// per server with seen lines. Called on the FlushInterval ticker, on
// MaxBatchLines overflow, and at session shutdown.
//
// On Loki failure: buffered entries and byte counts are PRESERVED so
// the next flush retries them. This means a Loki outage causes the
// per-stream buffers to grow during the outage; that's intentional —
// we'd rather use monitor RAM than silently drop log lines. The
// MaxBatchLines guard is a soft ceiling, not a hard bound.
//
// flushAttempt records what we snapshotted for one stream so we can,
// on success, drop EXACTLY that prefix from the live buffer (without
// throwing away anything OnLine appended while the push was in
// flight). On failure we leave the live buffer untouched.
func (s *LokiSink) Flush(ctx context.Context) error {
	type flushAttempt struct {
		key      streamKey
		entryLen int   // how many entries we attempted to push
		bytes    int64 // bytes those entries represent in the source file
	}

	s.mu.Lock()
	if len(s.streams) == 0 {
		s.mu.Unlock()
		return nil
	}
	streamsToPush := make([]logs.Stream, 0, len(s.streams))
	attempts := make([]flushAttempt, 0, len(s.streams))
	for key, buf := range s.streams {
		if len(buf.entries) == 0 {
			continue
		}
		streamsToPush = append(streamsToPush, logs.Stream{
			Labels:  buf.labels,
			// Copy so a later append to buf.entries (during push)
			// doesn't mutate the slice we hand to Loki.
			Entries: append([]logs.Entry(nil), buf.entries...),
		})
		attempts = append(attempts, flushAttempt{
			key:      key,
			entryLen: len(buf.entries),
			bytes:    s.bytes[key],
		})
	}
	// Snapshot lastSeen for lag emission while we hold the lock.
	lagSnapshot := make(map[int]time.Time, len(s.lastSeen))
	for sid, t := range s.lastSeen {
		lagSnapshot[sid] = t
	}
	s.mu.Unlock()

	if len(streamsToPush) == 0 {
		return nil
	}

	// Push to Loki with a bounded timeout so a slow Loki doesn't
	// block the session loop forever (the caller's ctx may be the
	// monitor root, which lives for the whole process).
	pushCtx, cancel := context.WithTimeout(ctx, s.cfg.PushTimeout)
	err := s.loki.Push(pushCtx, streamsToPush)
	cancel()
	if err != nil {
		s.logger.Warn("streamer: loki push failed; entries retained for retry",
			"err", err, "stream_count", len(streamsToPush))
		return fmt.Errorf("loki push: %w", err)
	}

	// Push succeeded — drop EXACTLY the prefix we attempted from
	// each live buffer (anything OnLine appended after the snapshot
	// stays for the next flush). Drain bytes by the same amount,
	// preserving anything that arrived during the push.
	serversWithEntries := map[int]struct{}{}
	cursorBumps := make(map[streamKey]int64, len(attempts))
	s.mu.Lock()
	for _, a := range attempts {
		buf, ok := s.streams[a.key]
		if ok {
			if a.entryLen >= len(buf.entries) {
				buf.entries = buf.entries[:0]
			} else {
				// Slide remaining entries to the front. The slice
				// header keeps its capacity so steady-state ingest
				// doesn't reallocate.
				buf.entries = append(buf.entries[:0], buf.entries[a.entryLen:]...)
			}
		}
		if a.bytes > 0 {
			cursorBumps[a.key] = a.bytes
			s.bytes[a.key] -= a.bytes
		}
		serversWithEntries[a.key.serverID] = struct{}{}
	}
	s.mu.Unlock()

	// Advance every cursor we accumulated bytes for. Failures here
	// are logged but don't abort — if a cursor write fails we'll
	// re-ingest some lines on next restart, which Loki's dedup
	// handles. We don't roll back the byte map because the ingest
	// succeeded; the cursor just has a slightly stale offset.
	for key, delta := range cursorBumps {
		if delta == 0 {
			continue
		}
		path, ok := s.resolver(key.serverID, key.fileID)
		if !ok {
			s.logger.Warn("streamer: no path resolver match; cursor not advanced",
				"server_id", key.serverID, "file_id", key.fileID)
			continue
		}
		cur, err := s.store.GetLogCursor(ctx, key.serverID, path)
		var prevOff int64
		if err == nil && cur != nil {
			prevOff = cur.ByteOffset
		} else if err != nil && !isNotFound(err) {
			s.logger.Warn("streamer: get cursor failed",
				"server_id", key.serverID, "path", path, "err", err)
			continue
		}
		newOff := prevOff + delta
		if err := s.store.UpsertLogCursor(ctx, storage.LogCursor{
			ServerID:   key.serverID,
			LogPath:    path,
			ByteOffset: newOff,
			LastSeenAt: time.Now(),
		}); err != nil {
			s.logger.Warn("streamer: upsert cursor failed",
				"server_id", key.serverID, "path", path, "err", err)
		}
	}

	// Emit lag gauges for the servers that produced entries.
	for sid := range serversWithEntries {
		s.emitLagMetric(ctx, sid, lagSnapshot[sid])
	}

	return nil
}

// labelsFor builds the Loki stream labels. Phase 8b.1 only knows
// {server, log_type, monitor}; bench/site labels arrive in 8b.2 once
// per-file content parsing is in place. log_type is just the file id
// — operators choose meaningful ids in their --files configuration.
func (s *LokiSink) labelsFor(serverID int, fileID string) map[string]string {
	out := map[string]string{
		"server":   serverIDLabel(serverID),
		"log_type": fileID,
	}
	if s.cfg.MonitorID != "" {
		out["monitor"] = s.cfg.MonitorID
	}
	return out
}

func (s *LokiSink) emitHealthMetric(ctx context.Context, serverID int, connected bool) {
	if s.vm == nil {
		return
	}
	val := 0
	if connected {
		val = 1
	}
	body := fmt.Sprintf("frappe_stream_connected,server=%s value=%d %d\n",
		escapeTag(serverIDLabel(serverID)), val, time.Now().UnixNano())
	pushCtx, cancel := context.WithTimeout(ctx, s.cfg.PushTimeout)
	defer cancel()
	if err := s.vm.Push(pushCtx, body); err != nil {
		s.logger.Warn("streamer: push health metric failed",
			"server_id", serverID, "err", err)
	}
}

func (s *LokiSink) emitLagMetric(ctx context.Context, serverID int, lastSeen time.Time) {
	if s.vm == nil || lastSeen.IsZero() {
		return
	}
	lag := time.Since(lastSeen).Seconds()
	if lag < 0 {
		lag = 0
	}
	body := fmt.Sprintf("frappe_stream_lag_seconds,server=%s value=%.3f %d\n",
		escapeTag(serverIDLabel(serverID)), lag, time.Now().UnixNano())
	pushCtx, cancel := context.WithTimeout(ctx, s.cfg.PushTimeout)
	defer cancel()
	if err := s.vm.Push(pushCtx, body); err != nil {
		s.logger.Warn("streamer: push lag metric failed",
			"server_id", serverID, "err", err)
	}
}

// serverIDLabel turns the int serverID into the canonical "id-N"
// label form. We intentionally don't surface hostnames as labels —
// hostnames change and would invalidate historical series.
func serverIDLabel(id int) string {
	return fmt.Sprintf("id-%d", id)
}

// escapeTag is the Influx-line-protocol escape for a tag value:
// commas, equals, and spaces must be backslash-escaped. Reuses the
// same shape as internal/metrics/lineproto.go without importing it.
func escapeTag(s string) string {
	r := strings.NewReplacer(",", `\,`, " ", `\ `, "=", `\=`)
	return r.Replace(s)
}

// isNotFound recognises storage.ErrNotFound without importing the
// errors package directly — the cursor-store interface intentionally
// keeps storage.Store out of the LokiSink's dependency graph.
func isNotFound(err error) bool {
	return err == storage.ErrNotFound
}
