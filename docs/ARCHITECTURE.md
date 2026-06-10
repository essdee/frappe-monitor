# frappe-monitor — System Architecture (Deep Dive)

> **Audience:** engineers working *on* frappe-monitor (extending it, debugging it, reviewing it).
> For an operator-oriented "what runs where" overview see [`docs/guide/architecture.md`](guide/architecture.md); this document is the exhaustive engineering reference that covers every package, the wire formats, the data model, and the extension points.
>
> Everything below is grounded in the code as of the `develop` branch. File references are `path:line` and are clickable.

---

## 1. What it is, in one paragraph

frappe-monitor is a **single Go binary** that monitors a fleet of Frappe/ERPNext servers. It pulls metrics and logs from each server **over SSH** (no agent daemon to install — just a bash script shipped to `~/.frappe-monitor/`), writes time-series to **VictoriaMetrics** and log lines to **Loki**, and serves a **Vue 3 dashboard** from the same process. It runs as two systemd units on a small Linux VM next to the fleet: the binary, and a docker-compose stack for VictoriaMetrics + Loki. There are two ingest paths: a **scheduled snapshot collector** (cron, every 15 min by default) and an optional **real-time log streamer** (one long-lived SSH session per server). Alerting (Phase 6) evaluates PromQL rules against VictoriaMetrics and pages Telegram. Auth (Phase 7) is an HMAC cookie session over an optional dashboard password.

---

## 2. System at a glance

```
                                  ┌──────────────────────────── monitor host ───────────────────────────────┐
                                  │                                                                          │
   browser (Vue 3 SPA) ──────────┼──► chi HTTP router  ── /api/v1/* ──┐                                      │
        ▲                         │      :8080           ── /healthz   │                                      │
        │ embedded SPA + JSON     │      (embed_dist)    ── SPA fallback│                                      │
        └─────────────────────────┼──────────────────────────────────┘                                      │
                                  │        │            │              │                                      │
                                  │        │ proxy      │ read/write   │ read/write                           │
                                  │        ▼            ▼              ▼                                      │
                                  │   ┌──────────┐  ┌────────────┐ ┌────────────┐                            │
                                  │   │ SQLite   │  │ Victoria   │ │   Loki     │                            │
                                  │   │ (ent ORM)│  │ Metrics    │ │  :3100     │  (docker-compose stack)    │
                                  │   │ servers, │  │ :8428      │ │  logs      │                            │
                                  │   │ cursors, │  │ metrics    │ └────────────┘                            │
                                  │   │ alerts,  │  └────────────┘       ▲                                   │
                                  │   │ snapshots│        ▲              │                                   │
                                  │   └──────────┘        │ push         │ push                              │
                                  │   ┌───────────────────┴──────────────┴───────────────────────────────┐  │
                                  │   │ in-process services (goroutines):                                 │  │
                                  │   │   • scheduler (cron) → collector.Pipeline.PullOnce per server     │  │
                                  │   │   • alerts.Service  → evaluate PromQL → Telegram                  │  │
                                  │   │   • streamer.Manager → 1 long-lived SSH session per server        │  │
                                  │   │   • SSH connection pool (shared by all of the above)              │  │
                                  │   └────────────────────────────────┬─────────────────────────────────┘  │
                                  └────────────────────────────────────┼────────────────────────────────────┘
                                                                       │ outbound SSH
                          ┌────────────────────────────────────────────┼────────────────────────────────────┐
                          ▼                                             ▼                                     ▼
                 ┌──────────────────┐                         ┌──────────────────┐                  ┌──────────────────┐
                 │ frappe server A  │                         │ frappe server B  │       ...        │ frappe server N  │
                 │ ~/.frappe-monitor│                         │ ~/.frappe-monitor│                  │                  │
                 │   collect.sh     │ ← deployed over SSH     │   stream.sh      │                  │                  │
                 │   system.sh      │                         │                  │                  │                  │
                 └──────────────────┘                         └──────────────────┘                  └──────────────────┘
```

Two failure domains are deliberately separated: the **bench** (reached over SSH) and the **metrics tier** (VictoriaMetrics + Loki). An SSH/parse failure marks a server *unreachable*; a VM/Loki push failure does **not** — the bench is fine, the storage tier is the problem.

---

## 3. Repository layout

```
cmd/monitor/main.go          # binary entry point + lifecycle orchestration
internal/
  config/                    # koanf loader (defaults → YAML → env), validation
  scheduler/                 # robfig/cron + semaphore + per-job timeout, keyed entries
  ssh/                       # Executor interface, connection pool, typed errors, fake
  collector/                 # snapshot pipeline: ssh→parse→push metrics + tail logs→Loki
  parser/                    # tokenizes collector ###SECTION output into typed structs
  metrics/                   # ServerMetrics/BenchMetrics/SiteMetrics + Influx line proto + VM client
  logs/                      # Loki JSON push client (Stream/Entry types)
  alerts/                    # Phase 6: rule evaluator, fingerprinting, Telegram fan-out
  streamer/                  # real-time log streaming: manager, session, sink, protocol, handler
  storage/                   # Store interface + ent-backed SQLite implementation
  api/                       # chi router, middleware, auth, handlers, proxies, respond helpers
  web/                       # //go:embed dist (embed_dist tag) + placeholder fallback
ent/                         # ent schema (4 entities) + generated client (committed)
  schema/                    # server, logcursor, alertstate, systemsnapshot
web/                         # Vue 3 + Vite + TS SPA source → builds into internal/web/dist
scripts/
  frappe-monitor-collect.sh  # snapshot metrics collector (embedded, v2.5.0)
  frappe-monitor-system.sh   # one-shot system inventory → JSON (embedded, v1.0.0)
  frappe-monitor-stream.sh   # real-time log tailer (embedded, v8.0.0)
  embed.go                   # //go:embed wiring + version extraction helpers
  smoke.sh                   # end-to-end smoke test
deploy/
  install.sh                 # idempotent production installer
  systemd/                   # frappe-monitor.service + frappe-monitor-stack.service
  docker-compose.{dev,prod}.yml
  Caddyfile.example          # optional TLS reverse proxy
  config/monitor.yaml.example
  local-test.sh              # hermetic laptop demo (no sudo/systemd)
docs/
  ARCHITECTURE.md            # ← this file
  guide/                     # evergreen operator docs
  YYYY-MM-DD/                # dated, frozen design decisions
  hardening-backlog.md
Makefile                     # build/test/run/vm-up targets
```

---

## 4. The binary: bootstrap & lifecycle

**Entry:** `cmd/monitor/main.go`. `main()` parses one flag, `-config` (default `./config/monitor.yaml`), and calls `run(cfgPath)`. `run` wires everything up in a fixed order and blocks until a signal.

### 4.1 Boot order

1. **Config + logging** — `config.Load(cfgPath)` (defaults → YAML → `MONITOR_*` env). Logger is `slog` (JSON default, or text), level from config.
2. **Signal context** — `ctx, cancel := signal.NotifyContext(context.Background(), SIGTERM, SIGINT)`. This root `ctx` is set as the HTTP server's `BaseContext`, so SIGTERM cancels in-flight SSH probes and request handlers. (The data directory is `MkdirAll`'d at mode `0750` just before this, between logging setup and the signal context.)
3. **Storage** — `storage.OpenEntStore(ctx, dsn)`. The SQLite DSN is built with WAL journal mode, `synchronous=normal`, `busy_timeout=5000`, `foreign_keys=on` (required for cascade deletes), `temp_store=memory`, `mmap_size=128MiB`, `cache_size=-64000` (negative = KiB, ≈ 64 MB). `SetMaxOpenConns(1)` — one writer; WAL gives concurrent readers.
4. **SSH pool** — `ssh.NewPool(PoolConfig{DialTimeout, CommandTimeout})`, shared by the collector, streamer, and the test-connection handler.
5. **Metrics + collector** — `metrics.NewVMClient(vmURL, pushTimeout)` and a `collector.Pipeline{Store, Exec: pool, Push, Logger}`.
6. **Scheduler** — `scheduler.New(maxParallel, perJobTimeout, logger)`; `registerScheduledPulls` adds one keyed cron entry per server (`@every <default_interval_seconds>s` → `pipeline.PullOnce(serverID)`), then `scheduler.Start()`.
7. **Alerts** (optional, default off) — when `alerts.enabled`, validate the Telegram config, build `alerts.New(...)`, and start its goroutine.
8. **Streamer** (optional, default off) — when `streaming.enabled`, `streamer.NewManager(...)` (returns `nil` when disabled, treated as a no-op everywhere) and `manager.Start(ctx)` to launch one session per server.
9. **HTTP server** — `api.NewRouter(deps)` on `server.listen_addr` (default `:8080`), read/write timeouts from config, `BaseContext` = root signal ctx. Runs in a goroutine; errors land on a buffered channel.

### 4.2 Hot lifecycle hooks

The router is given two callbacks so server CRUD does **not** require a restart:

- `onServerCreated(id)` — hot-adds the scheduler entry, launches a streamer session (10s timeout, background), and captures a system snapshot (30s timeout, background, via `scripts.SystemScript`).
- `onServerDeleted(id)` — removes the scheduler entry and tears down the streamer session.

### 4.3 Graceful shutdown

On signal or fatal server error, shutdown runs in a deliberate order so jobs that hold the store/pool finish first:

1. **Scheduler** — stop firing, drain in-flight pulls. Scheduler jobs touch the store and pool directly (not via HTTP), so they must drain before those close.
2. **Alerts** (if running).
3. **Streamer** — final flush before store/pool close.
4. **HTTP server** — `srv.Shutdown`.
5. Deferred (LIFO at return): close pool, then close store, then `cancel()` the signal context.

Steps 1 and 4 **share a single 10s `shutdownCtx`**: the scheduler drains first, then the HTTP server gets whatever remains of that 10s. The streamer (step 3) has its own independent 5s timeout. `systemd`'s `TimeoutStopSec=15s` budgets the whole sequence.

---

## 5. Configuration

**Package:** `internal/config`. Backed by [koanf](https://github.com/knadh/koanf) with a three-layer merge, lowest to highest precedence:

1. **`defaults()`** — an in-memory confmap of ~33 dotted keys with hard-coded defaults.
2. **YAML file** — `file.Provider(path)` + `yaml.Parser()`. Missing keys fall back to defaults; an explicit empty override (e.g. `path: ""`) is rejected by validation.
3. **Env vars** — `env.Provider("MONITOR_", ...)`. Transform: strip `MONITOR_`, lowercase, `__` → `.`. So `MONITOR_SCHEDULER__MAX_PARALLEL` → `scheduler.max_parallel`.

Then `k.Unmarshal("", &cfg)` populates the struct and `cfg.validate()` enforces invariants (required strings non-empty; most timeouts/intervals/counts ≥ 1, but `alerts.notify_repeat_seconds` ≥ 0; enums for `log.level` ∈ {debug,info,warn,error} and `log.format` ∈ {json,text}; `alerts.evaluation_interval_seconds` ≥ 15; streaming backoff sanity). The alerts and streaming numeric guards only run when that subsystem is **enabled**. Deeper validation (Telegram token presence, script path) lives in the alerts/streamer packages themselves.

### 5.1 Config reference

| Key | Default | Notes |
|---|---|---|
| `server.listen_addr` | `:8080` | required |
| `server.read_timeout_seconds` / `write_timeout_seconds` | 15 / 15 | |
| `database.path` | `./data/monitor.db` | dir auto-created 0750 |
| `ssh.dial_timeout_seconds` | 10 | |
| `ssh.command_timeout_seconds` | 30 | per-command cap (not applied to streams) |
| `ssh.max_connections_per_host` | 2 | **validated but not enforced** by the pool (unbounded map) |
| `log.level` / `log.format` | info / json | |
| `metrics.vm_url` | `http://127.0.0.1:8428` | required; `/write` for push, `/api/v1/query_range` for queries |
| `metrics.push_timeout_seconds` / `query_timeout_seconds` | 5 / 15 | |
| `logs.loki_url` | `http://127.0.0.1:3100` | required |
| `logs.push_timeout_seconds` / `query_timeout_seconds` | 5 / 15 | |
| `scheduler.default_interval_seconds` | 900 | per-server pull cadence (15 min) |
| `scheduler.max_parallel` | 10 | concurrent pulls (semaphore) |
| `scheduler.per_job_timeout_seconds` | 30 | per-pull ctx timeout |
| `auth.password` | `""` | **empty = auth disabled** |
| `auth.realm` | `frappe-monitor` | |
| `alerts.enabled` | false | opt-in |
| `alerts.evaluation_interval_seconds` | 60 | min 15 |
| `alerts.notify_repeat_seconds` | 3600 | re-page cooldown per (rule,fingerprint) |
| `alerts.vm_query_timeout_seconds` | 10 | |
| `alerts.telegram.bot_token` / `chat_ids` / `send_timeout_seconds` | / [] / 5 | |
| `alerts.disable_defaults` | false | drop the 4 built-in rules |
| `streaming.enabled` | false | opt-in |
| `streaming.monitor_id` | `""` | surfaces in Loki/metrics labels |
| `streaming.script_path` | | absolute path on bench (required if enabled) |
| `streaming.files[]` | | `{id, path}` list, global to all servers |
| `streaming.flush_interval_seconds` | 1 | sink flush cadence |
| `streaming.max_batch_lines` | 500 | per-stream overflow flush |
| `streaming.{min,max}_backoff_seconds` | 1 / 60 | reconnect backoff |
| `streaming.push_timeout_seconds` | 10 | caps each Loki/VM push |

---

## 6. Data model (ent + SQLite)

**Package:** `ent/schema` (definitions) → generated client in `ent/` (committed) → wrapped by `internal/storage`.

The `storage.Store` interface (`internal/storage/store.go`) is the only persistence surface the rest of the code sees; `EntStore` (`ent_store.go`) is the SQLite implementation. SQLite is the right call here: the data is small, read on every API call, and needs strict consistency for cursors. WAL gives concurrent readers with a single writer.

### 6.1 Entities

**Server** (`ent/schema/server.go`) — the registry. One row per monitored host.

| Field | Type | Notes |
|---|---|---|
| `name` | string | NotEmpty |
| `hostname` | string | NotEmpty, **Unique** |
| `ssh_user` | string | default `monitor` |
| `ssh_port` | int | default 22, positive |
| `ssh_key_path` | string | NotEmpty (key-based auth only today) |
| `bench_paths` | []string (JSON) | optional; overrides bench auto-discovery |
| `labels` | map[string]string (JSON) | optional |
| `status` | enum | `unknown` \| `reachable` \| `unreachable`, default `unknown` |
| `last_pinged_at` | *time | nullable |
| `last_error` | string | last failure (capped ~4096 bytes) |
| `created_at` / `updated_at` | time | immutable / UpdateDefault |

Edges: `log_cursors` (1→N, **cascade delete**), `system_snapshot` (1→1, cascade delete).

**LogCursor** (`ent/schema/logcursor.go`) — per-(server, file) byte offset for log tailing. `log_path` NotEmpty, `byte_offset` int64 ≥ 0, `last_seen_at` time. **Unique index `(log_path, server_id)`** — one cursor per file per server. Edge `server` (required, cascade). Used by *both* the snapshot collector's log-tail step and the streamer (shared cursor semantics).

**AlertState** (`ent/schema/alertstate.go`) — one row per firing/recently-firing alert instance. `rule_name`, `fingerprint` (16-char hash), `labels` (JSON), `status` enum `firing`\|`resolved`, `value` float, `first_fired_at` (immutable), `last_notified_at`, `updated_at`. **Unique index `(rule_name, fingerprint)`** — the reconciliation upsert key.

**SystemSnapshot** (`ent/schema/systemsnapshot.go`) — 1:1 with server. `captured_at`, `payload` ([]byte raw JSON, schema-on-read by the frontend), `last_error`. Empty payload on upsert preserves the last good snapshot. Edge `server` (required, unique, cascade).

### 6.2 Store behaviors worth knowing

- `CreateServer` / `UpdateServer` surface `ErrDuplicateHostname` (→ HTTP 409); `Get`/`Delete` surface `ErrNotFound` (→ 404).
- `UpdateServer` takes pointer fields: `nil` = leave unchanged, set = overwrite (PATCH semantics).
- `SetServerStatus(id, status, lastErr)` always stamps `last_pinged_at = now`.
- `UpsertLogCursor` auto-stamps `last_seen_at`.
- `DeleteServer` cascades log cursors and the snapshot; **AlertStates are not edge-tied** — they age out via reconciliation when the rule's series disappear.

---

## 7. Snapshot collection pipeline

This is the cron-driven path: once per `scheduler.default_interval_seconds`, every server gets one `PullOnce`.

### 7.1 Orchestration — `internal/collector/pipeline.go`

`Pipeline.PullOnce(ctx, serverID)`:

1. **SSH exec** the collector: `$HOME/.frappe-monitor/frappe-monitor-collect.sh`, with `BENCH_PATHS` injected as a shell-escaped colon-separated env var when the server has explicit bench paths. SSH error → wrap `ssh: %w` → **mark unreachable**, stop.
2. **Tokenize** stdout via `parser.Tokenize`. Parse error → wrap `parse: %w` → **mark unreachable**. A `###ERROR` section (the collector's error trap fired) surfaces `exit_code` + `line` → **unreachable**.
3. **Convert** sections to typed metrics: `ServerFromSections` (required), then per bench `BenchFromSections`, then per site `SiteFromSections`. Per-bench / per-site parse failures are logged and skipped (one bad site doesn't sink the pull). Torn-output defense: if **both** disks and net are empty → fatal (unreachable); if only one is empty → warn and continue.
4. **Build line protocol** — each metric type's `.LineProtocol(serverLabel)` is concatenated into one Influx body.
5. **Push to VictoriaMetrics** (`POST /write`). **Success → mark reachable.** **Push failure → still mark reachable** (the bench answered; the metrics tier is the failure domain), log ERROR, return the push error.

Status is persisted via `SetServerStatus` with the error message capped at 4096 bytes.

### 7.2 Log tailing — `internal/collector/logtail.go` + `pipeline.PullLogsOnce`

For each configured log file, an SSH one-liner runs `stat` then `tail -c +<offset>` capped by `head -c <maxBytes>` (default 1 MiB/cycle), emitting `SIZE:<n>\n<bytes>`. Rotation is detected when `fileSize < prevOffset` → offset reset to 0. All files are batched into **one Loki push per cycle**; cursors advance only on push success (transaction-like — a Loki flake retries the whole batch rather than tracking partial cursors).

### 7.3 Parser — `internal/parser`

`Tokenize(stdout) → Output{[]Section{Name, KVs}}`. Splits on `\n`, recognizes `###Name` markers, accumulates `key=value` lines. Errors on duplicate sections, content before the first section, malformed KV, or a missing `###END`. `splitKV` finds the `=` at brace-depth zero so labeled keys like `disk_used_bytes{mount="/"}=123` parse correctly.

Section grammar:
- `###META` — `version`, `timestamp` (unix sec), `hostname`
- `###SERVER` — CPU jiffies, memory (KB), load, uptime, `disk_*_bytes{mount=...}`, `net_*_bytes{iface=...}`
- `###BENCH:<name>` — `info{frappe_version=...}`, `apps_count`, `redis_queue_depth{queue=...}`, `supervisor_running`/`supervisor_total`
- `###SITE:<bench>:<site>` — `http_status_code`, `http_response_ms`, `is_healthy`
- `###END` (or `###ERROR` … `###END`)

Fixtures live in `internal/parser/testdata/*.txt`.

### 7.4 Metric types & line protocol — `internal/metrics`

`ServerMetrics`, `BenchMetrics`, `SiteMetrics` each have `.LineProtocol(serverLabel)` producing Influx line protocol with **nanosecond** timestamps (all lines in a cycle share one timestamp). Naming convention:

```
frappe_server_<metric>,server=<label>[,mount=…|iface=…] value=<v> <ts_ns>
frappe_bench_<metric>,server=<label>,bench=<bench>[,queue=…|frappe_version=…] value=<v> <ts_ns>
frappe_site_<metric>,server=<label>,bench=<bench>,site=<site> value=<v> <ts_ns>
```

- Memory KB is converted to bytes (×1024) at emit time.
- `frappe_bench_info,…,frappe_version=<v> value=1` carries the version as a tag.
- Tag values are escaped (`\`, `,`, `=`, space).

The `-influxSkipSingleField` VM flag keeps the metric name as `frappe_server_cpu_user` rather than appending `_value`.

`VMClient.Push` (`vmclient.go`) `POST`s `text/plain` to `<vm_url>/write`, expects 2xx (204), drains the body for keep-alive reuse, and includes a capped response snippet in errors.

### 7.5 Loki client — `internal/logs/lokiclient.go`

`Stream{Labels, []Entry{Time, Line}}` → JSON `{"streams":[{"stream":{...},"values":[["<ts_ns>","<line>"],…]}]}` `POST`ed to `<loki_url>/loki/api/v1/push`. Empty input is a no-op. Snapshot-path labels: `server` (required), `log_type` (required), `bench` (optional, omitted for server-wide logs).

### 7.6 The bench-side scripts — `scripts/`

**`frappe-monitor-collect.sh`** (v2.5.0) — no deps beyond bash + coreutils. Self-renices +10 and `ionice`s to best-effort class (`-c 2`) at the lowest priority 7 (`-n 7`) so it never competes with the bench. Reads `/proc/stat`, `/proc/meminfo`, `/proc/loadavg`, `/proc/uptime`, `df -B1`, `/proc/net/dev`; per bench reads the Frappe version, `apps.txt`, `redis-cli llen rq:queue:<short|default|long>` (three fixed queue names, not a glob), `supervisorctl status`; per site probes `curl -H "Host: <site>" http://127.0.0.1:<port>/api/method/ping`. Key behaviors:
- **Bench discovery**: `BENCH_PATHS` env overrides; else globs `/home/*/frappe-bench`, `/home/*/bench-*`, `/opt/bench/*` filtered by presence of `sites/`+`apps/`+`Procfile`.
- **Port caching**: webserver port detected once per bench (from `common_site_config.json`, else 8000, else 80) and reused for all its sites.
- **Site-probe budget**: a wall-clock budget (default 20s) for the whole site phase; once exceeded, remaining sites emit `is_healthy=0` without probing, so SSH never times out mid-output.
- **Loud zeros**: failed redis/supervisor/probe emits `0`, never omits the metric — the dashboard shows "stuck at 0" instead of a gap.
- **Error trap**: `trap 'on_error $LINENO' ERR` emits `###ERROR / exit_code / line / ###END`.

**`frappe-monitor-system.sh`** (v1.1.0 — the top-level `VERSION=`; the inner python emits a separate `schema_version=1.0.0` inside the JSON payload) — one-shot inventory → single JSON object (os/kernel/arch/uptime, CPU model+cores, memory, disks, load, top-10 by CPU and by RSS). Stored raw in `SystemSnapshot.payload`, rendered by `SystemDetailsCard.vue`.

**`frappe-monitor-stream.sh`** (v8.0.0) — covered in §10.

---

## 8. Scheduler & SSH

### 8.1 Scheduler — `internal/scheduler/scheduler.go`

Wraps `robfig/cron/v3`. A buffered-channel **semaphore** of size `maxParallel` caps concurrency; each job runs under a per-job `context.WithTimeout(parentCtx, perJobTimeout)`. `New` clamps `maxParallel`<1→1 and `perJobTimeout`<1s→1s. Entries are **keyed** by server id (`AddKeyed`/`RemoveKeyed`) so the hot create/delete hooks can add and remove servers at runtime without a restart. `Stop(ctx)` cancels the parent context, stops cron, and waits on a `WaitGroup` for in-flight jobs to drain. Job panics are **not** recovered — jobs are expected to return errors, not panic. Missed ticks during pauses are dropped (best-effort, fine for metrics).

### 8.2 SSH — `internal/ssh`

`Executor` interface: `Run`, `RunWithInput`, `Stream`. Sentinel errors `ErrDial`, `ErrAuth`, `ErrTimeout` (crypto/ssh doesn't expose typed errors, so `classifyDialError` string-matches and wraps — callers use `errors.Is`). `Target{Host, Port, User, KeyPath}`; port 0 → 22.

`Pool` (`pool.go`) caches one `*ssh.Client` per `host:port|user|keypath`. A new **session per command**; if `NewSession` fails on a cached client it's dropped and re-dialed once (stale-connection recovery). Concurrent dials to the same target are **not** coalesced — callers may dial in parallel; under lock only the cached *result* is deduped (the redundant client is closed, the stored one reused). `Run`/`RunWithInput` enforce `CommandTimeout` and `ctx`; on either, send `SIGKILL` and return partial output + error. `Stream` (used by the streamer) opens a session, starts the command, **does not** apply `CommandTimeout` (streams run forever), discards stderr so the remote pipe never wedges, and returns a `poolStream` (`io.ReadCloser`) whose `Close` sends `SIGTERM` (lets the remote trap clean up child `tail`s) and is idempotent via `sync.Once`. Auth is public-key only today (`loadKey` → `ssh.PublicKeys`).

`fake.go` is the test double: per-host canned responses, call counters, last-cmd/last-stdin capture, configurable stream readers, and `OpenStreamCount()` so tests can assert leak-freedom.

---

## 9. HTTP API

**Package:** `internal/api`. chi router (`router.go`) wires the route table; the **global** middleware chain (outermost first) is **recoverer → requestLogger**. `authGate` is applied only to the `/api/v1` subrouter (`api.Use(auth)`), so it gates `/api/v1/*` but **not** `/healthz`, `/api/v1/login`, `/api/v1/logout`, or the SPA fallback.

### 9.1 Route table

| Method | Path | Auth | Purpose |
|---|---|---|---|
| GET | `/healthz` | none | liveness |
| POST | `/api/v1/login` | none | validate password, set session cookie (204/401/429/503) |
| POST | `/api/v1/logout` | none | expire cookie (204) |
| GET | `/api/v1/whoami` | gated | session probe (200) |
| POST | `/api/v1/servers` | gated | create server (201/400/409) |
| GET | `/api/v1/servers` | gated | list |
| GET | `/api/v1/servers/{id}` | gated | get (200/404) |
| PATCH | `/api/v1/servers/{id}` | gated | partial update (200/400/404/409) |
| DELETE | `/api/v1/servers/{id}` | gated | delete + cascade (204/404) |
| POST | `/api/v1/servers/{id}/test-connection` | gated | SSH ping → `{reachable, latency_ms, error, error_kind}` |
| POST | `/api/v1/servers/{id}/deploy-collector` | gated | pipe collector.sh over SSH → `{deployed, version}` |
| POST | `/api/v1/servers/{id}/refresh-system` | gated | run system.sh, store snapshot |
| GET | `/api/v1/servers/{id}/system` | gated | last system snapshot |
| GET | `/api/v1/benches` | gated | all server+bench pairs (derived from VM) |
| GET | `/api/v1/benches/{server}/{bench}` | gated | bench detail |
| GET | `/api/v1/sites` | gated | all server+bench+site triples (derived from VM) |
| GET | `/api/v1/sites/{server}/{bench}/{site}` | gated | site detail (+ 1h avg response) |
| GET | `/api/v1/metrics/query` | gated | **proxy** → VM `/api/v1/query_range` |
| GET | `/api/v1/logs/query` | gated | **proxy** → Loki `/loki/api/v1/query_range` |
| GET | `/api/v1/alerts` | gated | configured rules + firing state |
| * | everything else | none | SPA fallback (serves `index.html`) |

Hierarchy endpoints (`/benches`, `/sites`) have **no separate source of truth** — they're derived from VictoriaMetrics label queries at request time, so they can never disagree with the metrics.

Several routes mount **conditionally**: `POST /api/v1/login` only when `auth.password` is set; `/metrics/query` only when `metrics.vm_url` is set; `/logs/query` only when `logs.loki_url` is set; and `/benches*` + `/sites*` only when `metrics.vm_url` is set. When unmounted they fall through to the SPA handler (so an unconfigured dev binary 404s on `/api/...`).

### 9.2 Auth — `internal/api/auth.go`, `login.go`

Phase 7 model:
- **No password configured** → `authGate` is a pass-through (dev / trusted-network mode).
- **Login** (`POST /login`, JSON `{password}`) compares with `subtle.ConstantTimeCompare`, then issues cookie `monitor_session = <exp_unix>.<hex_hmac>` where the HMAC key is `HMAC-SHA256("frappe-monitor.session.v1", password)`. TTL 7 days. Cookie flags `HttpOnly`, `SameSite=Strict`, `Secure` (when TLS detected). Brute-force throttle: 5 failures/min/IP (in-memory, from `X-Forwarded-For`/`RemoteAddr`).
- **authGate** validates the cookie (constant-time HMAC + expiry), else falls back to HTTP basic auth, else returns `401 {"error":"unauthorized"}` with **no `WWW-Authenticate`** header (so browsers don't pop the native basic-auth dialog; the SPA catches 401 and routes to `/login`).
- Changing the password invalidates all sessions (the signing key is password-derived). No server-side session store → restart-safe.

### 9.3 Proxies — `internal/api/proxy.go`

`/metrics/query` and `/logs/query` are deliberately **thin** pass-throughs to VM/Loki — no PromQL/LogQL rewriting, no caching. They exist to avoid CORS/token plumbing in the browser and to centralize TLS/auth/rate-limiting in one place. Upstream failure → 502.

---

## 10. Alerts subsystem (Phase 6)

**Package:** `internal/alerts`. An independent goroutine that turns PromQL into Telegram pages.

- **`Rule`** (`rule.go`): `{Name, Expr, Severity, Message (text/template), FingerprintLabels}`. `Fingerprint(labels)` = 16-char SHA256 over the deterministically-sorted fingerprint labels — the stable identity of one alert instance across restarts.
- **Defaults** (`defaults.go`): four bundled rules — `server_unreachable` (`time()-timestamp(frappe_server_load_1m) > 300`), `disk_almost_full` (>90% by server+mount), `site_unhealthy` (`frappe_site_is_healthy == 0`), `redis_queue_high` (depth > 1000). Merged with operator rules unless `disable_defaults`.
- **`Evaluator.EvaluateOnce`** (`evaluator.go`): for each rule, query VM for the firing series, build `firing[fingerprint]=Sample`, load prior `AlertState` rows for the rule, and reconcile:
  - newly firing → notify + upsert `status=firing`;
  - still firing past `notify_repeat_seconds` → re-page;
  - was firing, now absent → notify "resolved" + delete the state row.
  One rule's query failure is logged and skipped — it never breaks the others. A `Message` template that fails to parse silently falls back to a default formatter, so a typo never silences an alert.
- **`Service`** (`service.go`): `Start` fires an immediate `EvaluateOnce`, then ticks every `evaluation_interval_seconds`; `Stop` waits on a `WaitGroup` but is **not** idempotent — calling it twice panics on the already-closed `stop` channel.
- **VM query** (`vmclient.go`): `GET <vm>/api/v1/query?query=...`, parsing `data.result[].{metric, value[1]}`; empty result is not an error.
- **Telegram** (`telegram.go`): `MultiNotifier` fans out one `Notification` to N chat IDs (joined errors — one dead chat doesn't suppress the rest). Each `TelegramClient.Notify` `POST`s `sendMessage` with `parse_mode=HTML`. `formatMessage` builds a severity-iconed HTML message (`🚨` critical / `⚠️` warning / `ℹ️` info / `✅` resolved / `🔔` default) with a `server → bench → site` breadcrumb and an event-time footer (`fired …` on a fire, `cleared …` on a resolve).

---

## 11. Streamer subsystem (Phase 8) — real-time log ingest

**Package:** `internal/streamer`. *Not* covered by the operator architecture doc — documented here in full. Where the collector takes periodic snapshots, the streamer keeps **one long-lived SSH session per server** following logs with `tail -F` and ships lines to Loki continuously, with byte-offset resume across reconnects and restarts.

### 11.1 Wire protocol — `protocol.go`

Line-oriented, UTF-8, LF-terminated, three event types:

```
##V=<semver>                    version handshake, first line          → EventVersion
##F=<file_id>|<raw line>        one log line (split on FIRST '|' only)  → EventLine
##F=<file_id> ##E=<token>       per-file error sentinel                 → EventFileError
```

`file_id` matches `[A-Za-z0-9_]+`. Error tokens: `MISSING`, `PERMISSION`, `TAIL_EXITED`. `ParseLine(line) Event` is stateless and allocation-light; unrecognized prefixes become `EventUnknown` — skipped (forward-compatible), logged at debug on every occurrence once the version line has been seen, and dropped silently before it.

### 11.2 Session — `session.go`

`Session` runs an infinite reconnect loop (`loop` → `runOnce`) with exponential backoff (`min_backoff` 1s … `max_backoff` 60s). `runOnce` calls `exec.Stream(ctx, target, RemoteCommand)`, signals `Handler.OnSessionState(true)`, then `bufio.Scanner`s the stream (buffer bounded by an internal hardcoded 1 MiB `MaxLineBytes` default — not a config key), dispatching each parsed event to the `Handler`. Clean EOF (remote hung up) returns `nil` → reconnect; network error returns the error → backoff. `Stop(timeout)` signals and waits.

`BuildRemoteCommand(scriptPath, files, resume)` assembles `bash -lc '<script> --files=<id>:<path>,… --resume=<id>:<off>,…'`, **rejecting** any file id or path (and the script path) containing a single quote — ids additionally reject `,=:|` and newlines — then wrapping the inner command in single quotes. Resume offsets are `int64` and need no quote check.

### 11.3 Handler & sink — `handler.go`, `sink.go`

`Handler` is the callback surface: `OnVersion`, `OnLine(serverID, fileID, content, observedAt, byteLen)`, `OnFileError`, `OnSessionState(connected, reason)`, `Flush`. The production implementation is **`LokiSink`**:

- Buffers entries **per `(serverID, fileID)`** and accumulates `byteLen` (source bytes consumed) per stream. `OnLine` flushes synchronously if a stream hits `max_batch_lines`.
- **`Flush`** (called on a `flush_interval_seconds` ticker and on shutdown) snapshots non-empty streams under lock, pushes to Loki with a bounded timeout, and **only on success** drops exactly the flushed prefix and advances the `LogCursor` for each `(server, file)` by the consumed byte delta. **On Loki failure it preserves the buffered lines** — a Loki outage grows monitor RAM rather than silently dropping logs.
- Emits health/lag gauges to VM: `frappe_stream_connected{server=id-N}` (on connect/disconnect transitions) and `frappe_stream_lag_seconds{server=id-N}` (per flush, `now - lastSeen`).
- Loki labels (current phase): `{server: "id-<N>", log_type: <fileID>, monitor: <monitor_id>}`. `id-<N>` is used (not hostname) so a rename doesn't fork the stream.

### 11.4 Manager — `manager.go`

Owns one `Session` per server plus the single shared `LokiSink`. `NewManager` returns `nil` when streaming is disabled (no-op everywhere). `Start` enumerates servers and `launchOne` each (continuing past individual failures), then runs the flush ticker. `LaunchServer`/`RemoveServer` are the idempotent hot hooks called from `onServerCreated`/`onServerDeleted`. `launchOne` reads each file's `LogCursor` to compute resume offsets, builds the remote command, and starts the session goroutine. `Stop(timeout)` stops every session and runs a final flush. (The flush ticker/loop is **not** stopped by `Stop` — it exits only when the manager's `ctx` is canceled.)

### 11.5 Bench-side — `scripts/frappe-monitor-stream.sh` (v8.0.0)

Renices/ionices itself, emits `##V=8.0.0`, then spawns one backgrounded `tail -F` per file. With a resume offset it uses `tail -c +<offset+1>` (tail is 1-based); without, `tail -n 0` (only new lines). Each line is emitted with a single `printf '##F=%s|%s\n'` — one `write(2)` ≤ PIPE_BUF (4096 on Linux) is atomic, so concurrent file tailers interleave at line boundaries, never mid-line. Missing/unreadable files emit `MISSING`/`PERMISSION`; a dying `tail` emits `TAIL_EXITED`. An EXIT/TERM/INT trap reaps all child tails.

---

## 12. Frontend (Vue 3 SPA)

**Source:** `web/`. Vue 3 + Vite 6 + TypeScript + vue-router 4 + ECharts 5.5 + lucide-vue-next. Builds to `internal/web/dist/` (gitignored) for embedding.

### 12.1 Routes & views

| Path | View | Content |
|---|---|---|
| `/` → `/servers` | — | redirect |
| `/servers` | `Servers.vue` | status-sorted server cards, add/edit/delete forms |
| `/servers/:id` | `ServerDetail.vue` | header + actions (test SSH, deploy collector, edit, delete), `SystemDetailsCard`, CPU%/mem%/disk%/load charts |
| `/benches` | `Benches.vue` | benches grouped by server |
| `/benches/:server/:bench` | `BenchDetail.vue` | version/apps/supervisor summary, redis queue table, charts |
| `/sites` | `Sites.vue` | searchable/filterable table, responsive cards on mobile |
| `/sites/:server/:bench/:site` | `SiteDetail.vue` | status/response/health summary, charts, bench-scoped Loki logs |
| `/alerts` | `Alerts.vue` | firing alerts + configured-rules table |
| `/login` | `Login.vue` | password form (`meta.layout='bare'`); `whoami()` on mount, redirects to `?next=` |

### 12.2 API client & state — `web/src/api.ts`, composables

`api.ts` (same-origin, `credentials: 'same-origin'`) wraps every endpoint. The shared GET wrapper (`jsonGET`) has a 401 handler that redirects to `/login?next=<path>`; the mutating POST/PATCH/DELETE wrappers don't route through it. Composables: **`useTimeRange`** reads/writes the timeline from URL query (`?from&to&step&refresh`), with presets (15m…30d) and an auto-step heuristic; **`useMetricsRange`** polls a PromQL query over a range and re-queries on change/interval; **`useServers`** polls the server list. **All timeline state lives in the URL** — it survives navigation and is shareable. `MetricChart.vue` renders ECharts (canvas, dark-mode aware via `matchMedia`). `TimelineFilter.vue` is the shared preset/refresh control.

### 12.3 Build & embedding

`web/vite.config.ts` sets `outDir: ../internal/web/dist`; the dev server (:5173) proxies `/api/*` + `/healthz` to `:8080`. `make build` runs `web-install` + `web-build` then `go build -tags=embed_dist`. `internal/web/embed.go` (`//go:build embed_dist`) embeds `all:dist` and serves it with `index.html` fallback for client-side routes; `embed_placeholder.go` (`//go:build !embed_dist`) returns a "frontend not built" page so a plain `go build` still compiles. `scripts/embed.go` embeds the three bash scripts as strings and exposes `CollectorVersion()` and `StreamerVersion()` by scanning the script's `VERSION="…"` line (there is no `SystemVersion()` helper) — that's how deploy endpoints know which version they ship and the streamer detects drift.

---

## 13. Storage backends (VictoriaMetrics + Loki)

Run as a docker-compose stack (`frappe-monitor-stack.service`), loopback-only (127.0.0.1), fronted by the binary's proxies (and optionally Caddy for TLS).

- **VictoriaMetrics** `v1.142.0` on `:8428`. Push `/write` (Influx line proto), query `/api/v1/query_range`. Load-bearing flag `-influxSkipSingleField`. Dev retention 30d; prod 90d + `-memory.allowedPercent=40`, `mem_limit 512m`, `cpus 1.0`.
- **Loki** `3.4.1` on `:3100`, bundled example config. Push `/loki/api/v1/push`, query `/loki/api/v1/query_range`. Prod `mem_limit 256m`, `cpus 0.5`.

---

## 14. Deployment & operations

`deploy/install.sh` (idempotent; re-run = upgrade in place): checks prereqs (Go ≥1.25, Node ≥20, Docker ≥24 w/ compose; augments PATH for sudo+asdf/nvm), prompts/accepts password + optional Telegram, builds via `make build` **as the invoking user** (avoids root-owned node_modules), creates the `frappe-monitor` system user and `/var/lib` (0750), `/etc` (0750), `/opt` dirs, atomically swaps the binary into `/usr/local/bin/frappe-monitor`, renders `/etc/frappe-monitor/monitor.yaml` (0640 root:frappe-monitor), installs both systemd units, brings up the stack (waits for VM/Loki health), and starts the binary. `--uninstall` / `--purge` tear down.

Systemd: `frappe-monitor-stack.service` (oneshot, `docker compose up -d`) → `frappe-monitor.service` (`Requires`+`After` the stack; `Restart=on-failure`, `RestartSec=5s`, `TimeoutStopSec=15s`; hardened — `NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`, `MemoryDenyWriteExecute`, etc.; capped — `MemoryMax=512M`, `CPUQuota=200%`, `TasksMax=512`; logs as slog JSON to journald).

`deploy/local-test.sh` is the hermetic laptop demo (temp config + SQLite, dev stack, seeded demo data, foreground, Ctrl+C teardown) — `make local-test`.

Makefile highlights: `build`, `build-no-web`, `web-build`, `run`, `test` (`go test ./... -race -count=1`), `generate` (ent), `vm-up`/`vm-down`/`vm-logs`/`loki-logs`, `install`/`uninstall`/`local-test`.

---

## 15. Testing

Unit tests sit next to their packages (`*_test.go`) and lean on `ssh.FakeExecutor`, `ent/enttest` (in-memory SQLite), and parser fixtures in `testdata/`. `make test` runs the race detector. `scripts/smoke.sh` is the canonical end-to-end "did I break anything": build + bring up VM/Loki + exercise every route; success prints `==> ALL CHECKS PASSED`.

---

## 16. Failure domains (summary)

| Failure | Effect |
|---|---|
| One bench SSH down | that server's card → red (`unreachable`); others unaffected |
| Parse / torn output | server → `unreachable` with the parse error |
| VM push fails | server **stays** `reachable`; error logged; next cycle retries; charts show gap |
| Loki push fails (snapshot) | cursors **not** advanced → whole batch retried next cycle |
| Loki push fails (streamer) | lines **retained** in RAM and retried; monitor RAM grows |
| Monitor crash | systemd restarts in 5s; SQLite WAL is durable |
| Disk full on host | VM/Loki stop ingesting; binary runs but pushes fail |

---

## 17. Extension points — where new features plug in

A map for the feature work that follows this document:

- **New collected metric** → add emission in `scripts/frappe-monitor-collect.sh` (bump its `VERSION`), parse it in `internal/parser`, add the field + `LineProtocol` line in `internal/metrics`, surface it in a `web/` chart. Redeploy via the `deploy-collector` endpoint (version drift is visible).
- **New API endpoint** → add the route in `internal/api/router.go`, a handler, and (if it touches state) a `storage.Store` method + ent schema field (`make generate`).
- **New entity / column** → edit `ent/schema/*.go`, run `make generate`, extend the `Store` interface + `entStore`.
- **New alert rule** → ship via config `alerts.rules[]` (no code), or add to `internal/alerts/defaults.go` for a built-in. New severities/format → `telegram.go`.
- **New streamed file** → add to `streaming.files[]` config; for richer Loki labels (bench/site), parse content in `LokiSink` (`sink.go`).
- **New dashboard view** → add a route in `web/src/router/index.ts`, a view in `web/src/views/`, and client calls in `web/src/api.ts`; reuse `useTimeRange`/`MetricChart`/`TimelineFilter`.
- **New notification channel** (Slack/email/etc.) → implement the `Notifier` interface in `internal/alerts` and add it to the `MultiNotifier` fan-out.

Backlog candidates (`docs/guide/architecture.md` + `docs/hardening-backlog.md`): per-server schedule overrides, site-scoped log labels, deploy markers, multi-tenant scoping, p95/quantile on site detail, SSH password auth, and enforcing `ssh.max_connections_per_host` in the pool.

---

## 18. Glossary

- **Bench** — a Frappe "bench" directory (apps + sites + Procfile); one server hosts many.
- **Site** — a Frappe site within a bench (one database/domain).
- **Collector** — the snapshot bash script + the Go pipeline that runs it on a cron tick.
- **Streamer** — the real-time `tail -F` path (one long-lived SSH session per server).
- **Cursor** — a per-(server,file) byte offset (`LogCursor`) so logs resume without dup/loss.
- **Fingerprint** — stable hash identifying one alert instance across cycles/restarts.
- **Loud zero** — emitting `0` for a failed sub-probe instead of omitting the metric.
- **Reachable / unreachable** — server status driven *only* by SSH+parse success, never by VM/Loki.
