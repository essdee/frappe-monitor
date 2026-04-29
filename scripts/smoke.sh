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
make build >/dev/null
[ -x "$BINARY" ] || fail "binary not produced at $BINARY"
ok "$BINARY built"

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

# Make sure no leftover container OR volume from a previous run is using
# port 8428 — fresh volume is required so the metric-name regression
# assertion (no `_value` suffix) is actually meaningful.
make vm-down >/dev/null 2>&1 || true
docker volume rm frappe-monitor-vm-data >/dev/null 2>&1 || true

VM_TRAP_PREV="$(trap -p EXIT)"
trap 'make vm-down >/dev/null 2>&1 || true; docker volume rm frappe-monitor-vm-data >/dev/null 2>&1 || true; eval "$VM_TRAP_PREV"' EXIT

make vm-up >/dev/null
ok "vm-up dispatched (fresh volume)"

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

make vm-down >/dev/null
ok "vm-down clean"

# Restore the original trap (no need to vm-down twice on EXIT).
eval "$VM_TRAP_PREV"

echo "==> ALL CHECKS PASSED"
