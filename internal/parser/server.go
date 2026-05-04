package parser

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"frappe-monitor/internal/metrics"
)

// ServerFromSections builds a metrics.ServerMetrics from a tokenized Output.
// Required sections: META and SERVER. Unknown section names (e.g. Phase 3's
// BENCH:foo) are ignored so a newer collector running against an older
// monitor doesn't break the pull.
func ServerFromSections(o Output) (metrics.ServerMetrics, error) {
	meta, ok := o.Section("META")
	if !ok {
		return metrics.ServerMetrics{}, fmt.Errorf("missing META section")
	}
	server, ok := o.Section("SERVER")
	if !ok {
		return metrics.ServerMetrics{}, fmt.Errorf("missing SERVER section")
	}

	var m metrics.ServerMetrics

	// META
	if v, err := requireString(meta, "version"); err != nil {
		return m, fmt.Errorf("META: %w", err)
	} else {
		m.CollectorVersion = v
	}
	if v, err := requireString(meta, "hostname"); err != nil {
		return m, fmt.Errorf("META: %w", err)
	} else {
		m.Hostname = v
	}
	if ts, err := requireInt64(meta, "timestamp"); err != nil {
		return m, fmt.Errorf("META: %w", err)
	} else {
		m.Timestamp = time.Unix(ts, 0).UTC()
	}

	// SERVER — single-instance counters / gauges
	intFields := []struct {
		key string
		dst *int64
	}{
		{"cpu_user", &m.CPUUser},
		{"cpu_nice", &m.CPUNice},
		{"cpu_system", &m.CPUSystem},
		{"cpu_idle", &m.CPUIdle},
		{"cpu_iowait", &m.CPUIOWait},
		{"cpu_irq", &m.CPUIRQ},
		{"cpu_softirq", &m.CPUSoftIRQ},
		{"cpu_steal", &m.CPUSteal},
		{"mem_total_kb", &m.MemTotalKB},
		{"mem_available_kb", &m.MemAvailableKB},
		{"mem_free_kb", &m.MemFreeKB},
		{"mem_buffers_kb", &m.MemBuffersKB},
		{"mem_cached_kb", &m.MemCachedKB},
		{"swap_total_kb", &m.SwapTotalKB},
		{"swap_free_kb", &m.SwapFreeKB},
	}
	for _, f := range intFields {
		v, err := requireInt64(server, f.key)
		if err != nil {
			return m, fmt.Errorf("SERVER: %w", err)
		}
		*f.dst = v
	}

	// MemTotal=0 indicates a torn read or missing /proc — fail loud.
	if m.MemTotalKB <= 0 {
		return m, fmt.Errorf("SERVER: mem_total_kb must be positive, got %d", m.MemTotalKB)
	}

	floatFields := []struct {
		key string
		dst *float64
	}{
		{"load_1m", &m.Load1},
		{"load_5m", &m.Load5},
		{"load_15m", &m.Load15},
		{"uptime_seconds", &m.UptimeSeconds},
	}
	for _, f := range floatFields {
		v, err := requireFloat64(server, f.key)
		if err != nil {
			return m, fmt.Errorf("SERVER: %w", err)
		}
		*f.dst = v
	}

	// SERVER — multi-instance metrics with inline labels.
	disks, err := readDisks(server)
	if err != nil {
		return m, fmt.Errorf("SERVER disks: %w", err)
	}
	m.Disks = disks

	nets, err := readNet(server)
	if err != nil {
		return m, fmt.Errorf("SERVER net: %w", err)
	}
	m.Net = nets

	return m, nil
}

// requireString returns the string value for key in s.KVs, or an error
// if the key is absent or the value is empty.
func requireString(s Section, key string) (string, error) {
	v, ok := s.KVs[key]
	if !ok {
		return "", fmt.Errorf("missing required key %q", key)
	}
	if v == "" {
		return "", fmt.Errorf("key %q has empty value", key)
	}
	return v, nil
}

func requireInt64(s Section, key string) (int64, error) {
	v, err := requireString(s, key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("key %q: invalid int64 %q: %w", key, v, err)
	}
	return n, nil
}

func requireFloat64(s Section, key string) (float64, error) {
	v, err := requireString(s, key)
	if err != nil {
		return 0, err
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("key %q: invalid float64 %q: %w", key, v, err)
	}
	return f, nil
}

// readDisks scans the SERVER section for keys matching the
// disk_used_bytes{mount="..."} / disk_total_bytes{mount="..."} pair shape
// and assembles []metrics.DiskMount. A used-without-total or total-without-used
// is an error (torn output).
func readDisks(s Section) ([]metrics.DiskMount, error) {
	type acc struct {
		used, total int64
		hasUsed     bool
		hasTotal    bool
	}
	by := map[string]*acc{}
	for k, v := range s.KVs {
		base, labels, ok := splitLabeledKey(k)
		if !ok {
			continue
		}
		mount, has := labels["mount"]
		if !has {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("disk metric %q: invalid int64 %q: %w", k, v, err)
		}
		entry := by[mount]
		if entry == nil {
			entry = &acc{}
			by[mount] = entry
		}
		switch base {
		case "disk_used_bytes":
			entry.used, entry.hasUsed = n, true
		case "disk_total_bytes":
			entry.total, entry.hasTotal = n, true
		}
	}
	out := make([]metrics.DiskMount, 0, len(by))
	for mount, a := range by {
		switch {
		case !a.hasUsed && !a.hasTotal:
			return nil, fmt.Errorf("disk %q missing both used and total", mount)
		case !a.hasUsed:
			return nil, fmt.Errorf("disk %q missing disk_used_bytes", mount)
		case !a.hasTotal:
			return nil, fmt.Errorf("disk %q missing disk_total_bytes", mount)
		}
		out = append(out, metrics.DiskMount{Mount: mount, UsedBytes: a.used, TotalBytes: a.total})
	}
	return out, nil
}

// readNet does the symmetric job for net_rx_bytes / net_tx_bytes per iface.
func readNet(s Section) ([]metrics.NetIface, error) {
	type acc struct {
		rx, tx int64
		hasRx  bool
		hasTx  bool
	}
	by := map[string]*acc{}
	for k, v := range s.KVs {
		base, labels, ok := splitLabeledKey(k)
		if !ok {
			continue
		}
		iface, has := labels["iface"]
		if !has {
			continue
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("net metric %q: invalid int64 %q: %w", k, v, err)
		}
		entry := by[iface]
		if entry == nil {
			entry = &acc{}
			by[iface] = entry
		}
		switch base {
		case "net_rx_bytes":
			entry.rx, entry.hasRx = n, true
		case "net_tx_bytes":
			entry.tx, entry.hasTx = n, true
		}
	}
	out := make([]metrics.NetIface, 0, len(by))
	for name, a := range by {
		switch {
		case !a.hasRx && !a.hasTx:
			return nil, fmt.Errorf("iface %q missing both rx and tx", name)
		case !a.hasRx:
			return nil, fmt.Errorf("iface %q missing net_rx_bytes", name)
		case !a.hasTx:
			return nil, fmt.Errorf("iface %q missing net_tx_bytes", name)
		}
		out = append(out, metrics.NetIface{Name: name, RxBytes: a.rx, TxBytes: a.tx})
	}
	return out, nil
}

// splitLabeledKey splits a key like `disk_used_bytes{mount="/"}` into
// the base name `disk_used_bytes` and the labels map {"mount":"/"}.
// For bare keys like `cpu_user`, returns base + nil labels + ok=false to
// signal "not a labeled key" so callers can skip them.
//
// Label values are required to be double-quoted; multiple labels are
// comma-separated. The collector script always emits this shape.
func splitLabeledKey(k string) (base string, labels map[string]string, ok bool) {
	open := strings.Index(k, "{")
	if open < 0 {
		return k, nil, false
	}
	if !strings.HasSuffix(k, "}") {
		return k, nil, false
	}
	base = k[:open]
	body := k[open+1 : len(k)-1]
	if body == "" {
		return base, map[string]string{}, true
	}
	labels = map[string]string{}
	// Comma-separated label=value pairs; values are double-quoted.
	for _, pair := range strings.Split(body, ",") {
		eq := strings.Index(pair, "=")
		if eq < 0 {
			return k, nil, false
		}
		lk := strings.TrimSpace(pair[:eq])
		lv := strings.TrimSpace(pair[eq+1:])
		if len(lv) < 2 || lv[0] != '"' || lv[len(lv)-1] != '"' {
			return k, nil, false
		}
		labels[lk] = lv[1 : len(lv)-1]
	}
	return base, labels, true
}
