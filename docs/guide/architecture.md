# Architecture

What runs where, why, and how data flows.

## Components

```
┌──────────────────────────────────────────────────────────────────┐
│  monitor host                                                    │
│                                                                  │
│   ┌────────────────────────┐                                     │
│   │ frappe-monitor binary  │                                     │
│   │  (single Go process)   │                                     │
│   │                        │                                     │
│   │  • chi HTTP router     │      ┌────────────────────────┐     │
│   │  • SPA (embed_dist)    │◀─────│ browser (Vue 3 + Vite) │     │
│   │  • SQLite (ent ORM)    │      └────────────────────────┘     │
│   │  • SSH pool            │                                     │
│   │  • cron scheduler      │                                     │
│   │  • collector pipeline  │                                     │
│   └─────┬──────────────┬───┘                                     │
│         │ pushes       │ reads                                   │
│         ▼              ▼                                         │
│   ┌────────────┐  ┌────────────┐                                 │
│   │ VictoriaM. │  │   Loki     │                                 │
│   │   :8428    │  │   :3100    │                                 │
│   │  metrics   │  │   logs     │                                 │
│   └────────────┘  └────────────┘                                 │
│                                                                  │
└─────────┬────────────────────────────────────────────────────────┘
          │ outbound SSH (per-server cron tick, default 15m)
          ▼
   ┌──────────────────────────┐
   │ frappe bench server      │
   │  ~/.frappe-monitor/      │
   │     collect.sh           │ ← shipped via deploy-collector endpoint
   │  /home/frappe/...        │
   └──────────────────────────┘
```

### `frappe-monitor` binary

Single static Go binary. Includes:

- **Embedded SPA** — `web/dist/` baked in via `//go:embed` (with the `embed_dist` build tag), served on every non-API, non-`/healthz` path. Build-tag fallback (`embed_placeholder.go`) lets `go build` work without npm.
- **chi router** — HTTP handlers under `/api/v1/`, plus `/healthz` and the SPA fallback. Middleware: recoverer (outermost) → request logger → optional HTTP basic auth (when `auth.password` is set).
- **Storage** — SQLite via [ent](https://entgo.io/) (pure-Go driver `modernc.org/sqlite`, so CGO stays disabled). Stores: `Server` (registry), `LogCursor` (per-file tail offsets), `AlertState` (per-(rule, fingerprint) firing/resolved status).
- **SSH pool** — per-host connection pool with typed errors (`ErrAuth`, `ErrDial`, `ErrTimeout`). Underpins the test-connection diagnostic and every scheduled pull.
- **Scheduler** — `robfig/cron/v3` ticking at `scheduler.default_interval_seconds`. A semaphore caps concurrency at `scheduler.max_parallel`. Each tick fans out one job per registered server.
- **Collector pipeline** — for one server: `ssh exec collect.sh → parse output → push metrics to VM → tail logs → push to Loki → upsert cursor`. Failures are typed: SSH/parse failures mark the server unreachable; VM/Loki push failures don't (the bench is fine; the metrics tier is the failure domain).
- **Alerts service** (Phase 6) — independent goroutine ticking at `alerts.evaluation_interval_seconds`. For each rule, queries VM for the firing series, reconciles against last-cycle state in SQLite, fans out new fires + resolutions to Telegram. Send-only, no acks.

### VictoriaMetrics

Time-series store. Receives Influx-line-protocol pushes from the monitor on `/write`. The dashboard's `/api/v1/metrics/query` proxies PromQL to `/api/v1/query_range`. Image pinned in `deploy/docker-compose.prod.yml` (`v1.142.0`); the load-bearing `-influxSkipSingleField` flag keeps metric names matching the master plan's naming convention.

### Loki

Log store. Receives JSON pushes on `/loki/api/v1/push`. The dashboard's `/api/v1/logs/query` proxies LogQL to `/loki/api/v1/query_range`. Image pinned (`grafana/loki:3.4.1`). Loki uses its bundled example config — sufficient for our scale; tune via a custom config file if you outgrow it.

### Vue 3 SPA

Built with Vite + TypeScript + vue-router + ECharts. Lives in `web/`. Built into `internal/web/dist/` (gitignored), then embedded into the Go binary at compile time. Pages: Servers, Server detail, Benches, Bench detail, Sites, Site detail. URL-synced timeline filter (`?from=&to=&step=&refresh=`) drives every chart.

### Bench-side collector

`scripts/frappe-monitor-collect.sh` — a self-contained bash script embedded in the binary at build time. The `deploy-collector` endpoint pipes it over SSH to the target's `~/.frappe-monitor/collect.sh`. Outputs structured `key=val` lines under `###META`, `###SERVER`, `###BENCH`, `###SITE` sections plus `###END`.

The collector reads `/proc`, `/sys`, `bench` CLI output, Redis `LLEN`, and HTTP probes against site URLs. No external dependencies beyond bash + standard coreutils.

## Data flow per pull cycle

```
        ┌─────────────────────────────────────────────────────┐
        │ scheduler tick (every default_interval_seconds)     │
        └────────────────────────┬────────────────────────────┘
                                 │ for each registered server
                                 ▼
               ┌──────────────────────────────────────┐
               │ pull job (capped by sem max_parallel) │
               └────┬─────────────────────────────────┘
                    │
                    ▼
            ssh exec ~/.frappe-monitor/collect.sh
                    │ stdout
                    ▼
            parser tokenizes ###SERVER / ###BENCH / ###SITE
                    │ ServerMetrics / BenchMetrics / SiteMetrics
                    ▼
       ┌────────────┴────────────┐
       │                         │
       ▼ Influx-line proto       ▼ Loki JSON push
   POST /write to VM         POST /loki/api/v1/push
       │                         │
       ▼                         ▼
   200 OK                    200 OK + cursor offset
       │                         │
       └────────────┬────────────┘
                    ▼
        Storage.UpdateServerStatus("reachable")
        Storage.UpsertLogCursor(...)
```

If the SSH or parse step fails: `UpdateServerStatus("unreachable", err)`, the dashboard turns the card red, and the rest of the pipeline doesn't run for this cycle. If only the VM/Loki push fails: status stays `reachable` (the bench is fine), the failure goes to the structured log, and the next cycle retries.

## Storage

| What | Where | Why there |
|---|---|---|
| Server registry (id, hostname, ssh creds, status, last_error) | SQLite (ent ORM) | Read on every API call; durable; small. |
| Per-(server,file) log tail offsets | SQLite | Strict consistency required to avoid double-shipping or losing log lines on restart. |
| Time-series metrics | VictoriaMetrics | Purpose-built, Prom-compatible, very small footprint. |
| Log lines | Loki | Cheap, label-indexed; LogQL pairs naturally with PromQL. |
| Phase 5 hierarchy lists (which benches/sites exist) | (none — derived from VM at query time) | Avoids a second source of truth that can disagree with VM. |

## Dashboard query path

Browser hits `/api/v1/metrics/query?query=<PromQL>&start=&end=&step=` on the monitor. Monitor forwards to VM `/api/v1/query_range`. Response is mirrored back unchanged (same status, same content-type). Same pattern for logs via the Loki `/api/v1/logs/query` proxy.

The proxy is intentionally thin — no PromQL rewriting, no auth (yet), no caching. It exists to:

1. Avoid CORS / token plumbing in the browser.
2. Centralize TLS / auth / rate-limiting in one place (Phase 7).

## Failure domains

| Failure | Effect |
|---|---|
| Bench server SSH down | That server's card goes red. Other servers unaffected. |
| VM down | Pushes fail; logs note it. Server status still updates from SSH success. Dashboard charts show "vm unreachable". |
| Loki down | Log pushes fail; metrics path is unaffected. Site-detail log feed shows an error. |
| Monitor binary crashes | systemd restarts within 5s. SQLite is fsync-durable; no data lost. |
| Disk full on monitor host | VM + Loki stop ingesting; binary continues to run but pushes fail. |

## What's deferred

| Item | Status |
|---|---|
| Telegram alerting | shipped (Phase 6) |
| Built-in HTTP basic auth | shipped (Phase 7 v1) |
| In-dashboard "add server" form + delete + rename | shipped (Phase 7 v1) |
| Per-server schedule overrides | post-v1 backlog |
| Site-scoped log labels | post-v1 backlog |
| Deploy markers (annotation timeline) | post-v1 backlog |
| Multi-tenant access scoping (per-team password / SSO) | post-v1 backlog |
| p95 / quantile_over_time on site detail (currently avg_over_time) | post-v1 backlog |
