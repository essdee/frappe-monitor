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

VERSION="2.1.0"

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

  # Disk used/total per filesystem (skip pseudo).
  df -B1 --output=target,used,size -x tmpfs -x devtmpfs -x squashfs -x overlay 2>/dev/null \
    | tail -n +2 \
    | while read -r mount used total; do
        [ -z "$mount" ] && continue
        echo "disk_used_bytes{mount=\"$mount\"}=$used"
        echo "disk_total_bytes{mount=\"$mount\"}=$total"
      done

  # Network RX/TX per iface (skip lo).
  awk 'NR>2 {
        gsub(":", "", $1)
        if ($1 == "lo") next
        if ($1 == "") next
        printf "net_rx_bytes{iface=\"%s\"}=%s\n", $1, $2
        printf "net_tx_bytes{iface=\"%s\"}=%s\n", $1, $10
       }' /proc/net/dev

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
  if [ -f "$bench/sites/apps.txt" ]; then
    grep -cve '^[[:space:]]*$' "$bench/sites/apps.txt" 2>/dev/null || echo 0
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
    sup_lines=$(supervisorctl status 2>/dev/null | grep -E "^${name}[-:]" || true)
    if [ -n "$sup_lines" ]; then
      sup_total=$(printf '%s\n' "$sup_lines" | wc -l)
      sup_running=$(printf '%s\n' "$sup_lines" | grep -c RUNNING || true)
    fi
  fi
  echo "supervisor_running=$sup_running"
  echo "supervisor_total=$sup_total"

  echo
}

# Per-site HTTP probe + emit. Site name is the directory under sites/.
emit_site() {
  local bench_name="$1"
  local site_name="$2"

  echo "###SITE:$bench_name:$site_name"

  # HTTP ping: localhost with Host header so Frappe routes to the site.
  # -m 5 caps each probe at 5s. -w prints "HTTP_CODE TIME_TOTAL_S".
  local out http_code time_s time_ms healthy
  out=$(curl -sS -o /dev/null -m 5 \
        -w '%{http_code} %{time_total}' \
        -H "Host: $site_name" \
        "http://127.0.0.1/api/method/ping" 2>/dev/null || echo "0 0")
  http_code=$(echo "$out" | awk '{print $1}')
  time_s=$(echo "$out" | awk '{print $2}')
  time_ms=$(awk "BEGIN {printf \"%.1f\", ($time_s) * 1000}")

  echo "http_status_code=$http_code"
  echo "http_response_ms=$time_ms"
  if [ "$http_code" = "200" ]; then
    healthy=1
  else
    healthy=0
  fi
  echo "is_healthy=$healthy"

  echo
}

# --- main ---------------------------------------------------------------

emit_meta
emit_server

while IFS= read -r bench_path; do
  [ -n "$bench_path" ] || continue
  [ -d "$bench_path" ] || continue
  bench_name=$(bench_name_for "$bench_path")

  emit_bench "$bench_path" "$bench_name"

  if [ -d "$bench_path/sites" ]; then
    shopt -s nullglob
    for site_dir in "$bench_path/sites"/*/; do
      [ -d "$site_dir" ] || continue
      [ -f "$site_dir/site_config.json" ] || continue
      site_name=$(basename "$site_dir")
      emit_site "$bench_name" "$site_name"
    done
    shopt -u nullglob
  fi
done < <(discover_benches)

echo "###END"
