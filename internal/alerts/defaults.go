package alerts

// DefaultRules is the bundled-defaults rule set. Operators can override
// or extend via cfg.alerts.rules. These cover the failure modes that
// page operators most often:
//
//   - server stops reporting (covers SSH/network/host outages)
//   - disk almost full (covers slow capacity exhaustion)
//   - site is unhealthy (covers Frappe app crashes / site-down)
//   - redis queue piling up (covers worker outages / slow jobs)
//
// All four are written so that a missing series simply means "not
// firing"; we never alert on absent data unless the rule explicitly
// uses absent_over_time. Phase 7+ may add a "monitor lost contact
// with server" alert separately.
func DefaultRules() []Rule {
	return []Rule{
		{
			Name: "server_unreachable",
			// Latest sample older than 5 min == we've been failing to
			// reach this server for at least one full pull cycle.
			Expr:              `time() - timestamp(frappe_server_load_1m) > 300`,
			Severity:          "critical",
			FingerprintLabels: []string{"server"},
			Message:           `Server "{{.Labels.server}}" hasn't reported metrics in over 5 minutes (last value {{.Value}}s ago).`,
		},
		{
			// Value is already in percent (×100) so the message renders
			// without needing template helpers.
			Name:              "disk_almost_full",
			Expr:              `100 * frappe_server_disk_used_bytes / frappe_server_disk_total_bytes > 90`,
			Severity:          "warning",
			FingerprintLabels: []string{"server", "mount"},
			Message:           `Disk "{{.Labels.mount}}" on {{.Labels.server}} is {{printf "%.1f" .Value}}% full.`,
		},
		{
			Name:              "site_unhealthy",
			Expr:              `frappe_site_is_healthy == 0`,
			Severity:          "critical",
			FingerprintLabels: []string{"server", "bench", "site"},
			Message:           `Site "{{.Labels.site}}" on {{.Labels.server}}/{{.Labels.bench}} is unhealthy.`,
		},
		{
			Name:              "redis_queue_high",
			Expr:              `frappe_bench_redis_queue_depth > 1000`,
			Severity:          "warning",
			FingerprintLabels: []string{"server", "bench", "queue"},
			Message:           `Redis queue "{{.Labels.queue}}" on {{.Labels.server}}/{{.Labels.bench}} has {{.Value}} pending jobs.`,
		},
	}
}
