#!/usr/bin/env bash
# Phase 1 smoke test for frappe-monitor.
#
# Self-managing: builds the binary, starts it on a high port with a
# hermetic temp config + temp DB, exercises all 5 HTTP routes, verifies
# the SIGTERM clean-exit contract (≤ 10s), and cleans up.
#
# Run:   ./scripts/smoke.sh
# Env:
#   BASE      override the http base url (default: http://localhost:18080)
#   PORT      override the listen port    (default: 18080)
#
# Exits 0 on success, non-zero with a "FAIL:" line on failure.

set -euo pipefail

PORT="${PORT:-18080}"
BASE="${BASE:-http://localhost:$PORT}"
BINARY="./bin/monitor-server"
SHUTDOWN_TIMEOUT_MS=10000

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }

# ---------------------------------------------------------------------------
# Step 1: build (CGO disabled per Phase 1 acceptance)
# ---------------------------------------------------------------------------
echo "==> build"
# `make build` runs web-build first (npm install + npm run build) and
# then the Go build with -tags=embed_dist so the SPA is bundled into
# the binary. First run can take ~30s for npm install.
make build >/dev/null
[ -x "$BINARY" ] || fail "binary not produced at $BINARY"
ok "$BINARY built (with embedded SPA)"

# Confirm CGO disabled — readelf works on stripped Go binaries to look for
# libc dependency. Statically-linked Go binary has no NEEDED entries.
if readelf -d "$BINARY" 2>/dev/null | grep -q NEEDED; then
    fail "binary has dynamic dependencies — expected CGO_ENABLED=0 build"
fi
ok "binary is statically linked (CGO_ENABLED=0)"

# ---------------------------------------------------------------------------
# Step 2: hermetic config in a tmpdir (don't touch real user config/data)
# ---------------------------------------------------------------------------
TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"; if [ -n "${PID:-}" ]; then kill -KILL "$PID" 2>/dev/null || true; fi' EXIT
cat > "$TMPDIR/monitor.yaml" <<EOF
server:
  listen_addr: ":$PORT"
  read_timeout_seconds: 15
  write_timeout_seconds: 15
database:
  path: "$TMPDIR/data/monitor.db"
ssh:
  dial_timeout_seconds: 10
  command_timeout_seconds: 30
  max_connections_per_host: 2
log:
  level: "info"
  format: "json"
EOF
ok "hermetic config + data dir under $TMPDIR"

# ---------------------------------------------------------------------------
# Step 3: start binary, wait for healthz to come up
# ---------------------------------------------------------------------------
"$BINARY" --config "$TMPDIR/monitor.yaml" > "$TMPDIR/server.log" 2>&1 &
PID=$!
ok "started PID=$PID"

# Poll healthz up to 5s.
for _ in $(seq 1 50); do
    if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then break; fi
    sleep 0.1
done
curl -fsS "$BASE/healthz" >/dev/null || { cat "$TMPDIR/server.log" >&2; fail "healthz never came up"; }
ok "healthz reachable"

# ---------------------------------------------------------------------------
# Step 4: HTTP route coverage — all 5 routes
# ---------------------------------------------------------------------------
echo "==> routes"

# 1. GET /healthz — content-type + body
HZ=$(curl -fsS -D /tmp/hz_headers "$BASE/healthz")
grep -i 'content-type:.*application/json' /tmp/hz_headers >/dev/null \
    || fail "healthz wrong content-type: $(cat /tmp/hz_headers)"
[ "$(echo "$HZ" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')" = "ok" ] \
    || fail "healthz body wrong: $HZ"
ok "GET /healthz → 200 application/json"

# 2. GET /api/v1/servers (empty)
LIST=$(curl -fsS "$BASE/api/v1/servers")
[ "$LIST" = "[]" ] || fail "expected '[]', got: $LIST"
ok "GET /api/v1/servers → []"

# 3. POST /api/v1/servers (happy path)
CREATED=$(curl -fsS -X POST "$BASE/api/v1/servers" \
    -H 'Content-Type: application/json' \
    -d '{"name":"smoke","hostname":"smoke.local","ssh_user":"monitor","ssh_port":22,"ssh_key_path":"/nonexistent/smoke.key"}')
ID=$(echo "$CREATED" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
[ -n "$ID" ] || fail "no id returned: $CREATED"
ok "POST /api/v1/servers → 201 id=$ID"

# 3a. POST same hostname again → 409
HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/v1/servers" \
    -H 'Content-Type: application/json' \
    -d '{"name":"smoke-dup","hostname":"smoke.local","ssh_user":"monitor","ssh_port":22,"ssh_key_path":"/nonexistent/smoke.key"}')
[ "$HTTP" = "409" ] || fail "expected 409 on dup hostname, got $HTTP"
ok "POST /api/v1/servers duplicate hostname → 409"

# 3b. POST with bad json → 400
HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/v1/servers" \
    -H 'Content-Type: application/json' \
    -d '{')
[ "$HTTP" = "400" ] || fail "expected 400 on bad json, got $HTTP"
ok "POST /api/v1/servers bad json → 400"

# 4. GET /api/v1/servers/{id}
GOT=$(curl -fsS "$BASE/api/v1/servers/$ID")
HOST_BACK=$(echo "$GOT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["hostname"])')
[ "$HOST_BACK" = "smoke.local" ] || fail "round-trip hostname wrong: $GOT"
ok "GET /api/v1/servers/$ID → 200"

# 4a. GET unknown id → 404
HTTP=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/v1/servers/99999")
[ "$HTTP" = "404" ] || fail "expected 404 for unknown id, got $HTTP"
ok "GET /api/v1/servers/99999 → 404"

# 5. POST /api/v1/servers/{id}/test-connection
# The fake key path doesn't exist, so loadKey fails → reachable=false +
# error_kind="unknown" (no sentinel matched). The HTTP status must still
# be 200 (diagnostic-endpoint contract).
TC=$(curl -fsS -X POST "$BASE/api/v1/servers/$ID/test-connection")
REACH=$(echo "$TC" | python3 -c 'import json,sys; print(str(json.load(sys.stdin)["reachable"]).lower())')
[ "$REACH" = "false" ] || fail "expected reachable=false, got: $TC"
ERR_KIND=$(echo "$TC" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("error_kind",""))')
[ -n "$ERR_KIND" ] || fail "expected error_kind on failure, got: $TC"
ok "POST /api/v1/servers/$ID/test-connection → 200 reachable=false error_kind=$ERR_KIND"

# 5a. test-connection on unknown id → 404
HTTP=$(curl -s -o /dev/null -w "%{http_code}" -X POST "$BASE/api/v1/servers/99999/test-connection")
[ "$HTTP" = "404" ] || fail "expected 404 test-connection unknown id, got $HTTP"
ok "POST /api/v1/servers/99999/test-connection → 404"

# Verify status persisted to "unreachable" after failed probe.
GOT=$(curl -fsS "$BASE/api/v1/servers/$ID")
STATUS=$(echo "$GOT" | python3 -c 'import json,sys; print(json.load(sys.stdin)["status"])')
[ "$STATUS" = "unreachable" ] || fail "expected status=unreachable, got: $STATUS"
ok "server status persisted to unreachable after failed probe"

# 6. SPA serves at / (bundled Vite assets present, not the placeholder)
SPA_HTML=$(curl -fsS "$BASE/")
echo "$SPA_HTML" | grep -q 'src="/assets/index-' \
    || fail "GET / didn't reference Vite-built assets — binary missing embed_dist?"
ok "GET / → SPA index.html (Vite assets present)"

# 6a. SPA client-side routing fallback
curl -fsS "$BASE/servers/9999" | grep -q 'src="/assets/index-' \
    || fail "SPA fallback at /servers/9999 didn't return index.html"
ok "GET /servers/9999 → SPA fallback (vue-router takes over, not 404)"

# ---------------------------------------------------------------------------
# Step 5: SIGTERM-clean-exit ≤ 10s contract
# ---------------------------------------------------------------------------
echo "==> shutdown"
START_NS=$(date +%s%N)
kill -TERM "$PID"

# Poll up to 11s for the process to disappear.
for _ in $(seq 1 110); do
    if ! kill -0 "$PID" 2>/dev/null; then break; fi
    sleep 0.1
done
END_NS=$(date +%s%N)
ELAPSED_MS=$(( (END_NS - START_NS) / 1000000 ))

if kill -0 "$PID" 2>/dev/null; then
    fail "process still running ${ELAPSED_MS}ms after SIGTERM (limit ${SHUTDOWN_TIMEOUT_MS}ms)"
fi
wait "$PID" 2>/dev/null || true
ok "exited in ${ELAPSED_MS}ms (limit ${SHUTDOWN_TIMEOUT_MS}ms)"

# Verify the "shutdown signal received" log line was emitted.
grep -q '"shutdown signal received"' "$TMPDIR/server.log" \
    || fail "no 'shutdown signal received' in log:\n$(cat "$TMPDIR/server.log")"
ok "shutdown signal log line present"

PID=""  # so the trap doesn't try to kill again

# ---------------------------------------------------------------------------
# Step 6: Phase 2 — VictoriaMetrics round-trip
# ---------------------------------------------------------------------------
# Verifies the load-bearing -influxSkipSingleField flag in
# deploy/docker-compose.dev.yml: a frappe_server_load_1m line written to
# VM's /write must come back from /api/v1/label/__name__/values as the
# bare metric name (not frappe_server_load_1m_value, which would be VM's
# default behavior without the flag and would silently break every
# master-plan PromQL query).
#
# Requires Docker. Skipped with a notice if `docker compose` isn't
# available (so smoke is still runnable in non-containerized CI shards).

echo "==> Phase 2 (VictoriaMetrics)"
if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
    echo "  skip: docker compose not available — Phase 2 wiring not exercised"
    echo "==> ALL CHECKS PASSED"
    exit 0
fi

# Make sure no leftover containers OR volumes from a previous run are using
# port 8428/3100 — fresh volumes required so the metric-name regression
# and Loki round-trip assertions are actually meaningful.
make vm-down >/dev/null 2>&1 || true
docker volume rm frappe-monitor-vm-data frappe-monitor-loki-data >/dev/null 2>&1 || true

VM_TRAP_PREV="$(trap -p EXIT)"
trap 'make vm-down >/dev/null 2>&1 || true; docker volume rm frappe-monitor-vm-data frappe-monitor-loki-data >/dev/null 2>&1 || true; eval "$VM_TRAP_PREV"' EXIT

make vm-up >/dev/null
ok "vm-up dispatched (fresh volumes — VM + Loki)"

# Wait for VM /health to be 200 (max ~15s).
for _ in $(seq 1 30); do
    if curl -fsS http://127.0.0.1:8428/health >/dev/null 2>&1; then break; fi
    sleep 0.5
done
curl -fsS http://127.0.0.1:8428/health >/dev/null \
    || { docker compose -f deploy/docker-compose.dev.yml logs --tail 30 victoriametrics >&2; fail "VM /health never returned 200"; }
ok "VM /health → 200"

# Direct write of one line — same shape ServerMetrics.LineProtocol emits.
NOW_NS=$(date +%s%N)
SERIES_LABEL="smoke-$(date +%s)"
curl -fsS -X POST 'http://127.0.0.1:8428/write' \
    -d "frappe_server_load_1m,server=$SERIES_LABEL value=1.5 $NOW_NS" >/dev/null
ok "wrote frappe_server_load_1m,server=$SERIES_LABEL value=1.5 to /write"

# VM ingestion is async; wait for the data point to be visible. Up to ~10s.
SERIES_FOUND=""
for _ in $(seq 1 20); do
    sleep 0.5
    NAMES_JSON=$(curl -fsS 'http://127.0.0.1:8428/api/v1/label/__name__/values')
    if echo "$NAMES_JSON" | grep -q 'frappe_server_load_1m'; then
        SERIES_FOUND=1
        break
    fi
done
[ -n "$SERIES_FOUND" ] || fail "frappe_server_load_1m never appeared in VM after 10s; names=$NAMES_JSON"

# Assertion 1: the metric name in VM is exactly `frappe_server_load_1m`.
# Because we started with a fresh volume, anything else in the names
# list is a defect we should know about.
echo "$NAMES_JSON" | python3 -c "
import json, sys
names = json.load(sys.stdin)['data']
if 'frappe_server_load_1m_value' in names:
    print('REGRESSION: metric stored as frappe_server_load_1m_value — is -influxSkipSingleField missing from compose?', file=sys.stderr)
    sys.exit(1)
if 'frappe_server_load_1m' not in names:
    print(f'metric frappe_server_load_1m absent from VM. names: {names}', file=sys.stderr)
    sys.exit(1)
" || fail "metric-name regression: $NAMES_JSON"
ok "VM stores metric as 'frappe_server_load_1m' (no _value suffix — flag honored)"

# Assertion 2: the server label we wrote is present in VM's label values.
# This proves the specific write (not just any historical data) round-tripped.
SERVER_LABELS=$(curl -fsS 'http://127.0.0.1:8428/api/v1/label/server/values')
echo "$SERVER_LABELS" | python3 -c "
import json, sys
labels = json.load(sys.stdin)['data']
target = '$SERIES_LABEL'
if target not in labels:
    print(f'expected server={target} in label values, got {labels}', file=sys.stderr)
    sys.exit(1)
" || fail "server label not present: $SERVER_LABELS"
ok "VM has server=$SERIES_LABEL in label values"

# ---------------------------------------------------------------------------
# Step 7: Phase 3 — Loki round-trip
# ---------------------------------------------------------------------------
# Verifies Loki ingest + query path: push a stream with stable labels via
# /loki/api/v1/push, query it back via /loki/api/v1/query_range, and
# confirm both the labels and the line round-trip. Same shape that
# internal/logs.LokiClient.Push emits.

echo "==> Phase 3 (Loki)"

# Loki takes a few seconds longer than VM to be ready. Poll /ready.
for _ in $(seq 1 30); do
    if curl -fsS http://127.0.0.1:3100/ready >/dev/null 2>&1; then break; fi
    sleep 0.5
done
curl -fsS http://127.0.0.1:3100/ready >/dev/null \
    || { docker compose -f deploy/docker-compose.dev.yml logs --tail 30 loki >&2; fail "Loki /ready never returned 200"; }
ok "Loki /ready → 200"

LOKI_LABEL="smoke-$(date +%s)"
LOKI_NOW_NS=$(date +%s%N)
LOKI_LINE="phase3 smoke line $(date +%s)"
PAYLOAD=$(python3 -c "
import json, sys
print(json.dumps({
    'streams': [{
        'stream': {'server': '$LOKI_LABEL', 'log_type': 'error'},
        'values': [['$LOKI_NOW_NS', '$LOKI_LINE']],
    }]
}))
")
curl -fsS -X POST http://127.0.0.1:3100/loki/api/v1/push \
    -H 'Content-Type: application/json' \
    -d "$PAYLOAD" >/dev/null
ok "wrote stream {server=$LOKI_LABEL,log_type=error} to Loki"

# Loki ingestion is async; poll up to ~10s for the entry to appear.
QUERY_OK=""
for _ in $(seq 1 20); do
    sleep 0.5
    T_END=$(date +%s)
    T_START=$((T_END - 60))
    QR=$(curl -fsS "http://127.0.0.1:3100/loki/api/v1/query_range?query=%7Bserver%3D%22$LOKI_LABEL%22%7D&start=${T_START}000000000&end=${T_END}000000000" 2>/dev/null || echo '{}')
    if echo "$QR" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
except Exception:
    sys.exit(1)
res = d.get('data', {}).get('result', [])
if not res:
    sys.exit(1)
# at least one stream with our label and a value matching our line
for s in res:
    if s['stream'].get('server') == '$LOKI_LABEL' and s['stream'].get('log_type') == 'error':
        for ts, line in s.get('values', []):
            if line == '$LOKI_LINE':
                sys.exit(0)
sys.exit(1)
" 2>/dev/null; then
        QUERY_OK=1
        break
    fi
done
[ -n "$QUERY_OK" ] || fail "Loki query_range never returned the pushed line: $QR"
ok "Loki query_range returns {server=$LOKI_LABEL,log_type=error} with the pushed line"

# ---------------------------------------------------------------------------
# Step 8: Phase 4 — metrics-query proxy through the binary
# ---------------------------------------------------------------------------
# Start a fresh binary (the Phase 1 SIGTERM closed the previous one)
# now that VM has data from Phase 2's direct write. Verify that
# /api/v1/metrics/query proxies cleanly to VM and returns the series
# we stored. This is the only place the proxy is exercised end-to-end
# through the running binary.

echo "==> Phase 4 (metrics-query proxy)"

"$BINARY" --config "$TMPDIR/monitor.yaml" > "$TMPDIR/server-p4.log" 2>&1 &
P4_PID=$!
trap 'kill -KILL "$P4_PID" 2>/dev/null || true; make vm-down >/dev/null 2>&1 || true; docker volume rm frappe-monitor-vm-data frappe-monitor-loki-data >/dev/null 2>&1 || true; eval "$VM_TRAP_PREV"' EXIT

for _ in $(seq 1 50); do
    if curl -fsS "$BASE/healthz" >/dev/null 2>&1; then break; fi
    sleep 0.1
done
curl -fsS "$BASE/healthz" >/dev/null \
    || { cat "$TMPDIR/server-p4.log" >&2; fail "Phase 4 binary never came up"; }
ok "Phase 4 binary up"

# Use the Phase 2 series we wrote earlier ($SERIES_LABEL).
T_NOW=$(date +%s)
PROXY_RESP=$(curl -fsS "$BASE/api/v1/metrics/query?query=frappe_server_load_1m%7Bserver%3D%22$SERIES_LABEL%22%7D&start=$((T_NOW-300))&end=$T_NOW&step=10" 2>&1 || echo '{}')
echo "$PROXY_RESP" | python3 -c "
import json, sys
try:
    d = json.load(sys.stdin)
except Exception as e:
    print(f'malformed proxy response: {e}', file=sys.stderr)
    sys.exit(1)
if d.get('status') != 'success':
    print(f'expected status=success, got: {d}', file=sys.stderr)
    sys.exit(1)
" || fail "proxy response: $PROXY_RESP"
ok "GET /api/v1/metrics/query proxies to VM (status=success)"

# 8a. Missing 'query' param → 400 from our proxy (rejected before forward)
HTTP=$(curl -s -o /dev/null -w "%{http_code}" "$BASE/api/v1/metrics/query?start=1&end=2")
[ "$HTTP" = "400" ] || fail "expected 400 for missing query param, got $HTTP"
ok "GET /api/v1/metrics/query without query → 400"

# ---------------------------------------------------------------------------
# Step 9: Phase 5 — bench/site hierarchy endpoints derived from VM
# ---------------------------------------------------------------------------
# Push a synthetic bench (apps_count, supervisor_*, redis_queue_depth) and
# a synthetic site (is_healthy, http_status_code, http_response_ms) into
# VM, then verify the four new VM-derived endpoints return them. Same
# binary as Phase 4; VM is already up. Dev compose sets
# -search.latencyOffset=0 so /api/v1/query reflects writes immediately.

echo "==> Phase 5 (hierarchy endpoints)"

P5_SRV="smoke-srv-$(date +%s)"
P5_BENCH="bench-1"
P5_SITE="alpha.smoke.test"
P5_NS=$(date +%s%N)

# Bench-level metrics.
curl -fsS -X POST 'http://127.0.0.1:8428/write' --data-binary "$(cat <<EOF
frappe_bench_apps_count,server=$P5_SRV,bench=$P5_BENCH value=7 $P5_NS
frappe_bench_supervisor_running,server=$P5_SRV,bench=$P5_BENCH value=4 $P5_NS
frappe_bench_supervisor_total,server=$P5_SRV,bench=$P5_BENCH value=4 $P5_NS
frappe_bench_redis_queue_depth,server=$P5_SRV,bench=$P5_BENCH,queue=default value=12 $P5_NS
frappe_bench_redis_queue_depth,server=$P5_SRV,bench=$P5_BENCH,queue=long value=0 $P5_NS
frappe_bench_info,server=$P5_SRV,bench=$P5_BENCH,frappe_version=v15.42.1 value=1 $P5_NS
frappe_site_is_healthy,server=$P5_SRV,bench=$P5_BENCH,site=$P5_SITE value=1 $P5_NS
frappe_site_http_status_code,server=$P5_SRV,bench=$P5_BENCH,site=$P5_SITE value=200 $P5_NS
frappe_site_http_response_ms,server=$P5_SRV,bench=$P5_BENCH,site=$P5_SITE value=42.5 $P5_NS
EOF
)" >/dev/null
ok "wrote bench + site metrics for $P5_SRV/$P5_BENCH/$P5_SITE"

# Poll /benches until our synthetic bench shows up. VM ingest is async.
P5_BENCH_FOUND=""
for _ in $(seq 1 20); do
    sleep 0.5
    BLIST=$(curl -fsS "$BASE/api/v1/benches" 2>/dev/null || echo '[]')
    if echo "$BLIST" | python3 -c "
import json, sys
arr = json.load(sys.stdin)
for b in arr:
    if b.get('server') == '$P5_SRV' and b.get('bench') == '$P5_BENCH':
        sys.exit(0)
sys.exit(1)
" 2>/dev/null; then
        P5_BENCH_FOUND=1
        break
    fi
done
[ -n "$P5_BENCH_FOUND" ] || fail "bench $P5_SRV/$P5_BENCH never appeared in /api/v1/benches: $BLIST"
ok "GET /api/v1/benches contains $P5_SRV/$P5_BENCH"

# Bench detail.
BD=$(curl -fsS "$BASE/api/v1/benches/$P5_SRV/$P5_BENCH")
echo "$BD" | python3 -c "
import json, sys
d = json.load(sys.stdin)
def need(k, exp):
    if d.get(k) != exp:
        print(f'bench detail {k}: expected {exp!r}, got {d.get(k)!r}', file=sys.stderr)
        sys.exit(1)
need('server', '$P5_SRV')
need('bench', '$P5_BENCH')
need('apps_count', 7)
need('supervisor_running', 4)
need('supervisor_total', 4)
if d.get('frappe_version') != 'v15.42.1':
    print(f'expected frappe_version=v15.42.1, got {d.get(\"frappe_version\")!r}', file=sys.stderr)
    sys.exit(1)
qs = d.get('redis_queues', {})
if qs.get('default') != 12 or qs.get('long') != 0:
    print(f'unexpected redis_queues: {qs}', file=sys.stderr)
    sys.exit(1)
" || fail "bench detail wrong: $BD"
ok "GET /api/v1/benches/$P5_SRV/$P5_BENCH → all fields populated"

# Sites list.
SLIST=$(curl -fsS "$BASE/api/v1/sites")
echo "$SLIST" | python3 -c "
import json, sys
arr = json.load(sys.stdin)
for s in arr:
    if (s.get('server') == '$P5_SRV'
        and s.get('bench') == '$P5_BENCH'
        and s.get('site') == '$P5_SITE'):
        sys.exit(0)
print(f'site triple not in list: {arr}', file=sys.stderr)
sys.exit(1)
" || fail "sites list missing entry: $SLIST"
ok "GET /api/v1/sites contains $P5_SRV/$P5_BENCH/$P5_SITE"

# Site detail.
SD=$(curl -fsS "$BASE/api/v1/sites/$P5_SRV/$P5_BENCH/$P5_SITE")
echo "$SD" | python3 -c "
import json, sys
d = json.load(sys.stdin)
def need(k, exp):
    if d.get(k) != exp:
        print(f'site detail {k}: expected {exp!r}, got {d.get(k)!r}', file=sys.stderr)
        sys.exit(1)
need('server', '$P5_SRV')
need('bench', '$P5_BENCH')
need('site', '$P5_SITE')
need('http_status_code', 200)
need('is_healthy', 1)
if abs(d.get('http_response_ms', 0) - 42.5) > 0.01:
    print(f'expected http_response_ms~=42.5, got {d.get(\"http_response_ms\")}', file=sys.stderr)
    sys.exit(1)
" || fail "site detail wrong: $SD"
ok "GET /api/v1/sites/$P5_SRV/$P5_BENCH/$P5_SITE → all fields populated"

# 8b. SIGTERM clean exit (no scheduler ticks to drain since no servers
# are registered, so the budget here is much smaller than Phase 1's).
kill -TERM "$P4_PID"
for _ in $(seq 1 110); do
    if ! kill -0 "$P4_PID" 2>/dev/null; then break; fi
    sleep 0.1
done
if kill -0 "$P4_PID" 2>/dev/null; then
    fail "Phase 4 binary still alive after SIGTERM"
fi
wait "$P4_PID" 2>/dev/null || true
P4_PID=""
ok "Phase 4 binary exited cleanly on SIGTERM"

make vm-down >/dev/null
ok "vm-down clean"

# Restore the original trap (no need to vm-down twice on EXIT).
eval "$VM_TRAP_PREV"

echo "==> ALL CHECKS PASSED"
