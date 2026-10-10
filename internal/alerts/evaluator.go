package alerts

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/template"
	"time"

	"frappe-monitor/internal/storage"
)

// Evaluator runs every rule against VM, reconciles state in storage,
// and emits notifications via Notifier. One Evaluator instance is
// owned by the Service that ticks it.
type Evaluator struct {
	Rules            []Rule
	VM               VMQuerier
	Store            storage.Store
	Notifier         Notifier
	NotifyRepeatTime time.Duration // suppress duplicate fire pages within this window
	Logger           *slog.Logger
	Now              func() time.Time // overridable for tests
}

type evaluationCycle struct {
	reachableOnce sync.Once
	reachable     map[string]struct{}
	reachableErr  error
}

func (c *evaluationCycle) reachableServers(ctx context.Context, vm VMQuerier) (map[string]struct{}, error) {
	c.reachableOnce.Do(func() {
		samples, err := vm.Query(ctx, `frappe_server_reachable == 1`)
		if err != nil {
			c.reachableErr = err
			return
		}
		c.reachable = make(map[string]struct{}, len(samples))
		for _, sample := range samples {
			if server := sample.Labels["server"]; server != "" {
				c.reachable[server] = struct{}{}
			}
		}
	})
	return c.reachable, c.reachableErr
}

// EvaluateOnce runs every rule exactly once. The cron loop calls this
// on each tick. Errors from a single rule are logged and don't stop
// the others — one bad rule shouldn't break the whole alerting plane.
func (e *Evaluator) EvaluateOnce(ctx context.Context) {
	now := e.now()
	managedServers, registryKnown := e.managedServers(ctx)
	cycle := &evaluationCycle{}
	for _, rule := range e.Rules {
		if err := e.evaluateRule(ctx, rule, now, managedServers, registryKnown, cycle); err != nil {
			e.Logger.Warn("alerts: rule evaluation failed",
				"rule", rule.Name, "err", err)
		}
	}
}

func (e *Evaluator) evaluateRule(ctx context.Context, rule Rule, now time.Time, managedServers map[string]string, registryKnown bool, cycle *evaluationCycle) error {
	samples, err := e.VM.Query(ctx, rule.Expr)
	if err != nil {
		return fmt.Errorf("vm query: %w", err)
	}

	// Build the desired state: map[fingerprint]Sample for everything
	// the rule says is firing right now.
	if registryKnown {
		filtered := samples[:0]
		for _, sample := range samples {
			server := sample.Labels["server"]
			if server != "" {
				if _, managed := managedServers[server]; !managed {
					continue
				}
			}
			filtered = append(filtered, sample)
		}
		samples = filtered
	}

	fingerprints := make([]string, len(samples))
	for i, sample := range samples {
		// Enrichment adds display-only labels. Fingerprints must remain
		// based on the raw rule result so an HTTP status change does not
		// create a second alert for the same site.
		fingerprints[i] = rule.Fingerprint(sample.Labels)
	}
	samples = e.enrichSamples(ctx, rule, samples)
	firing := make(map[string]Sample, len(samples))
	for i, sample := range samples {
		firing[fingerprints[i]] = sample
	}

	// Pull the last cycle's state for this rule.
	prev, err := e.Store.ListAlertStatesByRule(ctx, rule.Name)
	if err != nil {
		return fmt.Errorf("list state: %w", err)
	}
	prevByFP := make(map[string]*storage.AlertState, len(prev))
	for _, p := range prev {
		prevByFP[p.Fingerprint] = p
	}
	resolutionCandidates := make(map[string]*storage.AlertState, len(prevByFP))
	for fp, previous := range prevByFP {
		if _, stillFiring := firing[fp]; stillFiring {
			continue
		}
		server := previous.Labels["server"]
		if registryKnown && server != "" {
			if _, managed := managedServers[server]; !managed {
				continue
			}
		}
		resolutionCandidates[fp] = previous
	}
	resolutionBlocked := e.resolutionBlocked(ctx, rule, resolutionCandidates, managedServers, registryKnown, cycle)

	// 1. Anything currently firing → upsert + notify if new or cooldown elapsed.
	for fp, sample := range firing {
		previous, isResume := prevByFP[fp]
		lastNotified := time.Unix(0, 0).UTC()
		if isResume && !previous.LastNotifiedAt.IsZero() {
			lastNotified = previous.LastNotifiedAt
		}
		neverNotified := lastNotified.Equal(time.Unix(0, 0).UTC())
		shouldNotify := !isResume || neverNotified || previous.Status != "firing" ||
			(e.NotifyRepeatTime > 0 && now.Sub(lastNotified) >= e.NotifyRepeatTime)

		state := storage.AlertState{
			RuleName: rule.Name, Fingerprint: fp, Labels: sample.Labels,
			Status: "firing", Value: sample.Value, LastNotifiedAt: lastNotified,
		}
		persisted, err := e.Store.UpsertAlertState(ctx, state)
		if err != nil {
			e.Logger.Warn("alerts: upsert state failed",
				"rule", rule.Name, "fp", fp, "err", err)
			continue
		}
		e.recordOpenIncident(ctx, rule, persisted, now)
		if shouldNotify {
			msg := renderMessage(rule, sample)
			notif := Notification{
				Severity:    rule.Severity,
				RuleName:    rule.Name,
				Fingerprint: fp,
				Body:        msg,
				Labels:      sample.Labels,
				Resolved:    false,
				Time:        now,
			}
			if err := e.Notifier.Notify(ctx, notif); err != nil {
				e.Logger.Warn("alerts: notify failed",
					"rule", rule.Name, "fp", fp, "err", err)
				continue
			}
			// Advance the cooldown only after every notifier accepted the
			// message. A failed send remains immediately retryable.
			state.LastNotifiedAt = now
			if _, err := e.Store.UpsertAlertState(ctx, state); err != nil {
				e.Logger.Warn("alerts: persist notification success failed",
					"rule", rule.Name, "fp", fp, "err", err)
			}
			notifiedAt := now
			e.recordOpenIncident(ctx, rule, &storage.AlertState{
				RuleName: state.RuleName, Fingerprint: state.Fingerprint, Labels: state.Labels,
				Value: state.Value, FirstFiredAt: persisted.FirstFiredAt, LastNotifiedAt: notifiedAt,
			}, now)
			e.Logger.Info("alerts: fired",
				"rule", rule.Name, "fp", fp,
				"severity", rule.Severity, "value", sample.Value)
		}
	}

	// 2. Anything previously firing but absent now → resolve + notify, then delete.
	for fp, previous := range prevByFP {
		if _, stillFiring := firing[fp]; stillFiring {
			continue
		}
		if registryKnown && previous.Labels["server"] != "" {
			if _, managed := managedServers[previous.Labels["server"]]; !managed {
				// The operator deleted or renamed this server. Silently prune
				// its state instead of sending a misleading recovery generated
				// by disappearance of the old telemetry series.
				if err := e.Store.DeleteAlertState(ctx, previous.ID); err != nil {
					e.Logger.Warn("alerts: prune unmanaged state failed",
						"rule", rule.Name, "fp", fp, "err", err)
				}
				continue
			}
		}
		if _, blocked := resolutionBlocked[fp]; blocked {
			continue
		}
		if previous.Status == "firing" {
			msg := renderResolvedMessage(rule, previous)
			notif := Notification{
				Severity:    rule.Severity,
				RuleName:    rule.Name,
				Fingerprint: fp,
				Body:        msg,
				Labels:      previous.Labels,
				Resolved:    true,
				Time:        now,
			}
			if err := e.Notifier.Notify(ctx, notif); err != nil {
				e.Logger.Warn("alerts: resolve notify failed",
					"rule", rule.Name, "fp", fp, "err", err)
				// Keep the firing row until recovery delivery succeeds so the
				// next evaluation retries instead of losing the resolution.
				continue
			}
			e.Logger.Info("alerts: resolved",
				"rule", rule.Name, "fp", fp)
		}
		e.resolveIncident(ctx, previous, now)
		if err := e.Store.DeleteAlertState(ctx, previous.ID); err != nil {
			e.Logger.Warn("alerts: delete state failed",
				"rule", rule.Name, "fp", fp, "err", err)
		}
	}
	return nil
}

func (e *Evaluator) recordOpenIncident(ctx context.Context, rule Rule, state *storage.AlertState, now time.Time) {
	incidentStore, ok := e.Store.(storage.AlertIncidentStore)
	if !ok || state == nil {
		return
	}
	startedAt := state.FirstFiredAt
	if startedAt.IsZero() {
		startedAt = now
	}
	var notifiedAt *time.Time
	if !state.LastNotifiedAt.IsZero() && !state.LastNotifiedAt.Equal(time.Unix(0, 0).UTC()) {
		value := state.LastNotifiedAt
		notifiedAt = &value
	}
	_, err := incidentStore.UpsertOpenAlertIncident(ctx, storage.AlertIncident{
		RuleName: state.RuleName, Fingerprint: state.Fingerprint, Severity: rule.Severity,
		Labels: state.Labels, Status: "firing", Value: state.Value, StartedAt: startedAt,
		LastNotifiedAt: notifiedAt,
	})
	if err != nil {
		e.Logger.Warn("alerts: record incident failed", "rule", rule.Name, "fp", state.Fingerprint, "err", err)
	}
}

func (e *Evaluator) resolveIncident(ctx context.Context, state *storage.AlertState, now time.Time) {
	incidentStore, ok := e.Store.(storage.AlertIncidentStore)
	if !ok {
		return
	}
	if err := incidentStore.ResolveOpenAlertIncident(ctx, state.RuleName, state.Fingerprint, state.Value, now); err != nil {
		e.Logger.Warn("alerts: resolve incident failed", "rule", state.RuleName, "fp", state.Fingerprint, "err", err)
	}
}

func (e *Evaluator) managedServers(ctx context.Context) (map[string]string, bool) {
	servers, err := e.Store.ListServers(ctx)
	if err != nil {
		e.Logger.Warn("alerts: server registry unavailable; stale-series filtering disabled", "err", err)
		return nil, false
	}
	out := make(map[string]string, len(servers))
	for _, server := range servers {
		out[server.Name] = server.Status
	}
	return out, true
}

// resolutionBlocked returns fingerprints that lack positive recovery
// evidence. Any server-scoped alert requires both a reachable registry state
// (when available) and a fresh positive reachability metric. Site alerts also
// remain firing while their probe result is unknown.
func (e *Evaluator) resolutionBlocked(
	ctx context.Context,
	rule Rule,
	previous map[string]*storage.AlertState,
	managedServers map[string]string,
	registryKnown bool,
	cycle *evaluationCycle,
) map[string]struct{} {
	blocked := map[string]struct{}{}
	if len(previous) == 0 {
		return blocked
	}

	hasServerScoped := false
	for _, state := range previous {
		if state.Labels["server"] != "" {
			hasServerScoped = true
			break
		}
	}
	if hasServerScoped {
		reachableServers, reachabilityErr := cycle.reachableServers(ctx, e.VM)
		if reachabilityErr != nil {
			e.Logger.Warn("alerts: reachability evidence unavailable; recoveries deferred", "err", reachabilityErr)
		}
		for fp, state := range previous {
			server := state.Labels["server"]
			if server == "" {
				continue
			}
			_, metricReachable := reachableServers[server]
			registryReachable := !registryKnown || managedServers[server] == "reachable"
			if reachabilityErr != nil || !metricReachable || !registryReachable {
				blocked[fp] = struct{}{}
			}
		}
	}

	if rule.Name != "site_unhealthy" {
		return blocked
	}
	unknown, err := e.VM.Query(ctx, `(frappe_site_public_is_healthy or frappe_site_is_healthy) < 0`)
	if err != nil {
		e.Logger.Warn("alerts: site probe evidence unavailable; recoveries deferred", "err", err)
		for fp := range previous {
			blocked[fp] = struct{}{}
		}
		return blocked
	}
	for _, sample := range unknown {
		blocked[rule.Fingerprint(sample.Labels)] = struct{}{}
	}
	return blocked
}

// enrichSamples fetches each companion metric once and joins it in memory.
// The previous implementation issued three VM queries per failing site, which
// turned a large outage into hundreds of requests on every evaluation tick.
func (e *Evaluator) enrichSamples(ctx context.Context, rule Rule, samples []Sample) []Sample {
	if rule.Name != "site_unhealthy" || len(samples) == 0 {
		return samples
	}

	var status, responseMS, reasonCode, publicProbe map[string]float64
	var wg sync.WaitGroup
	wg.Add(4)
	go func() {
		defer wg.Done()
		status = e.queryCompanions(ctx, "frappe_site_public_http_status_code or frappe_site_http_status_code")
	}()
	go func() {
		defer wg.Done()
		responseMS = e.queryCompanions(ctx, "frappe_site_public_http_response_ms or frappe_site_http_response_ms")
	}()
	go func() {
		defer wg.Done()
		reasonCode = e.queryCompanions(ctx, "frappe_site_public_failure_reason_code or frappe_site_failure_reason_code")
	}()
	go func() {
		defer wg.Done()
		publicProbe = e.queryCompanions(ctx, "frappe_site_public_is_healthy")
	}()
	wg.Wait()

	for i := range samples {
		labels := make(map[string]string, len(samples[i].Labels)+3)
		for k, v := range samples[i].Labels {
			labels[k] = v
		}
		labels["reason"] = "health probe failed"
		labels["http_status"] = "unknown"
		labels["response_ms"] = "unknown"

		key, ok := siteMetricKey(labels)
		if ok {
			statusValue, hasStatus := status[key]
			responseValue, hasResponse := responseMS[key]
			reasonValue, hasReason := reasonCode[key]
			_, isPublic := publicProbe[key]
			if hasStatus {
				labels["http_status"] = strconv.FormatInt(int64(statusValue), 10)
			}
			if hasResponse {
				labels["response_ms"] = strconv.FormatFloat(responseValue, 'f', 1, 64)
			}
			if hasReason && isPublic {
				labels["reason"] = publicSiteFailureReason(int64(reasonValue))
			} else if hasReason {
				labels["reason"] = siteFailureReason(int64(reasonValue))
			} else if hasStatus {
				labels["reason"] = siteFailureReason(reasonCodeFromHTTP(int64(statusValue)))
			}
		}
		samples[i].Labels = labels
	}
	return samples
}

func (e *Evaluator) queryCompanions(ctx context.Context, expr string) map[string]float64 {
	out := map[string]float64{}
	samples, err := e.VM.Query(ctx, expr)
	if err != nil {
		e.Logger.Warn("alerts: companion metric query failed", "expr", expr, "err", err)
		return out
	}
	for _, sample := range samples {
		if key, ok := siteMetricKey(sample.Labels); ok {
			out[key] = sample.Value
		}
	}
	return out
}

func siteMetricKey(labels map[string]string) (string, bool) {
	server, bench, site := labels["server"], labels["bench"], labels["site"]
	if server == "" || bench == "" || site == "" {
		return "", false
	}
	// NUL cannot occur in labels produced by the collector or accepted by
	// server validation, making this a collision-free composite key.
	return server + "\x00" + bench + "\x00" + site, true
}

func reasonCodeFromHTTP(status int64) int64 {
	switch {
	case status == 0:
		return 1
	case status >= 400 && status < 500:
		return 2
	case status >= 500 && status < 600:
		return 3
	default:
		return 6
	}
}

func siteFailureReason(code int64) string {
	switch code {
	case 1:
		return "no HTTP response (timeout or connection failure)"
	case 2:
		return "the site returned an HTTP 4xx response"
	case 3:
		return "the site returned an HTTP 5xx response"
	case 4:
		return "the site probe did not run"
	case 5:
		return "no Frappe web port was detected"
	case 6:
		return "the site returned an unexpected response"
	default:
		return "health probe failed"
	}
}

func publicSiteFailureReason(code int64) string {
	switch code {
	case 1:
		return "public DNS resolution failed"
	case 2:
		return "the public target resolved to a blocked private or local address"
	case 3:
		return "the public connection timed out or was refused"
	case 4:
		return "the TLS certificate or handshake failed"
	case 5:
		return "the public endpoint returned an HTTP 4xx response"
	case 6:
		return "the public endpoint returned an HTTP 5xx response"
	case 7:
		return "the public endpoint returned an unexpected HTTP status"
	case 8:
		return "the public endpoint did not return Frappe's pong response"
	case 9:
		return "the public probe URL is invalid"
	default:
		return "public health probe failed"
	}
}

func (e *Evaluator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// renderMessage applies the rule's message template (or a default) to
// the firing sample's labels and value. Template parse failures fall
// back to the default formatter so a typo never silences an alert.
func renderMessage(rule Rule, s Sample) string {
	if rule.Message == "" {
		return defaultMessage(rule, s.Labels, s.Value)
	}
	t, err := template.New(rule.Name).Parse(rule.Message)
	if err != nil {
		return defaultMessage(rule, s.Labels, s.Value)
	}
	var buf bytes.Buffer
	data := map[string]any{
		"Labels": s.Labels,
		"Value":  s.Value,
	}
	if err := t.Execute(&buf, data); err != nil {
		return defaultMessage(rule, s.Labels, s.Value)
	}
	return buf.String()
}

func renderResolvedMessage(rule Rule, prev *storage.AlertState) string {
	switch rule.Name {
	case "site_unhealthy":
		return fmt.Sprintf("Site %q on %s/%s is responding normally again.",
			prev.Labels["site"], prev.Labels["server"], prev.Labels["bench"])
	case "server_unreachable":
		return fmt.Sprintf("Server %q is reachable again.", prev.Labels["server"])
	default:
		return "Recovered: " + defaultMessage(rule, prev.Labels, prev.Value)
	}
}

// defaultMessage formats labels deterministically (sorted) so the same
// alert produces the same string across processes.
func defaultMessage(rule Rule, labels map[string]string, value float64) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if k == "__name__" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, labels[k]))
	}
	return fmt.Sprintf("%s {%s} value=%.4f", rule.Name, strings.Join(pairs, ","), value)
}
