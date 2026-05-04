# frappe-monitor

Monitoring and alerting for multi-server, multi-bench, multi-site Frappe deployments.

A single Go binary that pulls metrics over SSH, pushes time-series to VictoriaMetrics + logs to Loki, and serves a Vue dashboard from the same process. Designed to run as a systemd service on a small Linux VM next to (or near) your Frappe fleet.

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

## Quick start (laptop demo, one command)

```bash
make local-test
```

Builds the binary, brings up VM + Loki, seeds demo metrics + log lines, and serves the dashboard at `http://localhost:8080`. Ctrl+C tears everything down. No sudo, no systemd, no real bench server required. Full walkthrough in [`docs/guide/local-test.md`](docs/guide/local-test.md).

## Quick start (manual local dev)

```bash
make vm-up               # docker compose: VM + Loki on 127.0.0.1
make build               # builds the SPA + Go binary with embedded assets
make run                 # runs ./bin/monitor-server on :8080
```

Requires Go ≥ 1.25, Node ≥ 20, Docker ≥ 24 with the compose plugin.

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
  scheduler/               # robfig/cron + semaphore + per-job timeout
  ssh/                     # executor interface, pool, fake, typed errors
  storage/                 # ent-backed sqlite
  web/                     # //go:embed dist
ent/                       # ent schema + generated client (committed)
web/                       # Vue 3 + Vite SPA source
scripts/
  frappe-monitor-collect.sh  # bash collector (embedded into binary)
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

Phases 1–5 are done. Phase 6 (Telegram alerting) and Phase 7 (auth, multi-tenant scoping, deploy markers, in-dashboard server CRUD) are not started.

See [`docs/guide/README.md`](docs/guide/README.md) for the phase status table and what each phase delivered.
