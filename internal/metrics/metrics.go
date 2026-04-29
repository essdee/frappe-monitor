// Package metrics holds the typed value model that the parser produces and
// the VictoriaMetrics client consumes. ServerMetrics is the Phase 2 output
// of one collector cycle; bench- and site-level metrics will be added in
// Phase 3.
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
