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
| Go | ≥ 1.25 | `https://go.dev/doc/install` |
| Node | ≥ 20 | distro package or nodesource |
| npm | ships with Node | — |
| Docker Engine | ≥ 24 | `https://docs.docker.com/engine/install/` |
| Docker compose plugin | ≥ 2.20 | `apt install docker-compose-plugin` (or distro equivalent) |
| systemd | any modern version | already on every supported distro |

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

1. Verifies Go, Node, Docker.
2. Runs `make build` (npm install + Vite build + Go build with `-tags=embed_dist`).
3. Creates the `frappe-monitor` system user (no shell, no home).
4. Lays out `/etc/frappe-monitor/`, `/var/lib/frappe-monitor/`, `/opt/frappe-monitor/`.
5. Atomically installs the binary at `/usr/local/bin/frappe-monitor`.
6. Copies `monitor.yaml.example` to `/etc/frappe-monitor/monitor.yaml` (only on first install).
7. Installs two systemd units: `frappe-monitor.service` (the binary) and `frappe-monitor-stack.service` (VM + Loki via docker compose).
8. Enables and starts both.
9. Prints status + dashboard URL.

After it finishes, the dashboard is at `http://<host>:8080`. Move the cloned repo (`/tmp/frappe-monitor-src`) wherever you keep source — the installer copied everything it needs into `/opt/frappe-monitor/deploy/`.

## Layout produced

| Path | Owner | Purpose |
|---|---|---|
| `/usr/local/bin/frappe-monitor` | root:root 0755 | The binary (CGO=0, embedded SPA). |
| `/etc/frappe-monitor/monitor.yaml` | root:frappe-monitor 0640 | Config. Edit this and `systemctl restart frappe-monitor`. |
| `/var/lib/frappe-monitor/monitor.db` | frappe-monitor:frappe-monitor 0640 | SQLite: server registry + per-file log cursors. |
| `/opt/frappe-monitor/deploy/docker-compose.prod.yml` | root:root 0644 | VM + Loki compose, used by the stack unit. |
| `/etc/systemd/system/frappe-monitor.service` | root:root 0644 | The binary's unit. |
| `/etc/systemd/system/frappe-monitor-stack.service` | root:root 0644 | VM + Loki's unit. |

VM and Loki data live in named Docker volumes: `frappe-monitor-vm-data` and `frappe-monitor-loki-data`. They survive `docker compose down`/`up`.

## Configure SSH access to your bench servers

The monitor binary runs as the `frappe-monitor` user and opens SSH connections to every server you register. You need a key on the monitor host that's authorized on each bench server.

```bash
# 1. Generate a key as the frappe-monitor user.
sudo -u frappe-monitor ssh-keygen -t ed25519 -N "" \
    -f /var/lib/frappe-monitor/.ssh/id_ed25519 -C frappe-monitor

# 2. Copy the public key.
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

> **Auth caveat:** until Phase 7 ships built-in auth, the dashboard has no login. Restrict access by IP in the Caddyfile (the example shows the pattern) or only expose it on a private network / VPN.

## Upgrades

```bash
cd /path/to/frappe_monitor   # your checkout
git pull
sudo ./deploy/install.sh      # idempotent; rebuilds + restarts
```

The script rebuilds the binary, atomically replaces `/usr/local/bin/frappe-monitor`, and `systemctl restart`s the service. Config and data are untouched. Downtime is one restart cycle (≤ 15s for clean shutdown + cold start).

## Uninstall

```bash
sudo ./deploy/install.sh --uninstall
```

Stops both services, disables them, removes the binary and systemd units. **Config and data are preserved** — you delete those manually if you really mean it:

```bash
sudo rm -rf /etc/frappe-monitor /var/lib/frappe-monitor
sudo userdel frappe-monitor
docker volume rm frappe-monitor-vm-data frappe-monitor-loki-data
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

## What's next

- Add your first bench server: [`usage.md`](usage.md).
- Day-2 ops (start, stop, logs, upgrades, backups): [`operations.md`](operations.md).
- All config keys: [`configuration.md`](configuration.md).
