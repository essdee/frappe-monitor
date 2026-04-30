# Using Frappe Monitor

Day-to-day flow: register a bench server, watch its metrics, drill into benches and sites.

This assumes you already deployed via `deploy/install.sh` and the dashboard is reachable at `http://<host>:8080`.

## 1. Prepare SSH access

The monitor connects from the host running `frappe-monitor.service` (as the `frappe-monitor` system user) to each Frappe bench server you want to monitor. You only need to do this once, when you first set up the host.

```bash
# On the monitor host:
sudo -u frappe-monitor ssh-keygen -t ed25519 -N "" \
    -f /var/lib/frappe-monitor/.ssh/id_ed25519 -C frappe-monitor
sudo cat /var/lib/frappe-monitor/.ssh/id_ed25519.pub
```

Copy the printed public key. On each bench server, append it to the authorized_keys of a user with read access to the bench (typically `frappe`):

```bash
# On each bench server:
sudo -u frappe mkdir -p /home/frappe/.ssh
sudo -u frappe tee -a /home/frappe/.ssh/authorized_keys <<< "<paste pubkey here>"
sudo -u frappe chmod 600 /home/frappe/.ssh/authorized_keys
```

Verify it works from the monitor host:

```bash
sudo -u frappe-monitor ssh -i /var/lib/frappe-monitor/.ssh/id_ed25519 \
    -p 22 frappe@bench1.example.com 'echo hello'
```

If that prints `hello`, you're good.

## 2. Register a server

There's no in-dashboard "Add server" form yet (Phase 7 will add it). Use curl:

```bash
curl -X POST http://127.0.0.1:8080/api/v1/servers \
  -H 'Content-Type: application/json' \
  -d '{
    "name":         "prod1",
    "hostname":     "bench1.example.com",
    "ssh_user":     "frappe",
    "ssh_port":     22,
    "ssh_key_path": "/var/lib/frappe-monitor/.ssh/id_ed25519"
  }'
```

Response:

```json
{
  "id": 1,
  "name": "prod1",
  "hostname": "bench1.example.com",
  "ssh_user": "frappe",
  "ssh_port": 22,
  "ssh_key_path": "/var/lib/frappe-monitor/.ssh/id_ed25519",
  "status": "unknown",
  ...
}
```

`409` on duplicate hostname; `400` on missing fields.

## 3. Verify SSH from the monitor

```bash
curl -X POST http://127.0.0.1:8080/api/v1/servers/1/test-connection
# {"reachable":true,"latency_ms":42}
```

If `reachable: false`, look at `error_kind`:

| `error_kind` | Meaning | Fix |
|---|---|---|
| `auth` | Key rejected | Verify the pubkey is in the bench user's `authorized_keys`. |
| `dial` | TCP connect failed | Check hostname, port, firewall. |
| `timeout` | Took longer than `ssh.dial_timeout_seconds` | Network slow, or wrong host. |
| `unknown` | Anything else | Check the response's `error` field for details. |

The diagnostic endpoint is always 200 OK with `reachable=false` on failure (it's a *probe*, not a *health check*) — the HTTP layer succeeded; SSH is what failed.

## 4. Deploy the collector

```bash
curl -X POST http://127.0.0.1:8080/api/v1/servers/1/deploy-collector
# {"deployed":true,"version":"v3"}
```

This pipes the embedded `frappe-monitor-collect.sh` (a self-contained bash script) to the target via SSH and `chmod +x`s it. The script lands at `~/.frappe-monitor/collect.sh` for the SSH user. The scheduler then runs it every cycle.

The version is the first comment line in the embedded script. If you upgrade the monitor binary and the script changes, re-deploy:

```bash
curl -X POST http://127.0.0.1:8080/api/v1/servers/1/deploy-collector
```

## 5. Wait for the first cycle

The scheduler picks up the new server on the next tick. With the default 15-minute interval, the first batch of metrics arrives within 15 minutes; if you want immediate feedback, lower it temporarily:

```bash
sudo systemctl edit frappe-monitor
# Add:
[Service]
Environment=MONITOR_SCHEDULER__DEFAULT_INTERVAL_SECONDS=60
# Save, then:
sudo systemctl daemon-reload && sudo systemctl restart frappe-monitor
```

Watch the monitor logs:

```bash
sudo journalctl -u frappe-monitor -f
```

You'll see lines like `collector: pulled successfully server_id=1 took=1.2s`.

## 6. Open the dashboard

Browse to `http://<monitor-host>:8080`. The sidebar has three pages:

### Servers (`/servers`)

Cards sorted worst-first (unreachable → unknown → reachable). Each card shows status, hostname, and last-seen timestamp. Click one to drill into per-server charts:

- CPU usage % (derived from `frappe_server_cpu_*` jiffies)
- Memory usage %
- Disk usage % per mount
- Load average (1m, 5m, 15m)

### Benches (`/benches`)

All benches across all servers, grouped by server. Each bench is one card → click for:

- Frappe version, apps count, supervisor running/total, queue count
- Per-queue Redis depth (table)
- Time-series charts: supervisor processes, queue depths, apps count

### Sites (`/sites`)

Flat searchable table of every site reporting metrics. Substring filter across server / bench / site columns. Click into a site for:

- Health status block (HTTP code + healthy bit)
- Charts: response time (ms), HTTP status code, healthy
- "Recent error log lines" panel — pulls bench-scoped error logs from Loki for the active time window. (Per-site log labels arrive in Phase 7.)

## Timeline filter (top header)

Six presets: `15m`, `1h`, `6h`, `24h`, `7d`, `30d`. Picking one rewrites the URL with `?from=&to=&step=` query params. Every chart on every page reads from the same range. Refresh interval is selectable too (default 30s, `0` to disable polling).

## Removing a server

The current API has Create / List / Get / test-connection / deploy-collector. Delete is a Phase 7 deliverable; for now, you'd manipulate SQLite directly:

```bash
sudo systemctl stop frappe-monitor
sudo -u frappe-monitor sqlite3 /var/lib/frappe-monitor/monitor.db \
    "DELETE FROM servers WHERE id = 1;"
sudo systemctl start frappe-monitor
```

The metrics already in VictoriaMetrics will fall off the dashboard once they age past the retention window.

## What metrics are emitted

| Prefix | Cardinality | Examples |
|---|---|---|
| `frappe_server_*` | one series per `(server, mount/iface)` | `cpu_user`, `cpu_idle`, `mem_total_bytes`, `disk_used_bytes`, `load_1m`, `net_rx_bytes`, `uptime_seconds` |
| `frappe_bench_*` | one series per `(server, bench, queue?)` | `apps_count`, `supervisor_running`, `supervisor_total`, `redis_queue_depth`, `info` (with `frappe_version` label) |
| `frappe_site_*` | one series per `(server, bench, site)` | `http_status_code`, `http_response_ms`, `is_healthy` |

Every metric carries `server` as a label; bench metrics also carry `bench`; site metrics carry `bench` and `site`. The dashboard relies on this hierarchy when drilling down.

For raw PromQL exploration:

```bash
# Hit VM directly:
curl 'http://127.0.0.1:8428/api/v1/query?query=frappe_server_load_1m'

# Or through the monitor's proxy (so timeline filter works):
curl 'http://127.0.0.1:8080/api/v1/metrics/query?query=frappe_server_load_1m&start=1714468800&end=1714472400&step=15'
```
