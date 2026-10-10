# Architecture

Frappe Monitor is a single control-plane process for many SSH-reachable Frappe servers. It keeps only registry and reconciliation state in SQLite; VictoriaMetrics and Loki remain the data stores.

## Components

```text
browser ──HTTP──▶ frappe-monitor (Go + embedded Vue SPA)
                    │
                    ├── SQLite: registry, snapshots, cursors, incident history
                    ├── VictoriaMetrics: metric writes + PromQL reads
                    ├── Loki: live-log writes + LogQL reads
                    └── outbound SSH
                         ├── embedded collector over stdin every 60s by default
                         └── optional embedded log stream over stdin per server
```

The monitor opens no inbound connection on a bench server beyond its normal SSH daemon. VictoriaMetrics and Loki can run beside the binary through the bundled Docker Compose stack or on a separate internal host.

## Monitor process

- **HTTP/API** — chi routes under `/api/v1`, `/healthz`, and the embedded SPA. JSON bodies are bounded and reject unknown fields. Query proxies apply backend timeouts.
- **Authentication** — an optional single shared password. The SPA exchanges it for an HMAC-signed, HttpOnly session cookie; API automation may use HTTP basic credentials. Login attempts are bounded per client IP, and forwarded headers are trusted only from configured proxy CIDRs.
- **Storage** — ent over pure-Go SQLite in WAL mode. Server names are unique because they are canonical telemetry labels. Server deletion cascades cursors and system snapshots.
- **SSH pool** — cancellable TCP and SSH handshakes, parsed-key caching, strict `known_hosts` verification, connection reuse, and a per-host session semaphore. Streaming requires at least two sessions per host so normal probes cannot be starved.
- **Scheduler** — one keyed job per server, capped globally by `scheduler.max_parallel` and individually by `per_job_timeout_seconds`. Jobs run immediately at startup and registration, then at the configured cadence. An overlapping run for the same server is skipped.
- **Collector pipeline** — supplies the embedded metric collector to `bash -s` over SSH stdin, parses one complete framed result, pushes server/bench/site/service metrics and opaque site database identities to VictoriaMetrics, and updates reachability in SQLite. It then runs optional monitor-side public probes as a separate non-fatal phase.
- **Streamer** — optional. It discovers standard bench and site logs when no explicit list is configured. One reconnecting SSH stream per selected server runs the embedded streamer over stdin and reports file identity plus absolute byte offsets. Loki batches and cursor commits are serialized so reconnects retry rather than skip data.
- **Alert evaluator** — queries VictoriaMetrics, reconciles active state in SQLite, retains every incident episode, and sends Telegram fire/repeat/recovery messages. Site companion metrics are joined using a constant number of bulk queries, independent of the number of failed sites.

## Read-only target contract

The monitor never installs an agent and exposes no deploy, pull, update, or
restart operation for a target. Its embedded shell programs are supplied to
`bash -s` over SSH stdin. They read procfs, bench configuration, Supervisor,
Redis/RQ registries, Frappe metadata, site health endpoints, and existing log
files. They do not write a target file, change a service, or alter Frappe.

The metric collector emits framed `###META`, `###SERVER`, `###BENCH`, `###SITE`,
and `###END` sections. Bench data includes configured web/background-worker
capacity, Supervisor groups, individual process CPU/RSS/uptime, RQ
registered/busy/idle counts, and Redis queue depth. Site probes run with bounded
concurrency and a total safety budget; probes that cannot run are unknown
(`is_healthy=-1`), not falsely unhealthy.

The streamer accepts file IDs, paths, and resume positions. It starts new
streams at EOF, resumes known identities at their committed byte offset,
detects truncation/rotation, and reports appended lines with exclusive offsets.

## Metrics pull flow

```text
scheduler tick / immediate run
  → acquire global slot and per-server overlap guard
  → SSH exec `bash -s` with the embedded collector on stdin
  → validate complete framed output
  → parse server + every valid bench/site section
  → push one Influx-line-protocol body to VictoriaMetrics
  → persist reachable/unreachable status
  → optionally probe configured public site URLs and push public metrics
```

SSH or top-level parse failure marks the server unreachable and emits `frappe_server_reachable=0` when possible. A VictoriaMetrics failure does not misclassify the remote host: the job reports a push error while the registry remains reachable. Invalid data for one bench/site is logged and skipped without discarding the rest of a complete pull.

The metric-name contract requires VictoriaMetrics to run with
`-influxSkipSingleField`. Without it, single-field Influx writes are stored
with a `_value` suffix and the monitor's PromQL selectors cannot find them.
The bundled Compose stack sets the flag, and the smoke test verifies it.

## Live-log flow

```text
SSH stream → version/offset/line protocol → bounded per-(server,file) buffer
           → raw Loki lines + safe derived event summaries
           → derived VM event samples → SQLite absolute cursor commit → drain
```

Each discovered or configured file supplies canonical `server`, stable
`server_id`, `log_type`, and optional `bench`/`site` Loki labels. Discovery is
itself an embedded read-only SSH command and searches standard bench-level and
site-level log directories. Explicit entries can override discovery for
non-standard layouts.

A Loki, VictoriaMetrics event-write, or cursor-write failure retains the batch and does not advance its durable cursor. `max_batch_lines` is a hard per-stream bound; `max_buffered_lines` and `max_buffered_bytes` bound the entire sink across all files. Once any bound is reached, the SSH reader applies TCP backpressure until persistence recovers. The original remote file is therefore the durable backlog. This protects monitor memory without silently dropping lines; a simultaneous backend outage, monitor crash, and remote log rotation can still lose content, so backend availability and remote retention remain operational requirements.

Native Frappe monitor JSON is stored raw and projected into bounded per-site
request/job samples. Slow database logs are stored raw and parsed into duration,
lock, row-cost, engine, and operation samples. Site configuration database/user
identities are hashed on the target; the same one-way hash from a slow record
provides the join. A safe Loki summary with that opaque hash supports a readable
site-scoped slow-query feed. SQL, database/user names, request UUIDs, client IPs,
and exception messages never become metric labels.

## Site health and alerts

The SSH collector records internal site health, HTTP status, response latency,
and a failure reason code through localhost plus the site's Host header. The
optional public prober separately covers DNS, TLS, reverse-proxy routing, and
Frappe's ping response. Public metrics take precedence for site alerts and the
fleet summary when available; both signals remain visible on site detail.
Private and loopback public destinations are blocked unless explicitly allowed.
A recovery for any server-scoped rule requires a fresh positive reachability
metric and, when available, a reachable registry state. Unknown/skipped site
probes and failed evidence queries also defer recovery. Built-in rules cover:

- unreachable servers;
- disks above the configured expression threshold;
- unhealthy sites with HTTP/reason/latency detail;
- high Redis queue depth;
- all registered RQ workers occupied;
- stopped or failed bench services;
- disconnected live-log streams;
- sustained per-site request/job latency and HTTP 5xx rate;
- site-correlated database queries exceeding the bundled duration threshold.

Alert fingerprints use stable identifying labels. Notification cooldown advances only after delivery succeeds. Every fire-to-recovery episode is stored in the incident-history table, so recovery no longer erases diagnostic history. With multiple Telegram chats, successful destinations are remembered in process so a retry targets only failed chats. Alert samples whose `server` is no longer in the registry are ignored and prior active state is silently pruned, preventing a server deletion from looking like a recovery.

## Query and dashboard path

The browser calls the monitor rather than VictoriaMetrics or Loki directly. Metric/LogQL expressions are escaped at construction, query requests are time-bounded, and stale frontend responses cannot overwrite a newer route/range selection. Completion-based polling coalesces manual refreshes and avoids overlapping request storms.

Hierarchy lists are derived from recent VictoriaMetrics series rather than duplicated in SQLite. The lookback is tied to the collection cadence and timeout so normal between-pull gaps do not make benches/sites disappear.

## Storage ownership

| Data | Owner | Reason |
|---|---|---|
| Server registry and last status | SQLite | Small, transactional control state |
| Latest system snapshot | SQLite | One operator-facing document per server |
| Stream file identity and byte offset | SQLite | Durable replay boundary |
| Active alert fingerprints/cooldown | SQLite | Restart-safe notification reconciliation |
| Incident episodes and recovery times | SQLite | Durable operational history |
| Time-series metrics | VictoriaMetrics | PromQL and retention |
| Log lines | Loki | Label-indexed LogQL and retention |
| Bench/site list | Derived from VictoriaMetrics | Avoids a second inventory source of truth |

## Failure isolation

| Failure | Behavior |
|---|---|
| One SSH server is down | Its pull/stream fails and alerts; other servers continue. |
| A pull exceeds its timeout | Its context is canceled; later cadence retries it. |
| VictoriaMetrics is down | Metric writes/charts fail; SSH reachability remains distinct. |
| Loki is down | Live readers backpressure at the memory bound; metrics continue. |
| Telegram is down | Firing/recovery state remains retryable; cooldown is not advanced. |
| Monitor restarts | systemd restarts it; scheduler runs immediately and streams resume from cursor identity/offset. |
| A server is edited/deleted | Stream session is restarted/removed and scheduler registration changes without a process restart. |

## Current scope limits

- Authentication is single-tenant; there are no per-user roles or per-server visibility rules.
- Collection cadence is global, not per server.
- Alert rules and Telegram routes are configuration-driven and editable through the authenticated Settings dashboard; they require a process restart after save.
- There is no deploy-event annotation timeline.
- Stock Gunicorn exposes configured and running worker capacity but not a reliable per-request busy/free state. RQ workers do expose busy/idle state through Redis and are reported exactly.
