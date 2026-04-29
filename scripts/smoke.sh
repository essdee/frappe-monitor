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
echo "==> ALL CHECKS PASSED"
