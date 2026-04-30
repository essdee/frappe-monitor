package parser

import (
	"fmt"
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
