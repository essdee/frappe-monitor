# frappe-monitor

**Monitoring, log aggregation, alerting, and remote control for a fleet of Frappe / ERPNext servers — from one small Go binary.**

You run it on a tiny VM near your servers. It SSHes into each Frappe host on a
schedule, collects health metrics + logs, stores them, and shows everything on
a live dashboard — plus it can run common bench operations (migrate, restart,
clear-cache) on those hosts for you, and alert you on Telegram when something
breaks.

No agent to install on every server. No Prometheus/Grafana stack to wire up. No
Frappe app to `bench get-app`. One binary, one config file, one dashboard.

---

## Why it exists

Running several Frappe benches (multiple servers, each with multiple benches,
each with multiple sites) means there's no single place to answer "is everything
healthy?". `frappe-monitor` is that single place:

- **See the whole fleet at a glance** — CPU/RAM/disk per server, per-bench app +
  queue health, per-site HTTP status, DB replication lag.
- **Search logs across all hosts** without SSHing into each one.
- **Get paged** (Telegram) the moment a server goes unreachable, a disk fills, a
  site goes unhealthy, or a Redis queue backs up.
- **Operate hosts from the browser** — run an allowlisted set of bench/service
  commands (migrate, build, restart, clear-cache, supervisor restart) with a
  full audit trail, instead of opening N SSH sessions.

---

## How it works

```
                         ┌──────────────────────── frappe-monitor (one VM) ─────────────────────────┐
                         │                                                                            │
  Frappe host A  ──SSH──▶│  scheduler ─▶ collector ─▶ parser ─▶┬─▶ VictoriaMetrics (metrics, push)   │
  Frappe host B  ──SSH──▶│  (per-server, cron)                 └─▶ Loki (logs, push)                  │
  Frappe host C  ──SSH──▶│  alerts engine ─(PromQL)─▶ VictoriaMetrics ─▶ Telegram                     │
                         │  control panel ─(SSH)─▶ bench/service commands (allowlisted, audited)      │
                         │  HTTP API + embedded Vue dashboard  ◀── query proxy ── VM / Loki           │
                         └────────────────────────────────────────────────────────────────────────────┘
                                          ▲  browser (live dashboard over WebSocket)
```

1. **Collect.** A scheduler runs a small bash collector on each registered host
   over SSH (no remote agent). It reports server metrics (CPU/mem/disk/net),
   per-bench info (Frappe version, app count, Redis queue depths, supervisor
   process counts) and per-site HTTP health.
2. **Store.** Metrics are pushed to **VictoriaMetrics** (time-series); logs are
   tailed and pushed to **Loki**. The monitor's own registry (servers, audit
   log, DB targets) lives in a local **SQLite/MariaDB/Postgres** database.
3. **Show.** A single Go process serves the **Vue dashboard** (embedded in the
   binary) and proxies dashboard queries to VM/Loki. Updates stream to the
   browser over WebSocket — no polling.
4. **Alert.** An alerts engine evaluates PromQL rules against VM on a cadence and
   fans firing/resolved notifications out to Telegram.
5. **Control.** The control panel runs a fixed allowlist of bench/service
   commands on a host over the same SSH pool, recording every run.

The backing stack (VictoriaMetrics + Loki) runs as Docker containers the
installer brings up for you. Everything is one config file
(`monitor.yaml`) and every value is overridable by env var (`MONITOR_*`).

---

## Features

- Per-server / per-bench / per-site metrics, charts, and a fleet overview
- Cross-fleet log search (Loki) + optional live per-event log streaming
- Telegram alerting with sensible default rules + custom PromQL rules
- Browser-driven, audited remote control (migrate / build / restart / clear-cache / supervisor)
- DB replication monitoring (MySQL/MariaDB + PostgreSQL standbys)
- SSH host-key **trust-on-first-use** (auto-pins, rejects changed keys) — no manual `ssh-keyscan`
- Cookie-session auth on the dashboard + HTTP basic auth for scripts
- Pluggable store: SQLite (zero-config) or MariaDB / PostgreSQL
- One static binary (CGO-free), hardened systemd unit, idempotent installer

---

## Quick install (one command)

`deploy/install.sh` auto-detects the OS, builds the binary (with the dashboard
embedded), brings up VictoriaMetrics + Loki, generates an SSH key + a dashboard
password, installs the service, and starts everything. Idempotent — re-run to
upgrade in place.

```bash
# Linux production server (systemd + a frappe-monitor system user):
sudo ./deploy/install.sh

# macOS dev/local (launchd LaunchAgent under your user, no sudo):
./deploy/install.sh
```

Then open **http://<host>:8080**, log in with the password the installer
printed, and add your first server (use the SSH key path the installer prints;
authorize its `.pub` on each bench with `ssh-copy-id`).

> The dashboard serves plain HTTP. For production, restrict the port with a
> firewall and put TLS in front — a `deploy/Caddyfile.example` is installed
> alongside the compose file. See [`docs/guide/deployment.md`](docs/guide/deployment.md).

Useful flags: `--no-stack` (use an existing VM/Loki), `--uninstall`, `--purge`,
`--password=…`, `--enable-alerts --bot-token=… --chat-ids=…`.

## Quick demo (laptop, one command)

```bash
make local-test
```

Builds the binary, brings up VM + Loki, seeds demo metrics + logs, and serves
the dashboard at http://localhost:8080. Ctrl+C tears it all down. No sudo, no
real server required. Full walkthrough: [`docs/guide/local-test.md`](docs/guide/local-test.md).

## Manual local dev

```bash
make vm-up     # docker compose: VM + Loki on 127.0.0.1
make build     # builds the Vue SPA + Go binary with embedded assets
make run       # runs ./bin/monitor-server on :8080
```

Requires Go ≥ 1.25, Node ≥ 20, Docker ≥ 24 with the compose plugin.

---

## Documentation

The complete operator guide lives in **[`docs/guide/`](docs/guide/)**:

| | |
|---|---|
| Try it on a Mac/Linux laptop in 5 min | [`docs/guide/local-test.md`](docs/guide/local-test.md) |
| Deploy on a fresh server | [`docs/guide/deployment.md`](docs/guide/deployment.md) |
| Use the dashboard, add servers | [`docs/guide/usage.md`](docs/guide/usage.md) |
| Day-2 ops (start/stop/upgrade/logs) | [`docs/guide/operations.md`](docs/guide/operations.md) |
| Config reference (every key) | [`docs/guide/configuration.md`](docs/guide/configuration.md) |
| HTTP API reference | [`docs/guide/api.md`](docs/guide/api.md) |
| What runs where, why (operator view) | [`docs/guide/architecture.md`](docs/guide/architecture.md) |
| Full engineering deep-dive (every package, wire formats, data model) | [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) |
| Things broken? | [`docs/guide/troubleshooting.md`](docs/guide/troubleshooting.md) |
| Contributing & maintenance | [`docs/guide/contributing.md`](docs/guide/contributing.md) |

## Project layout

```
cmd/monitor/            # binary entry point + install/uninstall subcommands
internal/
  api/                  # chi router, handlers, auth, query proxies, WebSocket
  collector/ parser/    # ssh→parse→push pipeline
  metrics/ logs/        # VictoriaMetrics + Loki push clients
  scheduler/            # robfig/cron + semaphore + per-job timeout
  ssh/                  # SSH pool, host-key TOFU, key loading
  alerts/               # PromQL evaluation + Telegram notifier
  dbmonitor/            # MySQL/Postgres replication checks
  control/              # allowlisted bench/service commands + audit
  streamer/             # long-lived per-server log tailing → Loki
  storage/              # ent-backed registry (sqlite/mariadb/postgres)
  realtime/             # WebSocket hub
  installer/            # `frappe-monitor install` config/service renderer
ent/                    # ent schema + generated client (committed)
web/                    # Vue 3 + Vite SPA (embedded into the binary)
deploy/                 # install.sh, systemd units, docker-compose, Caddyfile, loki.yaml
scripts/                # embedded bash collector + smoke test
docs/                   # operator guides + engineering deep-dive
```

## Tests + smoke

```bash
make test            # go test ./... -race -count=1
./scripts/smoke.sh   # end-to-end: build + VM + Loki + every route
cd web && npm run test   # frontend unit tests (vitest)
```

`scripts/smoke.sh` is the canonical "did I break anything" check; last line on
success is `==> ALL CHECKS PASSED`.

## License

See [`LICENSE`](LICENSE).
