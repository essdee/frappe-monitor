# Deployment

How to get `frappe-monitor` running on a real server. The recommended path is `deploy/install.sh` — it's idempotent, builds the binary from your checkout, creates the system user, lays out FHS-compliant directories, installs two systemd units, and brings up the VictoriaMetrics + Loki stack.

## Recommended topology (v1)

All-in-one: one Linux host runs the monitor binary, VictoriaMetrics, and Loki together. The binary opens outbound SSH connections to your Frappe bench servers; everything else stays on `127.0.0.1`. Caddy on the same host terminates TLS if you expose the dashboard.

```
                  ┌───────────────────────────────────────────┐
                  │  monitor host                             │
                  │                                           │
   browser ──TLS─▶│  Caddy :443 ──▶ frappe-monitor :8080      │
                  │                       │                   │
                  │                       │ pushes/queries    │
                  │                       ▼                   │
                  │              VictoriaMetrics :8428        │
                  │                    Loki :3100             │
                  └───────────────┬───────────────────────────┘
                                  │ outbound SSH (22)
                                  ▼
                       ┌─────────────────────┐
                       │ frappe bench server │
                       │ frappe bench server │
                       │ frappe bench server │
                       └─────────────────────┘
```

For >50 monitored servers or shared metric ingestion, see [Split-tier deploy](#split-tier-deploy) at the bottom.

## System requirements

| | Minimum | Recommended (≤25 bench servers) |
|---|---|---|
| OS | Linux x86_64 with systemd | Ubuntu 22.04+ / Debian 12+ / RHEL 9+ |
| CPU | 1 vCPU | 2 vCPU |
| RAM | 1 GB | 2 GB |
| Disk | 10 GB | 50 GB SSD (90-day VM retention + Loki indices) |
| Outbound | 22/tcp to each bench server | same + 80/443 if running Caddy |

## Prerequisites on the host

The install script verifies all of these and bails with copy-pasteable hints if anything is missing.

| Tool | Version | Install |
|---|---|---|
| Go | ≥ 1.26.5 | `https://go.dev/doc/install` |
| Node | ≥ 20.19 | distro package or nodesource |
| npm | ships with Node | — |
| Docker Engine | ≥ 24 | apt/upstream Docker or Canonical's Snap package |
| Docker compose plugin | ≥ 2.23.1 | `apt install docker-compose-plugin` (or distro equivalent); required for the embedded Loki config |
| systemd | any modern version | already on every supported distro |
| OpenSSH client tools | any modern version | provides `ssh-keygen`, `ssh-keyscan`, and `ssh-keygen -l` |

## One-shot install

```bash
# 1. Clone the repo onto the target host.
git clone https://github.com/sakthikumarp/frappe_monitor.git /tmp/frappe-monitor-src
cd /tmp/frappe-monitor-src

# 2. Run the installer as root.
sudo ./deploy/install.sh

# Or via the Makefile shortcut:
sudo make install
```

That's it. The script:

1. Verifies Go, Node, Docker Compose, and that the Docker daemon is reachable.
2. **Prompts for credentials** on first install (skipped if `monitor.yaml` already exists):
   - Dashboard password (Enter to auto-generate a random one — printed for you to save).
   - Whether to enable Telegram alerts; if yes, prompts for bot token + chat IDs.
3. Runs `make build` (npm install + Vite build + Go build with `-tags=embed_dist`).
4. Creates the `frappe-monitor` system user (no login shell; state home at `/var/lib/frappe-monitor`).
5. Lays out `/etc/frappe-monitor/`, `/var/lib/frappe-monitor/`, `/opt/frappe-monitor/`.
6. Generates a dedicated Ed25519 identity at `/var/lib/frappe-monitor/.ssh/id_ed25519` if it does not already exist. Reinstalls and upgrades preserve it.
7. Writes `/etc/frappe-monitor/monitor.yaml` with the values you supplied (mode 0640, owner `root:frappe-monitor`). Telegram tokens and chat IDs are validated while prompting.
8. Runs the candidate binary's configuration preflight. An invalid config cannot replace the installed binary or start a systemd restart loop.
9. Atomically installs the binary at `/usr/local/bin/frappe-monitor`.
10. Installs two systemd units: `frappe-monitor.service` (the binary) and `frappe-monitor-stack.service` (VM + Loki via docker compose).
11. Enables and starts both, then verifies `/healthz` at the address derived from `server.listen_addr` in YAML.
12. If an upgraded service fails startup or health checks, atomically restores the previous binary, Compose file, units, and their enabled/running states.

After it finishes, the dashboard is at `http://<host>:8080` — log in with any username and the password you set. Move the cloned repo (`/tmp/frappe-monitor-src`) wherever you keep source — the installer copied everything it needs into `/opt/frappe-monitor/deploy/`.

### Skipping the prompts (CI / unattended install)

Pass values via env or args; the script only prompts for what's missing.

```bash
sudo MONITOR_PASSWORD=hunter2 ./deploy/install.sh
# or
sudo ./deploy/install.sh --password=hunter2 \
    --enable-alerts --bot-token=<TOKEN> --chat-ids=12345,67890
```

If `--password` and `MONITOR_PASSWORD` are both empty and stdin isn't a TTY, the script auto-generates a 32-char password and prints it before continuing.

## Layout produced

| Path | Owner | Purpose |
|---|---|---|
| `/usr/local/bin/frappe-monitor` | root:root 0755 | The binary (CGO=0, embedded SPA). |
| `/etc/frappe-monitor/monitor.yaml` | root:frappe-monitor 0640 | Config. Edit this and `systemctl restart frappe-monitor`. |
| `/var/lib/frappe-monitor/monitor.db` | frappe-monitor:frappe-monitor 0640 | SQLite: registry, snapshots, stream cursors, and alert state. |
| `/var/lib/frappe-monitor/install-rollback/` | root:root 0700 | Last known-good binary and service files used by automatic upgrade rollback. |
| `/var/lib/frappe-monitor/.ssh/id_ed25519` | frappe-monitor:frappe-monitor 0600 | Dedicated private identity generated once and preserved on upgrades. |
| `/var/lib/frappe-monitor/.ssh/known_hosts` | frappe-monitor:frappe-monitor 0600 | Strict host-key trust store. |
| `/opt/frappe-monitor/deploy/docker-compose.prod.yml` | root:root 0644 | VM + Loki compose, including Loki's local TSDB/filesystem configuration and 30-day compactor retention. |
| `/etc/systemd/system/frappe-monitor.service` | root:root 0644 | The binary's unit. |
| `/etc/systemd/system/frappe-monitor-stack.service` | root:root 0644 | VM + Loki's unit. |

VM and Loki data live in named Docker volumes: `frappe-monitor-vm-data` and `frappe-monitor-loki-data`. They survive `docker compose down`/`up`.

## Configure SSH access to your bench servers

The monitor binary runs as the `frappe-monitor` user and opens SSH connections to every server you register. The installer generates its dedicated key automatically. Print the public half and authorize it on each bench server:

```bash
sudo cat /var/lib/frappe-monitor/.ssh/id_ed25519.pub
```

On each Frappe bench server, add that public key to the authorized_keys of a user that can read the bench files (e.g. the existing `frappe` user, or a dedicated read-only `monitor` user added to the `frappe` group):

```bash
# On each bench server:
sudo -u frappe mkdir -p /home/frappe/.ssh
sudo -u frappe tee -a /home/frappe/.ssh/authorized_keys <<< "<paste pubkey here>"
sudo -u frappe chmod 600 /home/frappe/.ssh/authorized_keys
```

The key path you'll use when registering each server is `/var/lib/frappe-monitor/.ssh/id_ed25519`. See [`usage.md`](usage.md) for the registration call.

The installer also creates an empty strict host-key store at `/var/lib/frappe-monitor/.ssh/known_hosts`. Before the app connects to a bench, obtain its key and verify the fingerprint through the bench console or another trusted channel:

```bash
ssh-keyscan -p 22 -H bench1.example.com > /tmp/bench1.known-host
ssh-keygen -lf /tmp/bench1.known-host
# Only after the fingerprint matches the bench:
sudo tee -a /var/lib/frappe-monitor/.ssh/known_hosts < /tmp/bench1.known-host >/dev/null
sudo chown frappe-monitor:frappe-monitor /var/lib/frappe-monitor/.ssh/known_hosts
sudo chmod 600 /var/lib/frappe-monitor/.ssh/known_hosts
rm /tmp/bench1.known-host
```

Unknown and changed keys are rejected. `ssh-keyscan` alone is discovery, not identity verification.

## Changing the password later

The installer collects the password on first install and writes it to `/etc/frappe-monitor/monitor.yaml`. To change it later:

```bash
sudoedit /etc/frappe-monitor/monitor.yaml
# Find auth.password and change the value, save, exit.
sudo systemctl restart frappe-monitor
```

Changing the password invalidates every existing signed session. The dashboard presents its own login view; sessions last seven days. HTTP basic auth remains available for curl/CI (username ignored). `/healthz` stays open for external probes.

## Optional: TLS via Caddy

The monitor itself speaks plain HTTP on `:8080`. For internet-facing deployments, put Caddy in front:

```bash
# Install Caddy (Ubuntu/Debian).
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
    | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
    | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update && sudo apt install caddy

# Drop in our example.
sudo cp /opt/frappe-monitor/deploy/Caddyfile.example /etc/caddy/Caddyfile
# Edit /etc/caddy/Caddyfile — replace monitor.example.com with your real DNS name.
sudo systemctl reload caddy
```

Caddy provisions Let's Encrypt automatically. Open ports 80 + 443 on the host firewall and point your DNS at it.

Set `auth.password` before exposing the service. A VPN or source-IP restriction in Caddy is still recommended as defense in depth. Keep `server.trusted_proxy_cidrs` limited to the actual proxy address/subnet so clients cannot spoof forwarding headers or the Secure-cookie decision.

## Upgrades

```bash
cd /path/to/frappe_monitor   # your checkout
git pull
sudo ./deploy/install.sh      # idempotent; rebuilds + restarts
```

The script rebuilds the binary, atomically replaces `/usr/local/bin/frappe-monitor`, and restarts the service. Config and data are untouched. The post-start check calls `/healthz` on the port from `server.listen_addr`; there is no runtime port argument. If the candidate stack or monitor fails, the installer restores the last known-good service files and prior systemd state automatically. SQLite schema migrations are forward-only, so a database backup is still recommended before a major-version upgrade.

The existing config is validated by the newly built candidate before the
installed binary or units are replaced. You can run the same read-only check
yourself:

```bash
sudo /usr/local/bin/frappe-monitor \
  --config /etc/frappe-monitor/monitor.yaml --check-config
```

Generate a credential-safe, read-only production report before and after an upgrade:

```bash
sudo bash deploy/diagnose.sh | tee frappe-monitor-diagnostic.txt
```

## Uninstall

```bash
# Soft uninstall — keeps config + data so you can reinstall later
# without re-entering credentials or losing collected metrics:
sudo ./deploy/install.sh --uninstall

# Nuclear — removes config, data, the system user, and the docker
# volumes. Re-running install.sh after this is a clean slate
# (re-prompts for password etc.):
sudo ./deploy/install.sh --purge
```

## Split-tier deploy

If your VM + Loki tier already exists, or you want to run the metrics tier on a separate host:

```bash
sudo ./deploy/install.sh --no-stack
sudo nano /etc/frappe-monitor/monitor.yaml
#   metrics.vm_url: "http://your-vm-host:8428"
#   logs.loki_url:  "http://your-loki-host:3100"
sudo systemctl restart frappe-monitor
```

The `frappe-monitor-stack.service` unit is still installed but disabled. The monitor binary points wherever you tell it to. Verify the connection from the monitor host:

```bash
curl -s http://your-vm-host:8428/health
curl -s http://your-loki-host:3100/ready
```

This is also the smallest-footprint deployment: the monitor remains one
CGO-free Go process with SQLite, while VictoriaMetrics and Loki run on an
existing shared host. The normal all-in-one deployment is still bounded: the
binary usually uses tens of megabytes and both backend containers have explicit
CPU and memory caps. No process or agent is installed on monitored servers.

## What's next

- Add your first bench server: [`usage.md`](usage.md).
- Day-2 ops (start, stop, logs, upgrades, backups): [`operations.md`](operations.md).
- All config keys: [`configuration.md`](configuration.md).
