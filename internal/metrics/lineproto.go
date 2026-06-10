package metrics

import (
	"strconv"
	"strings"
)

// LineProtocol formats a ServerMetrics value as influx-line-protocol text
// suitable for POSTing to VictoriaMetrics' /write endpoint. The output is
// a sequence of newline-terminated lines:
//
//	metric_name[,label=value]* value=N <timestamp_ns>
//
// All metrics are emitted under the frappe_server_* prefix and carry a
// `server=<serverLabel>` tag matching the labelling convention in master
// plan §4. KB-denominated memory fields are converted to bytes (×1024)
// so the emitted metric names end in `_bytes` consistent with their unit.
func (m ServerMetrics) LineProtocol(serverLabel string) string {
	tsNs := m.Timestamp.UnixNano()
	tagsServer := "server=" + escapeTag(serverLabel)

	var b strings.Builder
	emit := func(name string, extraTags string, value string) {
		b.WriteString("frappe_server_")
		b.WriteString(name)
		b.WriteByte(',')
		b.WriteString(tagsServer)
		if extraTags != "" {
			b.WriteByte(',')
			b.WriteString(extraTags)
		}
		b.WriteString(" value=")
		b.WriteString(value)
		b.WriteByte(' ')
		b.WriteString(strconv.FormatInt(tsNs, 10))
		b.WriteByte('\n')
	}

	// CPU jiffies — raw monotonic counters.
	emit("cpu_user", "", strconv.FormatInt(m.CPUUser, 10))
	emit("cpu_nice", "", strconv.FormatInt(m.CPUNice, 10))
	emit("cpu_system", "", strconv.FormatInt(m.CPUSystem, 10))
	emit("cpu_idle", "", strconv.FormatInt(m.CPUIdle, 10))
	emit("cpu_iowait", "", strconv.FormatInt(m.CPUIOWait, 10))
	emit("cpu_irq", "", strconv.FormatInt(m.CPUIRQ, 10))
	emit("cpu_softirq", "", strconv.FormatInt(m.CPUSoftIRQ, 10))
	emit("cpu_steal", "", strconv.FormatInt(m.CPUSteal, 10))

	// Memory — convert kB → bytes (×1024) to match the metric-name unit.
	emit("mem_total_bytes", "", strconv.FormatInt(m.MemTotalKB*1024, 10))
	emit("mem_available_bytes", "", strconv.FormatInt(m.MemAvailableKB*1024, 10))
	emit("mem_free_bytes", "", strconv.FormatInt(m.MemFreeKB*1024, 10))
	emit("mem_buffers_bytes", "", strconv.FormatInt(m.MemBuffersKB*1024, 10))
	emit("mem_cached_bytes", "", strconv.FormatInt(m.MemCachedKB*1024, 10))
	emit("swap_total_bytes", "", strconv.FormatInt(m.SwapTotalKB*1024, 10))
	emit("swap_free_bytes", "", strconv.FormatInt(m.SwapFreeKB*1024, 10))

	// Load and uptime — gauges.
	emit("load_1m", "", strconv.FormatFloat(m.Load1, 'f', -1, 64))
	emit("load_5m", "", strconv.FormatFloat(m.Load5, 'f', -1, 64))
	emit("load_15m", "", strconv.FormatFloat(m.Load15, 'f', -1, 64))
	emit("uptime_seconds", "", strconv.FormatFloat(m.UptimeSeconds, 'f', -1, 64))

	// Disks — one line per (metric, mount) pair.
	for _, d := range m.Disks {
		mt := "mount=" + escapeTag(d.Mount)
		emit("disk_used_bytes", mt, strconv.FormatInt(d.UsedBytes, 10))
		emit("disk_total_bytes", mt, strconv.FormatInt(d.TotalBytes, 10))
	}

	// Network — one line per (metric, iface) pair.
	for _, n := range m.Net {
		it := "iface=" + escapeTag(n.Name)
		emit("net_rx_bytes", it, strconv.FormatInt(n.RxBytes, 10))
		emit("net_tx_bytes", it, strconv.FormatInt(n.TxBytes, 10))
	}

	return b.String()
}

// LineProtocol formats a BenchMetrics as influx-line-protocol text. Every
// line carries `server=<serverLabel>,bench=<m.Bench>` tags (plus
// queue=<name> for redis_queue_depth) per master plan §4.
func (m BenchMetrics) LineProtocol(serverLabel string) string {
	tsNs := m.Timestamp.UnixNano()
	tags := "server=" + escapeTag(serverLabel) + ",bench=" + escapeTag(m.Bench)

	var b strings.Builder
	emit := func(name string, extraTags string, value string) {
		b.WriteString("frappe_bench_")
		b.WriteString(name)
		b.WriteByte(',')
		b.WriteString(tags)
		if extraTags != "" {
			b.WriteByte(',')
			b.WriteString(extraTags)
		}
		b.WriteString(" value=")
		b.WriteString(value)
		b.WriteByte(' ')
		b.WriteString(strconv.FormatInt(tsNs, 10))
		b.WriteByte('\n')
	}

	emit("apps_count", "", strconv.FormatInt(m.AppsCount, 10))
	emit("supervisor_running", "", strconv.FormatInt(m.SupervisorRun, 10))
	emit("supervisor_total", "", strconv.FormatInt(m.SupervisorTotal, 10))

	// info-style metric so PromQL can join on frappe_version.
	if m.FrappeVersion != "" {
		emit("info", "frappe_version="+escapeTag(m.FrappeVersion), "1")
	}

	for _, q := range m.RedisQueues {
		emit("redis_queue_depth", "queue="+escapeTag(q.Name),
			strconv.FormatInt(q.Depth, 10))
	}

	return b.String()
}

// LineProtocol formats a SiteMetrics as influx-line-protocol text. Every
// line carries `server=<serverLabel>,bench=<m.Bench>,site=<m.Site>` tags
// per master plan §4.
func (m SiteMetrics) LineProtocol(serverLabel string) string {
	tsNs := m.Timestamp.UnixNano()
	tags := "server=" + escapeTag(serverLabel) +
		",bench=" + escapeTag(m.Bench) +
		",site=" + escapeTag(m.Site)

	var b strings.Builder
	emit := func(name, value string) {
		b.WriteString("frappe_site_")
		b.WriteString(name)
		b.WriteByte(',')
		b.WriteString(tags)
		b.WriteString(" value=")
		b.WriteString(value)
		b.WriteByte(' ')
		b.WriteString(strconv.FormatInt(tsNs, 10))
		b.WriteByte('\n')
	}

	emit("http_status_code", strconv.FormatInt(m.HTTPStatusCode, 10))
	emit("http_response_ms", strconv.FormatFloat(m.HTTPResponseMs, 'f', -1, 64))
	emit("is_healthy", strconv.FormatInt(m.IsHealthy, 10))

	return b.String()
}

// escapeTag escapes characters that are special in influx line protocol
// tag-key/tag-value position: backslash (must be first so it doesn't
// double-escape the others), comma, equals, space. Other characters
// (including `/` in mount paths) are safe as-is.
//
// Newline and carriage return are also stripped/escaped: the line
// protocol is newline-delimited, so a tag value containing a raw '\n'
// (e.g. an operator-set server name posted through the API) would split
// one metric line into a malformed second fragment and VM could reject
// the whole batch. Collector-derived tags can't contain newlines (the
// parser splits on them), but serverLabel comes from the server record,
// so we defend here.
var tagEscaper = strings.NewReplacer(
	`\`, `\\`,
	`,`, `\,`,
	`=`, `\=`,
	` `, `\ `,
	"\n", `\n`,
	"\r", `\r`,
)

func escapeTag(s string) string {
	return tagEscaper.Replace(s)
}
