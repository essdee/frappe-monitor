#!/usr/bin/env bash
# Local test / demo on a laptop. Brings up the whole stack in a single
# foreground command, seeds demo data so every dashboard page has
# something to look at, and tears everything down on Ctrl+C.
#
#   ./deploy/local-test.sh           # full demo on http://localhost:8080
#   PORT=9090 ./deploy/local-test.sh # different port
#   REBUILD=1 ./deploy/local-test.sh # force rebuild even if bin/ exists
#   KEEP_BACKENDS=1 …                # leave VM+Loki containers running
#                                     # after exit (faster re-runs)
#
# Hermetic — uses a temp config + temp SQLite DB; doesn't touch your
# real config/data. Works on macOS and Linux laptops. No sudo, no
# systemd, no system user. Stop with Ctrl+C in this terminal.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
TMPDIR="$(mktemp -d -t frappe-monitor-localtest.XXXXXX)"
PORT="${PORT:-8080}"
MONITOR_PID=""

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }

cleanup() {
    echo
    echo "==> tearing down"
    if [ -n "$MONITOR_PID" ]; then
        kill -TERM "$MONITOR_PID" 2>/dev/null || true
        for _ in $(seq 1 30); do
            kill -0 "$MONITOR_PID" 2>/dev/null || break
            sleep 0.2
        done
        kill -KILL "$MONITOR_PID" 2>/dev/null || true
        wait "$MONITOR_PID" 2>/dev/null || true
    fi
    if [ "${KEEP_BACKENDS:-0}" != "1" ]; then
        (cd "$REPO_ROOT" && docker compose -f deploy/docker-compose.dev.yml down >/dev/null 2>&1 || true)
        echo "  ok: VM + Loki stopped (data volumes preserved)"
    else
        echo "  >> KEEP_BACKENDS=1 — left VM + Loki running"
    fi
    rm -rf "$TMPDIR"
    echo "  ok: bye"
}
trap cleanup EXIT INT TERM

# ---------------------------------------------------------------------------
# 1) prerequisites
# ---------------------------------------------------------------------------
echo "==> checking prerequisites"
require() {
    command -v "$1" >/dev/null 2>&1 || fail "$1 not found — install: $2"
}
require go     "https://go.dev/doc/install (need Go 1.25+)"
require node   "https://nodejs.org or your distro (need 20+)"
require npm    "ships with Node"
require docker "https://www.docker.com/products/docker-desktop/ (or colima/orbstack on macOS)"
docker compose version >/dev/null 2>&1 || fail "docker compose plugin missing"
docker info >/dev/null 2>&1 || fail "docker daemon not running — start Docker Desktop / colima / OrbStack first"
require curl   "your distro's package manager"
require python3 "your distro's package manager (used to generate JSON for seeded log lines)"

GO_VERSION=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
MAJOR_MINOR=$(echo "$GO_VERSION" | awk -F. '{print $1"."$2}')
awk -v v="$MAJOR_MINOR" 'BEGIN { exit !(v+0 >= 1.25) }' \
    || fail "Go $GO_VERSION too old — need ≥ 1.25"
ok "go $GO_VERSION, node $(node --version), docker $(docker --version | awk '{print $3}' | tr -d ,)"

# ---------------------------------------------------------------------------
# 2) build
# ---------------------------------------------------------------------------
cd "$REPO_ROOT"
if [ ! -x bin/monitor-server ] || [ "${REBUILD:-0}" = "1" ]; then
    echo "==> building (npm install + vite build + go build)"
    echo "    first run takes ~30s; subsequent runs are seconds"
    make build >/dev/null
fi
ok "binary at bin/monitor-server ($(du -h bin/monitor-server | cut -f1))"

# ---------------------------------------------------------------------------
# 3) VictoriaMetrics + Loki
# ---------------------------------------------------------------------------
echo "==> starting VictoriaMetrics + Loki via docker compose"
docker compose -f deploy/docker-compose.dev.yml up -d >/dev/null 2>&1 \
    || fail "docker compose up failed; try 'docker compose -f deploy/docker-compose.dev.yml up' to see the error"

for _ in $(seq 1 60); do
    if curl -fsS http://127.0.0.1:8428/health >/dev/null 2>&1 \
       && curl -fsS http://127.0.0.1:3100/ready  >/dev/null 2>&1; then
        break
    fi
    sleep 1
done
curl -fsS http://127.0.0.1:8428/health >/dev/null 2>&1 || fail "VictoriaMetrics never came up — try: docker compose -f deploy/docker-compose.dev.yml logs victoriametrics"
curl -fsS http://127.0.0.1:3100/ready  >/dev/null 2>&1 || fail "Loki never came up — try: docker compose -f deploy/docker-compose.dev.yml logs loki"
ok "VM (127.0.0.1:8428) + Loki (127.0.0.1:3100) healthy"

# ---------------------------------------------------------------------------
# 4) hermetic config — temp DB, no auth, alerts off, scheduler interval
#    intentionally huge so the demo's fake servers don't trigger SSH
#    attempts during the test session.
# ---------------------------------------------------------------------------
cat > "$TMPDIR/monitor.yaml" <<EOF
server:
  listen_addr: ":$PORT"
  read_timeout_seconds: 15
  write_timeout_seconds: 15
database:
  path: "$TMPDIR/monitor.db"
ssh:
  dial_timeout_seconds: 10
  command_timeout_seconds: 30
  max_connections_per_host: 2
log:
  level: "info"
  format: "json"
metrics:
  vm_url: "http://127.0.0.1:8428"
  push_timeout_seconds: 5
  query_timeout_seconds: 15
logs:
  loki_url: "http://127.0.0.1:3100"
  push_timeout_seconds: 5
  query_timeout_seconds: 15
scheduler:
  default_interval_seconds: 86400
  max_parallel: 10
  per_job_timeout_seconds: 30
EOF
ok "hermetic config + DB at $TMPDIR/"

# ---------------------------------------------------------------------------
# 5) seed demo metrics across a 15-minute window so the charts render
#    something other than a single dot. Two synthetic servers with
#    contrasting health (demo-1 healthy, demo-2 stressed) so every
#    dashboard page has a meaningful before/after. Whole payload is
#    generated by python in one shot to keep escaping sane.
# ---------------------------------------------------------------------------
echo "==> seeding demo metrics + log lines"

python3 - "$TMPDIR/seed.txt" <<'PY'
import sys, time, random
out_path = sys.argv[1]
NOW = int(time.time())

def line(name, labels, value, ts_s):
    label_str = ",".join(f"{k}={v}" for k, v in labels.items())
    return f"{name},{label_str} value={value} {ts_s * 1_000_000_000}"

lines = []
SAMPLES = 30
INTERVAL = 30  # seconds between samples

for srv in ("demo-1", "demo-2"):
    stressed = (srv == "demo-2")
    load_mid    = 2.1 if stressed else 0.7
    mem_used_pc = 0.85 if stressed else 0.4
    disk_used_b = int((92 if stressed else 42) * 1024**3)
    busy_per_s  = 800 if stressed else 200   # CPU jiffies of busy-time per second

    # Counter base values for CPU jiffies (must be monotonically
    # increasing across samples).
    base_t = NOW - SAMPLES * INTERVAL
    user = sys_ = iow = idle = 0

    for i in range(SAMPLES):
        ts = NOW - (SAMPLES - 1 - i) * INTERVAL
        random.seed(srv + str(i))

        # Time-series values with mild jitter.
        load1  = round(load_mid          + random.uniform(-0.15, 0.15), 4)
        load5  = round(load_mid * 0.85   + random.uniform(-0.10, 0.10), 4)
        load15 = round(load_mid * 0.7    + random.uniform(-0.05, 0.05), 4)
        mem_avail = int(8 * 1024**3 * (1 - mem_used_pc) + random.randint(-100_000_000, 100_000_000))

        # CPU jiffies: counters that step forward each sample.
        idle += INTERVAL * (1000 - busy_per_s)
        user += INTERVAL * busy_per_s * 7 // 10
        sys_ += INTERVAL * busy_per_s * 2 // 10
        iow  += INTERVAL * busy_per_s * 1 // 10

        lines += [
            line("frappe_server_load_1m",            {"server": srv}, load1, ts),
            line("frappe_server_load_5m",            {"server": srv}, load5, ts),
            line("frappe_server_load_15m",           {"server": srv}, load15, ts),
            line("frappe_server_mem_total_bytes",    {"server": srv}, 8 * 1024**3, ts),
            line("frappe_server_mem_available_bytes",{"server": srv}, mem_avail, ts),
            line("frappe_server_disk_total_bytes",   {"server": srv, "mount": "/"}, 100 * 1024**3, ts),
            line("frappe_server_disk_used_bytes",    {"server": srv, "mount": "/"}, disk_used_b, ts),
            line("frappe_server_cpu_user",           {"server": srv}, user, ts),
            line("frappe_server_cpu_system",         {"server": srv}, sys_, ts),
            line("frappe_server_cpu_idle",           {"server": srv}, idle, ts),
            line("frappe_server_cpu_iowait",         {"server": srv}, iow, ts),
            line("frappe_server_cpu_irq",            {"server": srv}, 0, ts),
            line("frappe_server_cpu_softirq",        {"server": srv}, 0, ts),
            line("frappe_server_cpu_steal",          {"server": srv}, 0, ts),
            line("frappe_server_cpu_nice",           {"server": srv}, 0, ts),
            line("frappe_server_uptime_seconds",     {"server": srv}, NOW - base_t + 86400, ts),
        ]

# Bench-level metrics — apps_count etc are static, queue depth wiggles.
ts_now = NOW
for i in range(SAMPLES):
    ts = NOW - (SAMPLES - 1 - i) * INTERVAL
    random.seed("bench" + str(i))
    lines += [
        line("frappe_bench_apps_count",        {"server": "demo-1", "bench": "erpnext-bench"}, 8, ts),
        line("frappe_bench_apps_count",        {"server": "demo-2", "bench": "staging-bench"}, 4, ts),
        line("frappe_bench_supervisor_running",{"server": "demo-1", "bench": "erpnext-bench"}, 10, ts),
        line("frappe_bench_supervisor_total",  {"server": "demo-1", "bench": "erpnext-bench"}, 10, ts),
        line("frappe_bench_supervisor_running",{"server": "demo-2", "bench": "staging-bench"}, 4, ts),
        line("frappe_bench_supervisor_total",  {"server": "demo-2", "bench": "staging-bench"}, 5, ts),
        line("frappe_bench_info",              {"server": "demo-1", "bench": "erpnext-bench", "frappe_version": "v15.42.1"}, 1, ts),
        line("frappe_bench_info",              {"server": "demo-2", "bench": "staging-bench", "frappe_version": "v15.40.0"}, 1, ts),
        line("frappe_bench_redis_queue_depth", {"server": "demo-1", "bench": "erpnext-bench", "queue": "default"}, max(0, 12 + random.randint(-5, 5)), ts),
        line("frappe_bench_redis_queue_depth", {"server": "demo-1", "bench": "erpnext-bench", "queue": "long"},    max(0, random.randint(0, 1)), ts),
        line("frappe_bench_redis_queue_depth", {"server": "demo-1", "bench": "erpnext-bench", "queue": "short"},   max(0, 3 + random.randint(-2, 2)), ts),
        line("frappe_bench_redis_queue_depth", {"server": "demo-2", "bench": "staging-bench", "queue": "default"}, max(0, 200 + random.randint(-50, 50)), ts),
        line("frappe_bench_redis_queue_depth", {"server": "demo-2", "bench": "staging-bench", "queue": "long"},    max(0, 5 + random.randint(-2, 2)), ts),
    ]

# Site-level metrics — three sites, varying response times.
sites = [
    ("demo-1", "erpnext-bench", "app.example.com",      45,  15, 200, 1),
    ("demo-1", "erpnext-bench", "portal.example.com",   82,  25, 200, 1),
    ("demo-2", "staging-bench", "staging.example.com",  2500, 200, 502, 0),
]
for i in range(SAMPLES):
    ts = NOW - (SAMPLES - 1 - i) * INTERVAL
    for srv, bench, site, mid_ms, jitter_ms, code, healthy in sites:
        random.seed(site + str(i))
        rt = max(1, mid_ms + random.randint(-jitter_ms, jitter_ms))
        lines += [
            line("frappe_site_http_status_code",  {"server": srv, "bench": bench, "site": site}, code, ts),
            line("frappe_site_http_response_ms",  {"server": srv, "bench": bench, "site": site}, rt, ts),
            line("frappe_site_is_healthy",        {"server": srv, "bench": bench, "site": site}, healthy, ts),
        ]

with open(out_path, "w") as f:
    f.write("\n".join(lines) + "\n")
print(f"  wrote {len(lines)} lines to {out_path}")
PY

curl -fsS -X POST http://127.0.0.1:8428/write \
    --data-binary "@$TMPDIR/seed.txt" >/dev/null
ok "metrics seeded (2 servers, 2 benches, 3 sites; 30 samples × 30s = 15min window)"

# Push a few realistic-looking error log lines for site detail to render.
LOG_PAYLOAD=$(python3 - <<'PY'
import json, time
ns = int(time.time() * 1e9)
print(json.dumps({
  "streams": [{
    "stream": {"server": "demo-2", "bench": "staging-bench", "log_type": "error"},
    "values": [
      [str(ns - 600_000_000_000), "ConnectionError: Connection refused on staging.example.com:8000"],
      [str(ns - 300_000_000_000), "Worker died: redis timeout after 30s"],
      [str(ns - 120_000_000_000), "HTTP 502 from upstream — connect: connection refused"],
      [str(ns - 60_000_000_000),  "frappe.app.session_expired: Login required"],
      [str(ns),                   "RuntimeError: Database lock contention; retrying"]
    ]
  }]
}))
PY
)
curl -fsS -X POST http://127.0.0.1:3100/loki/api/v1/push \
    -H 'Content-Type: application/json' -d "$LOG_PAYLOAD" >/dev/null
ok "demo log lines pushed to Loki"

# ---------------------------------------------------------------------------
# 6) start the monitor binary
# ---------------------------------------------------------------------------
echo "==> starting frappe-monitor"
"$REPO_ROOT/bin/monitor-server" --config "$TMPDIR/monitor.yaml" \
    > "$TMPDIR/monitor.log" 2>&1 &
MONITOR_PID=$!

for _ in $(seq 1 50); do
    if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
        break
    fi
    sleep 0.1
done
curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null \
    || { tail -20 "$TMPDIR/monitor.log" >&2; fail "monitor never came up — log above"; }
ok "monitor on http://localhost:$PORT (pid $MONITOR_PID)"

# Register the demo servers in the SQLite registry so /servers shows
# them. SSH details are deliberately unreachable (key path /dev/null);
# we set scheduler.default_interval_seconds=86400 above so this won't
# trigger any actual SSH attempts during the demo session.
for s in demo-1 demo-2; do
    curl -fsS -X POST "http://127.0.0.1:$PORT/api/v1/servers" \
        -H 'Content-Type: application/json' \
        -d "{\"name\":\"$s\",\"hostname\":\"$s.local\",\"ssh_user\":\"demo\",\"ssh_port\":22,\"ssh_key_path\":\"/dev/null\"}" >/dev/null
done
ok "registered demo-1 + demo-2 in the registry"

cat <<EOF

  ────────────────────────────────────────────────────────────────────
   READY  →  http://localhost:$PORT
  ────────────────────────────────────────────────────────────────────

   What you'll see:

     /servers   demo-1 (status: unknown — never probed)
                demo-2 (status: unknown — never probed)
                Click a card → CPU / memory / disk / load charts.

     /benches   erpnext-bench (on demo-1, healthy)
                staging-bench (on demo-2, supervisor 4/5)

     /sites     app.example.com    healthy, ~45ms
                portal.example.com healthy, ~82ms
                staging.example.com unhealthy (HTTP 502, ~2.5s)
                Click an unhealthy site → see error log lines.

   Try the timeline filter (15m, 1h, 6h…) in the header — every
   chart re-queries against the chosen window.

   Logs from the binary:  tail -f $TMPDIR/monitor.log

   Press Ctrl+C HERE to stop and tear everything down.

EOF

wait "$MONITOR_PID"
