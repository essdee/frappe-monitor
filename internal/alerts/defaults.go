package alerts

// DefaultRules is the bundled-defaults rule set. Operators can override
// or extend via cfg.alerts.rules. These cover the failure modes that
// page operators most often:
//
//   - server stops reporting (covers SSH/network/host outages)
//   - disk almost full (covers slow capacity exhaustion)
//   - site is unhealthy (covers Frappe app crashes / site-down)
//   - redis queue piling up (covers worker outages / slow jobs)
//   - every RQ worker occupied (covers job-capacity exhaustion)
//   - a Supervisor service failed or stopped
//   - sustained CPU, memory, or swap pressure
//   - slow but still-successful site responses
//   - sustained per-site request latency and HTTP 5xx rates
//   - excessive background-job queue wait
//   - site-correlated database queries exceeding five seconds
//   - fewer live Gunicorn worker children than configured
//
// A missing rule series means "not firing", but the evaluator only emits a
// recovery after the server registry confirms that the parent is reachable.
// This keeps absent telemetry during a server outage from looking healthy.
func DefaultRules() []Rule {
	return []Rule{
		{
			Name: "server_unreachable",
			// The collector explicitly writes zero after SSH/parse failures;
			// this does not depend on an implicit query lookback window.
			Expr:              `frappe_server_reachable == 0`,
			Severity:          "critical",
			FingerprintLabels: []string{"server"},
			Message:           `Server "{{.Labels.server}}" could not be reached during its latest collection attempt.`,
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
			Expr:              `(frappe_site_public_is_healthy or frappe_site_is_healthy) == 0`,
			Severity:          "critical",
			FingerprintLabels: []string{"server", "bench", "site"},
			Message:           `Site "{{.Labels.site}}" on {{.Labels.server}}/{{.Labels.bench}} is unhealthy: {{.Labels.reason}} (HTTP {{.Labels.http_status}}, {{.Labels.response_ms}} ms).`,
		},
		{
			Name:              "redis_queue_high",
			Expr:              `frappe_bench_redis_queue_depth > 1000`,
			Severity:          "warning",
			FingerprintLabels: []string{"server", "bench", "queue"},
			Message:           `Redis queue "{{.Labels.queue}}" on {{.Labels.server}}/{{.Labels.bench}} has {{.Value}} pending jobs.`,
		},
		{
			Name:              "rq_workers_saturated",
			Expr:              `frappe_bench_workers_registered > 0 and on(server, bench) frappe_bench_workers_idle == 0`,
			Severity:          "critical",
			FingerprintLabels: []string{"server", "bench"},
			Message:           `All {{printf "%.0f" .Value}} registered RQ workers on {{.Labels.server}}/{{.Labels.bench}} are occupied. New background jobs cannot start.`,
		},
		{
			Name:              "bench_service_failed",
			Expr:              `frappe_bench_service_state == 3`,
			Severity:          "critical",
			FingerprintLabels: []string{"server", "bench", "service"},
			Message:           `Service "{{.Labels.service}}" ({{.Labels.kind}}) on {{.Labels.server}}/{{.Labels.bench}} is stopped or failed.`,
		},
		{
			Name:              "stream_disconnected",
			Expr:              `frappe_stream_connected == 0`,
			Severity:          "warning",
			FingerprintLabels: []string{"server"},
			Message:           `Live log streaming from server "{{.Labels.server}}" is disconnected; metrics collection may still be running.`,
		},
		{
			Name: "cpu_saturated",
			Expr: `100 * (1 - rate(frappe_server_cpu_idle[5m]) / (` +
				`rate(frappe_server_cpu_user[5m]) + rate(frappe_server_cpu_nice[5m]) + ` +
				`rate(frappe_server_cpu_system[5m]) + rate(frappe_server_cpu_idle[5m]) + ` +
				`rate(frappe_server_cpu_iowait[5m]) + rate(frappe_server_cpu_irq[5m]) + ` +
				`rate(frappe_server_cpu_softirq[5m]) + rate(frappe_server_cpu_steal[5m]))) > 95`,
			Severity:          "critical",
			FingerprintLabels: []string{"server"},
			Message:           `CPU utilization on {{.Labels.server}} has exceeded 95% over five minutes ({{printf "%.1f" .Value}}%).`,
		},
		{
			Name:              "memory_pressure",
			Expr:              `100 * (1 - frappe_server_mem_available_bytes / frappe_server_mem_total_bytes) > 90`,
			Severity:          "critical",
			FingerprintLabels: []string{"server"},
			Message:           `Memory utilization on {{.Labels.server}} is {{printf "%.1f" .Value}}%; less than 10% remains available.`,
		},
		{
			Name: "swap_pressure",
			Expr: `100 * (1 - frappe_server_swap_free_bytes / frappe_server_swap_total_bytes) > 90 ` +
				`and on(server) frappe_server_swap_total_bytes > 0`,
			Severity:          "warning",
			FingerprintLabels: []string{"server"},
			Message:           `Swap utilization on {{.Labels.server}} is {{printf "%.1f" .Value}}%.`,
		},
		{
			Name: "site_response_slow",
			Expr: `(frappe_site_public_http_response_ms or frappe_site_http_response_ms) > 3000 ` +
				`and on(server, bench, site) (frappe_site_public_is_healthy or frappe_site_is_healthy) == 1`,
			Severity:          "warning",
			FingerprintLabels: []string{"server", "bench", "site"},
			Message:           `Site "{{.Labels.site}}" on {{.Labels.server}}/{{.Labels.bench}} is responding slowly ({{printf "%.0f" .Value}} ms).`,
		},
		{
			Name: "site_request_p95_slow",
			Expr: `1000 * quantile_over_time(0.95, frappe_site_request_duration_seconds[5m]) > 2000 ` +
				`and on(server, bench, site) count_over_time(frappe_site_request_duration_seconds[5m]) >= 20`,
			Severity:          "warning",
			FingerprintLabels: []string{"server", "bench", "site"},
			Message:           `Site "{{.Labels.site}}" on {{.Labels.server}}/{{.Labels.bench}} has a five-minute request p95 of {{printf "%.0f" .Value}} ms.`,
		},
		{
			Name: "site_http_5xx_rate_high",
			Expr: `(100 * sum by(server, bench, site) (count_over_time(frappe_site_request_status{status_class="5xx"}[5m])) ` +
				`/ on(server, bench, site) clamp_min(count_over_time(frappe_site_request_duration_seconds[5m]), 1)) > 5 ` +
				`and on(server, bench, site) count_over_time(frappe_site_request_duration_seconds[5m]) >= 20`,
			Severity:          "critical",
			FingerprintLabels: []string{"server", "bench", "site"},
			Message:           `Site "{{.Labels.site}}" on {{.Labels.server}}/{{.Labels.bench}} has a five-minute HTTP 5xx rate of {{printf "%.1f" .Value}}%.`,
		},
		{
			Name: "site_job_wait_p95_high",
			Expr: `quantile_over_time(0.95, frappe_site_job_wait_seconds[10m]) > 30 ` +
				`and on(server, bench, site) count_over_time(frappe_site_job_wait_seconds[10m]) >= 5`,
			Severity:          "warning",
			FingerprintLabels: []string{"server", "bench", "site"},
			Message:           `Background jobs for "{{.Labels.site}}" on {{.Labels.server}}/{{.Labels.bench}} waited {{printf "%.1f" .Value}} seconds at p95 over ten minutes.`,
		},
		{
			Name: "site_database_query_slow",
			Expr: `max_over_time(frappe_database_slow_query_duration_seconds[5m]) ` +
				`* on(server, identity_hash, identity_kind) group_left(bench, site) frappe_site_database_identity > 5`,
			Severity:          "warning",
			FingerprintLabels: []string{"server", "bench", "site", "engine", "operation"},
			Message:           `A {{.Labels.engine}} {{.Labels.operation}} query for "{{.Labels.site}}" on {{.Labels.server}}/{{.Labels.bench}} took {{printf "%.2f" .Value}} seconds.`,
		},
		{
			Name: "web_worker_shortage",
			Expr: `frappe_bench_web_workers_running < on(server, bench) frappe_bench_web_workers_configured ` +
				`and on(server, bench) frappe_bench_supervisor_total > 0`,
			Severity:          "critical",
			FingerprintLabels: []string{"server", "bench"},
			Message:           `Only {{printf "%.0f" .Value}} Gunicorn workers are running on {{.Labels.server}}/{{.Labels.bench}}, below configured capacity.`,
		},
	}
}
