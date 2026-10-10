# Two-laptop production test

How to run a real production-style monitor on one laptop and have it monitor a Frappe bench on a second laptop. Same systemd unit, same install script, same dashboard — just at small scale, on local networking.

## Topology

```
   ┌────────────────────────────┐                 ┌────────────────────────────┐
   │  MONITOR LAPTOP            │                 │  BENCH LAPTOP              │
   │  (Linux + systemd)         │                 │  (any OS with sshd)        │
   │                            │   SSH every     │                            │
   │  frappe-monitor            │ ──60s default─▶ │  ~/.frappe-monitor/        │
   │  VictoriaMetrics :8428     │   collector     │     frappe-monitor-*.sh    │
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

The installer verifies Go ≥ 1.26.5, Node ≥ 20.19, Docker + compose; builds the binary; creates the `frappe-monitor` system user; lays out `/etc/frappe-monitor/`, `/var/lib/frappe-monitor/`, `/opt/frappe-monitor/`; installs two systemd units; brings up VM + Loki; starts the service.

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

Open `http://<MONITOR_IP>:8080` on the other laptop. The app shows its login page; enter the password you set. You should land on `/servers` with an empty list.

## Phase 3 — Configure SSH from monitor → bench

### On the monitor laptop

The installer already generated a dedicated key for the `frappe-monitor`
service user. Print its public half:

```bash
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

Discover the key, verify its fingerprint against the bench console, then trust it:

```bash
ssh-keyscan -p 22 -H <BENCH_IP> > /tmp/bench.known-host
ssh-keygen -lf /tmp/bench.known-host
# Continue only after the fingerprint matches:
sudo tee -a /var/lib/frappe-monitor/.ssh/known_hosts < /tmp/bench.known-host >/dev/null
sudo chown frappe-monitor:frappe-monitor /var/lib/frappe-monitor/.ssh/known_hosts
sudo chmod 600 /var/lib/frappe-monitor/.ssh/known_hosts
rm /tmp/bench.known-host
```

```bash
sudo -u frappe-monitor ssh -i /var/lib/frappe-monitor/.ssh/id_ed25519 \
    -o UserKnownHostsFile=/var/lib/frappe-monitor/.ssh/known_hosts \
    -o StrictHostKeyChecking=yes \
    frappe@<BENCH_IP> 'echo OK; bench --version 2>/dev/null || echo "no bench in PATH"'
```

Expect to see `OK` (and optionally a Frappe bench version). If it fails:

| Error | Fix |
|---|---|
| `Permission denied (publickey)` | The pubkey isn't in the bench user's `authorized_keys`, or perms are wrong (must be 600). |
| `Connection refused` | sshd isn't running on the bench laptop. |
| `Connection timed out` | Firewall, or the bench laptop is on a different network. |
| `Host key verification failed` | Verify and add the bench key as above; investigate an unexpected changed key. |

The application uses this same strict `known_hosts` file and never accepts a first-seen key automatically.

## Phase 4 — Register the bench target

Registration changes only the monitor's local database. The dashboard is
monitoring-only, so setup automation uses the API:

```bash
curl -u 'admin:<MONITOR_PASSWORD>' -X POST \
  http://127.0.0.1:8080/api/v1/servers \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "bench-laptop",
    "hostname": "<BENCH_IP>",
    "ssh_user": "frappe",
    "ssh_port": 22,
    "ssh_key_path": "/var/lib/frappe-monitor/.ssh/id_ed25519"
  }'
```

Collection begins immediately and repeats every 60 seconds. Embedded
inspection code runs through SSH stdin; nothing is deployed to the bench
laptop. Watch the monitor logs:

```bash
sudo journalctl -u frappe-monitor -f | grep -E "(collector|metrics)"
```

You should see something like:

```
INFO collector: pull ok server_id=1 body_lines=42 benches_ok=1 sites_ok=4
```

The card on `/servers` flips to **reachable**. The detail page's CPU, memory,
disk, load, network, and process data populate after the first cycle.

`/benches` shows configured and actual worker capacity, services, queues, and
process health. `/sites` shows each site with current HTTP health and latency.

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

Now turn off WiFi on the bench laptop. With 60-second collection and alert intervals, the Telegram fire should arrive within roughly two minutes. Reconnect and a recovery follows after a successful pull/evaluation.

The defaults also detect disk >90%, unhealthy sites, Redis queue >1000,
complete RQ-worker saturation, failed services, and stream disconnection.

## Verifying it works (checklist)

- [ ] `sudo systemctl status frappe-monitor` is `active (running)` on the monitor laptop.
- [ ] `curl http://127.0.0.1:8080/healthz` returns `{"status":"ok"}` from the monitor.
- [ ] Browser at `http://<MONITOR_IP>:8080` shows the login page and lands on the dashboard after sign-in.
- [ ] Direct strict-host-key SSH as the monitor user succeeds.
- [ ] The immediate agentless collection flips the card to reachable and host charts populate.
- [ ] `/benches` lists the bench, with the right Frappe version + apps count.
- [ ] Bench detail lists Supervisor/process and worker-capacity evidence.
- [ ] `/sites` lists each site under that bench.
- [ ] (Optional) Killing WiFi on the bench laptop produces a Telegram alert within ~5 min.

If any step fails, see the [troubleshooting section](#troubleshooting).

## Troubleshooting

**Browser can't reach `http://<MONITOR_IP>:8080`** — firewall on the monitor host. Check `sudo ufw status` (Ubuntu) or `sudo firewall-cmd --list-ports` (Fedora). Re-run the firewall step in Phase 2.

**`Test SSH` says `auth`** — pubkey not in `authorized_keys` on the bench, or wrong perms. The bench's `~/.ssh/` must be 700 and `authorized_keys` must be 600. Verify: `sudo -u frappe-monitor ssh -i /var/lib/frappe-monitor/.ssh/id_ed25519 frappe@<BENCH_IP> 'echo OK'` from the monitor laptop directly.

**`Test SSH` says `dial`** — bench laptop's IP changed (DHCP), sshd died, or firewall blocking 22. `nmap -p 22 <BENCH_IP>` from the monitor host should report `open`.

**SSH works but no metrics show up** — inspect `sudo journalctl -u frappe-monitor -f`. For a non-standard bench directory, set `bench_paths` on the local target registry instead of changing the target filesystem.

**Charts say "no data in this window"** — the immediate pull may still be running or failed. Check the collector log, then use a `1h` range; repeats occur every 60 seconds by default.

**`alerts: notify failed`** — bad bot token or chat ID. Test directly: `curl -X POST "https://api.telegram.org/bot<TOKEN>/sendMessage" -d "chat_id=<CHATID>" -d "text=test"`.

**WiFi on different SSIDs / one on cellular** — laptops can't reach each other. Either join both to the same WiFi, use a phone hotspot, or join a Tailscale/ZeroTier mesh.

**Login keeps failing** — the password in `monitor.yaml` does not match. Restart after editing; changing it invalidates all existing signed sessions. Five failed attempts in one minute temporarily rate-limit that client IP.

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
