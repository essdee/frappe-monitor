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
	"strings"
)

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
	// EventLine is a per-file content line: "##F=<id>|<content>".
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

	// Content is set on EventLine only — the raw source line as it
	// appeared in the source log file (without the trailing LF).
	Content string

	// ErrorToken is set on EventFileError only. Stable token names:
	// MISSING, PERMISSION, TAIL_EXITED.
	ErrorToken string
}

// SourceByteLen returns how many bytes the Content represents in the
// source log file: len(Content) + 1 for the LF terminator. The
// session uses this to advance the per-file cursor as it processes
// lines. Only meaningful for EventLine.
func (e Event) SourceByteLen() int {
	if e.Type != EventLine {
		return 0
	}
	return len(e.Content) + 1
}

const (
	versionPrefix    = "##V="
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

	if !strings.HasPrefix(line, filePrefix) {
		return Event{Type: EventUnknown}
	}

	// "##F=<id>|<content>" — the FIRST '|' is the protocol delimiter;
	// any '|' after that belongs to the content. We split on the first
	// pipe, not on every pipe.
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

	pipeIdx := strings.IndexByte(rest, contentSeparator)
	if pipeIdx < 0 {
		// "##F=<id>" with no separator and no error marker — not a
		// shape this protocol version emits. Log + skip upstream.
		return Event{Type: EventUnknown}
	}
	fid := rest[:pipeIdx]
	content := rest[pipeIdx+1:]
	return Event{Type: EventLine, FileID: fid, Content: content}
}
