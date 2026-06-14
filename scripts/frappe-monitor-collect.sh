#!/usr/bin/env bash
# frappe-monitor-collect.sh
#
# Outputs frappe-monitor metric sections to stdout.
# Phase 3 (v2.0.0): server + per-bench + per-site sections. Logs are
# tailed by the monitor's own log-cursor machinery — this script only
# emits metrics.
#
# Bench discovery: BENCH_PATHS env var (colon-separated) overrides
# auto-discovery; otherwise we look in /home/*/frappe-bench,
# /home/*/bench-* and /opt/bench/* for directories that look like a
# Frappe bench (have sites/, apps/, and a Procfile).
#
# The version line below is the canonical source of truth — the
# monitoring server reads it (via `head -3`) and re-deploys this
# script on mismatch.

set -euo pipefail

VERSION="2.5.0"

# Hard wall-clock budget for the WHOLE site-probe loop, regardless of
# how many sites the bench has. SSH's command timeout is typically
# 30s; we leave ~10s headroom for emit_meta + emit_server + per-bench
# work so a bench with 30+ sites doesn't get killed mid-loop and lose
# the trailing ###END marker. Sites past the budget are emitted with
# is_healthy=0 + http_status_code=0 — no curl call, no time spent.
SITE_PROBE_BUDGET_S="${SITE_PROBE_BUDGET_S:-20}"

# Self-renice + ionice so the collector NEVER outranks the bench's own
# workloads. CPU nice +10 deprioritizes us under load (default user
# procs run at 0); ionice best-effort class 7 is the lowest priority
# Linux exposes for IO. If renice/ionice aren't installed, we silently
# fall through — being polite is best-effort, not load-bearing.
renice +10 -p $$ >/dev/null 2>&1 || true
command -v ionice >/dev/null 2>&1 && ionice -c 2 -n 7 -p $$ >/dev/null 2>&1 || true

# Trap any error so the operator sees what actually failed, then end
# the output cleanly with ###END so the parser doesn't bail with a
# generic "missing ###END marker" message. The dashboard will surface
# the ###META section's "last_error" line in the server card.
on_error() {
  local exit_code=$?
  local line=$1
  echo
  echo "###ERROR"
  echo "exit_code=$exit_code"
  echo "line=$line"
  echo
  echo "###END"
  exit "$exit_code"
}
trap 'on_error $LINENO' ERR

emit_meta() {
  echo "###META"
  echo "version=$VERSION"
  echo "timestamp=$(date +%s)"
  echo "hostname=$(hostname)"
  echo
}

emit_server() {
  echo "###SERVER"

  # CPU jiffies — raw counters from /proc/stat.
  read -r _ user nice system idle iowait irq softirq steal _ < /proc/stat
  echo "cpu_user=$user"
  echo "cpu_nice=$nice"
  echo "cpu_system=$system"
  echo "cpu_idle=$idle"
  echo "cpu_iowait=$iowait"
  echo "cpu_irq=$irq"
  echo "cpu_softirq=$softirq"
  echo "cpu_steal=$steal"

  # Memory in kB.
  awk '/^MemTotal:/      {print "mem_total_kb="     $2}
       /^MemAvailable:/  {print "mem_available_kb=" $2}
       /^MemFree:/       {print "mem_free_kb="      $2}
       /^Buffers:/       {print "mem_buffers_kb="   $2}
       /^Cached:/        {print "mem_cached_kb="    $2}
       /^SwapTotal:/     {print "swap_total_kb="    $2}
       /^SwapFree:/      {print "swap_free_kb="     $2}' /proc/meminfo

  # Load averages
  read -r l1 l5 l15 _ < /proc/loadavg
  echo "load_1m=$l1"
  echo "load_5m=$l5"
  echo "load_15m=$l15"

  # Uptime (seconds, fractional)
  read -r up _ < /proc/uptime
  echo "uptime_seconds=$up"

  # Disk used/total per filesystem (skip pseudo). The trailing `|| true`
  # is load-bearing: under `set -euo pipefail` a failing `df` (stale NFS
  # mount, unreadable fs) would otherwise trip the ERR trap and truncate
  # the ENTIRE output mid-###SERVER — losing memory, load, disk, net, and
  # every per-bench/site section for exactly the degraded hosts we most
  # need data from. A disk read failure must not sink the whole payload.
  df -B1 --output=target,used,size -x tmpfs -x devtmpfs -x squashfs -x overlay 2>/dev/null \
    | tail -n +2 \
    | while read -r mount used total; do
        [ -z "$mount" ] && continue
        echo "disk_used_bytes{mount=\"$mount\"}=$used"
        echo "disk_total_bytes{mount=\"$mount\"}=$total"
      done || true

  # Network RX/TX per iface (skip lo). Same `|| true` guard as disk.
  awk 'NR>2 {
        gsub(":", "", $1)
        if ($1 == "lo") next
        if ($1 == "") next
        printf "net_rx_bytes{iface=\"%s\"}=%s\n", $1, $2
        printf "net_tx_bytes{iface=\"%s\"}=%s\n", $1, $10
       }' /proc/net/dev || true

  echo
}

# --- Bench / site discovery ---------------------------------------------

discover_benches() {
  if [ -n "${BENCH_PATHS:-}" ]; then
    echo "$BENCH_PATHS" | tr ':' '\n'
    return
  fi
  # Auto-discovery: directories that look like a Frappe bench. Use bash
  # globs with nullglob so non-matches are silent.
  shopt -s nullglob
  for p in /home/*/frappe-bench /home/*/bench-* /opt/bench/*; do
    if [ -d "$p/sites" ] && [ -d "$p/apps" ] && [ -f "$p/Procfile" ]; then
      echo "$p"
    fi
  done
  shopt -u nullglob
}

bench_name_for() {
  # Use the bench directory's basename as its identifier. /home/sakthi/
  # frappe-bench → "frappe-bench"; /home/u/bench-prod → "bench-prod".
  basename "$1"
}

read_frappe_version() {
  local bench="$1"
  local pyfile="$bench/apps/frappe/frappe/__init__.py"
  if [ -f "$pyfile" ]; then
    awk -F'"' '/^__version__/ {print $2; exit}' "$pyfile" 2>/dev/null \
      || awk -F"'" "/^__version__/ {print \$2; exit}" "$pyfile" 2>/dev/null \
      || echo ""
  fi
}

read_apps_count() {
  local bench="$1"
  local f="$bench/sites/apps.txt"
  if [ -f "$f" ]; then
    # Capture the count, THEN echo it. `grep -c` prints "0" AND exits 1
    # when there are zero matches, so the old `grep ... || echo 0` printed
    # TWO lines ("0\n0") on an empty/all-blank apps.txt — a torn metric
    # that corrupted the ###BENCH section. Assigning first captures the
    # "0" from stdout regardless of grep's exit code.
    local n
    n=$(grep -cve '^[[:space:]]*$' "$f" 2>/dev/null) || true
    echo "${n:-0}"
  else
    echo 0
  fi
}

# Inspect bench config to find the redis_queue port; stdout is the port
# number, or empty on miss.
redis_queue_port() {
  local bench="$1"
  local conf="$bench/config/redis_queue.conf"
  if [ -f "$conf" ]; then
    awk '/^[[:space:]]*port[[:space:]]+/ {print $2; exit}' "$conf"
  fi
}

emit_bench() {
  local bench="$1"
  local name="$2"

  echo "###BENCH:$name"

  local fv
  fv=$(read_frappe_version "$bench" || echo "")
  if [ -n "$fv" ]; then
    # info-style metric: value=1 with version as a label so PromQL can
    # group by version without parsing the string out of a value.
    echo "info{frappe_version=\"$fv\"}=1"
  fi

  echo "apps_count=$(read_apps_count "$bench")"

  # Redis queue depths. If redis-cli isn't reachable, emit 0 — better
  # than silently dropping the metric (operator sees "always 0" and
  # investigates).
  local rport
  rport=$(redis_queue_port "$bench" || echo "")
  for q in short default long; do
    local depth=0
    if [ -n "$rport" ] && command -v redis-cli >/dev/null 2>&1; then
      depth=$(redis-cli -p "$rport" llen "rq:queue:$q" 2>/dev/null || echo 0)
    fi
    echo "redis_queue_depth{queue=\"$q\"}=$depth"
  done

  # Supervisor process counts scoped to this bench (the "$name:" prefix
  # in supervisorctl output is conventional). All zeros if supervisorctl
  # isn't installed — same "loud zero" rationale as redis above.
  local sup_running=0 sup_total=0
  if command -v supervisorctl >/dev/null 2>&1; then
    local sup_lines
    # The supervisor socket is usually owned by the bench owner/root, not the
    # SSH user, so try non-interactive sudo first (matches the control panel's
    # `sudo supervisorctl`); fall back to a plain call when the SSH user can
    # already read it or sudo isn't permitted. Without this, a standard
    # SSH-user != bench-owner box silently reports 0/0 running processes.
    sup_lines=$(sudo -n supervisorctl status 2>/dev/null | grep -E "^${name}[-:]" || true)
    if [ -z "$sup_lines" ]; then
      sup_lines=$(supervisorctl status 2>/dev/null | grep -E "^${name}[-:]" || true)
    fi
    if [ -n "$sup_lines" ]; then
      sup_total=$(printf '%s\n' "$sup_lines" | wc -l)
      sup_running=$(printf '%s\n' "$sup_lines" | grep -c RUNNING || true)
    fi
  fi
  echo "supervisor_running=$sup_running"
  echo "supervisor_total=$sup_total"

  echo
}

# Detect the bench's webserver port ONCE per bench (was per-site in
# 2.2.0 — that scaled O(sites × candidate_ports) and timed out at 14+
# sites under SSH's 30s budget). Caller probes each candidate against
# the first available site so the Host header routes correctly. The
# returned port is then reused for every site in this bench.
#
# Returns the port number on stdout, or "0" if nothing is listening.
detect_bench_port_once() {
  local bench="$1"
  local probe_site="$2"   # any site under this bench, used as Host hdr
  local conf="$bench/sites/common_site_config.json"
  local candidates=()

  # 1. Configured webserver_port wins if parseable.
  if [ -f "$conf" ] && command -v python3 >/dev/null 2>&1; then
    local p
    p=$(python3 -c "import json,sys; d=json.load(open('$conf')); print(d.get('webserver_port',''))" 2>/dev/null || echo "")
    if [ -n "$p" ] && [ "$p" != "None" ]; then
      candidates+=("$p")
    fi
  fi
  # 2. Common defaults — 8000 (bench start) first; 80 (nginx) next.
  candidates+=(8000 80)

  for port in "${candidates[@]}"; do
    local code
    # Tighter 2s timeout (was 3s × 2 candidates × 14 sites = >> 30s
    # SSH budget). One quick connect-test per candidate is enough.
    code=$(curl -sS -o /dev/null -m 2 \
           -w '%{http_code}' \
           -H "Host: $probe_site" \
           "http://127.0.0.1:${port}/api/method/ping" 2>/dev/null || echo "000")
    if [ -n "$code" ] && [ "$code" != "000" ]; then
      echo "$port"
      return
    fi
  done
  echo "0"
}

# Per-site HTTP probe + emit using the cached port. Tighter per-probe
# timeout (2s) so 30+ sites still fit the SSH budget.
#
# A 4th argument "skip" short-circuits the probe and emits
# http_status_code=0 / is_healthy=0. Used by the main loop when the
# wall-clock budget is exceeded — without this guard, a bench with
# more sites than fit in the budget gets its remaining sites probed
# anyway, the SSH timeout fires, and the parser sees torn output.
emit_site() {
  local bench_name="$1"
  local site_name="$2"
  local port="$3"
  local mode="${4:-probe}"

  echo "###SITE:$bench_name:$site_name"

  local out http_code time_s time_ms healthy
  if [ "$mode" = "skip" ] || [ "$port" = "0" ]; then
    # Either (a) we ran out of SSH budget before reaching this site, or
    # (b) no port was detected for this bench. Same emit shape — saves
    # the dashboard from rendering "no data" for skipped sites and
    # makes the budget overrun observable as is_healthy=0.
    http_code=0
    time_ms=0
  else
    out=$(curl -sS -o /dev/null -m 2 \
          -w '%{http_code} %{time_total}' \
          -H "Host: $site_name" \
          "http://127.0.0.1:${port}/api/method/ping" 2>/dev/null || echo "000 0")
    http_code=$(echo "$out" | awk '{print $1}')
    time_s=$(echo "$out" | awk '{print $2}')
    time_ms=$(awk "BEGIN {printf \"%.1f\", ($time_s) * 1000}")
  fi

  echo "http_port=$port"
  echo "http_status_code=$http_code"
  echo "http_response_ms=$time_ms"
  # 2xx + 3xx are "site is responding" — count as healthy. 4xx/5xx
  # mean the server's up but the site isn't routed correctly. Force
  # http_code to a number first so a curl-failure value like "000" or
  # an empty string can't crash the test under set -e.
  case "$http_code" in
    ''|*[!0-9]*) healthy=0 ;;
    *)
      if [ "$http_code" -ge 200 ] && [ "$http_code" -lt 400 ]; then
        healthy=1
      else
        healthy=0
      fi
      ;;
  esac
  echo "is_healthy=$healthy"

  echo
}

# --- main ---------------------------------------------------------------

emit_meta
emit_server

# Mark the start of the site-probe phase so the loop can self-throttle
# against SITE_PROBE_BUDGET_S below. We don't include emit_meta /
# emit_server in the budget — those are bounded and small.
PROBE_START_TS="$(date +%s)"

while IFS= read -r bench_path; do
  [ -n "$bench_path" ] || continue
  [ -d "$bench_path" ] || continue
  bench_name=$(bench_name_for "$bench_path")

  emit_bench "$bench_path" "$bench_name"

  if [ -d "$bench_path/sites" ]; then
    shopt -s nullglob

    # Pick any one site to use as the "Host" header during port
    # detection, then reuse that port for every site in this bench.
    first_site=""
    for site_dir in "$bench_path/sites"/*/; do
      [ -d "$site_dir" ] || continue
      [ -f "$site_dir/site_config.json" ] || continue
      first_site=$(basename "$site_dir")
      break
    done

    bench_port="0"
    if [ -n "$first_site" ]; then
      bench_port=$(detect_bench_port_once "$bench_path" "$first_site")
    fi

    for site_dir in "$bench_path/sites"/*/; do
      [ -d "$site_dir" ] || continue
      [ -f "$site_dir/site_config.json" ] || continue
      site_name=$(basename "$site_dir")
      now_ts="$(date +%s)"
      elapsed=$((now_ts - PROBE_START_TS))
      if [ "$elapsed" -ge "$SITE_PROBE_BUDGET_S" ]; then
        # Budget blown — emit the site shape with mode=skip so the
        # parser still gets a complete section instead of the loop
        # being killed by SSH and producing torn output.
        emit_site "$bench_name" "$site_name" "$bench_port" "skip"
      else
        emit_site "$bench_name" "$site_name" "$bench_port"
      fi
    done
    shopt -u nullglob
  fi
done < <(discover_benches)

echo "###END"
