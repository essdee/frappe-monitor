package parser

import (
	"encoding/hex"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"frappe-monitor/internal/metrics"
)

// BenchSectionPrefix is "BENCH:". A bench section's name in Output is
// "BENCH:<bench>" — the colon is part of the name and survives the
// tokenizer's section-name handling (verified by Phase 2 tests).
const BenchSectionPrefix = "BENCH:"

// SiteSectionPrefix is "SITE:". A site section's name is
// "SITE:<bench>:<site>".
const SiteSectionPrefix = "SITE:"

// BenchNames returns every bench name present in the tokenized output,
// extracted from sections whose name starts with "BENCH:". Used by the
// pipeline to drive per-bench parsing without persisting a bench list.
func BenchNames(o Output) []string {
	var out []string
	for _, s := range o.Sections {
		if strings.HasPrefix(s.Name, BenchSectionPrefix) {
			out = append(out, strings.TrimPrefix(s.Name, BenchSectionPrefix))
		}
	}
	return out
}

// SiteNamesFor returns the (bench, site) pairs present in the tokenized
// output. Each result element is a 2-string slice [benchName, siteName].
func SiteNamesFor(o Output) [][2]string {
	var out [][2]string
	for _, s := range o.Sections {
		if !strings.HasPrefix(s.Name, SiteSectionPrefix) {
			continue
		}
		rest := strings.TrimPrefix(s.Name, SiteSectionPrefix)
		// rest is "<bench>:<site>". Split on first ":".
		colon := strings.Index(rest, ":")
		if colon < 0 {
			continue
		}
		out = append(out, [2]string{rest[:colon], rest[colon+1:]})
	}
	return out
}

// BenchFromSections builds a BenchMetrics from the section named
// "BENCH:<benchName>". Errors if the section is missing or any
// required field is malformed. Empty-result sections (no redis info, no
// supervisor info) parse cleanly with zero-valued fields — these
// happen on healthy systems where redis or supervisor isn't reachable
// from the collector and the script emits zeros (the "loud zero"
// pattern).
func BenchFromSections(o Output, benchName string) (metrics.BenchMetrics, error) {
	sectionName := BenchSectionPrefix + benchName
	s, ok := o.Section(sectionName)
	if !ok {
		return metrics.BenchMetrics{}, fmt.Errorf("missing %s section", sectionName)
	}

	// Timestamp comes from META; pipeline fills it in. Parser leaves zero.
	m := metrics.BenchMetrics{Bench: benchName}

	if v, err := requireInt64(s, "apps_count"); err != nil {
		return m, fmt.Errorf("BENCH %s: %w", benchName, err)
	} else {
		m.AppsCount = v
	}
	if v, err := requireInt64(s, "supervisor_running"); err != nil {
		return m, fmt.Errorf("BENCH %s: %w", benchName, err)
	} else {
		m.SupervisorRun = v
	}
	if v, err := requireInt64(s, "supervisor_total"); err != nil {
		return m, fmt.Errorf("BENCH %s: %w", benchName, err)
	} else {
		m.SupervisorTotal = v
	}
	for key, dst := range map[string]*int64{
		"web_workers_configured":        &m.WebWorkersConfigured,
		"background_workers_configured": &m.BackgroundWorkersConfigured,
		"workers_registered":            &m.WorkersRegistered,
		"workers_busy":                  &m.WorkersBusy,
		"workers_idle":                  &m.WorkersIdle,
	} {
		if raw, ok := s.KVs[key]; ok {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return m, fmt.Errorf("BENCH %s: %s: %w", benchName, key, err)
			}
			*dst = value
		}
	}

	// Optional info{frappe_version="..."}=1 — extract the version label.
	for k := range s.KVs {
		base, labels, ok := splitLabeledKey(k)
		if !ok || base != "info" {
			continue
		}
		if v, has := labels["frappe_version"]; has {
			m.FrappeVersion = v
		}
	}

	// redis_queue_depth{queue="<name>"}=N — aggregate.
	for k, val := range s.KVs {
		base, labels, ok := splitLabeledKey(k)
		if !ok || base != "redis_queue_depth" {
			continue
		}
		qname, has := labels["queue"]
		if !has {
			continue
		}
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return m, fmt.Errorf("BENCH %s: redis_queue_depth %q: %w", benchName, k, err)
		}
		m.RedisQueues = append(m.RedisQueues, metrics.RedisQueue{Name: qname, Depth: n})
	}

	services := map[string]*metrics.ServiceMetric{}
	for key, raw := range s.KVs {
		base, labels, ok := splitLabeledKey(key)
		if !ok || !strings.HasPrefix(base, "service_") {
			continue
		}
		name := labels["service"]
		if name == "" {
			continue
		}
		service := services[name]
		if service == nil {
			service = &metrics.ServiceMetric{Name: name, Kind: labels["kind"], Queue: labels["queue"]}
			services[name] = service
		}
		switch base {
		case "service_cpu_percent", "service_memory_percent":
			value, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return m, fmt.Errorf("BENCH %s: %s: %w", benchName, key, err)
			}
			if base == "service_cpu_percent" {
				service.CPUPercent = value
			} else {
				service.MemoryPercent = value
			}
		default:
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return m, fmt.Errorf("BENCH %s: %s: %w", benchName, key, err)
			}
			switch base {
			case "service_state":
				service.State = value
			case "service_pid":
				service.PID = value
			case "service_rss_bytes":
				service.RSSBytes = value
			case "service_uptime_seconds":
				service.UptimeSeconds = value
			}
		}
	}
	for _, service := range services {
		m.Services = append(m.Services, *service)
	}
	slices.SortFunc(m.Services, func(a, b metrics.ServiceMetric) int { return strings.Compare(a.Name, b.Name) })

	return m, nil
}

// SiteFromSections builds a SiteMetrics from the section named
// "SITE:<benchName>:<siteName>".
func SiteFromSections(o Output, benchName, siteName string) (metrics.SiteMetrics, error) {
	sectionName := SiteSectionPrefix + benchName + ":" + siteName
	s, ok := o.Section(sectionName)
	if !ok {
		return metrics.SiteMetrics{}, fmt.Errorf("missing %s section", sectionName)
	}

	m := metrics.SiteMetrics{Bench: benchName, Site: siteName}

	if v, err := requireInt64(s, "http_status_code"); err != nil {
		return m, fmt.Errorf("SITE %s/%s: %w", benchName, siteName, err)
	} else {
		m.HTTPStatusCode = v
	}
	if v, err := requireFloat64(s, "http_response_ms"); err != nil {
		return m, fmt.Errorf("SITE %s/%s: %w", benchName, siteName, err)
	} else {
		m.HTTPResponseMs = v
	}
	if v, err := requireInt64(s, "is_healthy"); err != nil {
		return m, fmt.Errorf("SITE %s/%s: %w", benchName, siteName, err)
	} else {
		m.IsHealthy = v
	}
	if raw, ok := s.KVs["failure_reason_code"]; ok {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return m, fmt.Errorf("SITE %s/%s: failure_reason_code: %w", benchName, siteName, err)
		}
		m.FailureReasonCode = v
	} else {
		// Compatibility for collector versions before 2.6.0.
		switch {
		case m.IsHealthy == 1:
			m.FailureReasonCode = 0
		case m.HTTPStatusCode == 0:
			m.FailureReasonCode = 1
		case m.HTTPStatusCode >= 400 && m.HTTPStatusCode < 500:
			m.FailureReasonCode = 2
		case m.HTTPStatusCode >= 500 && m.HTTPStatusCode < 600:
			m.FailureReasonCode = 3
		default:
			m.FailureReasonCode = 6
		}
	}
	if raw, ok := s.KVs["native_monitor_enabled"]; ok {
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || (v != 0 && v != 1) {
			return m, fmt.Errorf("SITE %s/%s: native_monitor_enabled must be 0 or 1", benchName, siteName)
		}
		m.NativeMonitorEnabled = v
	}
	for key, destination := range map[string]*string{
		"database_hash": &m.DatabaseHash, "database_user_hash": &m.DatabaseUserHash,
	} {
		if raw := strings.TrimSpace(s.KVs[key]); raw != "" {
			decoded, err := hex.DecodeString(raw)
			if err != nil || len(decoded) != 12 {
				return m, fmt.Errorf("SITE %s/%s: %s must be a 24-character hex identity", benchName, siteName, key)
			}
			*destination = strings.ToLower(raw)
		}
	}

	return m, nil
}

// MetaTimestamp returns the META section's timestamp as a time.Time, or
// an error if META is missing or malformed. Pipeline uses this to stamp
// every bench/site metric with the same instant the collector ran.
func MetaTimestamp(o Output) (time.Time, error) {
	meta, ok := o.Section("META")
	if !ok {
		return time.Time{}, fmt.Errorf("missing META section")
	}
	ts, err := requireInt64(meta, "timestamp")
	if err != nil {
		return time.Time{}, fmt.Errorf("META: %w", err)
	}
	return time.Unix(ts, 0).UTC(), nil
}
