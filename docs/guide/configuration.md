# Configuration

Every key, default, and meaning. The full schema lives in `internal/config/config.go`; this is the operator-facing version.

## File location

| Where it runs | Path |
|---|---|
| Production (after `install.sh`) | `/etc/frappe-monitor/monitor.yaml` |
| Dev (`make run`) | `./deploy/config/monitor.yaml.example` |
| Alternate YAML file | `frappe-monitor --config /any/path.yaml` |

## Reload behavior

Open **Settings** in the monitoring console to manage the HTTP service, local
database path, SSH policy, collection schedule, VictoriaMetrics, Loki,
authentication, Telegram, alert rules, and log streaming. The form validates
the complete configuration before writing it. Secrets are write-only: the
browser receives only a `configured` flag and a blank field means “keep the
current value.”

Settings are written atomically to the YAML file passed through `--config`.
Unknown keys are retained and the immediately previous file is saved beside it
as `monitor.yaml.bak`. Long-running services keep their boot-time settings, so
the UI shows a restart-required banner after a save:

```bash
sudo systemctl restart frappe-monitor
```

Server registrations are the exception: add, edit, test, and delete operations
apply immediately because the scheduler and stream manager update their local
registries live. None of these operations modifies a monitored host.

The YAML below remains the recovery/bootstrap interface and is useful for
automation. It is the single source of runtime settings: environment variables
and command-line arguments cannot override its listen port, backend URLs,
timeouts, credentials, or collection behavior. `--config` only selects which
YAML file to read; `--check-config` only validates that file and exits.

## Full reference

```yaml
server:
  # HTTP listen address. ":8080" binds all interfaces; "127.0.0.1:8080"
  # binds only loopback (use this when Caddy is in front).
  listen_addr: ":8080"

  # Per-request HTTP timeouts. The dashboard's longest call is the
  # /api/v1/metrics/query proxy; tune against your VM's response time.
  read_timeout_seconds: 15
  write_timeout_seconds: 15

  # Forwarded client IP/protocol headers are accepted only from these direct
  # peers. Defaults support a same-host reverse proxy; add a remote proxy CIDR
  # explicitly rather than trusting arbitrary X-Forwarded-* headers.
  trusted_proxy_cidrs: ["127.0.0.1/32", "::1/128"]

display:
  # IANA timezone used by dashboard dates, charts, logs, and Telegram alert
  # footers. Storage and API timestamps remain UTC so multiple servers and
  # daylight-saving transitions stay comparable.
  timezone: "Asia/Kolkata"

database:
  # SQLite path. Production install rewrites this to /var/lib/frappe-monitor/.
  # Parent dir is auto-created at mode 0750 on first start.
  path: "/var/lib/frappe-monitor/monitor.db"

ssh:
  # TCP-connect timeout to a bench server.
  dial_timeout_seconds: 10

  # Per-command timeout once the connection is up. The collector script
  # is fast (< 2s on healthy hosts); 30s tolerates startup spikes.
  #
  # The collector itself self-throttles its site-probe phase against an
  # internal 20s budget (env: SITE_PROBE_BUDGET_S) so a bench with 30+
  # sites doesn't overrun this timeout. Sites past the budget are
  # emitted with is_healthy=-1 and a reason code so they stay unknown rather
  # than becoming false outage alerts. If your
  # benches consistently host more sites than fit in 20s, tune the embedded
  # collector budget in source and keep this outer ceiling above it.
  command_timeout_seconds: 30

  # Pool: max simultaneous SSH connections per remote host. Two is
  # the minimum when streaming is enabled: one live stream plus commands.
  max_connections_per_host: 2

  # Strict host-key database. The process refuses unknown or changed keys.
  # The production installer creates this file for the service user.
  known_hosts_path: "/var/lib/frappe-monitor/.ssh/known_hosts"

log:
  # Minimum level emitted. "info" in prod; "debug" temporarily for
  # diagnostics (then revert — debug is verbose).
  level: "info"   # debug | info | warn | error

  # "json" routes nicely to journald + log aggregators. "text" is
  # easier to eyeball on a terminal.
  format: "json"  # json | text

metrics:
  # VictoriaMetrics root URL. The collector appends /write for pushes;
  # the dashboard's query proxy forwards to /api/v1/query_range. A custom
  # VictoriaMetrics deployment MUST start with -influxSkipSingleField so the
  # stored metric names match the PromQL queries. The bundled Compose stack
  # already enables this flag.
  vm_url: "http://127.0.0.1:8428"

  # Push timeout per cycle. The collector's payload is a few KB; failure
  # past this threshold marks the cycle "metrics push failed" but does
  # NOT mark the server unreachable (the bench is fine; VM is the
  # failure domain).
  push_timeout_seconds: 5

  # Query timeout for the dashboard proxy. Bump if your VM is slow
  # under load.
  query_timeout_seconds: 15

logs:
  # Loki root URL. The collector appends /loki/api/v1/push; the
  # dashboard proxy forwards to /loki/api/v1/query_range.
  loki_url: "http://127.0.0.1:3100"

  push_timeout_seconds: 5
  query_timeout_seconds: 15

scheduler:
  # Per-server pull interval. The scheduler also runs once immediately at
  # startup and registration. Overlapping pulls are skipped.
  default_interval_seconds: 60

  # Cap on simultaneously-running pull jobs across the whole monitor.
  # Bump for large fleets; lower if pulls are saturating the host's
  # CPU or network.
  max_parallel: 10

  # Per-job ctx timeout. Pick well above P99(ssh + parse + push).
  # This must include the bounded site-probe phase.
  per_job_timeout_seconds: 45

# Monitor-side public checks. These complement the internal SSH/localhost
# probe with DNS, TLS, reverse-proxy, and browser-path coverage.
public_probes:
  enabled: false
  auto_discover: true
  default_scheme: "https"
  path: "/api/method/ping"
  timeout_seconds: 5
  max_parallel: 10
  allow_private_targets: false
  require_pong: true
  targets:
    # Exact URL override or disabled=true for one discovered site.
    # - server: Production
    #   bench: frappe-bench
    #   site: erp.example.com
    #   url: https://edge.example.net/api/method/ping
    #   disabled: false

# --- Authentication ----------------------------------------------------
auth:
  # Empty disables auth. Non-empty enables the SPA login page and a signed,
  # HttpOnly seven-day session cookie. API automation may use HTTP basic auth;
  # its username is ignored. /healthz and the SPA shell stay open.
  password: ""

  # Stable realm used for API basic-auth compatibility.
  realm: "frappe-monitor"

# --- Phase 6 alerts -----------------------------------------------------
# Telegram alerting. enabled=false (default) means no goroutine, no
# telegram traffic, no extra rows in SQLite. Operators must opt in.
alerts:
  enabled: false

  # Cron tick. Floor 15s; default 60s is comfortable.
  evaluation_interval_seconds: 60

  # Cooldown before re-paging the same firing alert. 0 means notify
  # only on first fire and on resolution.
  notify_repeat_seconds: 3600

  # Per-rule VM query timeout.
  vm_query_timeout_seconds: 10

  telegram:
    # @BotFather token. Treat as a secret — keep this file 0640
    # owned by root:frappe-monitor.
    bot_token: ""
    # Admin chat IDs to fan out to. From the Telegram getUpdates API
    # after sending /start to your bot.
    chat_ids: []
    send_timeout_seconds: 5

  # Skip the bundled DefaultRules() if true. Use when an operator
  # wants total control. Most deployments leave this false.
  disable_defaults: false

  # Custom rules merge with the defaults. A custom rule with the same name
  # replaces that bundled default, so thresholds are directly overrideable.
  rules:
    # - name: cpu_saturated
    #   expr: 100 * (1 - rate(frappe_server_cpu_idle[5m]) /
    #                    rate(frappe_server_cpu_user[5m] +
    #                         frappe_server_cpu_system[5m] +
    #                         frappe_server_cpu_idle[5m] +
    #                         frappe_server_cpu_iowait[5m])) > 95
    #   severity: warning
    #   fingerprint_labels: [server]
    #   message: "CPU on {{.Labels.server}} is {{printf \"%.1f\" .Value}}% used."

# --- Phase 8 streaming (per-event capture) -----------------------------
# A long-lived SSH session per selected server runs the embedded resumable
# streamer protocol and pushes appended lines to Loki. Disabled by default.
streaming:
  enabled: false

  # Free-form short string surfaced as a Loki/VM label so multi-monitor
  # deployments don't collide. Empty is fine for single-monitor setups.
  monitor_id: ""

  # Deprecated compatibility key. The embedded script is executed through
  # SSH stdin and this value is ignored.
  script_path: ".frappe-monitor/frappe-monitor-stream.sh"

  # Empty files enables read-only discovery of standard bench/site log paths.
  # server_id=0 (or omitted) applies an explicit entry globally. A positive server_id
  # targets one stable database row and supports heterogeneous bench roots.
  # IDs must be unique within the set that applies to a server. log_type,
  # bench, and site become Loki labels; site requires bench.
  files:
    # - server_id: 1
    #   id: prod_web_error
    #   path: /home/frappe/frappe-bench/logs/web.error.log
    #   log_type: error
    #   bench: frappe-bench
    # - server_id: 2
    #   id: staging_web_error
    #   path: /srv/bench/logs/web.error.log
    #   log_type: error
    #   bench: bench

  # How often the LokiSink flushes buffered entries. Lower = lower
  # dashboard latency, higher = bigger Loki batches. Default 1s.
  flush_interval_seconds: 1

  # Hard per-stream memory ceiling. On Loki failure, the reader applies
  # SSH/TCP backpressure; the remote file remains durable and cursors do not
  # advance until the batch commits. Default 500.
  max_batch_lines: 500

  # Global outage bounds across every discovered/configured log file.
  max_buffered_lines: 5000
  max_buffered_bytes: 33554432

  # Per-push timeout for Loki + VM. Default 10s.
  push_timeout_seconds: 10

  # Reconnect backoff. Doubles up to max on each disconnect. Default
  # 1s → 60s.
  min_backoff_seconds: 1
  max_backoff_seconds: 60
```

## Validated invariants

The binary refuses to start if any of these are wrong:

| Constraint | Reason |
|---|---|
| `database.path` non-empty | Required — there's no in-memory mode. |
| `server.listen_addr` non-empty | Same. |
| Every `server.trusted_proxy_cidrs` value is a CIDR | Forwarded identity must have an explicit trust boundary. |
| `log.level ∈ {debug,info,warn,error}` | Enforced by slog; bad values would silently default. |
| `log.format ∈ {json,text}` | Same. |
| All `*_timeout_seconds` ≥ 1 | Zero-second timeouts are bugs disguised as features. |
| `metrics.vm_url` non-empty | Phase 2+ requires it. |
| `logs.loki_url` non-empty | Phase 3+ requires it. |
| `scheduler.*` ≥ 1 | Same reasoning. |
| `scheduler.per_job_timeout_seconds` ≥ SSH dial + command + VM push timeouts | The outer deadline must not cancel a valid inner operation first. |
| `alerts.evaluation_interval_seconds` ≥ 15 (when enabled) | Anything lower is a tight loop on VM. |
| `alerts.telegram.bot_token` non-empty (when enabled) | Without it, no notification can land. |
| `alerts.telegram.chat_ids` non-empty (when enabled) | Same — fan-out target required. |
| `ssh.max_connections_per_host` ≥ 2 (when streaming) | Keeps one session available for metric collection beside the live stream. |
| Streaming file IDs/paths unique per applicable server | Prevents ambiguous protocol cursors and duplicate tailers. |
| `streaming.files[].server_id` ≥ 0 | Zero means global; positive IDs select one server. |
| `streaming.flush_interval_seconds` ≥ 1 (when enabled) | Subsecond flushes burn CPU for no gain. |
| `streaming.max_batch_lines` ≥ 1 (when enabled) | Zero would mean "flush on every line" — same problem. |
| `streaming.max_buffered_lines` ≥ `max_batch_lines` (when enabled) | Guarantees at least one complete batch can be retried. |
| `streaming.max_buffered_bytes` ≥ 1 MiB (when enabled) | Prevents an accidentally tiny cap from permanently stalling normal log lines. |
| `streaming.max_backoff_seconds` ≥ `min_backoff_seconds` (when enabled) | Otherwise reconnect would jitter forever. |

The error message names the offending key, e.g. `metrics.push_timeout_seconds must be >= 1, got 0`.

## Layered configs (multi-environment)

The Go side reads exactly one file. To run the same binary with different configs:

```bash
# Per-environment.
frappe-monitor --config /etc/frappe-monitor/monitor.dev.yaml
frappe-monitor --config /etc/frappe-monitor/monitor.staging.yaml
frappe-monitor --config /etc/frappe-monitor/monitor.prod.yaml
```

Or override `--config` in a systemd drop-in:

```bash
sudo systemctl edit frappe-monitor
# In the editor:
[Service]
ExecStart=
ExecStart=/usr/local/bin/frappe-monitor --config /etc/frappe-monitor/monitor.staging.yaml
```

(The empty `ExecStart=` clears the unit's default before the new one kicks in.)

## Tuning cheatsheet

| Symptom | Knob to try |
|---|---|
| Dashboard charts feel stale | `scheduler.default_interval_seconds: 300` (5 min) |
| Pulls pile up at top of the hour | `scheduler.max_parallel: 20` (or higher) |
| SSH timeouts on a slow bench host | `ssh.command_timeout_seconds: 60` and `scheduler.per_job_timeout_seconds: 75` or higher |
| VM eating disk too fast | shorten `--retentionPeriod` in `deploy/docker-compose.prod.yml`, then restart the stack unit |
| Loki eating disk too fast | shorten the embedded Loki `limits_config.retention_period` and `max_query_lookback` together in `deploy/docker-compose.prod.yml`, then restart the stack unit |
| Need verbose collector output | `log.level: "debug"` (revert when done — high volume) |
