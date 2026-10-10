#!/usr/bin/env bash
# frappe-monitor-collect.sh
#
# Outputs frappe-monitor metric sections to stdout.
# Emits server + per-bench + per-site sections. Live logs use the separate
# resumable frappe-monitor-stream.sh protocol; this script emits metrics only.
#
# Bench discovery: BENCH_PATHS env var (colon-separated) overrides
# auto-discovery; otherwise we look for common names such as frappe-bench,
# versioned frappe-15/frappe-16 directories, bench-*, and /opt/bench/*.
#
# The monitor sends this embedded script over SSH standard input for each
# collection. Nothing is installed or changed on the monitored server.

set -euo pipefail

VERSION="3.1.0"

# Hard wall-clock budget for the WHOLE site-probe loop, regardless of
# how many sites the bench has. SSH's command timeout is typically
# 30s; we leave ~10s headroom for emit_meta + emit_server + per-bench
# work so a bench with 30+ sites doesn't get killed mid-loop and lose
# the trailing ###END marker. Sites past the budget are emitted with
# is_healthy=-1 + http_status_code=0 — unknown, not a false outage.
SITE_PROBE_BUDGET_S="${SITE_PROBE_BUDGET_S:-20}"
SITE_PROBE_CONCURRENCY="${SITE_PROBE_CONCURRENCY:-8}"
if ! [[ "$SITE_PROBE_CONCURRENCY" =~ ^[1-9][0-9]*$ ]]; then
  SITE_PROBE_CONCURRENCY=8
fi

PROBE_TMPDIR=$(mktemp -d "${TMPDIR:-/tmp}/frappe-monitor-probes.XXXXXX")
cleanup_probe_tmp() {
  rm -rf -- "$PROBE_TMPDIR"
}
trap cleanup_probe_tmp EXIT

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

  # Disk used/total per filesystem (skip pseudo).
  df -B1 --output=target,used,size -x tmpfs -x devtmpfs -x squashfs -x overlay 2>/dev/null \
    | tail -n +2 \
    | while read -r mount used total; do
        [ -z "$mount" ] && continue
        printf 'disk_used_bytes{mount="%s"}=%s\n' "$(escape_label_value "$mount")" "$used"
        printf 'disk_total_bytes{mount="%s"}=%s\n' "$(escape_label_value "$mount")" "$total"
      done

  # Network RX/TX per iface (skip lo). Bash formatting lets us escape the
  # interface name with the same label grammar as disk mounts.
  while IFS=: read -r iface counters; do
    iface=${iface//[[:space:]]/}
    [ -n "$iface" ] || continue
    [ "$iface" != "lo" ] || continue
    read -r rx _ _ _ _ _ _ _ tx _ <<< "$counters"
    [ -n "${rx:-}" ] && [ -n "${tx:-}" ] || continue
    printf 'net_rx_bytes{iface="%s"}=%s\n' "$(escape_label_value "$iface")" "$rx"
    printf 'net_tx_bytes{iface="%s"}=%s\n' "$(escape_label_value "$iface")" "$tx"
  done < <(tail -n +3 /proc/net/dev)

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
  local -A seen=()
  for p in /home/*/frappe-bench /home/*/frappe-[0-9]* /home/*/bench-* /opt/bench/*; do
    if [ -d "$p/sites" ] && [ -d "$p/apps" ] && [ -f "$p/Procfile" ]; then
      [ -z "${seen[$p]+present}" ] || continue
      seen["$p"]=1
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

escape_label_value() {
  local value="$1"
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  printf '%s' "$value"
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

service_kind() {
  local value="${1,,}"
  case "$value" in
    *redis*queue*) echo "redis-queue" ;;
    *redis*cache*) echo "redis-cache" ;;
    *redis*socketio*) echo "redis-socketio" ;;
    *socketio*) echo "socketio" ;;
    *eventdispatcher*) echo "event-dispatcher" ;;
    *jsonworker*) echo "json-worker" ;;
    *watch*) echo "watch" ;;
    *schedule*) echo "scheduler" ;;
    *worker*) echo "worker" ;;
    *web*|*gunicorn*|*serve*) echo "web" ;;
    *) echo "other" ;;
  esac
}

worker_queue() {
  local value="${1,,}"
  case "$value" in
    *short*) echo "short" ;;
    *long*) echo "long" ;;
    *default*) echo "default" ;;
    *) echo "all" ;;
  esac
}

emit_service() {
  local service="$1" state="$2" pid="$3" kind="$4" queue="$5"
  local cpu="0" mem="0" rss_kb="0" uptime="0"
  if [[ "$pid" =~ ^[0-9]+$ ]] && [ "$pid" -gt 0 ]; then
    read -r cpu mem rss_kb uptime < <(ps -p "$pid" -o pcpu=,pmem=,rss=,etimes= 2>/dev/null || echo "0 0 0 0")
  else
    pid=0
  fi
  local labels
  labels="service=\"$(escape_label_value "$service")\",kind=\"$(escape_label_value "$kind")\",queue=\"$(escape_label_value "$queue")\""
  printf 'service_state{%s}=%s\n' "$labels" "$state"
  printf 'service_pid{%s}=%s\n' "$labels" "$pid"
  printf 'service_cpu_percent{%s}=%s\n' "$labels" "${cpu:-0}"
  printf 'service_memory_percent{%s}=%s\n' "$labels" "${mem:-0}"
  printf 'service_rss_bytes{%s}=%s\n' "$labels" "$(( ${rss_kb:-0} * 1024 ))"
  printf 'service_uptime_seconds{%s}=%s\n' "$labels" "${uptime:-0}"
}

emit_process_services() {
  local bench="$1"
  local -A indexes=()
  while read -r pid ppid comm args; do
    [ -n "${pid:-}" ] || continue
    case "$comm" in sh|bash|rg|grep) continue ;; esac
    # Procfile commands commonly use relative paths (redis-server
    # config/redis_queue.conf, node apps/frappe/socketio.js), so their argv
    # does not identify the bench. Their cwd does; accept either signal.
    local cwd
    cwd=$(readlink -f "/proc/$pid/cwd" 2>/dev/null || true)
    if [[ "$args" != *"$bench"* ]] && [[ "$cwd" != "$bench" && "$cwd" != "$bench/"* ]]; then
      continue
    fi
    local parent_args process_context
    parent_args=$(ps -p "$ppid" -o args= 2>/dev/null || true)
    process_context="$args $parent_args"
    local kind=""
    case "${args,,}" in
      *"frappe.utils.bench_helper frappe worker"*|*"bench worker"*) kind="worker" ;;
      *"socketio.js"*) kind="socketio" ;;
      *"frappe.utils.bench_helper frappe watch"*|*"bench watch"*) kind="watch" ;;
      *"frappe.utils.bench_helper frappe"*" eventdispatcher "*|*"bench"*" eventdispatcher "*) kind="event-dispatcher" ;;
      *"frappe.utils.bench_helper frappe"*" jsonworker "*|*"bench"*" jsonworker "*) kind="json-worker" ;;
      *"frappe.utils.bench_helper frappe schedule"*|*"bench schedule"*) kind="scheduler" ;;
      *"redis-server"*"redis_queue"*) kind="redis-queue" ;;
      *"redis-server"*"redis_cache"*) kind="redis-cache" ;;
      *"redis-server"*"redis_socketio"*) kind="redis-socketio" ;;
      *gunicorn*)
        if [[ "${parent_args,,}" == *gunicorn* ]] && [[ "$parent_args" == *"$bench"* ]]; then
          kind="web-worker"
        else
          kind="web-master"
        fi
        ;;
      *"frappe.utils.bench_helper frappe serve"*|*"bench serve"*) kind="web" ;;
      *)
        # redis-server rewrites argv to only its listen address; the parent
        # Procfile shell still carries the config filename that identifies
        # cache vs queue.
        case "${parent_args,,}" in
          *"redis-server"*"redis_queue"*) kind="redis-queue" ;;
          *"redis-server"*"redis_cache"*) kind="redis-cache" ;;
          *"redis-server"*"redis_socketio"*) kind="redis-socketio" ;;
          *) continue ;;
        esac
        ;;
    esac

    # Werkzeug's development reloader keeps one parent process and starts a
    # child with the same `frappe serve` command. They form one Procfile web
    # service, not two available web workers. Keep the serving leaf process so
    # its CPU/RSS are reported without inflating capacity in the dashboard.
    if [ "$kind" = "web" ] && [[ "${args,,}" == *"frappe.utils.bench_helper frappe serve"* ]]; then
      if ps --ppid "$pid" -o args= 2>/dev/null \
        | grep -Fq "frappe.utils.bench_helper frappe serve"; then
        continue
      fi
    fi

    # An RQ worker forks a short-lived work-horse process while executing a
    # job. The child inherits the worker command line, but it is not another
    # unit of queue capacity. RQ's registry reports the parent worker; omit
    # inherited children from the service inventory to keep both views equal.
    if [ "$kind" = "worker" ] && [[ "${parent_args,,}" == *"frappe.utils.bench_helper frappe worker"* ]]; then
      continue
    fi

    indexes[$kind]=$(( ${indexes[$kind]:-0} + 1 ))
    local service="$kind-${indexes[$kind]}"
    local queue="all"
    [ "$kind" = "worker" ] && queue=$(worker_queue "$process_context")
    emit_service "$service" 1 "$pid" "$kind" "$queue"
  # Pre-filter before /proc cwd inspection; large shared hosts may have
  # thousands of unrelated processes and probing each one would inflate the
  # collection budget.
  done < <(ps -eo pid=,ppid=,comm=,args= 2>/dev/null \
    | grep -Ei '(bench|frappe|gunicorn|redis-server|socketio\.js)' \
    | grep -Ev '[g]rep -E' || true)
}

read_common_config_number() {
  local bench="$1" key="$2"
  local conf="$bench/sites/common_site_config.json"
  if [ -f "$conf" ] && command -v python3 >/dev/null 2>&1; then
    python3 -c 'import json,sys; value=json.load(open(sys.argv[1])).get(sys.argv[2], 0); print(value if isinstance(value, int) and value >= 0 else 0)' "$conf" "$key" 2>/dev/null || echo 0
  else
    echo 0
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
    printf 'info{frappe_version="%s"}=1\n' "$(escape_label_value "$fv")"
  fi

  echo "apps_count=$(read_apps_count "$bench")"
  echo "web_workers_configured=$(read_common_config_number "$bench" gunicorn_workers)"
  echo "background_workers_configured=$(read_common_config_number "$bench" background_workers)"

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
    printf 'redis_queue_depth{queue="%s"}=%s\n' "$(escape_label_value "$q")" "$depth"
  done

  # Supervisor process counts scoped to this bench (the "$name:" prefix
  # in supervisorctl output is conventional). All zeros if supervisorctl
  # isn't installed — same "loud zero" rationale as redis above.
  local sup_running=0 sup_total=0
  if command -v supervisorctl >/dev/null 2>&1; then
    local sup_lines
    sup_lines=$(timeout 4 supervisorctl status 2>/dev/null | grep -E "^${name}[-:]" || true)
    if [ -n "$sup_lines" ]; then
      sup_total=$(printf '%s\n' "$sup_lines" | wc -l)
      sup_running=$(printf '%s\n' "$sup_lines" | grep -c RUNNING || true)
			while read -r service state rest; do
				[ -n "${service:-}" ] || continue
				local state_code=4 pid=0 kind queue
				case "$state" in RUNNING) state_code=1 ;; STARTING) state_code=2 ;; STOPPED|EXITED|FATAL|BACKOFF) state_code=3 ;; esac
				pid=$(printf '%s' "$rest" | sed -n 's/.*pid \([0-9][0-9]*\).*/\1/p')
				kind=$(service_kind "$service")
				queue="all"; [ "$kind" = "worker" ] && queue=$(worker_queue "$service")
				emit_service "$service" "$state_code" "${pid:-0}" "$kind" "$queue"
			done <<< "$sup_lines"
    fi
  fi
  echo "supervisor_running=$sup_running"
  echo "supervisor_total=$sup_total"

	# Enumerate the actual process tree even when Supervisor is available.
	# Supervisor reports service groups; process inspection reveals individual
	# Gunicorn children and queue workers, which is the capacity operators need.
	emit_process_services "$bench"

	# RQ maintains a registry of workers in Redis. This is stronger evidence
	# than CPU heuristics for answering whether capacity is free or saturated.
	local workers_registered=0 workers_busy=0 workers_idle=0
	if [ -n "$rport" ] && command -v redis-cli >/dev/null 2>&1; then
		local worker_key worker_state current_job
		while IFS= read -r worker_key; do
			[ -n "$worker_key" ] || continue
			workers_registered=$((workers_registered + 1))
			worker_state=$(redis-cli -p "$rport" --raw HGET "$worker_key" state 2>/dev/null || true)
			current_job=$(redis-cli -p "$rport" --raw HGET "$worker_key" current_job 2>/dev/null || true)
			if [ "$worker_state" = "busy" ] || [ -n "$current_job" ]; then
				workers_busy=$((workers_busy + 1))
			else
				workers_idle=$((workers_idle + 1))
			fi
		done < <(redis-cli -p "$rport" --raw SMEMBERS rq:workers 2>/dev/null || true)
	fi
	echo "workers_registered=$workers_registered"
	echo "workers_busy=$workers_busy"
	echo "workers_idle=$workers_idle"

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
    p=$(python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); print(d.get("webserver_port", ""))' "$conf" 2>/dev/null || echo "")
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
# http_status_code=0 / is_healthy=-1. Used by the main loop when the
# wall-clock budget is exceeded — without this guard, a bench with
# more sites than fit in the budget gets its remaining sites probed
# anyway, the SSH timeout fires, and the parser sees torn output.
emit_site() {
  local bench_name="$1"
  local site_name="$2"
  local port="$3"
  local mode="${4:-probe}"
  local native_monitor_enabled="${5:-0}"
  local database_hash="${6:-}"
  local database_user_hash="${7:-}"

  echo "###SITE:$bench_name:$site_name"

  local out http_code time_s time_ms healthy failure_reason
  if [ "$mode" = "skip" ]; then
    http_code=0
    time_ms=0
    healthy=-1
    failure_reason=4
  elif [ "$port" = "0" ]; then
    http_code=0
    time_ms=0
    healthy=0
    failure_reason=5
  else
    if ! out=$(curl -sS -o /dev/null -m 2 \
          -w '%{http_code} %{time_total}' \
          -H "Host: $site_name" \
          "http://127.0.0.1:${port}/api/method/ping" 2>/dev/null); then
      out="000 0"
    fi
    http_code=$(echo "$out" | awk '{print $1}')
    time_s=$(echo "$out" | awk '{print $2}')
    time_ms=$(awk "BEGIN {printf \"%.1f\", ($time_s) * 1000}")
    case "$http_code" in
      ''|*[!0-9]*) healthy=0; failure_reason=6 ;;
      000) healthy=0; failure_reason=1 ;;
      *)
        if [ "$http_code" -ge 200 ] && [ "$http_code" -lt 400 ]; then
          healthy=1; failure_reason=0
        elif [ "$http_code" -ge 400 ] && [ "$http_code" -lt 500 ]; then
          healthy=0; failure_reason=2
        elif [ "$http_code" -ge 500 ] && [ "$http_code" -lt 600 ]; then
          healthy=0; failure_reason=3
        else
          healthy=0; failure_reason=6
        fi
        ;;
    esac
  fi

  echo "http_port=$port"
  echo "http_status_code=$http_code"
  echo "http_response_ms=$time_ms"
  echo "is_healthy=$healthy"
  echo "failure_reason_code=$failure_reason"
  echo "native_monitor_enabled=$native_monitor_enabled"
  echo "database_hash=$database_hash"
  echo "database_user_hash=$database_user_hash"

  echo
}

# Print site|monitor-enabled|database-hash|database-user-hash. This is one
# bounded Python process per bench (not per site) and only reads config. Raw
# database identities and credentials never leave the host.
read_site_observability_settings() {
  local bench_path="$1"
  local python_bin=""
  if [ -x "$bench_path/env/bin/python" ]; then
    python_bin="$bench_path/env/bin/python"
  elif command -v python3 >/dev/null 2>&1; then
    python_bin="$(command -v python3)"
  else
    return 0
  fi
  "$python_bin" - "$bench_path" <<'PY' 2>/dev/null || true
import hashlib
import json
import pathlib
import sys

bench = pathlib.Path(sys.argv[1])
sites = bench / "sites"

def read_json(path):
    try:
        with path.open(encoding="utf-8") as handle:
            value = json.load(handle)
            return value if isinstance(value, dict) else {}
    except (OSError, ValueError):
        return {}

def enabled(value):
    if isinstance(value, str):
        return value.strip().lower() not in {"", "0", "false", "no", "off", "none", "null"}
    return bool(value)

def identity_hash(value):
    value = str(value or "").strip()
    return hashlib.sha256(value.encode()).hexdigest()[:24] if value else ""

common = read_json(sites / "common_site_config.json")
default = common.get("monitor", 0)
try:
    children = sorted(sites.iterdir(), key=lambda item: item.name)
except OSError:
    children = []
for child in children:
    config_path = child / "site_config.json"
    if not child.is_dir() or not config_path.is_file():
        continue
    site_config = read_json(config_path)
    database = site_config.get("db_name", common.get("db_name", ""))
    database_user = site_config.get("db_user", common.get("db_user", database)) or database
    print(
        f"{child.name}|{1 if enabled(site_config.get('monitor', default)) else 0}"
        f"|{identity_hash(database)}|{identity_hash(database_user)}"
    )
PY
}

# --- main ---------------------------------------------------------------

emit_meta
emit_server

# Mark the start of the site-probe phase so the loop can self-throttle
# against SITE_PROBE_BUDGET_S below. We don't include emit_meta /
# emit_server in the budget — those are bounded and small.
PROBE_START_TS="$(date +%s)"
PROBE_FILE_SEQ=0

while IFS= read -r bench_path; do
  [ -n "$bench_path" ] || continue
  [ -d "$bench_path" ] || continue
  bench_name=$(bench_name_for "$bench_path")

  emit_bench "$bench_path" "$bench_name"

  if [ -d "$bench_path/sites" ]; then
    shopt -s nullglob
    unset native_monitor_by_site
    declare -A native_monitor_by_site=()
    unset database_hash_by_site database_user_hash_by_site
    declare -A database_hash_by_site=()
    declare -A database_user_hash_by_site=()
    while IFS='|' read -r monitor_site monitor_enabled database_hash database_user_hash; do
      [ -n "$monitor_site" ] || continue
      native_monitor_by_site["$monitor_site"]="${monitor_enabled:-0}"
      database_hash_by_site["$monitor_site"]="${database_hash:-}"
      database_user_hash_by_site["$monitor_site"]="${database_user_hash:-}"
    done < <(read_site_observability_settings "$bench_path")

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

    probe_files=()
    active_probes=0
    for site_dir in "$bench_path/sites"/*/; do
      [ -d "$site_dir" ] || continue
      [ -f "$site_dir/site_config.json" ] || continue
      site_name=$(basename "$site_dir")
      native_monitor_enabled="${native_monitor_by_site[$site_name]:-0}"
      database_hash="${database_hash_by_site[$site_name]:-}"
      database_user_hash="${database_user_hash_by_site[$site_name]:-}"
      probe_file="$PROBE_TMPDIR/probe-$PROBE_FILE_SEQ"
      PROBE_FILE_SEQ=$((PROBE_FILE_SEQ + 1))
      probe_files+=("$probe_file")
      now_ts="$(date +%s)"
      elapsed=$((now_ts - PROBE_START_TS))
      if [ "$elapsed" -ge "$SITE_PROBE_BUDGET_S" ]; then
        # Budget blown — emit the site shape with mode=skip so the
        # parser still gets a complete section instead of the loop
        # being killed by SSH and producing torn output.
        emit_site "$bench_name" "$site_name" "$bench_port" "skip" "$native_monitor_enabled" "$database_hash" "$database_user_hash" >"$probe_file"
      else
        emit_site "$bench_name" "$site_name" "$bench_port" "probe" "$native_monitor_enabled" "$database_hash" "$database_user_hash" >"$probe_file" &
        active_probes=$((active_probes + 1))
        if [ "$active_probes" -ge "$SITE_PROBE_CONCURRENCY" ]; then
          wait -n
          active_probes=$((active_probes - 1))
        fi
      fi
    done
    wait
    for probe_file in "${probe_files[@]}"; do
      cat -- "$probe_file"
    done
    shopt -u nullglob
  fi
done < <(discover_benches)

echo "###END"
