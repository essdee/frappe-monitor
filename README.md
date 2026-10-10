# frappe-monitor

Monitoring and alerting for multi-server, multi-bench, multi-site Frappe deployments.

A single Go binary that performs read-only inspection over SSH, verifies optional public DNS/TLS/Frappe availability, pushes time-series to VictoriaMetrics and searchable logs to Loki, retains incident history, and serves a Vue monitoring console. No agent or script is installed on a monitored server, and the product exposes no deploy, update, pull, or restart operation for Frappe targets.

## Documentation

The complete operator guide lives in **[`docs/guide/`](docs/guide/)**:

| | |
|---|---|
| Try it on a Mac/Linux laptop in 5 min | [`docs/guide/local-test.md`](docs/guide/local-test.md) |
| Deploy on a fresh server | [`docs/guide/deployment.md`](docs/guide/deployment.md) |
| Use the dashboard, add servers | [`docs/guide/usage.md`](docs/guide/usage.md) |
| Day-2 ops (start/stop/upgrade/logs) | [`docs/guide/operations.md`](docs/guide/operations.md) |
| Config reference | [`docs/guide/configuration.md`](docs/guide/configuration.md) |
| HTTP API reference | [`docs/guide/api.md`](docs/guide/api.md) |
| What runs where, why | [`docs/guide/architecture.md`](docs/guide/architecture.md) |
| 2026-08-13 read-only redesign | [`docs/2026-08-13/3.md`](docs/2026-08-13/3.md) |
| Things broken? | [`docs/guide/troubleshooting.md`](docs/guide/troubleshooting.md) |
| Contributing & maintenance | [`docs/guide/contributing.md`](docs/guide/contributing.md) |

Design decisions (frozen-in-time) live in `docs/YYYY-MM-DD/N.md`. The Phase 1 master plan is in [`docs/2026-04-22/1.md`](docs/2026-04-22/1.md).

## Quick install (production, one shot)

```bash
git clone <this repo> /tmp/frappe-monitor-src
cd /tmp/frappe-monitor-src
sudo ./deploy/install.sh
```

That builds the binary, creates the `frappe-monitor` system user, lays out `/etc/frappe-monitor/`, `/var/lib/frappe-monitor/`, `/opt/frappe-monitor/`, installs two systemd units (`frappe-monitor.service` for the binary, `frappe-monitor-stack.service` for VictoriaMetrics + Loki via docker compose), and starts everything. Idempotent — re-running upgrades in place. See [`docs/guide/deployment.md`](docs/guide/deployment.md) for the full reference.

```bash
sudo systemctl status frappe-monitor              # check it's up
sudo journalctl -u frappe-monitor -f              # tail logs
# then open http://<host>:8080
```

Add your first bench server: [`docs/guide/usage.md`](docs/guide/usage.md).
After sign-in, server onboarding and the full local monitor configuration are
available in **Servers → Add server** and **Settings**; YAML remains the
bootstrap/recovery interface.

## Quick start (laptop demo, one command)

```bash
make local-test
```

Builds the binary, brings up VM + Loki, seeds demo metrics + log lines, and serves the dashboard at `http://localhost:8080`. Ctrl+C tears everything down. No sudo, no systemd, no real bench server required. Full walkthrough in [`docs/guide/local-test.md`](docs/guide/local-test.md).

## Quick start (manual local dev)

```bash
make vm-up               # docker compose: VM + Loki on 127.0.0.1
make build               # builds the SPA + Go binary with embedded assets
make run                 # starts the embedded SPA/API via go run on :8080
```

Requires Go ≥ 1.26.5 and Node ≥ 20.19. Docker ≥ 24 with the compose plugin is required only for the bundled VM/Loki stack.

## Project layout

```
cmd/monitor/main.go        # binary entry point
internal/
  api/                     # chi router, middleware, handlers
  collector/               # ssh→parse→push pipeline
  config/                  # koanf-backed loader
  metrics/                 # VictoriaMetrics push client + line proto
  logs/                    # Loki push client
  parser/                  # collector-output tokenizer
  probes/                  # bounded monitor-side public site checks
  scheduler/               # robfig/cron + semaphore + per-job timeout
  alerts/                  # VM rule evaluator + durable Telegram state
  ssh/                     # strict-host-key connection/session pool
  storage/                 # ent-backed sqlite
  streamer/                # resumable live logs over long-lived SSH
  web/                     # //go:embed dist
ent/                       # ent schema + generated client (committed)
web/                       # Vue 3 + Vite SPA source
scripts/
  frappe-monitor-collect.sh  # metrics collector (embedded into binary)
  frappe-monitor-stream.sh   # resumable live-log streamer
  embed.go                   # //go:embed wiring + version helper
  smoke.sh                   # end-to-end smoke
deploy/
  install.sh                 # production installer (idempotent)
  systemd/                   # systemd unit files
  docker-compose.dev.yml     # VM + Loki for dev (make vm-up)
  docker-compose.prod.yml    # VM + Loki for prod (used by the stack unit)
  Caddyfile.example          # optional TLS reverse proxy
docs/
  guide/                     # evergreen operator docs
  YYYY-MM-DD/                # dated decisions
  hardening-backlog.md       # non-blocking quality items
```

## Tests + smoke

```bash
make test                  # go test ./... -race -count=1
./scripts/smoke.sh         # end-to-end: build + VM + Loki + every route
```

`scripts/smoke.sh` is the canonical "did I break anything" check. Last line on success: `==> ALL CHECKS PASSED`.

## Project status

Multi-server metrics, split internal/public site health, native per-site request/job and route performance, site-correlated slow-query telemetry, bench service/process health, configured and live worker capacity, exact RQ occupancy, resumable auto-discovered logs, global/trace log search, durable incidents, Telegram fire/recovery alerts, UI-based server onboarding and control-plane settings, and the redesigned responsive Vue console are implemented. Target access is read-only and agentless. Multi-tenant authorization remains outside the current scope.

See [`docs/guide/README.md`](docs/guide/README.md) for the current capability table.
