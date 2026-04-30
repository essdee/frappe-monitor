# Local laptop test

How to run frappe-monitor on a Mac or Linux laptop in 5 minutes — with seeded demo data so every dashboard page has something to look at, and no real Frappe bench server required.

This is **not** a production install. For production see [`deployment.md`](deployment.md). This is the path for "I want to try it on my machine" or "I want to demo it to a teammate."

## Prerequisites

| | macOS | Linux (Ubuntu/Debian/Fedora) |
|---|---|---|
| **Go ≥ 1.25** | `brew install go` | distro Go is often too old; use [go.dev/doc/install](https://go.dev/doc/install) |
| **Node ≥ 20** | `brew install node` | `apt install nodejs npm` (verify `node --version` is ≥ 20; nodesource if not) |
| **Docker + compose** | Docker Desktop, [OrbStack](https://orbstack.dev), or [colima](https://github.com/abiosoft/colima) | `apt install docker.io docker-compose-plugin` and add yourself to the `docker` group |
| **Python 3** | preinstalled | preinstalled on most distros |
| **curl** | preinstalled | `apt install curl` |
| **git, make, bash** | preinstalled | preinstalled |

Docker must be **running** before you start — `docker info` should print without error.

> Windows: use WSL2 with Ubuntu and follow the Linux instructions inside the WSL shell.

## One-shot demo (recommended)

```bash
git clone <this repo> ~/frappe-monitor
cd ~/frappe-monitor
make local-test
```

Or directly: `./deploy/local-test.sh`.

That single command:

1. Verifies Go, Node, Docker, compose, Python, curl.
2. Builds the binary (Vite SPA + `go build`; ~30s on first run, seconds after).
3. Brings up VictoriaMetrics + Loki via `deploy/docker-compose.dev.yml`.
4. Seeds 30 samples × 30s of demo metrics for two synthetic servers (one healthy, one stressed) and pushes a few error log lines to Loki.
5. Starts the monitor binary against a hermetic temp config + temp SQLite DB.
6. Registers `demo-1` and `demo-2` in the registry so the Servers page lists them.
7. Prints the dashboard URL.

Open `http://localhost:8080` in your browser. **Press Ctrl+C in the terminal to stop and tear everything down.**

### What you'll see

| Page | Demo content |
|---|---|
| `/servers` | `demo-1` and `demo-2` cards. Click one for CPU / memory / disk / load charts. |
| `/benches` | `erpnext-bench` (on demo-1, healthy 10/10 supervisors), `staging-bench` (on demo-2, supervisor 4/5). |
| `/sites` | `app.example.com` healthy ~45ms, `portal.example.com` healthy ~82ms, `staging.example.com` unhealthy (HTTP 502, ~2.5s). Click the unhealthy one to see the error log feed. |

The timeline filter in the header (`15m / 1h / 6h / 24h`) re-queries every chart. The seeded data covers the last 15 minutes; ranges beyond that show fewer points.

## Useful flags

```bash
PORT=9090 ./deploy/local-test.sh         # different listen port
REBUILD=1 ./deploy/local-test.sh         # force npm + go rebuild
KEEP_BACKENDS=1 ./deploy/local-test.sh   # leave VM+Loki running on Ctrl+C
                                         # (faster re-runs)
```

`KEEP_BACKENDS=1` is the right flag for an iterative dev loop — you keep VM+Loki running between script restarts so they don't churn through the slow startup path each time.

## Manual setup (if you want to control each step)

```bash
# 1. Build.
make build

# 2. Backends.
make vm-up     # docker compose up -d for VM + Loki
# Verify:
curl http://127.0.0.1:8428/health  # VM
curl http://127.0.0.1:3100/ready   # Loki

# 3. Run the binary.
make run       # uses ./config/monitor.yaml; defaults are fine for local dev
# In another terminal:
curl http://127.0.0.1:8080/healthz # → {"status":"ok"}
```

Open `http://localhost:8080`. The dashboard is empty until you register a server (next section).

To stop: Ctrl+C the binary, then `make vm-down`.

## Pointing at a real Frappe bench server

If you have an SSH-reachable Frappe bench host (your own server, a VM, a colleague's box) the demo gets much richer.

```bash
# 1. Generate a key on your laptop dedicated to the monitor.
ssh-keygen -t ed25519 -N "" -f ~/.ssh/frappe-monitor -C frappe-monitor
cat ~/.ssh/frappe-monitor.pub

# 2. On the bench server, append that pubkey to the bench user's authorized_keys.
#    (Ssh in once manually first to confirm it works.)

# 3. Verify from your laptop:
ssh -i ~/.ssh/frappe-monitor frappe@<your-bench-host> 'echo OK'

# 4. With make local-test or make run already running, register the server:
curl -X POST http://127.0.0.1:8080/api/v1/servers \
  -H 'Content-Type: application/json' \
  -d '{
    "name":         "real-bench",
    "hostname":     "your-bench-host",
    "ssh_user":     "frappe",
    "ssh_port":     22,
    "ssh_key_path": "/Users/you/.ssh/frappe-monitor"
  }'

# 5. Test the connection (should return reachable: true):
curl -X POST http://127.0.0.1:8080/api/v1/servers/<id>/test-connection

# 6. Push the bench-side collector script to the host:
curl -X POST http://127.0.0.1:8080/api/v1/servers/<id>/deploy-collector
```

Wait one scheduler tick (default 15min) for the first real metrics to land. To shorten the wait, edit `./config/monitor.yaml` and set `scheduler.default_interval_seconds: 60`, then `make run` again.

In the dashboard, the new server's card will switch from "unknown" to "reachable" once the first cycle succeeds, and CPU / memory / disk / load charts will start populating.

## Telegram alerts on a laptop

Edit `./config/monitor.yaml`:

```yaml
alerts:
  enabled: true
  telegram:
    bot_token: "<from @BotFather>"
    chat_ids: ["<your chat id from getUpdates>"]
```

Re-run `make run`. The default rules (`server_unreachable`, `disk_almost_full`, `site_unhealthy`, `redis_queue_high`) start evaluating against whatever metrics are in VM. The seeded `staging.example.com` site has `is_healthy=0`, so within ~60s of starting you should receive a Telegram message.

See [`usage.md`](usage.md#setting-up-telegram-alerting) for the full bot-creation walkthrough.

## Tear down completely

```bash
# Stop the binary: Ctrl+C in the terminal, or:
pkill monitor-server

# Stop VM + Loki:
make vm-down

# Remove all collected metrics + logs:
docker volume rm frappe-monitor-vm-data frappe-monitor-loki-data

# Remove the cloned repo (optional):
rm -rf ~/frappe-monitor
```

That's it — nothing was installed at the OS level, no system user was created, no systemd unit. Nothing to clean up beyond the above.

## Troubleshooting (laptop-specific)

**"docker daemon not running"** — start Docker Desktop / OrbStack / colima before running the script.

**"port 8080 already in use"** — something else on your laptop is using it. Run with `PORT=9090 make local-test`.

**`make build` fails on `npm install`** — your Node is too old (need ≥ 20). Run `node --version`; on macOS upgrade with `brew upgrade node`; on Linux see [nodesource](https://github.com/nodesource/distributions).

**Build takes forever the first time** — `npm install` downloads ~50 MB. Subsequent builds are seconds because `node_modules/` is cached.

**Charts show "no data in this window"** — the seeded data window is the last 15 minutes. If your timeline filter is set to a longer range (24h, 7d), that's expected; switch back to 15m or 1h.

**macOS firewall prompts about Go binary** — click Allow. Go binaries need a one-time approval to listen on a port.

**"alerts: rule evaluation failed: vm unreachable"** — the binary started before VM was ready. Wait 30s or `make vm-down && make vm-up && make run`.

For non-laptop-specific issues, see [`troubleshooting.md`](troubleshooting.md).
