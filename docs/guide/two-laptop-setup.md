# Two-laptop production test

How to run a real production-style monitor on one laptop and have it monitor a Frappe bench on a second laptop. Same systemd unit, same install script, same dashboard — just at small scale, on local networking.

## Topology

```
   ┌────────────────────────────┐                 ┌────────────────────────────┐
   │  MONITOR LAPTOP            │                 │  BENCH LAPTOP              │
   │  (Linux + systemd)         │                 │  (any OS with sshd)        │
   │                            │   SSH every     │                            │
   │  frappe-monitor            │ ─15 min──────▶  │  ~/.frappe-monitor/        │
   │  VictoriaMetrics :8428     │   collect.sh    │     collect.sh             │
   │  Loki            :3100     │                 │                            │
   │  dashboard       :8080  ◀──── you browse ────│  Frappe bench at           │
   │                            │  here from this │  ~/frappe-bench/           │
   │                            │  side too       │                            │
   └────────────────────────────┘                 └────────────────────────────┘
```

## Pick which laptop runs what

| Role | Requirements | Why |
|---|---|---|
| **Monitor laptop** | Linux with systemd (Ubuntu / Debian / Fedora / Arch). Always-on while testing. | `deploy/install.sh` ships systemd units. macOS doesn't have systemd — see the [macOS fallback](#macos-monitor-host-fallback). |
| **Bench laptop** | Any OS, sshd running, an installed Frappe bench under `~/frappe-bench`. | The collector reads bench files via SSH. |

Pick whichever laptop is more reliably online (and ideally Linux) as the monitor. The bench laptop can be macOS, Windows + WSL, or another Linux box.

If neither laptop is Linux, see the [macOS fallback](#macos-monitor-host-fallback) — same flow, no systemd.

## Phase 1 — Network prep (both laptops)

1. **Put both laptops on the same network**: same WiFi, same LAN, or join both to a Tailscale/ZeroTier mesh. The monitor needs to SSH into the bench laptop, and you need to browse the dashboard from any device.

2. **Find each laptop's IP**:

   ```bash
   # Linux
   ip -4 addr | grep -oP 'inet \K[0-9.]+' | grep -v '^127'
   # macOS
   ipconfig getifaddr en0
   ```

   Note them down: `MONITOR_IP` and `BENCH_IP`. Examples below use `192.168.1.10` (monitor) and `192.168.1.20` (bench).

3. **Ping each from each**:

   ```bash
   # On the monitor laptop:
   ping -c 2 <BENCH_IP>
   # On the bench laptop:
   ping -c 2 <MONITOR_IP>
   ```

   Both must succeed. If one direction fails, fix the network/firewall first — nothing past this point will work.

## Phase 2 — Install on the monitor laptop (Linux)

```bash
git clone <repo url> /tmp/frappe-monitor-src
cd /tmp/frappe-monitor-src
sudo ./deploy/install.sh
```

The installer verifies Go ≥ 1.25, Node ≥ 20, Docker + compose; builds the binary; creates the `frappe-monitor` system user; lays out `/etc/frappe-monitor/`, `/var/lib/frappe-monitor/`, `/opt/frappe-monitor/`; installs two systemd units; brings up VM + Loki; starts the service.

Verify:

```bash
sudo systemctl status frappe-monitor frappe-monitor-stack
curl http://127.0.0.1:8080/healthz   # → {"status":"ok"}
```

### Set a password before exposing

```bash
# Generate one:
openssl rand -base64 24

# Edit /etc/frappe-monitor/monitor.yaml:
#   auth:
#     password: "<the random string>"
sudo nano /etc/frappe-monitor/monitor.yaml
sudo systemctl restart frappe-monitor
```

Without this, anyone on your LAN can read the dashboard.

### Open the firewall for the dashboard

```bash
# Ubuntu/Debian (ufw):
sudo ufw allow 8080/tcp comment 'frappe-monitor dashboard'

# Fedora/RHEL (firewalld):
sudo firewall-cmd --permanent --add-port=8080/tcp
sudo firewall-cmd --reload
```

(Keep VM:8428 and Loki:3100 closed — they're internal to the monitor host.)

### Browse from the bench laptop

Open `http://<MONITOR_IP>:8080` on the other laptop. Browser prompts for credentials — username doesn't matter, password is what you set. You should land on `/servers` with an empty list.

## Phase 3 — Configure SSH from monitor → bench

### On the monitor laptop

Generate a key as the `frappe-monitor` user (the systemd unit runs as that user):

```bash
sudo -u frappe-monitor mkdir -p /var/lib/frappe-monitor/.ssh
sudo -u frappe-monitor ssh-keygen -t ed25519 -N "" \
    -f /var/lib/frappe-monitor/.ssh/id_ed25519 \
    -C "frappe-monitor@$(hostname)"
sudo cat /var/lib/frappe-monitor/.ssh/id_ed25519.pub
```

Copy the printed public key.

### On the bench laptop

Pick the user that owns the Frappe bench (typically `frappe`, sometimes the same user you log in as). On the bench laptop:

```bash
# Replace 'frappe' with your bench user.
sudo -u frappe mkdir -p /home/frappe/.ssh
sudo -u frappe tee -a /home/frappe/.ssh/authorized_keys <<< "<paste pubkey here>"
sudo -u frappe chmod 700 /home/frappe/.ssh
sudo -u frappe chmod 600 /home/frappe/.ssh/authorized_keys
```

If the bench laptop is macOS and sshd isn't running, enable it:

```
System Settings → General → Sharing → Remote Login: ON
```

### Verify SSH from the monitor

```bash
sudo -u frappe-monitor ssh -i /var/lib/frappe-monitor/.ssh/id_ed25519 \
    -o StrictHostKeyChecking=accept-new \
    frappe@<BENCH_IP> 'echo OK; bench --version 2>/dev/null || echo "no bench in PATH"'
```

Expect to see `OK` (and optionally a Frappe bench version). If it fails:

| Error | Fix |
|---|---|
| `Permission denied (publickey)` | The pubkey isn't in the bench user's `authorized_keys`, or perms are wrong (must be 600). |
| `Connection refused` | sshd isn't running on the bench laptop. |
| `Connection timed out` | Firewall, or the bench laptop is on a different network. |
| `Host key verification failed` | Re-run with `-o StrictHostKeyChecking=accept-new` (already in the command above). |

The `accept-new` flag stores the bench's host key on first connect; subsequent connections (the scheduler's) reuse it.

## Phase 4 — Add the bench from the dashboard

In your browser at `http://<MONITOR_IP>:8080/servers`:

1. Click **+ Add server**.
2. Fill the form:
   - **Name**: `bench-laptop` (free-form)
   - **Hostname**: the bench laptop's IP, e.g. `192.168.1.20`
   - **SSH user**: the bench user (`frappe` typically)
   - **Port**: `22`
   - **SSH key path**: `/var/lib/frappe-monitor/.ssh/id_ed25519`
3. Click **Add server**.

Click into the new card. On the detail page:

1. **Test SSH** → should turn green: `SSH reachable (latency Xms)`.
2. **Deploy collector** → should show: `Collector v3 deployed.` (this pipes the bash collector script to `~/.frappe-monitor/collect.sh` on the bench laptop and `chmod +x`'s it).

The status badge will still say `unknown` until the next scheduler tick. **By default that's 15 minutes.** To shorten the wait for testing:

```bash
# Override the scheduler interval temporarily:
sudo systemctl edit frappe-monitor
# In the editor, add:
[Service]
Environment=MONITOR_SCHEDULER__DEFAULT_INTERVAL_SECONDS=60
# Save, exit.
sudo systemctl daemon-reload
sudo systemctl restart frappe-monitor
```

Now a pull cycle fires every 60s. Watch the logs:

```bash
sudo journalctl -u frappe-monitor -f | grep -E "(collector|metrics)"
```

You should see something like:

```
INFO collector: pulled successfully  server_id=1  took=1.2s
INFO collector: metrics pushed       series=42
```

The card on `/servers` flips to **reachable** (green dot). The detail page's CPU / memory / disk / load charts populate after the first cycle, fill in over the next few cycles.

`/benches` shows the discovered bench (whatever's in `~/frappe-bench/sites/` and `~/frappe-bench/apps/`); `/sites` shows each site with its current HTTP status + response time.

## Phase 5 — Telegram alerts (optional)

If you want page-on-failure during the test:

1. DM `@BotFather` on Telegram → `/newbot` → copy the token.
2. DM your new bot, send `/start`, then visit `https://api.telegram.org/bot<TOKEN>/getUpdates` — find `"chat":{"id": 123456789, ...}`.
3. Edit `/etc/frappe-monitor/monitor.yaml`:

   ```yaml
   alerts:
     enabled: true
     telegram:
       bot_token: "<token>"
       chat_ids: ["<chat id>"]
   ```

4. `sudo systemctl restart frappe-monitor`.

Now turn off WiFi on the bench laptop. Within ~5 min you'll get a Telegram message that `bench-laptop` is unreachable. Reconnect and you get a recovery message.

The default rules also catch disk > 90%, site unhealthy, redis queue > 1000.

## Verifying it works (checklist)

- [ ] `sudo systemctl status frappe-monitor` is `active (running)` on the monitor laptop.
- [ ] `curl http://127.0.0.1:8080/healthz` returns `{"status":"ok"}` from the monitor.
- [ ] Browser at `http://<MONITOR_IP>:8080` from the other laptop prompts for password and lands on the dashboard.
- [ ] Test SSH on the bench laptop's server card shows reachable=true.
- [ ] Deploy collector returns version v3.
- [ ] After one scheduler tick, the card flips to "reachable" and CPU/memory charts populate.
- [ ] `/benches` lists the bench, with the right Frappe version + apps count.
- [ ] `/sites` lists each site under that bench.
- [ ] (Optional) Killing WiFi on the bench laptop produces a Telegram alert within ~5 min.

If any step fails, see the [troubleshooting section](#troubleshooting).

## Troubleshooting

**Browser can't reach `http://<MONITOR_IP>:8080`** — firewall on the monitor host. Check `sudo ufw status` (Ubuntu) or `sudo firewall-cmd --list-ports` (Fedora). Re-run the firewall step in Phase 2.

**`Test SSH` says `auth`** — pubkey not in `authorized_keys` on the bench, or wrong perms. The bench's `~/.ssh/` must be 700 and `authorized_keys` must be 600. Verify: `sudo -u frappe-monitor ssh -i /var/lib/frappe-monitor/.ssh/id_ed25519 frappe@<BENCH_IP> 'echo OK'` from the monitor laptop directly.

**`Test SSH` says `dial`** — bench laptop's IP changed (DHCP), sshd died, or firewall blocking 22. `nmap -p 22 <BENCH_IP>` from the monitor host should report `open`.

**`Deploy collector` succeeds but no metrics show up** — the collector ran but parsing or VM push failed. Check `sudo journalctl -u frappe-monitor -f`. Common cause: the bench's `~/frappe-bench/` directory has a different name (the collector expects `frappe-bench`). Symlink: `ln -s ~/your-actual-bench ~/frappe-bench`.

**Charts say "no data in this window"** — first cycle hasn't run yet. Default interval is 15 min; switch the timeline filter to `1h` or `6h`, or shorten the interval as shown above.

**`alerts: notify failed`** — bad bot token or chat ID. Test directly: `curl -X POST "https://api.telegram.org/bot<TOKEN>/sendMessage" -d "chat_id=<CHATID>" -d "text=test"`.

**WiFi on different SSIDs / one on cellular** — laptops can't reach each other. Either join both to the same WiFi, use a phone hotspot, or join a Tailscale/ZeroTier mesh.

**Browser keeps prompting for credentials** — the password in `monitor.yaml` doesn't match what you're typing. Username is ignored — only password matters. Restart after editing.

## macOS-monitor-host fallback

If neither laptop runs Linux, you can still test, just without systemd. Use the dev-mode flow on the monitor laptop:

```bash
# On the (macOS or Linux) monitor laptop:
git clone <repo url> ~/frappe-monitor
cd ~/frappe-monitor
make vm-up                                          # VM + Loki
make build                                          # binary
# Edit ./config/monitor.yaml — set auth.password.
./bin/monitor-server --config ./config/monitor.yaml &
```

Everything else (Phase 3 + Phase 4) is the same. The trade-off vs systemd: no auto-restart on failure, no `journalctl`, you're responsible for keeping the binary running. Fine for a test session; not for a real production deploy.

To stop: `pkill monitor-server && make vm-down`.

## Tear down (when done)

```bash
# On the monitor laptop:
sudo ./deploy/install.sh --uninstall
sudo rm -rf /etc/frappe-monitor /var/lib/frappe-monitor
sudo userdel frappe-monitor
docker volume rm frappe-monitor-vm-data frappe-monitor-loki-data

# On the bench laptop:
rm -rf ~/.frappe-monitor                                    # collector script
sudo -u frappe sed -i '/frappe-monitor@/d' /home/frappe/.ssh/authorized_keys
```

That removes everything frappe-monitor put on either laptop.
