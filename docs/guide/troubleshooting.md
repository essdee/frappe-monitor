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

## "test-connection / deploy fails: host key CHANGED"

Host-key handling is **trust-on-first-use** by default: the first time the
monitor connects to a host it pins that host's key to `known_hosts` and accepts
it (no manual `ssh-keyscan` — fresh installs and upgrades onboard hosts
automatically). After that, a host presenting a **different** key is rejected
with a "host key … CHANGED" error — either a man-in-the-middle, or the host was
legitimately rebuilt/re-keyed.

If the change is legitimate, drop the stale pin and let it re-pin:

```bash
# as the service user (e.g. frappe-monitor); path is ssh.known_hosts_path or ~/.ssh/known_hosts
ssh-keygen -R <host> -f /var/lib/frappe-monitor/.ssh/known_hosts
```

For dev only, you can disable host-key checking entirely (incl. the changed-key
detection) with `ssh.insecure_skip_host_key_check: true` (logged at startup;
never use it in production — it makes the connection MITM-able).

Passphrase-protected keys are not supported — give the monitor an unencrypted
key dedicated to it. The error message says so explicitly.

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

## "Server detail says 'unreachable' but the host is fine"

A few things to check, in order:

1. **Did the last collector pull fail?** Server status is set by the
   collector pipeline, not a live ping. Click **Test SSH** on the server
   detail page — if it returns reachable, the badge updates immediately.
2. **Stale "Last capture failed" banner on System Details.** As of the
   2026-05-02 sweep, a transient SSH failure no longer wipes the prior
   payload — you'll see the old inventory + a "Last capture failed:"
   banner until the next successful Refresh. The banner clears the moment
   a Refresh succeeds.
3. **Collector script error in `last_error`.** If the field reads
   `collector script error (exit=N line=M)`, something in
   `frappe-monitor-collect.sh` failed at line M with exit code N — most
   commonly missing tooling (`redis-cli`, `supervisorctl`) on the bench.
   Run the script manually to reproduce:
   ```bash
   ssh frappe@<host> bash ~/.frappe-monitor/frappe-monitor-collect.sh
   ```

## "Site shows 'Healthy: no, HTTP 0'"

The collector skipped this site because the per-cycle wall-clock budget
(`SITE_PROBE_BUDGET_S`, default 20s) was already exhausted by earlier
sites. Each site probe is up to 2s, and SSH's command timeout is 30s —
benches with 30+ sites legitimately exceed the budget. Fixes:

- Run the collector more often: lower `scheduler.tick_interval_seconds`.
- Raise the SSH command timeout: `ssh.command_timeout_seconds: 60` (also bump
  `server.write_timeout_seconds` to be >= it — the monitor refuses to start
  otherwise, so a slow SSH-backed HTTP response can't be severed).
- Reduce the bench's site count (most user-visible value comes from a
  small number of frequently-checked sites).

The "skipped" state is intentional and visible — better than the loop
getting killed mid-run by SSH and leaving torn output for the parser.

## "Click Sign-out, but I'm right back in the dashboard"

The session cookie is HttpOnly + cookie-session, so logout posts to
`/api/v1/logout` and clears it server-side. As of 2026-05-02 the SPA
also short-circuits the auto-redirect on `/login?signed_out=1` so even a
stale-but-valid cached session can't bounce you back. If you still see
the issue:

```bash
curl -i -c /tmp/cookies -X POST http://127.0.0.1:8080/api/v1/logout
# Check the response — should include:
#   Set-Cookie: monitor_session=; Path=/; Max-Age=0; HttpOnly; SameSite=Strict
```

If `Secure` appears here but the dashboard is served over plain HTTP
(or vice versa), the browser refuses to delete the cookie because the
attributes don't match the original. The handler reads
`X-Forwarded-Proto` for that decision; verify your reverse proxy sets
it correctly.

## "Browser keeps prompting for credentials"

`auth.password` is set and the password you're typing doesn't match. Username is ignored — only the password matters. Reset by editing `/etc/frappe-monitor/monitor.yaml` and `sudo systemctl restart frappe-monitor`.

If you want to disable auth temporarily, set `auth.password: ""` and restart.

If the prompt re-appears mid-session, the realm changed — Chrome rebinds credentials per `(origin, realm)`. Set a stable `auth.realm` in config and don't change it.

## "Telegram alerts aren't firing"

In order:

```bash
# 1. Is the service even enabled?
grep -A 8 '^alerts:' /etc/frappe-monitor/monitor.yaml

# 2. Did it start?
sudo journalctl -u frappe-monitor | grep "alerts service started"

# 3. Are evaluations running?
sudo journalctl -u frappe-monitor -f | grep "alerts:"
```

If you see `alerts: rule evaluation failed` lines, the PromQL is rejected by VM. Test it manually:

```bash
curl 'http://127.0.0.1:8428/api/v1/query?query=<URL-encoded-promql>'
```

If you see `alerts: notify failed`, your Telegram bot token or chat ID is wrong:

```bash
# Verify the bot token directly:
curl "https://api.telegram.org/bot<TOKEN>/getMe"
# {"ok":true,"result":{"id":...}}

# Send a test message manually:
curl -X POST "https://api.telegram.org/bot<TOKEN>/sendMessage" \
  -d "chat_id=<CHATID>" -d "text=test"
```

If `getUpdates` returns nothing, send `/start` to your bot first — Telegram suppresses the chat from `getUpdates` until the user has interacted with the bot.

## "Telegram alerts firing constantly"

`notify_repeat_seconds` is too low for your noise level. Default 3600 (1h). Bump to 4h or 1d for noisy production environments. Setting it to `0` makes the monitor notify only once per fire and once per resolution — quietest setting.

If a *single* alert is constantly firing+resolving, the underlying condition is flapping. Check the metric directly in the dashboard or VM. Common cause: `disk_almost_full` at exactly 90% and disk usage oscillating ±0.1%. Raise the threshold in your custom rule.

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

The pool multiplexes one connection per `host|user|key`, so a healthy idle state is roughly `len(servers)` connections. Far more than that and the pool isn't releasing — file an issue with the log output.

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
