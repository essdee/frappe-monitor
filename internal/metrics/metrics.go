// Package metrics holds the typed value model that the parser produces and
// the VictoriaMetrics client consumes. Phase 2 added ServerMetrics; Phase 3
// adds BenchMetrics and SiteMetrics for the full Server → Bench → Site
// hierarchy.
package metrics

import "time"

// DiskMount is one entry from /proc/mounts (filtered) — used + total bytes.
type DiskMount struct {
	Mount      string
	UsedBytes  int64
	TotalBytes int64
}

// NetIface is one row from /proc/net/dev (excluding lo) — cumulative counters.
type NetIface struct {
	Name    string
	RxBytes int64
	TxBytes int64
}

// ServerMetrics is the parsed result of one collector cycle. CPU jiffies
// and network bytes are monotonic counters (raw values) — do not divide
// or compute deltas here. PromQL `rate()` handles that downstream.
type ServerMetrics struct {
	Timestamp        time.Time
	Hostname         string
	CollectorVersion string

	// CPU jiffies — raw counters from /proc/stat.
	CPUUser    int64
	CPUNice    int64
	CPUSystem  int64
	CPUIdle    int64
	CPUIOWait  int64
	CPUIRQ     int64
	CPUSoftIRQ int64
	CPUSteal   int64

	// Memory in kB — gauges.
	MemTotalKB     int64
	MemAvailableKB int64
	MemFreeKB      int64
	MemBuffersKB   int64
	MemCachedKB    int64
	SwapTotalKB    int64
	SwapFreeKB     int64

	// Load averages — gauges.
	Load1  float64
	Load5  float64
	Load15 float64

	// Uptime in seconds (fractional) — gauge.
	UptimeSeconds float64

	// Multi-instance gauges.
	Disks []DiskMount
	Net   []NetIface
}

// RedisQueue is one (queue_name, depth) pair from a bench's RQ.
type RedisQueue struct {
	Name  string // "short" | "default" | "long"
	Depth int64
}

// BenchMetrics is the parsed result of one ###BENCH:<name> section.
// Server is filled in by the pipeline (the collector script doesn't
// know its own server label).
type BenchMetrics struct {
	Timestamp time.Time
	Server    string
	Bench     string

	FrappeVersion   string // info-style; emitted as a tag, not a value
	AppsCount       int64
	SupervisorRun   int64
	SupervisorTotal int64
	RedisQueues     []RedisQueue
}

// SiteMetrics is the parsed result of one ###SITE:<bench>:<site> section.
type SiteMetrics struct {
	Timestamp time.Time
	Server    string
	Bench     string
	Site      string

	HTTPStatusCode int64
	HTTPResponseMs float64
	IsHealthy      int64 // 0 or 1
}
