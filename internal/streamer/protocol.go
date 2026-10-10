// Package streamer is the monitor-side driver for the bench-side
// frappe-monitor-stream.sh streamer. It opens one long-lived SSH
// session per server, parses the stream protocol, fans out events to
// a Handler, and survives reconnects with byte-offset resume.
//
// Phase 8 (per-event capture). Design: docs/2026-05-07/2.md.
//
// The package is split:
//
//   - protocol.go — stateless line parser. Pure function from string
//     to Event. No I/O, no goroutines. Table-tested.
//   - handler.go  — Handler interface. The session loop calls
//     OnLine / OnFileError / OnVersion as it parses; tests pass in a
//     recording handler, production passes in *LokiSink.
//   - sink.go     — *LokiSink: batches lines per (server, file_id)
//     stream, flushes to Loki every flushInterval or maxBatch, and
//     advances the SQLite cursor on each successful flush.
//   - session.go  — runs one server's stream: dial → exec script →
//     scan lines → call handler → on disconnect, backoff and resume
//     from the cursor.
//   - manager.go  — owns N sessions, lifecycle (Start/Stop), config
//     wiring.
package streamer

import (
	"strconv"
	"strings"
)

// ProtocolVersion is shared with scripts/frappe-monitor-stream.sh. Version 9
// adds file identity and absolute offsets so reconnects and rotations cannot
// corrupt a cursor by treating bytes from different files as one sequence.
const ProtocolVersion = "9.2.0"

// EventType discriminates the parsed line. The session loop handles
// each variant; unknown lines are logged and ignored so a forward-
// compatible streamer can add new prefixes without crashing the
// monitor.
type EventType int

const (
	// EventUnknown is a line that didn't match any known prefix.
	// The session logs and skips it.
	EventUnknown EventType = iota
	// EventVersion is the first line: "##V=<semver>".
	EventVersion
	// EventOffset announces the current file identity and the byte offset
	// from which subsequent lines start: "##O=<id>|<identity>|<offset>".
	EventOffset
	// EventLine is a per-file content line with its absolute end position:
	// "##F=<id>|<identity>|<end_offset>|<content>".
	EventLine
	// EventFileError is a per-file error sentinel: "##F=<id> ##E=<token>".
	EventFileError
)

// Event is the parsed shape of one streamer-protocol line.
type Event struct {
	Type EventType

	// Version is set on EventVersion only.
	Version string

	// FileID is set on EventLine and EventFileError. It comes
	// directly from the bash --files=<id>:<path> argument, so it
	// matches whatever the operator configured for this server.
	FileID string

	// FileIdentity is the remote device/inode pair. It changes when a log is
	// rotated and is present on EventOffset and EventLine.
	FileIdentity string

	// Offset is the starting offset for EventOffset and the exclusive end
	// offset for EventLine.
	Offset int64

	// Content is set on EventLine only — the raw source line as it
	// appeared in the source log file (without the trailing LF).
	Content string

	// ErrorToken is set on EventFileError only. Stable token names:
	// MISSING, PERMISSION, TAIL_EXITED.
	ErrorToken string
}

const (
	versionPrefix    = "##V="
	offsetPrefix     = "##O="
	filePrefix       = "##F="
	errorMarker      = "##E="
	contentSeparator = '|'
)

// ParseLine maps one stream line to an Event. Stateless. Empty lines
// (which the bash streamer doesn't emit but which can appear after a
// trailing newline at EOF) become EventUnknown — the caller logs and
// skips. The function is allocation-light: only the unknown / version
// branches allocate.
func ParseLine(line string) Event {
	if line == "" {
		return Event{Type: EventUnknown}
	}

	if rest, ok := strings.CutPrefix(line, versionPrefix); ok {
		return Event{Type: EventVersion, Version: strings.TrimSpace(rest)}
	}
	if rest, ok := strings.CutPrefix(line, offsetPrefix); ok {
		parts := strings.SplitN(rest, "|", 3)
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
			return Event{Type: EventUnknown}
		}
		offset, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || offset < 0 {
			return Event{Type: EventUnknown}
		}
		return Event{
			Type:         EventOffset,
			FileID:       parts[0],
			FileIdentity: parts[1],
			Offset:       offset,
		}
	}

	if !strings.HasPrefix(line, filePrefix) {
		return Event{Type: EventUnknown}
	}

	rest := line[len(filePrefix):]

	// "##F=<id> ##E=<token>" form (no pipe). Detect by the literal
	// " ##E=" between id and the error token.
	if idx := strings.Index(rest, " "+errorMarker); idx >= 0 {
		fid := rest[:idx]
		token := rest[idx+len(" "+errorMarker):]
		return Event{
			Type:       EventFileError,
			FileID:     fid,
			ErrorToken: strings.TrimSpace(token),
		}
	}

	parts := strings.SplitN(rest, string(contentSeparator), 4)
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" {
		// Deliberately reject the pre-v9 "id|content" shape. Advancing a
		// cursor without a file identity and absolute offset is unsafe.
		return Event{Type: EventUnknown}
	}
	endOffset, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || endOffset < 0 {
		return Event{Type: EventUnknown}
	}
	return Event{
		Type:         EventLine,
		FileID:       parts[0],
		FileIdentity: parts[1],
		Offset:       endOffset,
		Content:      parts[3],
	}
}
