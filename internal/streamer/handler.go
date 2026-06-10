package streamer

import (
	"context"
	"time"
)

// Handler is the consumer side of the stream. The session loop calls
// these methods as it parses lines off the wire. Handlers must be
// safe to call from a single goroutine per server (the session loop
// owns the calls) and should be cheap — anything slow here stalls
// the read loop and causes backpressure on the bench's tail pipe.
//
// In production the handler is *LokiSink (sink.go), which buffers
// per-(server, file_id) and flushes to Loki in the background. Tests
// pass in a recordingHandler.
type Handler interface {
	// OnVersion is called once at session start with the version line
	// the bench-side script declared. Handlers can compare against
	// scripts.StreamerVersion() and decide to redeploy on mismatch.
	OnVersion(ctx context.Context, serverID int, version string)

	// OnLine is called for each EventLine. observedAt is the
	// monitor's wall clock when the line arrived — used as the Loki
	// entry timestamp since the bash streamer doesn't emit per-line
	// timestamps. byteLen is len(Content)+1 (LF), the number of
	// source-file bytes this line represents.
	OnLine(ctx context.Context, serverID int, fileID, content string, observedAt time.Time, byteLen int)

	// OnFileError is called when the bash script emits a per-file
	// error sentinel (e.g. ##F=web ##E=MISSING). The session keeps
	// running for other files; this is purely informational.
	OnFileError(ctx context.Context, serverID int, fileID, errorToken string)

	// OnSessionState is called when the session transitions between
	// connected/disconnected. Used for the
	// frappe_stream_connected{server} health gauge.
	OnSessionState(ctx context.Context, serverID int, connected bool, reason string)

	// Flush forces any buffered output to its destination. Called by
	// the session before tearing down the goroutine, and by the
	// manager on shutdown. Returning an error from Flush does NOT
	// stop the shutdown — it's logged.
	Flush(ctx context.Context) error
}
