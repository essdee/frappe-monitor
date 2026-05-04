// Package alerts implements Phase 6: a small alerting engine that
// queries VictoriaMetrics on a schedule, reconciles per-(rule,
// fingerprint) state in SQLite, and fans out notifications to a
// Telegram bot.
//
// Design constraints (from docs/2026-04-29/1.md):
//   - single bot, send-only, fan-out to all admin chat IDs
//   - no two-way commands, no acks, no per-chat subscriptions
//   - rules are static config (yaml), not editable from the dashboard
package alerts

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// Rule is one alert definition. Loaded from YAML or supplied as part
// of the bundled defaults.
type Rule struct {
	// Name uniquely identifies the rule. Stable: changing it loses
	// state in storage (a renamed rule re-fires on its next eval).
	Name string `koanf:"name"`

	// Expr is a PromQL expression evaluated against VM. Any non-empty
	// result vector means the rule is "firing" for each series. Use
	// comparison operators (>, ==, etc.) to threshold.
	Expr string `koanf:"expr"`

	// Severity is surfaced in the notification body. Conventional
	// values: "critical", "warning", "info".
	Severity string `koanf:"severity"`

	// Message is a Go text/template applied with .Value (float64) and
	// one .Labels.<key> per series label. Empty falls back to a
	// default formatter (rule name + key labels + value).
	Message string `koanf:"message"`

	// FingerprintLabels names the labels that distinguish one
	// occurrence of this alert from another. Empty means use every
	// label on the series. Specify when the metric has noisy unrelated
	// labels you don't want in the fingerprint.
	FingerprintLabels []string `koanf:"fingerprint_labels"`
}

// Validate returns an error if the rule is unusable. Called once at
// startup so misconfigured rules don't make it past the boot.
func (r Rule) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("rule.name is required")
	}
	if strings.TrimSpace(r.Expr) == "" {
		return fmt.Errorf("rule[%s].expr is required", r.Name)
	}
	if r.Severity != "" {
		switch r.Severity {
		case "critical", "warning", "info":
		default:
			return fmt.Errorf("rule[%s].severity must be one of critical|warning|info, got %q",
				r.Name, r.Severity)
		}
	}
	return nil
}

// Fingerprint returns a stable hash of the labels chosen by
// FingerprintLabels. Deterministic across processes so a restarting
// monitor doesn't lose state. Sorted-key iteration so map order
// doesn't shift the hash.
func (r Rule) Fingerprint(labels map[string]string) string {
	keys := r.FingerprintLabels
	if len(keys) == 0 {
		keys = make([]string, 0, len(labels))
		for k := range labels {
			keys = append(keys, k)
		}
		sort.Strings(keys)
	}
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(labels[k])
		b.WriteByte('\x00')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8]) // 16-char hex; uniqueness is per-rule
}
