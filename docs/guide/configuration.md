# Configuration

Every key, default, and meaning. The full schema lives in `internal/config/config.go`; this is the operator-facing version.

## File location

| Where it runs | Path |
|---|---|
| Production (after `install.sh`) | `/etc/frappe-monitor/monitor.yaml` |
| Dev (`make run`) | `./config/monitor.yaml` |
| Override at startup | `frappe-monitor --config /any/path.yaml` |

## Reload behavior

Config is read once at boot. To pick up a change: `sudo systemctl restart frappe-monitor` (≤15s). There is no SIGHUP reload; the trade-off is simpler internal wiring (no live-config plumbing in the scheduler / SSH pool / handlers).

## Full reference

```yaml
server:
  # HTTP listen address. ":8080" binds all interfaces; "127.0.0.1:8080"
  # binds only loopback (use this when Caddy is in front).
  listen_addr: ":8080"

  # Per-request HTTP timeouts. The dashboard's longest call is the
  # /api/v1/metrics/query proxy; tune against your VM's response time.
  # write_timeout_seconds MUST be >= ssh.command_timeout_seconds (validated
  # at startup) so a slow SSH-backed response (test-connection / deploy /
  # refresh) isn't severed mid-flight; those routes also extend their own
  # write deadline at runtime.
  read_timeout_seconds: 15
  write_timeout_seconds: 60

  # Request-body size cap (bytes). Applies to every JSON endpoint including
  # the pre-auth /login. Default 1 MiB.
  max_body_bytes: 1048576

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
  # emitted with is_healthy=0 / http_status_code=0 so the parser still
  # gets a complete output instead of being killed mid-loop. If your
  # benches consistently host more sites than fit in 20s, raise this
  # ceiling AND export SITE_PROBE_BUDGET_S on the bench host before the
  # next deploy-collector cycle.
  command_timeout_seconds: 30

  # Host-key handling: trust-on-first-use (TOFU) by default. A host's key is
  # pinned to known_hosts on first connect and accepted (so a fresh install and
  # an upgrade both onboard hosts automatically — no manual ssh-keyscan); a
  # later CHANGED key is rejected as a possible man-in-the-middle. Empty
  # known_hosts_path resolves to ~/.ssh/known_hosts of the service user, created
  # automatically if absent.
  known_hosts_path: ""
  # Set true ONLY for dev — it disables verification entirely (incl. the
  # changed-key check) and makes every connection MITM-able. Logged at startup.
  insecure_skip_host_key_check: false

log:
  # Minimum level emitted. "info" in prod; "debug" temporarily for
  # diagnostics (then revert — debug is verbose).
  level: "info"   # debug | info | warn | error

  # "json" routes nicely to journald + log aggregators. "text" is
  # easier to eyeball on a terminal.
  format: "json"  # json | text

metrics:
  # VictoriaMetrics root URL. The collector appends /write for pushes;
  # the dashboard's query proxy forwards to /api/v1/query_range.
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
  # Default per-server pull interval. Master plan §5: do not go below
  # 5 min over real SSH; 60s is the practical floor in dev.
  default_interval_seconds: 900

  # Cap on simultaneously-running pull jobs across the whole monitor.
  # Bump for large fleets; lower if pulls are saturating the host's
  # CPU or network.
  max_parallel: 10

  # Per-job ctx timeout. Pick well above P99(ssh + parse + push).
  # 30s is generous for healthy clusters.
  per_job_timeout_seconds: 30

# --- Phase 7 auth -------------------------------------------------------
auth:
  # Empty disables auth (dev or behind-internal-network deploys).
  # Non-empty applies HTTP basic auth to /api/v1/* and the SPA root —
  # browsers handle the prompt natively. /healthz stays open.
  # Username is ignored; only the password must match.
  password: ""

  # Realm shown in the browser credential prompt. Use a stable value;
  # browsers cache credentials per (origin, realm).
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

  # Custom rules merged with the defaults (unless disable_defaults).
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
# Long-lived SSH session per server that tails Frappe + MariaDB log
# files via tail -F and pushes lines to Loki as they arrive. Disabled
# by default — opt in once the streamer script is deployed to the
# bench (see docs/guide/usage.md → "Enable per-event streaming").
streaming:
  enabled: false

  # Free-form short string surfaced as a Loki/VM label so multi-monitor
  # deployments don't collide. Empty is fine for single-monitor setups.
  monitor_id: ""

  # Absolute path on each bench where frappe-monitor-stream.sh lives.
  # Phase 8b.1 expects you to deploy it there manually (or via a
  # config-management tool); auto-deploy from the monitor lands in 8b.2.
  script_path: "/home/frappe/.frappe-monitor/stream.sh"

  # Files the streamer should tail. Every server tails the same set in
  # 8b.1; per-server overrides arrive post-v1. The id appears as the
  # log_type label in Loki.
  files:
    # - id:   web
    #   path: /home/frappe/frappe-bench/logs/web.log
    # - id:   err
    #   path: /home/frappe/frappe-bench/logs/web.error.log
    # - id:   slowq
    #   path: /var/log/mysql/mariadb-slow.log

  # How often the LokiSink flushes buffered entries. Lower = lower
  # dashboard latency, higher = bigger Loki batches. Default 1s.
  flush_interval_seconds: 1

  # Per-stream buffer ceiling. Reaching this triggers an early flush
  # so a burst doesn't grow buffers unbounded. Default 500.
  max_batch_lines: 500

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
| `log.level ∈ {debug,info,warn,error}` | Enforced by slog; bad values would silently default. |
| `log.format ∈ {json,text}` | Same. |
| All `*_timeout_seconds` ≥ 1 | Zero-second timeouts are bugs disguised as features. |
| `metrics.vm_url` non-empty | Phase 2+ requires it. |
| `logs.loki_url` non-empty | Phase 3+ requires it. |
| `scheduler.*` ≥ 1 | Same reasoning. |
| `alerts.evaluation_interval_seconds` ≥ 15 (when enabled) | Anything lower is a tight loop on VM. |
| `alerts.telegram.bot_token` non-empty (when enabled) | Without it, no notification can land. |
| `alerts.telegram.chat_ids` non-empty (when enabled) | Same — fan-out target required. |
| `streaming.script_path` non-empty (when enabled) | Required — sessions can't exec an empty path. |
| `streaming.files` non-empty (when enabled) | Required — at least one (id, path) pair to tail. |
| `streaming.flush_interval_seconds` ≥ 1 (when enabled) | Subsecond flushes burn CPU for no gain. |
| `streaming.max_batch_lines` ≥ 1 (when enabled) | Zero would mean "flush on every line" — same problem. |
| `streaming.max_backoff_seconds` ≥ `min_backoff_seconds` (when enabled) | Otherwise reconnect would jitter forever. |

The error message names the offending key, e.g. `metrics.push_timeout_seconds must be >= 1, got 0`.

## Env-var overrides

Any key can be overridden by an environment variable. Pattern:

```
MONITOR_<SECTION>__<KEY>
```

Double-underscore separates levels. Example:

```bash
# Override scheduler.default_interval_seconds without editing yaml:
sudo systemctl edit frappe-monitor
# In the editor:
[Service]
Environment=MONITOR_SCHEDULER__DEFAULT_INTERVAL_SECONDS=300
# Save, exit, reload:
sudo systemctl daemon-reload
sudo systemctl restart frappe-monitor
```

Useful for one-off troubleshooting (`MONITOR_LOG__LEVEL=debug`) without committing changes to `monitor.yaml`.

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

## Control panel + DB monitor

```yaml
# Allowlisted bench/service commands run over SSH (no free-form commands).
# These timeouts are SEPARATE from ssh.command_timeout_seconds: a control
# action runs under its own ceiling, so a long `bench update`/`migrate` is
# not killed at the 30s command timeout.
control:
  action_timeout_seconds: 300          # normal action ceiling (5 min)
  danger_action_timeout_seconds: 1800  # Dangerous actions e.g. bench update (30 min)
  read_timeout_seconds: 20             # synchronous reads (site-config fetch)
  max_output_bytes: 65536              # stored command-output cap
  max_concurrent: 4                    # in-flight control runs

# DB replication monitor (idle until targets are added in the dashboard).
dbmonitor:
  interval_seconds: 60          # sweep cadence across all targets
  max_parallel: 4               # concurrent target checks per sweep
  command_timeout_seconds: 15   # per status query
```

## Tuning cheatsheet

| Symptom | Knob to try |
|---|---|
| Dashboard charts feel stale | `scheduler.default_interval_seconds: 300` (5 min) |
| Pulls pile up at top of the hour | `scheduler.max_parallel: 20` (or higher) |
| SSH timeouts on a slow bench host | `ssh.command_timeout_seconds: 60` (also bump `server.write_timeout_seconds` to match) |
| `bench update`/`migrate` from the control panel needs longer | `control.danger_action_timeout_seconds: 3600` |
| Adding a server fails with a host-key "CHANGED" error | the host's key changed (rebuilt host or MITM); if legitimate, remove its line from `known_hosts` and reconnect (or, dev only, `ssh.insecure_skip_host_key_check: true`) |
| VM eating disk too fast | edit `--retentionPeriod=30d` in `deploy/docker-compose.prod.yml`, then restart the stack unit |
| Need verbose collector output | `log.level: "debug"` (revert when done — high volume) |
