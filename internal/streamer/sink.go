package streamer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"frappe-monitor/internal/logs"
	monitorMetrics "frappe-monitor/internal/metrics"
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

	// MaxBufferedLines and MaxBufferedBytes cap the entire sink, not each
	// stream. Without global limits, hundreds of quiet log files could each
	// retain a full per-stream batch during a backend outage.
	MaxBufferedLines int
	MaxBufferedBytes int64

	// PushTimeout caps each Loki / VM HTTP push. Default 10s.
	PushTimeout time.Duration

	// LabelResolver maps a server/file pair to stable Loki labels. The
	// manager supplies operator-facing server, bench, site, and log_type
	// labels. Tests/simple callers may omit it and get legacy id labels.
	LabelResolver func(serverID int, fileID string) map[string]string
}

// NewLokiSink builds a sink that pushes lines to Loki with canonical
// server/log_type and optional bench/site labels, advances the absolute
// (server,file_path) cursor after a successful flush, and emits stream
// health metrics to VictoriaMetrics.
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
	if cfg.MaxBufferedLines <= 0 {
		cfg.MaxBufferedLines = 5000
	}
	if cfg.MaxBufferedLines < cfg.MaxBatchLines {
		cfg.MaxBufferedLines = cfg.MaxBatchLines
	}
	if cfg.MaxBufferedBytes <= 0 {
		cfg.MaxBufferedBytes = 32 * 1024 * 1024
	}
	if cfg.PushTimeout <= 0 {
		cfg.PushTimeout = 10 * time.Second
	}
	return &LokiSink{
		cfg:         cfg,
		loki:        loki,
		vm:          vm,
		store:       store,
		resolver:    resolver,
		logger:      logger,
		streams:     map[streamKey]*streamBuf{},
		positions:   map[streamKey]ResumePosition{},
		lastSeen:    map[int]time.Time{},
		health:      map[int]bool{},
		slowParsers: map[streamKey]*monitorMetrics.SlowQueryParser{},
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

	// flushMu makes a flush a single transaction from snapshot through
	// Loki push, cursor persistence, and buffer drain. Without it, an
	// overflow flush and the periodic flush can ingest and commit the same
	// prefix twice.
	flushMu sync.Mutex
	mu      sync.Mutex
	streams map[streamKey]*streamBuf
	// positions holds the latest absolute position observed from each
	// remote stream, including buffered (not yet persisted) entries. It is
	// used to build a lossless resume command after an in-process reconnect.
	positions map[streamKey]ResumePosition
	// lastSeen: monitor wall-clock time of the most recent line
	// observed per server. Drives frappe_stream_lag_seconds.
	lastSeen map[int]time.Time
	// health: most recently reported connected state per server.
	// Drives frappe_stream_connected. Only emitted on transitions
	// (and from FlushHealth) to keep VM ingest noise low.
	health map[int]bool
	// slowParsers hold bounded multi-line parser state for database slow logs.
	// They never retain credentials or emit raw SQL to metrics.
	slowParsers map[streamKey]*monitorMetrics.SlowQueryParser
	// Global, exact counts for buffered entries and their retained line /
	// derived-metric payloads. Both are updated under mu.
	bufferedLines int
	bufferedBytes int64
}

// SinkStats is an instantaneous, lock-safe view of bounded in-memory state.
// It contains no paths, labels, log content, or credentials.
type SinkStats struct {
	Connected     int
	BufferedLines int
	BufferedBytes int64
}

func (s *LokiSink) Stats() SinkStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	connected := 0
	for _, healthy := range s.health {
		if healthy {
			connected++
		}
	}
	return SinkStats{
		Connected: connected, BufferedLines: s.bufferedLines, BufferedBytes: s.bufferedBytes,
	}
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
	labels       map[string]string
	entries      []logs.Entry
	positions    []ResumePosition // exclusive end position, parallel to entries
	metricBodies []string         // derived metric samples, parallel to entries
	derivedLogs  [][]derivedLog   // safe correlated events, parallel to entries
}

type derivedLog struct {
	labels map[string]string
	entry  logs.Entry
}

// OnVersion is currently informational only — the manager handles
// diagnose a rolling-version mismatch. We log so version drift is visible.
func (s *LokiSink) OnVersion(_ context.Context, serverID int, version string) {
	s.logger.Info("streamer: bench version", "server_id", serverID, "version", version)
}

// OnOffset establishes a file's absolute origin. An empty buffer means no
// content depends on an older cursor, so the baseline can be persisted at
// once. With buffered content we retain the new position in memory and let
// the next successful flush commit the position belonging to its last entry.
func (s *LokiSink) OnOffset(ctx context.Context, serverID int, fileID, fileIdentity string, offset int64) {
	if fileIdentity == "" || offset < 0 {
		s.logger.Warn("streamer: invalid offset event",
			"server_id", serverID, "file_id", fileID,
			"file_identity", fileIdentity, "offset", offset)
		return
	}

	key := streamKey{serverID: serverID, fileID: fileID}
	pos := ResumePosition{FileIdentity: fileIdentity, ByteOffset: offset}
	s.mu.Lock()
	previous, hadPrevious := s.positions[key]
	if !hadPrevious || previous.FileIdentity != fileIdentity || offset < previous.ByteOffset {
		delete(s.slowParsers, key)
	}
	s.positions[key] = pos
	buf := s.streams[key]
	canPersistBaseline := buf == nil || len(buf.entries) == 0
	s.mu.Unlock()

	if !canPersistBaseline {
		return
	}
	path, ok := s.resolver(serverID, fileID)
	if !ok {
		s.logger.Warn("streamer: no path resolver match; baseline not persisted",
			"server_id", serverID, "file_id", fileID)
		return
	}
	if err := s.store.UpsertLogCursor(ctx, storage.LogCursor{
		ServerID: serverID, LogPath: path, FileIdentity: fileIdentity,
		ByteOffset: offset, LastSeenAt: time.Now().UTC(),
	}); err != nil {
		s.logger.Warn("streamer: persist cursor baseline failed",
			"server_id", serverID, "path", path, "err", err)
	}
}

// OnLine appends to the per-(server, file_id) buffer. Per-stream and global
// line/byte limits are hard in-memory bounds: when a backend is unavailable,
// the SSH reader applies backpressure until the buffered prefix can be
// committed. The remote file remains durable and its cursor is not advanced.
func (s *LokiSink) OnLine(ctx context.Context, serverID int, fileID, fileIdentity string, endOffset int64, content string, observedAt time.Time) {
	key := streamKey{serverID, fileID}
	for {
		s.mu.Lock()
		buf := s.streams[key]
		perStreamFull := buf != nil && len(buf.entries) >= s.cfg.MaxBatchLines
		globalFull := s.bufferedLines >= s.cfg.MaxBufferedLines ||
			(s.bufferedLines > 0 && s.bufferedBytes+int64(len(content)) > s.cfg.MaxBufferedBytes)
		if perStreamFull || globalFull {
			s.mu.Unlock()
			if err := s.Flush(ctx); err == nil {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
				continue
			}
		}

		current, hasPosition := s.positions[key]
		if !hasPosition || current.FileIdentity != fileIdentity || endOffset <= current.ByteOffset {
			s.mu.Unlock()
			s.logger.Warn("streamer: rejected line with invalid position",
				"server_id", serverID, "file_id", fileID,
				"file_identity", fileIdentity, "end_offset", endOffset)
			return
		}
		buf, ok := s.streams[key]
		if !ok {
			initialCapacity := min(s.cfg.MaxBatchLines, 64)
			buf = &streamBuf{
				labels:       s.labelsFor(serverID, fileID),
				entries:      make([]logs.Entry, 0, initialCapacity),
				positions:    make([]ResumePosition, 0, initialCapacity),
				metricBodies: make([]string, 0, initialCapacity),
				derivedLogs:  make([][]derivedLog, 0, initialCapacity),
			}
			s.streams[key] = buf
		} else if len(buf.entries) == 0 {
			// Server names and per-file metadata may change after an API edit.
			// Refresh labels only at a batch boundary so one batch never mixes
			// old and new identities.
			buf.labels = s.labelsFor(serverID, fileID)
		}
		entryTime := observedAt
		metricBody := ""
		derivedForLine := []derivedLog(nil)
		switch buf.labels["log_type"] {
		case "monitor":
			if event, err := monitorMetrics.ParseFrappeMonitorEvent(content); err == nil {
				if !event.Timestamp.IsZero() {
					entryTime = event.Timestamp
				}
				// Native monitor timestamps have microsecond precision. The stable
				// byte-offset suffix preserves multiple equal-label events in the
				// same microsecond and remains idempotent after a retry/reconnect.
				entryTime = entryTime.Add(time.Duration(endOffset%1000) * time.Nanosecond)
				metricBody = event.LineProtocol(buf.labels["server"], buf.labels["bench"], entryTime)
			}
		case "slow-query":
			parser := s.slowParsers[key]
			if parser == nil {
				parser = &monitorMetrics.SlowQueryParser{}
				s.slowParsers[key] = parser
			}
			for index, event := range parser.Consume(content, observedAt) {
				eventTime := event.Timestamp
				if eventTime.IsZero() {
					eventTime = observedAt
				}
				// Stable offset suffix keeps retries idempotent and separates the
				// rare case of multiple parsed events on one source line.
				eventTime = eventTime.Add(time.Duration((endOffset+int64(index))%1000) * time.Nanosecond)
				metricBody += event.LineProtocol(buf.labels["server"], eventTime)
				labels := cloneLabels(buf.labels)
				labels["log_type"] = "slow-query-event"
				labels["identity_hash"] = event.IdentityHash
				if labels["identity_hash"] == "" {
					labels["identity_hash"] = "unknown"
				}
				labels["identity_kind"] = event.IdentityKind
				labels["engine"] = event.Engine
				labels["operation"] = event.Operation
				derivedForLine = append(derivedForLine, derivedLog{
					labels: labels,
					entry:  logs.Entry{Time: eventTime, Line: event.LogSummary()},
				})
			}
		}
		buf.entries = append(buf.entries, logs.Entry{Time: entryTime, Line: content})
		pos := ResumePosition{FileIdentity: fileIdentity, ByteOffset: endOffset}
		buf.positions = append(buf.positions, pos)
		buf.metricBodies = append(buf.metricBodies, metricBody)
		buf.derivedLogs = append(buf.derivedLogs, derivedForLine)
		s.bufferedLines++
		s.bufferedBytes += int64(len(content) + len(metricBody) + derivedLogBytes(derivedForLine))
		s.positions[key] = pos
		s.lastSeen[serverID] = observedAt
		full := len(buf.entries) >= s.cfg.MaxBatchLines ||
			s.bufferedLines >= s.cfg.MaxBufferedLines || s.bufferedBytes >= s.cfg.MaxBufferedBytes
		s.mu.Unlock()

		if full {
			// An initial failure leaves this full batch in memory. The next
			// OnLine call enters the backpressure loop above and cannot append
			// past the configured bound.
			_ = s.Flush(ctx)
		}
		return
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
// the next flush retries them. MaxBatchLines is enforced as a hard bound
// by OnLine, which applies SSH/TCP backpressure until this succeeds.
//
// flushAttempt records what we snapshotted for one stream so we can,
// on success, drop EXACTLY that prefix from the live buffer (without
// throwing away anything OnLine appended while the push was in
// flight). On failure we leave the live buffer untouched.
func (s *LokiSink) Flush(ctx context.Context) error {
	type flushAttempt struct {
		key      streamKey
		entryLen int // how many entries we attempted to push
		position ResumePosition
	}

	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	if len(s.streams) == 0 {
		s.mu.Unlock()
		return nil
	}
	streamsToPush := make([]logs.Stream, 0, len(s.streams))
	derivedStreams := map[string]*logs.Stream{}
	attempts := make([]flushAttempt, 0, len(s.streams))
	var metricBodies strings.Builder
	for key, buf := range s.streams {
		if len(buf.entries) == 0 {
			continue
		}
		streamsToPush = append(streamsToPush, logs.Stream{
			Labels: buf.labels,
			// Copy so a later append to buf.entries (during push)
			// doesn't mutate the slice we hand to Loki.
			Entries: append([]logs.Entry(nil), buf.entries...),
		})
		attempts = append(attempts, flushAttempt{
			key:      key,
			entryLen: len(buf.entries),
			position: buf.positions[len(buf.entries)-1],
		})
		for _, body := range buf.metricBodies[:len(buf.entries)] {
			metricBodies.WriteString(body)
		}
		for _, lineEvents := range buf.derivedLogs[:len(buf.entries)] {
			for _, event := range lineEvents {
				key := labelsKey(event.labels)
				stream := derivedStreams[key]
				if stream == nil {
					stream = &logs.Stream{Labels: cloneLabels(event.labels)}
					derivedStreams[key] = stream
				}
				stream.Entries = append(stream.Entries, event.entry)
			}
		}
	}
	derivedKeys := make([]string, 0, len(derivedStreams))
	for key := range derivedStreams {
		derivedKeys = append(derivedKeys, key)
	}
	sort.Strings(derivedKeys)
	for _, key := range derivedKeys {
		streamsToPush = append(streamsToPush, *derivedStreams[key])
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

	// Native Frappe and database slow-query records are stored as searchable raw
	// lines in Loki and bounded-cardinality samples in VictoriaMetrics. Cursor
	// advancement waits for both writes so a transient VM outage cannot create
	// a silent analytics hole. Replays use identical timestamps.
	if s.vm != nil && metricBodies.Len() > 0 {
		pushCtx, cancel = context.WithTimeout(ctx, s.cfg.PushTimeout)
		err = s.vm.Push(pushCtx, metricBodies.String())
		cancel()
		if err != nil {
			s.logger.Warn("streamer: derived metric push failed; entries retained for retry", "err", err)
			return fmt.Errorf("derived stream metrics push: %w", err)
		}
	}

	// Push succeeded. Persist each absolute cursor before draining its
	// buffer. If a cursor write fails, retaining that stream's entries
	// causes an idempotent Loki retry with the same timestamps rather than
	// risking a restart that skips uncommitted content.
	serversWithEntries := map[int]struct{}{}
	persisted := make(map[streamKey]struct{}, len(attempts))
	var persistErrs []error
	for _, a := range attempts {
		path, ok := s.resolver(a.key.serverID, a.key.fileID)
		if !ok {
			err := fmt.Errorf("no path resolver match for server %d file %q", a.key.serverID, a.key.fileID)
			persistErrs = append(persistErrs, err)
			s.logger.Warn("streamer: cursor not persisted", "err", err)
			continue
		}
		if err := s.store.UpsertLogCursor(ctx, storage.LogCursor{
			ServerID: a.key.serverID, LogPath: path,
			FileIdentity: a.position.FileIdentity,
			ByteOffset:   a.position.ByteOffset, LastSeenAt: time.Now().UTC(),
		}); err != nil {
			s.logger.Warn("streamer: upsert cursor failed",
				"server_id", a.key.serverID, "path", path, "err", err)
			persistErrs = append(persistErrs, fmt.Errorf("persist cursor %d/%s: %w", a.key.serverID, path, err))
			continue
		}
		persisted[a.key] = struct{}{}
		serversWithEntries[a.key.serverID] = struct{}{}
	}

	s.mu.Lock()
	for _, a := range attempts {
		if _, ok := persisted[a.key]; !ok {
			continue
		}
		buf, ok := s.streams[a.key]
		if !ok {
			continue
		}
		for index := 0; index < a.entryLen; index++ {
			s.bufferedBytes -= int64(len(buf.entries[index].Line) + len(buf.metricBodies[index]) + derivedLogBytes(buf.derivedLogs[index]))
		}
		s.bufferedLines -= a.entryLen
		if a.entryLen >= len(buf.entries) {
			buf.entries = buf.entries[:0]
			buf.positions = buf.positions[:0]
			buf.metricBodies = buf.metricBodies[:0]
			buf.derivedLogs = buf.derivedLogs[:0]
		} else {
			buf.entries = append(buf.entries[:0], buf.entries[a.entryLen:]...)
			buf.positions = append(buf.positions[:0], buf.positions[a.entryLen:]...)
			buf.metricBodies = append(buf.metricBodies[:0], buf.metricBodies[a.entryLen:]...)
			buf.derivedLogs = append(buf.derivedLogs[:0], buf.derivedLogs[a.entryLen:]...)
		}
	}
	s.mu.Unlock()

	// Emit lag gauges for the servers that produced entries.
	for sid := range serversWithEntries {
		s.emitLagMetric(ctx, sid, lagSnapshot[sid])
	}

	return errors.Join(persistErrs...)
}

func cloneLabels(labels map[string]string) map[string]string {
	cloned := make(map[string]string, len(labels)+4)
	for key, value := range labels {
		cloned[key] = value
	}
	return cloned
}

func labelsKey(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		out.WriteString(key)
		out.WriteByte(0)
		out.WriteString(labels[key])
		out.WriteByte(0)
	}
	return out.String()
}

func derivedLogBytes(events []derivedLog) int {
	total := 0
	for _, event := range events {
		total += len(event.entry.Line)
		for key, value := range event.labels {
			total += len(key) + len(value)
		}
	}
	return total
}

// ResumePositions returns the safest known reconnect position for each file:
// in-memory observations win over SQLite because they include lines retained
// in the sink during a backend outage. Missing cursors are omitted so the
// remote script starts a genuinely new stream at EOF.
func (s *LokiSink) ResumePositions(ctx context.Context, serverID int, files []FileSpec) (map[string]ResumePosition, error) {
	out := make(map[string]ResumePosition, len(files))
	s.mu.Lock()
	for _, file := range files {
		if pos, ok := s.positions[streamKey{serverID: serverID, fileID: file.ID}]; ok {
			out[file.ID] = pos
		}
	}
	s.mu.Unlock()

	for _, file := range files {
		if _, ok := out[file.ID]; ok {
			continue
		}
		cur, err := s.store.GetLogCursor(ctx, serverID, file.Path)
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get cursor for %s: %w", file.Path, err)
		}
		out[file.ID] = ResumePosition{
			FileIdentity: cur.FileIdentity,
			ByteOffset:   cur.ByteOffset,
		}
	}
	return out, nil
}

// labelsFor builds canonical Loki labels. Operator-provided bench/site and
// log_type metadata is resolved by the manager; file ID is the safe fallback.
func (s *LokiSink) labelsFor(serverID int, fileID string) map[string]string {
	out := map[string]string{}
	if s.cfg.LabelResolver != nil {
		for key, value := range s.cfg.LabelResolver(serverID, fileID) {
			if key != "" && value != "" {
				out[key] = value
			}
		}
	}
	if out["server"] == "" {
		out["server"] = serverIDLabel(serverID)
	}
	if out["log_type"] == "" {
		out["log_type"] = fileID
	}
	if s.cfg.MonitorID != "" {
		out["monitor"] = s.cfg.MonitorID
	}
	return out
}

func (s *LokiSink) serverLabel(serverID int) string {
	return s.labelsFor(serverID, "health")["server"]
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
		escapeTag(s.serverLabel(serverID)), val, time.Now().UnixNano())
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
		escapeTag(s.serverLabel(serverID)), lag, time.Now().UnixNano())
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
