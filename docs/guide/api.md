# API Reference

All HTTP endpoints exposed by the monitor binary. Mounted on the address from `server.listen_addr` (default `:8080`). Curl examples assume `http://127.0.0.1:8080`; replace with your host.

## Auth

If `auth.password` is set, protected `/api/v1/*` routes accept either the SPA's signed session cookie or HTTP basic auth. The SPA shell, login/logout endpoints, and `/healthz` remain reachable so the login page and external liveness probes work. The basic-auth username is ignored.

```bash
# With auth enabled:
curl -u "admin:<password>" http://127.0.0.1:8080/api/v1/servers

# Cookie flow used by the SPA:
curl -c /tmp/monitor.cookies -X POST http://127.0.0.1:8080/api/v1/login \
  -H 'Content-Type: application/json' -d '{"password":"<password>"}'
curl -b /tmp/monitor.cookies http://127.0.0.1:8080/api/v1/servers
```

The session cookie is HttpOnly, SameSite=Strict, valid for seven days, and marked Secure when TLS is direct or reported by a trusted proxy. Changing `auth.password` invalidates existing sessions. Failed logins are rate-limited per verified client IP. `GET /api/v1/whoami` is a public state-only endpoint that always returns 200; it reports whether auth is enabled and whether the request has a valid session without exposing credentials.

When `auth.password` is empty, the API is open. Restrict at the network layer if exposed (see [`deployment.md`](deployment.md#optional-tls-via-caddy)).

## Health

### `GET /healthz`

Cheap liveness check. Always 200 if the HTTP server is up.

```bash
curl -fsS http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

## Servers

### `POST /api/v1/servers/test-connection` — probe an unsaved target

Used by the onboarding form. The body contains only `hostname`, `ssh_user`,
`ssh_port`, and `ssh_key_path` in the same format as server creation. It runs
`echo ok` over SSH and never creates a database row. A completed probe returns
200 with the same `reachable`, `latency_ms`, `error`, and `error_kind` shape as
the saved-server diagnostic below. Validation errors return 400.

### `POST /api/v1/servers` — register a server

Body:

```json
{
  "name": "prod1",
  "hostname": "bench1.example.com",
  "ssh_user": "frappe",
  "ssh_port": 22,
  "ssh_key_path": "/var/lib/frappe-monitor/.ssh/id_ed25519",
  "bench_paths": ["/home/frappe/frappe-bench"],
  "labels": { "env": "prod" }
}
```

| Field | Required | Notes |
|---|---|---|
| `name` | yes | Free-form display label, must be unique. |
| `hostname` | yes | Resolvable from the monitor host. Unique. |
| `ssh_user` | yes | The SSH login user. |
| `ssh_port` | yes | Usually 22. |
| `ssh_key_path` | yes | Absolute path on the *monitor* host, readable by `frappe-monitor`. |
| `bench_paths` | no | Up to 64 absolute bench roots. Empty enables standard-path auto-discovery. |
| `labels` | no | Free-form `string→string` map. Stored but not yet surfaced in the UI. |

Responses:

| Status | Meaning |
|---|---|
| 201 | Created. Body is the full server record (with `id`, `status: "unknown"`). |
| 400 | Bad JSON or missing required field. |
| 409 | Duplicate `name` or `hostname`. |

### `GET /api/v1/servers` — list

```bash
curl -fsS http://127.0.0.1:8080/api/v1/servers | jq .
```

Returns an array of server records. Empty array if none registered yet.

### `GET /api/v1/servers/{id}` — fetch one

```bash
curl -fsS http://127.0.0.1:8080/api/v1/servers/1
```

| Status | Meaning |
|---|---|
| 200 | Found. |
| 404 | Unknown id. |

### `PATCH /api/v1/servers/{id}` — partial update

Body is JSON with any subset of these fields. Absent (or `null`) fields are left unchanged.

```json
{
  "name": "renamed",
  "hostname": "new-host.example.com",
  "ssh_user": "frappe",
  "ssh_port": 2222,
  "ssh_key_path": "/var/lib/frappe-monitor/.ssh/id_ed25519",
  "bench_paths": ["/srv/frappe-bench"],
  "labels": { "env": "staging" }
}
```

| Status | Meaning |
|---|---|
| 200 | Updated. Body is the full server record. |
| 400 | Bad/unknown JSON field or invalid value (`ssh_port` must be 1–65535). |
| 404 | Unknown id. |
| 409 | Hostname rename collides with another server. |

### `DELETE /api/v1/servers/{id}` — remove

```bash
curl -X DELETE http://127.0.0.1:8080/api/v1/servers/1
```

| Status | Meaning |
|---|---|
| 204 | Deleted. Cursor/snapshot rows cascade; scheduler/stream sessions stop. Alert state is silently pruned rather than reported as a recovery. |
| 404 | Unknown id. |

Existing metrics + logs in VictoriaMetrics / Loki age out on their retention. The dashboard stops listing the server immediately.

### `POST /api/v1/servers/{id}/test-connection` — diagnostic SSH probe

Always 200 (or 404 if id unknown). Body shape:

```json
{
  "reachable": false,
  "latency_ms": 0,
  "error": "ssh: handshake failed: ssh: unable to authenticate",
  "error_kind": "auth"
}
```

`error_kind` is one of `auth`, `dial`, `timeout`, `host_key`, `unknown`. `host_key` means the target is absent from `ssh.known_hosts_path` or its key changed. Side effect: persists the result to the server status row.

### `GET /api/v1/servers/{id}/system` — most recent inventory snapshot

```json
{
  "captured_at": "2026-05-02T08:11:13Z",
  "payload": { "system": { … }, "cpu": { … }, "memory": { … }, "disks": [ … ], "top_cpu": [ … ] },
  "last_error": ""
}
```

`payload` and `last_error` are independent. A successful refresh sets payload + clears last_error. A *failed* refresh persists last_error but **preserves the previously-captured payload** — so a transient SSH blip never leaves the dashboard with a blank "System details" card. The card shows both: prior data + a "Last capture failed: …" banner until the next successful Refresh clears it.

`404` if no snapshot has ever been captured for this server.

### `POST /api/v1/servers/{id}/refresh-system` — capture inventory now

Same response shape as `GET …/system` on success. SSH or JSON-parse failure returns `502` / `504` / `500` with an error body **and** persists `last_error` (without nuking the prior payload).

## Hierarchy (VM-derived)

These four endpoints derive bench / site lists from VictoriaMetrics queries — no SQL tables involved. They only mount when `metrics.vm_url` is configured.

### `GET /api/v1/benches` — list `(server, bench)` pairs

```bash
curl -fsS http://127.0.0.1:8080/api/v1/benches
# [{"server":"prod1","bench":"bench1"}, ...]
```

Backed by `count by (server, bench) (frappe_bench_apps_count)`.

### `GET /api/v1/benches/{server}/{bench}` — bench detail

```json
{
  "server": "prod1",
  "bench": "bench1",
  "frappe_version": "v15.42.1",
  "apps_count": 7,
  "web_workers_configured": 17,
  "background_workers_configured": 6,
  "supervisor_running": 4,
  "supervisor_total": 4,
  "redis_queues": { "default": 12, "long": 0 },
  "workers_registered": 6,
  "workers_busy": 5,
  "workers_idle": 1,
  "services": [
    {
      "name": "bench1-worker-default",
      "kind": "worker",
      "queue": "default",
      "state": 1,
      "pid": 4321,
      "cpu_percent": 81.2,
      "memory_percent": 1.7,
      "rss_bytes": 268435456,
      "uptime_seconds": 7200
    }
  ]
}
```

Returns 200 even when some fields have no data (zeros / empty map). 502 if VM is unreachable.

### `GET /api/v1/sites` — list `(server, bench, site)` triples

Uses public health when configured and otherwise falls back to the internal SSH
probe: `max by (...) (frappe_site_public_is_healthy) or max by (...)
(frappe_site_is_healthy)`.

### `GET /api/v1/sites/{server}/{bench}/{site}` — site detail

```json
{
  "server": "prod1",
  "bench": "bench1",
  "site": "alpha.example.com",
  "http_status_code": 200,
  "http_response_ms": 42.5,
  "is_healthy": 1,
  "failure_reason_code": 0,
  "internal_http_status_code": 200,
  "internal_http_response_ms": 18.4,
  "internal_is_healthy": 1,
  "internal_failure_reason_code": 0,
  "public_probe_available": true,
  "public_http_status_code": 200,
  "public_http_response_ms": 42.5,
  "public_is_healthy": 1,
  "public_failure_reason_code": 0,
  "public_tls_cert_expiry_timestamp_seconds": 1794277803,
  "native_monitor_enabled": 1,
  "database_identities": [
    {"hash": "0123456789abcdef01234567", "kind": "database"},
    {"hash": "89abcdef0123456789abcdef", "kind": "user"}
  ],
  "avg_response_ms_1h": 38.2
}
```

The top-level HTTP/health fields are the preferred operator view: public when
available, otherwise internal. The prefixed fields always retain both signals
for diagnosis. `avg_response_ms_1h` follows the same public-first fallback.
`native_monitor_enabled` reports the read-only Frappe telemetry prerequisite.
`database_identities` contains opaque, target-side SHA-256-derived equality
keys used by the authenticated UI to select site-correlated slow-query event
summaries; it never contains a database/user name or credential.

## Query proxies

These forward the request, with the same query string, to the configured backend. Query params follow the upstream's contract verbatim — see VictoriaMetrics / Loki docs for the full grammar.

### `GET /api/v1/metrics/query` — proxies to VM `/api/v1/query_range`

| Param | Meaning |
|---|---|
| `query` | PromQL expression (URL-encoded). Required; 400 if missing. |
| `start` | Unix seconds. |
| `end` | Unix seconds. |
| `step` | Resolution in seconds. |

```bash
curl -fsS "http://127.0.0.1:8080/api/v1/metrics/query?query=frappe_server_load_1m&start=1714468800&end=1714472400&step=15"
```

Status codes: 200 (with VM body, status mirrors upstream), 400 (missing `query`), 502 (VM unreachable).

### `GET /api/v1/logs/query` — proxies to Loki `/loki/api/v1/query_range`

| Param | Meaning |
|---|---|
| `query` | LogQL expression (URL-encoded). Required; 400 if missing. |
| `start` | Unix nanoseconds. |
| `end` | Unix nanoseconds. |
| `limit` | Max entries to return. |
| `direction` | `backward` (newest first) or `forward`. |

```bash
curl -fsS 'http://127.0.0.1:8080/api/v1/logs/query?query=%7Bserver%3D%22prod1%22%2Clog_type%3D%22error%22%7D&start=1714468800000000000&end=1714472400000000000&limit=100&direction=backward'
```

`limit` must be between 1 and 1000.

## Monitor settings

These endpoints modify only the monitor's local bootstrap YAML. They never run
a mutation on a monitored target. They are protected by the same authentication
gate as the rest of `/api/v1`.

### `GET /api/v1/status`

Returns a credential-free runtime snapshot without depending on VictoriaMetrics
or Loki: process start time and uptime, Go heap allocation/reservation,
goroutine count, log-stream session/connection counts, and exact buffered
line/byte usage against the configured global caps. It never includes log
content, paths, labels, or backend credentials.

### `GET /api/v1/settings`

Returns the complete UI-manageable configuration: `server`, `database`, `ssh`,
`log`, `metrics`, `logs`, `scheduler`, `auth`, `alerts`, and `streaming`.
Passwords and Telegram bot tokens are never returned; the response contains
`password_configured` and `bot_token_configured` booleans instead. Empty lists
are encoded as `[]`, not `null`. `restart_required` reports whether this
process has saved settings that its boot-time services have not yet loaded.

### `PUT /api/v1/settings`

Replaces the UI-manageable configuration with the same non-secret sections
returned by GET. Secret fields are optional:

- omit `auth.password` or `alerts.telegram.bot_token` to retain it;
- send a non-empty value to replace it;
- send an empty string to clear it.

The complete candidate is validated before writing. Success atomically replaces
the configured YAML, creates a sibling `.bak`, retains unknown YAML keys, and
returns the redacted settings response with `restart_required: true`. A 400
names an invalid key or rule and leaves the file unchanged.

### `POST /api/v1/settings/test`

Body is `{ "kind": "metrics" }`, `{ "kind": "logs" }`, or
`{ "kind": "telegram", "bot_token": "optional unsaved token" }`. Metrics
checks `/health`, logs checks `/ready`, and Telegram authenticates the bot via
`getMe`. A completed diagnostic returns 200 with:

```json
{ "ok": true, "latency_ms": 4, "message": "Connection healthy" }
```

The Telegram token is never echoed in the response or application logs.

## Alerts and incident history

### `GET /api/v1/alerts`

Returns alert enablement, configured rules, current firing reconciliation state,
and up to 250 newest durable incident episodes:

```json
{
  "enabled": true,
  "rules": [{ "name": "rq_workers_saturated", "severity": "critical", "expr": "..." }],
  "firing": [{ "rule_name": "rq_workers_saturated", "status": "firing", "labels": { "server": "prod1", "bench": "bench1" } }],
  "incidents": [{
    "id": 41,
    "rule_name": "rq_workers_saturated",
    "severity": "critical",
    "status": "resolved",
    "value": 17,
    "started_at": "2026-08-13T08:00:00Z",
    "resolved_at": "2026-08-13T08:12:00Z",
    "labels": { "server": "prod1", "bench": "bench1" }
  }]
}
```

This endpoint is read-only. Rules and Telegram routes come from monitor-local
configuration; target servers are never changed.

## Error model

All non-2xx responses follow:

```json
{ "error": "human-readable description" }
```

Most errors use the HTTP status plus `error` text. The test-connection endpoint intentionally returns 200 with `reachable:false` for a completed negative probe and includes its `error_kind` enum.

## Static files

Anything not under `/api/` or `/healthz` is served by the embedded SPA:

| Path | Behavior |
|---|---|
| `/` | Fleet overview. |
| `/assets/*` | Vite-built JS / CSS bundles. |
| Anything else | Returns `index.html` (so vue-router takes over). |
