# frappe-monitor

Monitoring and alerting for multi-server, multi-bench, multi-site Frappe deployments.

See `docs/2026-04-22/1.md` for the master design plan, `docs/2026-04-22/2.md` for the Phase 1 plan, and `docs/2026-04-29/3.md` for the Phase 2 plan. Subsequent dated `docs/YYYY-MM-DD/` folders capture decisions and amendments. `docs/hardening-backlog.md` tracks known non-blocking quality items.

## Phase 1 surface (HTTP)

| Method | Path | Purpose |
|---|---|---|
| GET  | `/healthz` | Liveness; returns `{"status":"ok"}`. |
| POST | `/api/v1/servers` | Create a server. 409 on duplicate hostname; 400 on bad JSON or missing required fields. |
| GET  | `/api/v1/servers` | List all servers. |
| GET  | `/api/v1/servers/{id}` | Fetch one server. 404 on unknown id. |
| POST | `/api/v1/servers/{id}/test-connection` | Diagnostic SSH probe. Always 200 (or 404 if id unknown). Body: `{reachable, latency_ms, error?, error_kind?}`. |

## Phase 2 surface (HTTP + scheduler)

Phase 2 adds the metrics pipeline:

- `POST /api/v1/servers/{id}/deploy-collector` — pipes the embedded `frappe-monitor-collect.sh` to the target via SSH and `chmod +x`'s it. Returns 200 with `{deployed, version}`.
- A **per-server scheduler** registered at boot: every `cfg.scheduler.default_interval_seconds` (default 900s = 15 min), the binary SSHes to each server, runs the collector, parses the output, and pushes influx-line-protocol to VictoriaMetrics.
- Server status is updated in storage on every cycle: `reachable` on success, `unreachable` with `last_error` on SSH or parse failure. **VM push failure does NOT mark the server unreachable** — the bench is fine; the metrics backend is the failure domain.

## Run (Phase 1 only — no metrics flow)

```bash
make build
cp deploy/config/monitor.yaml.example config/monitor.yaml
make run
```

The binary auto-creates the parent of `database.path` (default `./data/`) at mode 0o750 on first run.

## Run (Phase 2 — with VictoriaMetrics)

```bash
# 1. Bring up VictoriaMetrics in a Docker container, bound to 127.0.0.1:8428.
make vm-up

# 2. Run the monitor on the host. It pushes to http://127.0.0.1:8428.
make run

# 3. Tail VM logs in another terminal (optional).
make vm-logs

# 4. Query metrics:
curl 'http://127.0.0.1:8428/api/v1/query?query=frappe_server_load_1m'

# 5. When done, tear down VM (data persists in named volume `frappe-monitor-vm-data`).
make vm-down

# To wipe accumulated dev metrics history, also remove the volume:
docker volume rm frappe-monitor-vm-data
```

If you copy `deploy/docker-compose.dev.yml` to another project, **keep the `-influxSkipSingleField` flag** in the VM command. Without it, VictoriaMetrics's Influx-line-protocol ingestion appends `_value` to every metric name (so `frappe_server_load_1m` becomes `frappe_server_load_1m_value`) and PromQL queries written against the master plan §4 naming convention will silently miss every series.

Send `SIGTERM` (or Ctrl+C) to the monitor for graceful shutdown — the scheduler stops accepting new ticks, in-flight pulls observe ctx.Done() and unwind, then `srv.Shutdown` drains HTTP. Combined budget is 10s.

### Smoke test (Phase 1 only)

`scripts/smoke.sh` exercises every Phase 1 route end-to-end against a local hermetic config (its own port and tempdir, doesn't touch your real config or data), verifies the duplicate-hostname-409 contract, the test-connection-on-unknown-id-404 contract, and the SIGTERM ≤10s clean-exit contract.

```bash
./scripts/smoke.sh
```

Expected last line: `==> ALL CHECKS PASSED`. Phase 2 smoke (with VM) lands in Task 20.

## Project layout

```
cmd/monitor/main.go        # binary entry point
internal/
  api/                     # chi router, middleware, handlers
  collector/               # ssh→parse→push pipeline (Phase 2)
  config/                  # koanf-backed loader
  metrics/                 # ServerMetrics + VictoriaMetrics push client
  parser/                  # collector-output tokenizer + ServerFromSections
  scheduler/               # robfig/cron + semaphore + per-job timeout
  ssh/                     # executor interface, pool, fake, typed errors
  storage/                 # store interface, ent-backed sqlite
ent/                       # ent schema + generated client (committed)
scripts/
  frappe-monitor-collect.sh  # bash collector (embedded into binary)
  embed.go                   # //go:embed wiring + version helper
  smoke.sh                   # Phase 1 smoke
deploy/
  config/monitor.yaml.example
  docker-compose.dev.yml     # VictoriaMetrics for dev (Phase 2)
docs/                      # design docs + hardening backlog + dated decisions
tasks/                     # gitignored review-loop folder
```

## Tests

```bash
make test
```

Phase 2 has 9 test packages: `internal/{api, collector, config, metrics, parser, scheduler, ssh, storage}` and `scripts`. The `ent/` subpackages contain only generated code and have no test files (expected). The full suite runs in under 30 s; `make test` is the canonical run.

## Workflow

- Per-task review uses the gitignored `tasks/` folder — see `tasks/README.md` for the convention.
- Decisions go in `docs/YYYY-MM-DD/N.md`; non-blocking quality items go in `docs/hardening-backlog.md`.
