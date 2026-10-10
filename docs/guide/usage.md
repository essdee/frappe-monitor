# Using Frappe Monitor

Frappe Monitor is a read-only observability console for SSH-reachable Frappe
servers. It does not deploy, pull application code, update packages, restart a
bench, or install a target-side agent.

## 1. Prepare least-privilege SSH access

The production installer creates a dedicated key on the monitor host. Print
its public half and authorize it for a user that can read the Frappe bench,
process table, Supervisor status, and log files:

```bash
sudo cat /var/lib/frappe-monitor/.ssh/id_ed25519.pub
```

Append the public key to the target user's `authorized_keys`. Capture the
target host key, verify its fingerprint through an independent channel, then
add it to the monitor's strict trust store:

```bash
ssh-keyscan -p 22 -H bench1.example.com > /tmp/bench1.known-host
ssh-keygen -lf /tmp/bench1.known-host
# Only after verifying the fingerprint:
sudo tee -a /var/lib/frappe-monitor/.ssh/known_hosts \
  < /tmp/bench1.known-host >/dev/null
sudo chown frappe-monitor:frappe-monitor \
  /var/lib/frappe-monitor/.ssh/known_hosts
sudo chmod 600 /var/lib/frappe-monitor/.ssh/known_hosts
```

The target user does not need permission to change services or write into a
bench. A restricted account or SSH policy is recommended in production.

## 2. Register a target

Target registration changes only the monitor's local SQLite registry. It does
not touch the remote server.

In the console, open **Servers → Add server**, enter the hostname, SSH user,
port, and private-key path, then choose **Test connection**. The diagnostic
runs only `echo ok` over SSH and does not create the target. Save when the
connection and host-key policy are correct. Bench paths may be left empty for
read-only automatic discovery, including common `frappe-bench` and versioned
`frappe-13`, `frappe-15`, or `frappe-16` home-directory layouts. Labels such as
environment, region, and owner are optional and searchable.

The equivalent API request is:

```bash
curl -u "admin:<password>" -X POST http://127.0.0.1:8080/api/v1/servers \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "prod1",
    "hostname": "bench1.example.com",
    "ssh_user": "frappe",
    "ssh_port": 22,
    "ssh_key_path": "/var/lib/frappe-monitor/.ssh/id_ed25519",
    "bench_paths": ["/home/frappe/frappe-bench"]
  }'
```

`bench_paths` is optional. An empty list enables standard-path discovery. The
monitor collects immediately after registration and on every scheduler tick.
The embedded collector is sent to `bash -s` over SSH stdin, so there is no
collector deployment or remote version to maintain.

## 3. Use the monitoring console

- **Overview** shows fleet reachability, bench/site counts, current incidents,
  and the targets that need attention.
- **Servers** is a searchable inventory with UI-managed onboarding. Server
  detail shows CPU, memory, disks, load, network, uptime, and top processes;
  **Configure** edits or removes the monitor-local target record.
- **Benches** shows Frappe version, configured web/background-worker capacity,
  Supervisor state, actual web and queue-worker processes, per-process
  CPU/RSS/uptime, RQ busy/idle occupancy, and Redis queue depth.
- **Sites** shows current health, HTTP status, response time, failure reason,
  request/job performance, busiest API routes, correlated database slow-query
  charts, readable error signals, and shortcuts into related logs.
- **Logs** searches Loki across server, bench, site, and log type. It supports
  text filtering, live/pause, time-window selection, result limits, and export.
- **Incidents** shows current failures, durable fire/recovery history, duration,
  affected target, related-log links, and configured detection rules.
- **Settings** manages the whole local control plane: storage, SSH, collection,
  VictoriaMetrics/Loki, console security, Telegram, custom rules, and log
  streaming. Its runtime card shows live uptime, heap/goroutines, stream
  connectivity, and buffer usage without relying on the metrics backend.
  Connectivity can be tested before saving. Saved service settings take effect
  after the restart command shown in the UI.

The global timeline controls every historical query. Browser polling is
completion-based, so a slow backend cannot create overlapping refresh storms.

## 4. Service and worker interpretation

Frappe has two distinct forms of worker capacity:

- **Web/Gunicorn workers:** the monitor reads configured capacity from
  `common_site_config.json` and enumerates running Gunicorn processes. Stock
  Gunicorn does not expose an exact request-by-request busy/free state through
  process inspection, so the console does not invent one.
- **RQ background workers:** Redis maintains a worker registry with worker
  state and current-job identity. The monitor therefore reports exact
  registered, busy, and idle RQ counts and alerts when registered workers are
  present but none are idle.

Supervisor service groups and individual bench processes are both inspected.
For development `bench start` installations, the process inventory also
identifies Redis, web, Socket.IO, scheduler, watcher, RQ, event-dispatcher, and
JSON-worker units. Werkzeug reloaders and RQ work-horse children are collapsed
into their owning service so they do not inflate capacity. Stopped/failed
service state and complete RQ saturation are bundled critical alert rules.

## 5. Enable searchable logs

Open **Settings → Log streaming**, enable the streamer, and save/restart. Empty
manual file overrides enable standard-path discovery. The equivalent YAML is:

```yaml
streaming:
  enabled: true
  files: []
```

With an empty list, the monitor discovers `<bench>/logs/*.log` and
`<bench>/sites/*/logs/*.log` over read-only SSH. It then runs the embedded
streamer through SSH stdin. New files start at EOF; persisted device/inode and
byte offsets resume known files after reconnect or monitor restart.

For a non-standard file, configure it explicitly:

```yaml
streaming:
  enabled: true
  files:
    - server_id: 1
      id: prod_custom_error
      path: /srv/bench-prod/logs/custom.error.log
      log_type: error
      bench: bench-prod
```

The stream reads existing files only. It does not enable logging, rotate a
file, or alter Frappe.

### Per-site request and job telemetry

Frappe's existing `monitor` setting must be enabled by an operator on the
target bench or site. Frappe Monitor reports this prerequisite on every site
and does not change it. Once Frappe writes `logs/monitor.json.log`, automatic
log discovery ingests each request/job event and derives bounded per-site
metrics for request rate, p95 latency, HTTP errors, route performance, job
duration, and queue wait. Request UUIDs and raw paths remain in Loki, never in
time-series labels.

### Site-correlated slow queries

Slow-query logging must likewise be enabled in MariaDB/MySQL or PostgreSQL by
the database operator, and the target SSH account must be able to read the
resulting `*slow*.log`. The monitor discovers only explicitly slow-log-looking
files. It hashes each site's database and user identity on the target, joins
slow events to the matching site, and produces a safe structured event for the
site dashboard. Database names, users, passwords, and SQL are not copied into
metrics. Exact SQL stays in the original host-level Loki log and can be reached
through trace search when `FRAPPE_TRACE_ID` is present.

## 6. Enable Telegram incidents

Open **Settings → Alerts**, enter the BotFather token and one chat ID per line,
use **Test bot**, enable the alert engine, and save/restart. The equivalent YAML
is:

```yaml
alerts:
  enabled: true
  evaluation_interval_seconds: 60
  notify_repeat_seconds: 3600
  telegram:
    bot_token: "<bot token>"
    chat_ids: ["<chat id>"]
    send_timeout_seconds: 5
```

Bundled rules cover unreachable servers, nearly full disks, sustained CPU,
memory and swap pressure, unhealthy or slow sites, high Redis queues, complete
RQ saturation, missing Gunicorn capacity, failed bench services, slow per-site
requests/jobs/database queries, high HTTP 5xx rates, and log-stream
disconnection. Fires, configured repeats, and recoveries are delivered to
Telegram. Each episode remains in SQLite incident history after recovery.

## 7. Metrics emitted

| Prefix | Scope | Examples |
|---|---|---|
| `frappe_server_*` | server, mount, interface | reachability, CPU counters, memory, disk, load, network, uptime |
| `frappe_bench_*` | server + bench + queue/service | apps, configured workers, Supervisor, RQ busy/idle, queue depth, service PID/state/CPU/RSS/uptime |
| `frappe_site_*` | server + bench + site | internal localhost health, HTTP status, response time, failure reason |
| `frappe_site_public_*` | server + bench + site | public DNS/TLS/HTTP health, response time, failure reason, certificate expiry |
| `frappe_site_request_*`, `frappe_site_job_*` | server + bench + site | request/job duration, rate inputs, status/error, route/method and queue wait |
| `frappe_database_slow_query_*` | server + opaque identity | duration, lock time, rows sent/examined, engine and operation |
| `frappe_stream_*` | server | stream connection state |

Every metric carries the canonical server name. Bench and site labels preserve
the hierarchy used by the UI and alert rules.

## 8. Local registry changes

`PATCH` and `DELETE /api/v1/servers/{id}` affect monitor-local configuration.
They can change where future read-only SSH inspection connects or remove a
target from the registry; they never mutate the target. Existing
VictoriaMetrics and Loki data ages out according to backend retention.
