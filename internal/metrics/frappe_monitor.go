package metrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// FrappeMonitorEvent is one low-overhead request or background-job record
// written by Frappe's native monitor to logs/monitor.json.log. The UUID and
// client IP intentionally remain in the raw Loki record; using either as a
// VictoriaMetrics label would create unbounded cardinality.
type FrappeMonitorEvent struct {
	Site            string
	Timestamp       time.Time
	TransactionType string
	DurationMicros  float64
	Method          string
	Path            string
	StatusCode      int64
	ResponseLength  int64
	Scheduled       bool
	WaitMicros      float64
	Exception       string
}

type frappeMonitorWire struct {
	Site            string        `json:"site"`
	Timestamp       string        `json:"timestamp"`
	TransactionType string        `json:"transaction_type"`
	Duration        json.Number   `json:"duration"`
	Request         frappeRequest `json:"request"`
	Job             frappeJob     `json:"job"`
	// Flat fields keep compatibility with monitor logs written by older
	// Frappe releases and third-party instrumentation.
	Method         string          `json:"method"`
	Path           string          `json:"path"`
	StatusCode     json.Number     `json:"status_code"`
	ResponseLength json.Number     `json:"response_length"`
	Scheduled      bool            `json:"scheduled"`
	Wait           json.Number     `json:"wait"`
	Exception      json.RawMessage `json:"exception"`
}

type frappeRequest struct {
	Method         string      `json:"method"`
	Path           string      `json:"path"`
	StatusCode     json.Number `json:"status_code"`
	ResponseLength json.Number `json:"response_length"`
}

type frappeJob struct {
	Method    string      `json:"method"`
	Scheduled bool        `json:"scheduled"`
	Wait      json.Number `json:"wait"`
}

// ParseFrappeMonitorEvent validates one native monitor JSON line. Timestamps
// without an explicit timezone are deliberately left unset: the streamer will
// use its observed time instead of guessing the remote host's timezone.
func ParseFrappeMonitorEvent(line string) (FrappeMonitorEvent, error) {
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	var wire frappeMonitorWire
	if err := decoder.Decode(&wire); err != nil {
		return FrappeMonitorEvent{}, fmt.Errorf("decode native monitor line: %w", err)
	}

	event := FrappeMonitorEvent{
		Site:            cleanMetricTag(wire.Site, 255),
		TransactionType: strings.ToLower(strings.TrimSpace(wire.TransactionType)),
		Method:          cleanMetricTag(strings.TrimSpace(wire.Method), 255),
		Path:            strings.TrimSpace(wire.Path),
		Scheduled:       wire.Scheduled,
	}
	if event.Site == "" {
		return FrappeMonitorEvent{}, errors.New("native monitor event has no site")
	}
	if event.TransactionType != "request" && event.TransactionType != "job" {
		return FrappeMonitorEvent{}, fmt.Errorf("unsupported native monitor transaction_type %q", wire.TransactionType)
	}
	if event.TransactionType == "request" {
		if wire.Request.Method != "" {
			event.Method = cleanMetricTag(strings.TrimSpace(wire.Request.Method), 255)
		}
		if wire.Request.Path != "" {
			event.Path = strings.TrimSpace(wire.Request.Path)
		}
		if wire.Request.StatusCode != "" {
			wire.StatusCode = wire.Request.StatusCode
		}
		if wire.Request.ResponseLength != "" {
			wire.ResponseLength = wire.Request.ResponseLength
		}
	} else {
		if wire.Job.Method != "" {
			event.Method = cleanMetricTag(strings.TrimSpace(wire.Job.Method), 255)
		}
		event.Scheduled = wire.Job.Scheduled
		if wire.Job.Wait != "" {
			wire.Wait = wire.Job.Wait
		}
	}

	var err error
	if event.DurationMicros, err = optionalFloat(wire.Duration); err != nil || event.DurationMicros < 0 {
		return FrappeMonitorEvent{}, fmt.Errorf("invalid native monitor duration %q", wire.Duration)
	}
	if event.WaitMicros, err = optionalFloat(wire.Wait); err != nil || event.WaitMicros < 0 {
		return FrappeMonitorEvent{}, fmt.Errorf("invalid native monitor wait %q", wire.Wait)
	}
	if event.StatusCode, err = optionalInt(wire.StatusCode); err != nil {
		return FrappeMonitorEvent{}, fmt.Errorf("invalid native monitor status_code %q", wire.StatusCode)
	}
	if event.ResponseLength, err = optionalInt(wire.ResponseLength); err != nil || event.ResponseLength < 0 {
		return FrappeMonitorEvent{}, fmt.Errorf("invalid native monitor response_length %q", wire.ResponseLength)
	}
	event.Exception = exceptionName(wire.Exception)
	event.Timestamp = parseMonitorTimestamp(wire.Timestamp)
	return event, nil
}

func optionalFloat(value json.Number) (float64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseFloat(string(value), 64)
}

func optionalInt(value json.Number) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(string(value), 10, 64)
}

func parseMonitorTimestamp(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	// Modern Frappe writes an aware ISO timestamp. Older releases emitted a
	// naive local timestamp, which cannot safely be converted by the monitor.
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05Z07:00",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// LineProtocol converts an event to raw event samples. Counts, rates, and
// quantiles are derived with count_over_time/rate-style PromQL queries. This
// avoids a stateful counter in the monitor and makes log replay idempotent.
func (e FrappeMonitorEvent) LineProtocol(serverLabel, benchLabel string, at time.Time) string {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	baseTags := "server=" + escapeTag(cleanMetricTag(serverLabel, 255)) +
		",bench=" + escapeTag(cleanMetricTag(benchLabel, 255)) +
		",site=" + escapeTag(cleanMetricTag(e.Site, 255))
	ts := strconv.FormatInt(at.UnixNano(), 10)
	var b strings.Builder
	emit := func(name, extraTags string, value float64) {
		b.WriteString(name)
		b.WriteByte(',')
		b.WriteString(baseTags)
		if extraTags != "" {
			b.WriteByte(',')
			b.WriteString(extraTags)
		}
		b.WriteString(" value=")
		b.WriteString(strconv.FormatFloat(value, 'f', -1, 64))
		b.WriteByte(' ')
		b.WriteString(ts)
		b.WriteByte('\n')
	}

	switch e.TransactionType {
	case "request":
		route, kind := normalizeFrappeRoute(e.Path)
		method := cleanMetricTag(strings.ToUpper(e.Method), 16)
		if method == "" {
			method = "UNKNOWN"
		}
		routeTags := "method=" + escapeTag(method) +
			",route=" + escapeTag(route) +
			",kind=" + escapeTag(kind)
		statusTags := "status=" + escapeTag(strconv.FormatInt(e.StatusCode, 10)) +
			",status_class=" + escapeTag(statusClass(e.StatusCode))
		// The aggregate series has one bounded series per site and supports an
		// exact quantile_over_time. The route series powers endpoint analysis.
		emit("frappe_site_request_duration_seconds", "", e.DurationMicros/1_000_000)
		emit("frappe_site_request_route_duration_seconds", routeTags, e.DurationMicros/1_000_000)
		emit("frappe_site_request_response_bytes", routeTags, float64(e.ResponseLength))
		emit("frappe_site_request_status", statusTags, 1)
		if e.StatusCode >= 400 {
			emit("frappe_site_request_route_error", routeTags+","+statusTags, 1)
		}
		if e.Exception != "" {
			emit("frappe_site_transaction_error", "transaction=request,exception="+escapeTag(e.Exception), 1)
		}
	case "job":
		method := cleanMetricTag(e.Method, 255)
		if method == "" {
			method = "unknown"
		}
		scheduled := "false"
		if e.Scheduled {
			scheduled = "true"
		}
		tags := "method=" + escapeTag(method) + ",scheduled=" + scheduled
		emit("frappe_site_job_duration_seconds", "", e.DurationMicros/1_000_000)
		emit("frappe_site_job_wait_seconds", "", e.WaitMicros/1_000_000)
		emit("frappe_site_job_method_duration_seconds", tags, e.DurationMicros/1_000_000)
		emit("frappe_site_job_method_wait_seconds", tags, e.WaitMicros/1_000_000)
		if e.Exception != "" {
			emit("frappe_site_transaction_error", "transaction=job,exception="+escapeTag(e.Exception), 1)
		}
	}
	return b.String()
}

func statusClass(code int64) string {
	if code < 100 || code > 599 {
		return "unknown"
	}
	return strconv.FormatInt(code/100, 10) + "xx"
}

// normalizeFrappeRoute keeps stable API operations while replacing document
// names and collapsing arbitrary website paths. It is deliberately strict to
// keep the time-series cardinality bounded on large multi-site fleets.
func normalizeFrappeRoute(raw string) (route, kind string) {
	path := raw
	if parsed, err := url.Parse(raw); err == nil && parsed.Path != "" {
		path = parsed.Path
	}
	path = "/" + strings.TrimLeft(path, "/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i := range parts {
		if decoded, err := url.PathUnescape(parts[i]); err == nil {
			parts[i] = decoded
		}
		parts[i] = cleanMetricTag(parts[i], 120)
	}

	switch {
	case len(parts) >= 3 && parts[0] == "api" && parts[1] == "method":
		return cleanMetricTag("/api/method/"+strings.Join(parts[2:], "/"), 300), "api"
	case len(parts) >= 4 && parts[0] == "api" && parts[1] == "v2" && parts[2] == "method":
		return cleanMetricTag("/api/v2/method/"+strings.Join(parts[3:], "/"), 300), "api"
	case len(parts) >= 3 && parts[0] == "api" && parts[1] == "resource":
		base := "/api/resource/" + parts[2]
		if len(parts) > 3 {
			base += "/:name"
		}
		return cleanMetricTag(base, 300), "api"
	case len(parts) >= 4 && parts[0] == "api" && parts[1] == "v2" && parts[2] == "document":
		base := "/api/v2/document/" + parts[3]
		if len(parts) > 4 {
			base += "/:name"
		}
		return cleanMetricTag(base, 300), "api"
	case len(parts) > 0 && parts[0] == "api":
		return "/api/_other", "api"
	default:
		return "/_web", "web"
	}
}

func cleanMetricTag(value string, limit int) string {
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	if len(value) > limit {
		value = value[:limit]
	}
	return value
}

func exceptionName(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return "recorded"
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	// Frappe may serialize either an exception class or a full message. Only
	// retain the class-like prefix so user data cannot become a metric label.
	if index := strings.IndexAny(text, ":\n\r"); index >= 0 {
		text = text[:index]
	}
	return cleanMetricTag(text, 80)
}
