package api

import (
	"net/http"
	"runtime"
	"time"
)

// StreamRuntimeStatus is a credential-free snapshot of the bounded log
// ingestion subsystem. Main supplies it from streamer.Manager when enabled.
type StreamRuntimeStatus struct {
	Enabled          bool  `json:"enabled"`
	Sessions         int   `json:"sessions"`
	Connected        int   `json:"connected"`
	Launching        int   `json:"launching"`
	BufferedLines    int   `json:"buffered_lines"`
	BufferedBytes    int64 `json:"buffered_bytes"`
	MaxBufferedLines int   `json:"max_buffered_lines"`
	MaxBufferedBytes int64 `json:"max_buffered_bytes"`
}

type runtimeStatusResponse struct {
	StartedAt      time.Time           `json:"started_at"`
	UptimeSeconds  int64               `json:"uptime_seconds"`
	Goroutines     int                 `json:"goroutines"`
	HeapAllocBytes uint64              `json:"heap_alloc_bytes"`
	HeapSysBytes   uint64              `json:"heap_sys_bytes"`
	Streaming      StreamRuntimeStatus `json:"streaming"`
}

func runtimeStatusHandler(startedAt time.Time, streamStatus func() StreamRuntimeStatus) http.HandlerFunc {
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		var memory runtime.MemStats
		runtime.ReadMemStats(&memory)
		streaming := StreamRuntimeStatus{}
		if streamStatus != nil {
			streaming = streamStatus()
		}
		writeJSON(w, http.StatusOK, runtimeStatusResponse{
			StartedAt:      startedAt,
			UptimeSeconds:  max(0, int64(time.Since(startedAt).Seconds())),
			Goroutines:     runtime.NumGoroutine(),
			HeapAllocBytes: memory.HeapAlloc,
			HeapSysBytes:   memory.HeapSys,
			Streaming:      streaming,
		})
	}
}
