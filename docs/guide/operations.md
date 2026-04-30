# Operations

Day-2 reference: lifecycle, logs, health, upgrades, backups, sizing.

## Lifecycle

```bash
# Status of both units.
sudo systemctl status frappe-monitor frappe-monitor-stack

# Stop everything (monitor first, stack second).
sudo systemctl stop frappe-monitor
sudo systemctl stop frappe-monitor-stack

# Start everything (stack first so VM + Loki are ready).
sudo systemctl start frappe-monitor-stack
sudo systemctl start frappe-monitor

# Restart just the binary (e.g. after editing config).
sudo systemctl restart frappe-monitor

# Disable autostart (units stay installed; will not boot with the host).
sudo systemctl disable frappe-monitor frappe-monitor-stack

# Re-enable autostart.
sudo systemctl enable frappe-monitor frappe-monitor-stack
```

The shutdown contract on `frappe-monitor.service`: scheduler stops accepting new ticks → in-flight pulls observe `ctx.Done()` and unwind → HTTP server drains. Total budget is 10s; the unit gives it 15s before SIGKILL.

## Logs

`frappe-monitor` writes structured JSON to stdout/stderr; systemd routes it to journald.

```bash
# Tail live.
sudo journalctl -u frappe-monitor -f

# Last 200 lines.
sudo journalctl -u frappe-monitor -n 200

# Since a specific time.
sudo journalctl -u frappe-monitor --since "1 hour ago"

# Just errors.
sudo journalctl -u frappe-monitor -p err

# VM + Loki logs (from inside the containers).
sudo journalctl -u frappe-monitor-stack -f       # the compose unit's bring-up output
docker logs -f frappe-monitor-vm                  # VM itself
docker logs -f frappe-monitor-loki                # Loki itself
```

Each log line is a JSON object. Filter with `jq`:

```bash
sudo journalctl -u frappe-monitor -o cat | jq 'select(.level == "WARN")'
sudo journalctl -u frappe-monitor -o cat | jq 'select(.msg | test("ssh"))'
```

## Health checks

```bash
# Monitor binary.
curl -fsS http://127.0.0.1:8080/healthz
# {"status":"ok"}

# VictoriaMetrics.
curl -fsS http://127.0.0.1:8428/health

# Loki.
curl -fsS http://127.0.0.1:3100/ready

# All servers known to the monitor + their last-pinged status.
curl -fsS http://127.0.0.1:8080/api/v1/servers | jq '.[] | {name, status, last_pinged_at, last_error}'
```

For external probes (load balancer, uptime monitor): use `/healthz` over the public hostname; that's the cheapest endpoint and never requires a backend.

## Upgrades

```bash
cd /path/to/frappe_monitor
git pull
sudo ./deploy/install.sh
```

The install script:

- Rebuilds the binary (npm + vite + go).
- Atomically replaces `/usr/local/bin/frappe-monitor` (so an in-flight upgrade can't land a half-deployed binary).
- Reinstalls systemd units only if changed.
- Restarts `frappe-monitor.service`.
- **Does not touch** `/etc/frappe-monitor/monitor.yaml` or `/var/lib/frappe-monitor/`.

Roll back: `git checkout <previous-tag-or-sha>` then re-run the installer. SQLite migrations are forward-only via `ent` — major schema changes get a release note in `docs/YYYY-MM-DD/N.md` with the rollback story.

## Backups

Three things to back up (in priority order):

1. **`/etc/frappe-monitor/monitor.yaml`** — your config. Tiny.
2. **`/var/lib/frappe-monitor/monitor.db`** — server registry + log cursors. SQLite, single file, restartable.
3. **VM + Loki volumes** — historical metrics + logs. Larger; restore is "best-effort" since the collector will repopulate from the next cycle anyway.

```bash
# Snapshot config + sqlite (consistent — sqlite uses WAL but a file copy of the
# main DB is atomic enough for backup; for absolute correctness use `.backup`).
sudo install -m 0640 /etc/frappe-monitor/monitor.yaml /backup/monitor.yaml.$(date +%F)
sudo sqlite3 /var/lib/frappe-monitor/monitor.db ".backup '/backup/monitor.db.$(date +%F)'"

# Snapshot VM + Loki volumes (stop the stack first so files are quiescent).
sudo systemctl stop frappe-monitor frappe-monitor-stack
sudo tar -C /var/lib/docker/volumes -czf /backup/vm.$(date +%F).tar.gz frappe-monitor-vm-data
sudo tar -C /var/lib/docker/volumes -czf /backup/loki.$(date +%F).tar.gz frappe-monitor-loki-data
sudo systemctl start frappe-monitor-stack frappe-monitor
```

Restore: reverse the order (extract volumes, replace sqlite/config, start services).

## Capacity & sizing

| Workload | Per pull cycle (15 min default) |
|---|---|
| SSH connections | 1 per server (pooled, max 2 concurrent per host) |
| Bytes ingested to VM | ~5 KB per server + ~3 KB per bench + ~2 KB per site |
| Bytes ingested to Loki | depends on log volume; bounded by `MaxBytesPerCycle` (1 MiB per file) |
| SQLite writes | one row per server status update + one row per log cursor |

Disk growth (rough — heavily depends on cardinality):

- VM at 90-day retention: ~50 MB per server, plus ~30 MB per bench/site combination.
- Loki: ~10–500 MB per server per day, depending on log noise.
- SQLite: stays under 100 MB even with hundreds of servers.

Tune `metrics.vm_url`'s `--retentionPeriod` (set in `deploy/docker-compose.prod.yml`, default 90d) to fit your disk. For ≤ 25 servers on a 50 GB SSD, defaults are fine for years.

## Scheduler tuning

`scheduler.default_interval_seconds` (default 900s = 15 min) is the per-server pull cadence. Floor: 60s — anything lower and you're hammering the bench host's SSH daemon. Phase 7 will add a per-server override via the API.

`scheduler.max_parallel` (default 10) caps simultaneous pulls. Bump if you have lots of servers and the cycle overlaps; lower if SSHing all of them at once is causing load spikes on the monitor host.

`scheduler.per_job_timeout_seconds` (default 30) — kill any single pull that hangs longer than this. P99 of a healthy cycle is well under 5s; 30s is a generous safety margin.

## Resource hardening

The `frappe-monitor.service` unit already applies `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `MemoryDenyWriteExecute`, etc. To verify:

```bash
sudo systemd-analyze security frappe-monitor
```

Score should be in the 1–3 range (lower is better; 1.0 is "exposed", 9.x is "unsafe"). If you don't run Caddy on the same host, you can also drop `CapabilityBoundingSet=` to empty for further isolation.

## Reading the dashboard's data freshness

- **Server status badges**: updated on every successful (or failed) pull cycle, persisted to SQLite. Worst case staleness is `default_interval_seconds`.
- **Server / bench / site charts**: query VM in real time via the monitor's `/api/v1/metrics/query` proxy. The dev compose sets `-search.latencyOffset=0s` so freshly pushed samples are immediately visible; production compose does the same.
- **Site-detail log feed**: queries Loki via `/api/v1/logs/query`, scoped to `{server, bench, log_type="error"}`. Ingest delay is typically < 5s.

If a chart shows "no data in this window" but you expect data, see [`troubleshooting.md`](troubleshooting.md).
