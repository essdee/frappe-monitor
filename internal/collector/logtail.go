package collector

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"frappe-monitor/internal/logs"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// DefaultMaxLogBytesPerCycle caps how many bytes a single log file may
// contribute to one tail cycle. Prevents a runaway log from filling
// Loki's request body in one shot.
const DefaultMaxLogBytesPerCycle = 1 * 1024 * 1024 // 1 MiB

// LogFile describes one bench-side log to tail. Path is the absolute
// path on the remote server; Type lands in the Loki stream's
// log_type label.
type LogFile struct {
	Path string
	Type string // "error" | "slow_query" | etc. (stable across versions)
}

// LogTailer reads incremental bytes from remote log files via SSH,
// tracks per-(server, log_path) cursors in storage, and produces
// Loki streams suitable for LokiClient.Push.
type LogTailer struct {
	Store            storage.Store
	Exec             sshpkg.Executor
	Logger           *slog.Logger
	MaxBytesPerCycle int64 // 0 → DefaultMaxLogBytesPerCycle
}

// TailResult is one file's read for one cycle.
type TailResult struct {
	NewOffset int64
	Stream    logs.Stream // empty Entries if there were no new bytes
}

// TailFile reads incremental bytes from file.Path on the target,
// builds a Loki stream tagged with the supplied labels, and returns
// the new cursor offset. Caller is responsible for pushing the stream
// to Loki and then calling Store.UpsertLogCursor with NewOffset on
// success.
//
// Rotation handling: if the remote file's current size is smaller
// than the previously-recorded offset (typical of logrotate), the
// tailer resets the offset to 0 and reads from the start of the
// (now-shorter) file.
func (t *LogTailer) TailFile(
	ctx context.Context,
	tgt sshpkg.Target,
	serverID int,
	streamLabels map[string]string,
	file LogFile,
) (TailResult, error) {
	maxBytes := t.MaxBytesPerCycle
	if maxBytes <= 0 {
		maxBytes = DefaultMaxLogBytesPerCycle
	}

	// Look up the previous cursor; ErrNotFound → offset=0.
	var prevOffset int64
	cur, err := t.Store.GetLogCursor(ctx, serverID, file.Path)
	switch {
	case err == nil:
		prevOffset = cur.ByteOffset
	case errors.Is(err, storage.ErrNotFound):
		prevOffset = 0
	default:
		return TailResult{}, fmt.Errorf("logtail: get cursor: %w", err)
	}

	// One SSH round-trip: emits "SIZE:<n>\n" then the requested bytes.
	// Using a heredoc-quoted path keeps spaces and special chars from
	// breaking the remote shell.
	remoteCmd := fmt.Sprintf(
		`set -e; f=%s; if [ ! -r "$f" ]; then echo "SIZE:0"; exit 0; fi; `+
			`sz=$(stat -c %%s -- "$f" 2>/dev/null || echo 0); echo "SIZE:$sz"; `+
			`if [ "$sz" -lt %d ]; then start=1; else start=%d; fi; `+
			`tail -c "+$start" -- "$f" 2>/dev/null | head -c %d || true`,
		shellQuote(file.Path),
		prevOffset,           // rotation marker — if size < prevOffset, start from byte 1
		prevOffset+1,         // tail -c +N is 1-indexed (1 = whole file from byte 1)
		maxBytes,
	)

	stdout, err := t.Exec.Run(ctx, tgt, remoteCmd)
	if err != nil {
		return TailResult{}, fmt.Errorf("logtail: ssh run %q: %w", file.Path, err)
	}

	size, body, err := parseTailOutput(stdout)
	if err != nil {
		return TailResult{}, fmt.Errorf("logtail: parse %q: %w", file.Path, err)
	}

	// Compute new offset.
	//
	// LIMITATION: rotation is detected by size alone (size < prevOffset).
	// If a file is rotated AND its replacement has already grown past
	// prevOffset by the next poll (busy logs at a coarse interval), this
	// misses the rotation and skips the new file's first prevOffset bytes.
	// A robust fix tracks the file's inode (stat -c %i) in the cursor and
	// resets on inode change regardless of size — that needs an inode
	// column on LogCursor. Deferred: this path is not yet wired into the
	// scheduler (the streamer is the live log path).
	var newOffset int64
	if size < prevOffset {
		// Rotation. Caller starts from 0; we read at most maxBytes worth.
		newOffset = int64(len(body))
	} else {
		// Steady state. We read at most maxBytes from prevOffset.
		consumed := int64(len(body))
		newOffset = prevOffset + consumed
		// Don't claim to have read past the file's actual size.
		if newOffset > size {
			newOffset = size
		}
	}

	stream := logs.Stream{Labels: streamLabels}
	now := time.Now().UTC()
	for _, line := range splitLines(body) {
		if line == "" {
			continue
		}
		stream.Entries = append(stream.Entries, logs.Entry{Time: now, Line: line})
	}

	if t.Logger != nil {
		t.Logger.Debug("logtail: file",
			"server_id", serverID,
			"path", file.Path,
			"prev_offset", prevOffset,
			"size", size,
			"new_offset", newOffset,
			"entries", len(stream.Entries))
	}

	return TailResult{NewOffset: newOffset, Stream: stream}, nil
}

// parseTailOutput pulls the "SIZE:<n>\n" header off the front of the
// raw SSH stdout and returns (size, remaining_bytes, err).
func parseTailOutput(raw string) (int64, string, error) {
	const prefix = "SIZE:"
	nl := strings.IndexByte(raw, '\n')
	if nl < 0 || !strings.HasPrefix(raw, prefix) {
		return 0, "", fmt.Errorf("missing SIZE header")
	}
	sizeStr := strings.TrimSpace(raw[len(prefix):nl])
	size, err := strconv.ParseInt(sizeStr, 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("malformed SIZE %q: %w", sizeStr, err)
	}
	return size, raw[nl+1:], nil
}

// splitLines is strings.Split-on-\n minus a trailing empty element.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	// Drop a single trailing empty token caused by a final "\n".
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// shellQuote wraps s in single quotes for safe interpolation into a
// remote shell command line. Embedded single quotes are escaped via
// the standard '\'' trick.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
