# Troubleshooting

Start with the read-only diagnostic report. It validates configuration, derives
the monitor health endpoint from YAML, reports systemd resource use, checks the
bundled storage services when installed, performs SQLite `quick_check` when the
CLI is available, and includes recent warning-level journal entries. It does not
print passwords, tokens, SSH keys, or backend URLs.

```bash
cd /path/to/frappe_monitor
sudo bash deploy/diagnose.sh | tee frappe-monitor-diagnostic.txt
```

## Installer reports `Unit docker.service not found`

Older units depended on the apt/upstream service name even when Docker came
from Snap (`snap.docker.dockerd.service`). Pull the current source and rerun the
idempotent installer:

```bash
cd /path/to/frappe_monitor
git pull
sudo ./deploy/install.sh
```

The current stack unit checks the Docker daemon directly and supports apt,
upstream, and Snap Docker. If the preflight says the daemon is unavailable,
start the package you installed:

```bash
sudo systemctl enable --now docker  # apt/upstream
sudo snap start docker              # Snap
```

## Monitor restart-loops after an alert configuration edit

Stop the loop and run the side-effect-free preflight:

```bash
sudo systemctl stop frappe-monitor
sudo /usr/local/bin/frappe-monitor \
  --config /etc/frappe-monitor/monitor.yaml --check-config
```

Telegram bot tokens must be copied from `@BotFather`; chat IDs must be numeric
and unique. Disable alerts while correcting them if necessary, validate again,
then start the service. The current installer and Settings page reject these
errors before saving or restarting.

## No target data appears

Check the monitor service and its registry first:

```bash
curl -fsS http://127.0.0.1:8080/healthz
curl -fsS http://127.0.0.1:8080/api/v1/servers | jq .
sudo journalctl -u frappe-monitor -n 100 --no-pager
```

The first collection starts at monitor startup and target registration, then
repeats at `scheduler.default_interval_seconds`. There is no remote collector
to install. Every cycle sends the embedded read-only program to `bash -s` over
SSH stdin.

Test the same credentials outside the app:

```bash
sudo -u frappe-monitor ssh \
  -i /var/lib/frappe-monitor/.ssh/id_ed25519 \
  -o UserKnownHostsFile=/var/lib/frappe-monitor/.ssh/known_hosts \
  -o StrictHostKeyChecking=yes frappe@bench1.example.com \
  'hostname; test -r /proc/stat; echo ok'
```

Common failures:

| Symptom | Cause | Action |
|---|---|---|
| `key` | Private-key path is missing or unreadable by the service user | Use `/var/lib/frappe-monitor/.ssh/id_ed25519`; do not point the sandboxed service at a personal `/home/.../.ssh` key. |
| `host_key` | Missing or changed known-host entry | Verify the new fingerprint independently before updating `known_hosts`. |
| `auth` | Key/user mismatch | Check `authorized_keys`, target user, key ownership, and key path. |
| `dial` | DNS, route, port, or firewall | Test TCP/SSH from the monitor host. |
| `timeout` | Slow target or network | Measure SSH latency; only then tune the validated timeout settings. |
| reachable but no benches | Non-standard bench root or read permissions | Add an explicit `bench_paths` entry and verify `sites/`, `apps/`, and `Procfile` are readable. |

## Metrics are collected but charts are empty

```bash
curl -fsS http://127.0.0.1:8428/health
curl -GfsS http://127.0.0.1:8428/api/v1/query \
  --data-urlencode 'query=frappe_server_reachable' | jq .
```

VictoriaMetrics must run with `-influxSkipSingleField`; the bundled Compose
files already set it. Without it, series gain an unexpected `_value` suffix.
Also widen the UI time window to cover at least two collection cycles.

## A bench reports fewer web workers than configured

The configured number comes from `sites/common_site_config.json`. Running
capacity comes from the actual process tree. A lower number is useful evidence:
the service may still be starting, children may have crashed, or the config may
not match the active Supervisor/Gunicorn command. Inspect the service table,
PID uptime, CPU/RSS, and Supervisor state together.

Do not compare the web count to RQ `busy/idle`. They are separate pools. Exact
busy/idle is available for RQ through Redis; stock Gunicorn does not expose an
exact request-occupancy value through read-only process inspection.

## RQ shows zero registered workers

Verify the Redis queue port is readable from `<bench>/config/redis_queue.conf`
and that `redis-cli` is available to the SSH user:

```bash
redis-cli -p <queue-port> SCARD rq:workers
redis-cli -p <queue-port> SMEMBERS rq:workers
```

A worker process can exist while its Redis registration is missing or stale.
The process table and RQ registry are intentionally shown as separate signals.

## Supervisor state is zero or absent

The SSH user may not be permitted to run `supervisorctl status`, or the bench
may be a development process not managed by Supervisor. The monitor still
enumerates matching bench processes. Grant read-only Supervisor status access
if production service-group state is required; restart/update permissions are
not needed.

## Sites are unknown or unhealthy

The collector probes `/api/method/ping` locally on the target with the site Host
header. `unknown` means the bounded probe budget expired; it is not treated as
a confirmed outage. Check:

- `webserver_port` and the active listener;
- Host-header routing for the site;
- response latency under load;
- `is_healthy`, `http_status_code`, and `failure_reason_code` together;
- related web and error logs in Log Explorer.

## Log Explorer has no data

Confirm Loki and streaming:

```bash
curl -fsS http://127.0.0.1:3100/ready
sudo journalctl -u frappe-monitor -n 200 --no-pager | grep streamer
```

With `streaming.files: []`, discovery searches standard bench and site log
directories. Explicitly configure paths for a non-standard layout. A newly
discovered file starts at EOF, so only lines appended after the first stream
connection appear. Existing known files resume from their committed cursor.

Loki rejection or cursor-write failure applies bounded backpressure and does
not advance the durable cursor. Check Loki availability, retention, and the
streamer's file-read permission.

## Incidents or Telegram messages do not appear

Check that `alerts.enabled` is true, the bot token/chat IDs are valid, and
VictoriaMetrics has fresh rule input. Alert recovery is deliberately deferred
when fresh positive evidence is missing, which prevents a telemetry outage
from being reported as recovery.

The Incidents page separates:

- current active reconciliation state;
- durable historical episodes, including resolved time and duration;
- configured rules.

A Telegram delivery failure leaves the notification retryable and does not
advance its cooldown.

## Authentication loops

Changing `auth.password` invalidates existing signed sessions. Clear the site
cookie and sign in again. If a reverse proxy terminates TLS, ensure its address
is included in `server.trusted_proxy_cidrs`; untrusted forwarded headers are
ignored by design.

## Backend retention or disk pressure

VictoriaMetrics and Loki are the high-volume stores; SQLite contains only
registry, snapshots, cursors, active alert state, and compact incident history.
Tune backend retention in the Compose configuration and monitor the storage
filesystem itself. Restarting the monitor does not erase cursor or incident
state.
