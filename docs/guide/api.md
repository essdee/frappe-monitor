# API Reference

All HTTP endpoints exposed by the monitor binary. Mounted on the address from `server.listen_addr` (default `:8080`). Curl examples assume `http://127.0.0.1:8080`; replace with your host.

## Auth

If `auth.password` is set in the config, every `/api/v1/*` route and the SPA root require HTTP basic auth. `/healthz` stays open so external probes work without credentials. The username is ignored — any value works as long as the password matches.

```bash
# With auth enabled:
curl -u "admin:<password>" http://127.0.0.1:8080/api/v1/servers

# Browsers handle the credential prompt natively — no SPA login page.
```

When `auth.password` is empty, the API is open. Restrict at the network layer if exposed (see [`deployment.md`](deployment.md#optional-tls-via-caddy)).

## Health

### `GET /healthz`

Cheap liveness check. Always 200 if the HTTP server is up.

```bash
curl -fsS http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

## Servers

### `POST /api/v1/servers` — register a server

Body:

```json
{
  "name": "prod1",
  "hostname": "bench1.example.com",
  "ssh_user": "frappe",
  "ssh_port": 22,
  "ssh_key_path": "/var/lib/frappe-monitor/.ssh/id_ed25519",
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
  "labels": { "env": "staging" }
}
```

| Status | Meaning |
|---|---|
| 200 | Updated. Body is the full server record. |
| 400 | Bad JSON, invalid `ssh_port` (must be > 0). |
| 404 | Unknown id. |
| 409 | Hostname rename collides with another server. |

### `DELETE /api/v1/servers/{id}` — remove

```bash
curl -X DELETE http://127.0.0.1:8080/api/v1/servers/1
```

| Status | Meaning |
|---|---|
| 204 | Deleted. The schema's edge cascade removes log cursors. Alert states age out on the next reconciliation cycle. |
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

`error_kind` is one of `auth`, `dial`, `timeout`, `unknown`. Side effect: persists the result to the `status` field on the server record.

### `POST /api/v1/servers/{id}/deploy-collector` — push the collector script

```json
{ "deployed": true, "version": "v3" }
```

Pipes the embedded `frappe-monitor-collect.sh` to `~/.frappe-monitor/collect.sh` on the target via SSH and `chmod +x`s it. Re-run after upgrading the monitor binary if the embedded script changed.

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
  "supervisor_running": 4,
  "supervisor_total": 4,
  "redis_queues": { "default": 12, "long": 0 }
}
```

Returns 200 even when some fields have no data (zeros / empty map). 502 if VM is unreachable.

### `GET /api/v1/sites` — list `(server, bench, site)` triples

Backed by `count by (server, bench, site) (frappe_site_is_healthy)`.

### `GET /api/v1/sites/{server}/{bench}/{site}` — site detail

```json
{
  "server": "prod1",
  "bench": "bench1",
  "site": "alpha.example.com",
  "http_status_code": 200,
  "http_response_ms": 42.5,
  "is_healthy": 1,
  "avg_response_ms_1h": 38.2
}
```

`avg_response_ms_1h` is `avg_over_time(frappe_site_http_response_ms[1h])`. Phase 7 may upgrade to `quantile_over_time` for a true p95.

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

## Error model

All non-2xx responses follow:

```json
{ "error": "human-readable description" }
```

There's no machine-readable error code field today (Phase 7 cleanup). Inspect the HTTP status + the `error` text. The `test-connection` endpoint is the one exception — it returns 200 with `reachable: false` on probe failures, and surfaces a separate `error_kind` enum.

## Static files

Anything not under `/api/` or `/healthz` is served by the embedded SPA:

| Path | Behavior |
|---|---|
| `/` | Redirects to `/servers`. |
| `/assets/*` | Vite-built JS / CSS bundles. |
| Anything else | Returns `index.html` (so vue-router takes over). |
