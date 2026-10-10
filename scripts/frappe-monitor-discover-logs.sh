#!/usr/bin/env bash
# Read-only Frappe log discovery. The monitor pipes this script to `bash -s`
# over SSH; it is never installed on the target.
set -u
export LC_ALL=C

discover_benches() {
  if [ -n "${BENCH_PATHS:-}" ]; then
    # Keep the final newline: a while-read consumer otherwise silently drops
    # the last configured bench path.
    printf '%s\n' "$BENCH_PATHS" | tr ':' '\n'
    return
  fi
  shopt -s nullglob
  local -A seen=()
  for path in /home/*/frappe-bench /home/*/frappe-[0-9]* /home/*/bench-* /opt/bench/*; do
    if [ -d "$path/sites" ] && [ -d "$path/apps" ] && [ -f "$path/Procfile" ]; then
      [ -z "${seen[$path]+present}" ] || continue
      seen["$path"]=1
      printf '%s\n' "$path"
    fi
  done
  shopt -u nullglob
}

safe_field() { printf '%s' "$1" | tr '|\r\n' '___'; }
file_id() {
  local stem checksum
  stem=$(printf '%s' "$1" | sed 's/[^A-Za-z0-9_]/_/g' | tail -c 49)
  checksum=$(cksum <<<"$1" | awk '{print $1}')
  printf 'log_%s_%s' "$stem" "$checksum"
}
log_type() {
  local base="${1##*/}" lower="${1,,}"
  case "$lower" in
    *monitor.json.log*) echo monitor ;;
    *slow*) echo slow-query ;;
    *error*|*stderr*) echo error ;;
    *access*) echo access ;;
    *worker*) echo worker ;;
    *scheduler*) echo scheduler ;;
    *socketio*) echo socketio ;;
    *database*|*mariadb*|*mysql*) echo database ;;
    *) safe_field "${base%.log}" ;;
  esac
}

count=0
while IFS= read -r bench; do
  [ -d "$bench" ] || continue
  bench_name=$(basename "$bench")
  shopt -s nullglob
  for path in "$bench"/logs/*.log "$bench"/sites/*/logs/*.log; do
    [ -f "$path" ] || continue
    site=""
    case "$path" in "$bench"/sites/*/logs/*) site=${path#"$bench/sites/"}; site=${site%%/*} ;; esac
    printf '%s|%s|%s|%s|%s\n' "$(file_id "$path")" "$path" "$(log_type "$path")" "$(safe_field "$bench_name")" "$(safe_field "$site")"
    count=$((count + 1))
    [ "$count" -ge 512 ] && exit 0
  done
  shopt -u nullglob
done < <(discover_benches)

# Database slow-query logs are host-level and therefore have no bench/site
# label until their contents can be correlated to a site database or native
# FRAPPE_TRACE_ID. Only explicitly slow-log-looking, readable files are
# included; the monitor never streams a general database log by default.
shopt -s nullglob
declare -A seen_database_logs=()
for path in \
  /var/log/mysql/*slow*.log \
  /var/log/mariadb/*slow*.log \
  /var/lib/mysql/*slow*.log \
  /var/log/postgresql/*slow*.log \
  /var/lib/postgresql/*/main/log/*slow*.log; do
  [ -f "$path" ] || continue
  [ -r "$path" ] || continue
  [ -z "${seen_database_logs[$path]+present}" ] || continue
  seen_database_logs["$path"]=1
  printf '%s|%s|slow-query||\n' "$(file_id "$path")" "$path"
  count=$((count + 1))
  [ "$count" -ge 512 ] && exit 0
done
shopt -u nullglob
