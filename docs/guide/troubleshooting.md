# Troubleshooting

When something's not working, work the list top-down.

## Quick triage

```bash
# Is everything running?
sudo systemctl status frappe-monitor frappe-monitor-stack

# Recent errors?
sudo journalctl -u frappe-monitor -p err -n 50

# Are VM + Loki healthy?
curl -fsS http://127.0.0.1:8428/health   # → "VictoriaMetrics is Healthy"
curl -fsS http://127.0.0.1:3100/ready    # → "ready"

# Is the API reachable?
curl -fsS http://127.0.0.1:8080/healthz  # → {"status":"ok"}

# Are servers being pulled?
sudo journalctl -u frappe-monitor -f | grep collector
```

## "I registered a server but no data shows up"

Walk this list:

### 1. Did `test-connection` succeed?

```bash
curl -X POST http://127.0.0.1:8080/api/v1/servers/<id>/test-connection
```

If `reachable: false`, fix that first — see [`usage.md`](usage.md#3-verify-ssh-from-the-monitor) for `error_kind` meanings.

### 2. Did `deploy-collector` succeed?

```bash
curl -X POST http://127.0.0.1:8080/api/v1/servers/<id>/deploy-collector
# Expect: {"deployed":true,"version":"v3"}
```

If it fails: probably an SSH issue (key path readable to `frappe-monitor`?), or the bench user can't write to its own home dir.

### 3. Has the scheduler ticked yet?

The default interval is 15 minutes. Check the logs:

```bash
sudo journalctl -u frappe-monitor -f | grep -E "(scheduler|collector)"
```

You should see lines like:

```
INFO collector: pulled successfully server_id=1 took=1.2s
INFO collector: metrics pushed series=42
```

If you see `collector: ssh exec failed`, the SSH part is broken. If you see `collector: parse failed`, the bench script returned malformed output (run it manually and inspect: `ssh frappe@<host> bash ~/.frappe-monitor/collect.sh`).

### 4. Did metrics actually land in VM?

```bash
curl -fsS 'http://127.0.0.1:8428/api/v1/label/server/values' | jq .
```

If your server's name is in the array, metrics are landing. If not, the push step is failing — check the monitor logs for `metrics push failed` lines.

### 5. Is the dashboard querying the right thing?

```bash
curl -fsS "http://127.0.0.1:8080/api/v1/metrics/query?query=frappe_server_load_1m{server=\"<your-name>\"}&start=$(($(date +%s)-3600))&end=$(date +%s)&step=15" | jq .
```

If this returns a populated `data.result`, metrics are queryable; the issue is browser-side. Open devtools, check the Network tab for the `/api/v1/metrics/query` calls.

## "test-connection says auth"

The most common cause: the SSH key path you registered isn't readable by the `frappe-monitor` user.

```bash
sudo -u frappe-monitor ls -la /var/lib/frappe-monitor/.ssh/
sudo -u frappe-monitor cat /var/lib/frappe-monitor/.ssh/id_ed25519 >/dev/null \
    && echo OK || echo "frappe-monitor cannot read this key"
```

Permissions must be `0600` for the private key, owned by `frappe-monitor:frappe-monitor`.

Then verify the pubkey is on the bench server:

```bash
sudo -u frappe-monitor ssh -i /var/lib/frappe-monitor/.ssh/id_ed25519 -p <port> <user>@<host> 'echo OK'
```

If that works but `test-connection` fails, double-check the registered `ssh_user`, `ssh_port`, and `ssh_key_path` match exactly:

```bash
curl -fsS http://127.0.0.1:8080/api/v1/servers/<id>
```

## "VM unreachable" / "Loki unreachable"

Are the containers up?

```bash
docker ps | grep -E "frappe-monitor-(vm|loki)"
```

If neither is running:

```bash
sudo systemctl status frappe-monitor-stack
sudo systemctl restart frappe-monitor-stack
```

If they're up but unreachable from the binary, check the URLs in `/etc/frappe-monitor/monitor.yaml` actually match what's bound:

```bash
ss -tlnp | grep -E "(8428|3100)"
```

For split-tier deploys (monitor on a different host than VM/Loki), check the firewall on the metrics host allows inbound from the monitor host's IP.

## "Dashboard charts show 'no data in this window'"

In order of likelihood:

1. **Time range is too narrow / before the data started.** Switch the timeline filter to `24h` or `7d`. New servers won't have older data.
2. **Server registered but never pulled.** See [`#i-registered-a-server-but-no-data-shows-up`](#i-registered-a-server-but-no-data-shows-up).
3. **VM's `-search.latencyOffset` is non-zero in your compose.** Production compose sets it to 0; check `/opt/frappe-monitor/deploy/docker-compose.prod.yml` if you customized.
4. **Browser's clock is wildly off.** The chart axis is in browser time; if the host is set to a future time the data lands "before now" in the chart.

## "Site detail shows 'Failed to load logs'"

The Loki endpoint or log_type filter mismatch. Sanity-check directly:

```bash
curl -fsS 'http://127.0.0.1:3100/loki/api/v1/labels' | jq .
curl -fsS 'http://127.0.0.1:3100/loki/api/v1/label/log_type/values' | jq .
```

You should see `error` and `slow_query` (whichever your collector tails). If the labels are different, the collector isn't tailing the bench's log files. Check on the bench:

```bash
ls -la ~/frappe-bench/logs/
# Are there error.log / slow.log files?
```

Loki streams are bench-scoped; site-scoped logs land in Phase 7.

## "frappe-monitor.service won't start"

```bash
sudo journalctl -u frappe-monitor -n 50 --no-pager
```

Common causes:

| Log line | Cause | Fix |
|---|---|---|
| `database.path is required` | Empty path in yaml | Set `database.path` in `monitor.yaml`. |
| `metrics.vm_url is required` | Same idea | Set `metrics.vm_url`. |
| `permission denied` on SQLite path | `frappe-monitor` user can't write there | `sudo chown -R frappe-monitor:frappe-monitor /var/lib/frappe-monitor`. |
| `bind: address already in use` | Something else is on `:8080` | `ss -tlnp \| grep 8080` and kill it, or change `server.listen_addr`. |

## "Disk filling up fast"

Most likely VM retention. Check usage:

```bash
sudo du -sh /var/lib/docker/volumes/frappe-monitor-vm-data
sudo du -sh /var/lib/docker/volumes/frappe-monitor-loki-data
```

Lower retention by editing `/opt/frappe-monitor/deploy/docker-compose.prod.yml`:

```yaml
- "--retentionPeriod=30d"   # was 90d
```

Then `sudo systemctl restart frappe-monitor-stack`. VM compacts older data on the next merge; reclamation isn't instant.

For Loki, the bundled example config has limited retention controls — if log volume is the problem, use a custom Loki config with `retention_enabled: true` and a compactor schedule.

## "High CPU on the monitor host"

Likely cause: `scheduler.max_parallel` too high for your fleet, plus all servers ticking on the same minute. Lower `max_parallel`, or stagger pulls (Phase 7 will add jitter).

Also check the SSH pool isn't leaking connections:

```bash
sudo -u frappe-monitor ss -tnp | wc -l
```

A healthy idle state is ≤ 2 × `max_connections_per_host` × `len(servers)`. Far more than that and the pool isn't releasing — file an issue with the log output.

## Resetting everything

If state is hopelessly tangled and you want a clean slate (this **destroys all metric/log history**):

```bash
sudo systemctl stop frappe-monitor frappe-monitor-stack
sudo rm /var/lib/frappe-monitor/monitor.db
docker volume rm frappe-monitor-vm-data frappe-monitor-loki-data
sudo systemctl start frappe-monitor-stack frappe-monitor
```

Re-register your servers and re-deploy the collector.

## Filing an issue

Include:

- Output of `sudo journalctl -u frappe-monitor -n 100 --no-pager`
- Output of `frappe-monitor --config /etc/frappe-monitor/monitor.yaml --version` (when this flag exists; for now: the binary's `git rev-parse HEAD` from the build host)
- Your `monitor.yaml` (redact paths/secrets if needed — the SSH key path is enough; never paste the key itself)
- What you ran, what you expected, what you got
