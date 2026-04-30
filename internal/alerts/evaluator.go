package alerts

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
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

// EvaluateOnce runs every rule exactly once. The cron loop calls this
// on each tick. Errors from a single rule are logged and don't stop
// the others — one bad rule shouldn't break the whole alerting plane.
func (e *Evaluator) EvaluateOnce(ctx context.Context) {
	now := e.now()
	for _, rule := range e.Rules {
		if err := e.evaluateRule(ctx, rule, now); err != nil {
			e.Logger.Warn("alerts: rule evaluation failed",
				"rule", rule.Name, "err", err)
		}
	}
}

func (e *Evaluator) evaluateRule(ctx context.Context, rule Rule, now time.Time) error {
	samples, err := e.VM.Query(ctx, rule.Expr)
	if err != nil {
		return fmt.Errorf("vm query: %w", err)
	}

	// Build the desired state: map[fingerprint]Sample for everything
	// the rule says is firing right now.
	firing := make(map[string]Sample, len(samples))
	for _, s := range samples {
		fp := rule.Fingerprint(s.Labels)
		firing[fp] = s
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

	// 1. Anything currently firing → upsert + notify if new or cooldown elapsed.
	for fp, sample := range firing {
		previous, isResume := prevByFP[fp]
		shouldNotify := !isResume ||
			previous.Status != "firing" ||
			now.Sub(previous.LastNotifiedAt) >= e.NotifyRepeatTime

		state := storage.AlertState{
			RuleName:    rule.Name,
			Fingerprint: fp,
			Labels:      sample.Labels,
			Status:      "firing",
			Value:       sample.Value,
		}
		if shouldNotify {
			state.LastNotifiedAt = now
		} else if isResume {
			state.LastNotifiedAt = previous.LastNotifiedAt
		}
		if _, err := e.Store.UpsertAlertState(ctx, state); err != nil {
			e.Logger.Warn("alerts: upsert state failed",
				"rule", rule.Name, "fp", fp, "err", err)
			continue
		}
		if shouldNotify {
			msg := renderMessage(rule, sample)
			if err := e.Notifier.Notify(ctx, rule.Severity, rule.Name, msg); err != nil {
				e.Logger.Warn("alerts: notify failed",
					"rule", rule.Name, "fp", fp, "err", err)
			} else {
				e.Logger.Info("alerts: fired",
					"rule", rule.Name, "fp", fp,
					"severity", rule.Severity, "value", sample.Value)
			}
		}
	}

	// 2. Anything previously firing but absent now → resolve + notify, then delete.
	for fp, previous := range prevByFP {
		if _, stillFiring := firing[fp]; stillFiring {
			continue
		}
		if previous.Status == "firing" {
			msg := renderResolvedMessage(rule, previous)
			if err := e.Notifier.Notify(ctx, rule.Severity, rule.Name+" RESOLVED", msg); err != nil {
				e.Logger.Warn("alerts: resolve notify failed",
					"rule", rule.Name, "fp", fp, "err", err)
			} else {
				e.Logger.Info("alerts: resolved",
					"rule", rule.Name, "fp", fp)
			}
		}
		if err := e.Store.DeleteAlertState(ctx, previous.ID); err != nil {
			e.Logger.Warn("alerts: delete state failed",
				"rule", rule.Name, "fp", fp, "err", err)
		}
	}
	return nil
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
	return defaultMessage(rule, prev.Labels, prev.Value)
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
