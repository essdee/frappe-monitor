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

// ServiceMetric is one stable Supervisor process or an inferred bench process.
// State: 1 running, 2 starting, 3 failed/stopped, 4 unknown.
type ServiceMetric struct {
	Name          string
	Kind          string
	Queue         string
	State         int64
	PID           int64
	CPUPercent    float64
	MemoryPercent float64
	RSSBytes      int64
	UptimeSeconds int64
}

// BenchMetrics is the parsed result of one ###BENCH:<name> section.
// Server is filled in by the pipeline (the collector script doesn't
// know its own server label).
type BenchMetrics struct {
	Timestamp time.Time
	Server    string
	Bench     string

	FrappeVersion               string // info-style; emitted as a tag, not a value
	AppsCount                   int64
	WebWorkersConfigured        int64
	BackgroundWorkersConfigured int64
	SupervisorRun               int64
	SupervisorTotal             int64
	RedisQueues                 []RedisQueue
	WorkersRegistered           int64
	WorkersBusy                 int64
	WorkersIdle                 int64
	Services                    []ServiceMetric
}

// SiteMetrics is the parsed result of one ###SITE:<bench>:<site> section.
type SiteMetrics struct {
	Timestamp time.Time
	Server    string
	Bench     string
	Site      string

	HTTPStatusCode int64
	HTTPResponseMs float64
	IsHealthy      int64 // -1 unknown/not probed, 0 unhealthy, 1 healthy
	// NativeMonitorEnabled reports whether Frappe's continuous, low-overhead
	// request/job event log is configured for this site. The collector only
	// reads this setting; it never changes the monitored host.
	NativeMonitorEnabled int64
	// DatabaseHash and DatabaseUserHash are truncated SHA-256 identities read
	// from site_config.json. Raw database/user names and passwords never leave
	// the target host. These hashes join host-level slow queries to sites.
	DatabaseHash     string
	DatabaseUserHash string
	// FailureReasonCode is stable and numeric so the time-series labels do
	// not churn as a site moves between failure modes.
	// 0=ok, 1=no response, 2=HTTP 4xx, 3=HTTP 5xx,
	// 4=not probed, 5=no web port, 6=unexpected response.
	FailureReasonCode int64
}

// PublicSiteMetrics is the monitor-side view of a site through DNS, TLS, the
// reverse proxy, and Frappe's ping endpoint. It intentionally uses separate
// metric names from SiteMetrics so operators can compare public reachability
// with the SSH collector's internal localhost result.
type PublicSiteMetrics struct {
	Timestamp              time.Time
	Server                 string
	Bench                  string
	Site                   string
	HTTPStatusCode         int64
	HTTPResponseMs         float64
	IsHealthy              int64
	FailureReasonCode      int64
	TLSCertExpiryTimestamp int64
}
