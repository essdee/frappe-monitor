# frappe-monitor

Monitoring and alerting for multi-server, multi-bench, multi-site Frappe deployments.

See `docs/2026-04-22/1.md` for the master design plan and `docs/2026-04-22/2.md` for the Phase 1 implementation plan. Subsequent dated `docs/YYYY-MM-DD/` folders capture decisions and amendments. `docs/hardening-backlog.md` tracks known non-blocking quality items.

## Phase 1 surface

After Phase 1, the binary exposes five HTTP routes:

| Method | Path | Purpose |
|---|---|---|
| GET  | `/healthz` | Liveness; returns `{"status":"ok"}`. |
| POST | `/api/v1/servers` | Create a server. 409 on duplicate hostname; 400 on bad JSON or missing required fields. |
| GET  | `/api/v1/servers` | List all servers. |
| GET  | `/api/v1/servers/{id}` | Fetch one server. 404 on unknown id. |
| POST | `/api/v1/servers/{id}/test-connection` | Probe via SSH. Always 200 (or 404 if id unknown). Body: `{reachable, latency_ms, error?, error_kind?}`. |

## Run

```bash
# 1. Build (static, CGO disabled)
make build

# 2. Create a config from the example
cp deploy/config/monitor.yaml.example config/monitor.yaml

# 3. Run. The binary auto-creates the parent of database.path
#    (default ./data/) at mode 0o750 on first run, so no manual mkdir
#    is required.
make run
```

The binary listens on `cfg.server.listen_addr` (default `:8080`). Send `SIGTERM` (or Ctrl+C) for graceful shutdown — in-flight HTTP requests and SSH probes are cancelled via context propagation, then `srv.Shutdown` waits up to 10 s for active connections to drain.

### Smoke test

`scripts/smoke.sh` exercises every Phase 1 route end-to-end against a local hermetic config (its own port and tempdir, doesn't touch your real config or data), verifies the duplicate-hostname-409 contract, the test-connection-on-unknown-id-404 contract, and the SIGTERM ≤10s clean-exit contract.

```bash
./scripts/smoke.sh
```

Expected last line: `==> ALL CHECKS PASSED`.

## Project layout

```
cmd/monitor/main.go        # binary entry point
internal/
  api/                     # chi router, middleware, handlers
  config/                  # koanf-backed loader
  ssh/                     # executor interface, pool, fake, sentinels
  storage/                 # store interface, ent-backed sqlite
ent/                       # ent schema + generated client (committed)
deploy/config/             # example monitor.yaml
docs/                      # design docs + hardening backlog
scripts/smoke.sh           # phase 1 smoke
tasks/                     # gitignored review-loop folder
```

## Tests

```bash
go test ./... -race -count=1
```

Phase 1 has 26 tests across `internal/api` (10), `internal/config` (6), `internal/ssh` (4), and `internal/storage` (6). The `ent/` subpackages contain only generated code and have no test files (expected).

## Workflow

- All work targets `develop`. Each feature branch PRs into `develop`; the user merges manually.
- Per-task review uses the gitignored `tasks/` folder — see `tasks/README.md` for the convention.
- Decisions go in `docs/YYYY-MM-DD/N.md`; non-blocking quality items go in `docs/hardening-backlog.md`.
