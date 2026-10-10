#!/usr/bin/env bash
# Read-only production diagnostic report for Frappe Monitor.
#
# Usage:
#   sudo ./deploy/diagnose.sh
#   sudo ./deploy/diagnose.sh /path/to/monitor.yaml
#
# The report never prints monitor.yaml, SSH keys, dashboard passwords, backend
# URLs, or Telegram credentials. Redirect stdout to a file when sharing it.

set -u

CONFIG_PATH="${1:-/etc/frappe-monitor/monitor.yaml}"
MONITOR_BIN="/usr/local/bin/frappe-monitor"

section() { printf '\n==> %s\n' "$1"; }
check() {
    local label="$1"
    shift
    if "$@" >/dev/null 2>&1; then
        printf '  ok: %s\n' "$label"
    else
        printf '  FAIL: %s\n' "$label"
    fi
}

section "identity"
printf '  generated_at: %s\n' "$(date -Iseconds)"
printf '  hostname: %s\n' "$(hostname)"
printf '  kernel: %s\n' "$(uname -srmo)"
printf '  config_path: %s\n' "$CONFIG_PATH"

section "configuration"
if [ -x "$MONITOR_BIN" ] && [ -f "$CONFIG_PATH" ]; then
    if output=$("$MONITOR_BIN" --config "$CONFIG_PATH" --check-config 2>&1); then
        printf '  ok: candidate/runtime configuration is valid\n'
    else
        printf '  FAIL: configuration validation: %s\n' "$output"
    fi
else
    [ -x "$MONITOR_BIN" ] || printf '  FAIL: %s is missing or not executable\n' "$MONITOR_BIN"
    [ -f "$CONFIG_PATH" ] || printf '  FAIL: %s is missing\n' "$CONFIG_PATH"
fi
if [ -e "$CONFIG_PATH" ]; then
    stat -c '  config_mode: %A %U:%G (%a)' "$CONFIG_PATH" 2>/dev/null || true
fi

section "systemd"
for unit in frappe-monitor.service frappe-monitor-stack.service; do
    state=$(systemctl is-active "$unit" 2>/dev/null || true)
    enabled=$(systemctl is-enabled "$unit" 2>/dev/null || true)
    printf '  %s: active=%s enabled=%s\n' "$unit" "${state:-unknown}" "${enabled:-unknown}"
done
systemctl show frappe-monitor.service \
    -p MainPID -p MemoryCurrent -p MemoryPeak -p CPUUsageNSec -p TasksCurrent \
    --no-pager 2>/dev/null | sed 's/^/  /' || true

section "health"
listen_addr=$(awk '
    /^server:[[:space:]]*$/ { in_server=1; next }
    in_server && /^[^[:space:]]/ { in_server=0 }
    in_server && /^[[:space:]]+listen_addr:/ {
        sub(/^[[:space:]]+listen_addr:[[:space:]]*/, "")
        gsub(/^["'\'' ]+|["'\'' ]+$/, "")
        print
        exit
    }
' "$CONFIG_PATH" 2>/dev/null || true)
case "$listen_addr" in
    :*) monitor_health_url="http://127.0.0.1:${listen_addr#:}/healthz" ;;
    0.0.0.0:*) monitor_health_url="http://127.0.0.1:${listen_addr##*:}/healthz" ;;
    \[::\]:*) monitor_health_url="http://127.0.0.1:${listen_addr##*:}/healthz" ;;
    \[*\]:*) monitor_health_url="http://${listen_addr}/healthz" ;;
    *:*) monitor_health_url="http://${listen_addr}/healthz" ;;
    *) monitor_health_url="" ;;
esac
if [ -n "$monitor_health_url" ]; then
    check "monitor /healthz from YAML listen_addr" curl -fsS --max-time 3 "$monitor_health_url"
else
    printf '  FAIL: could not derive monitor health endpoint from YAML listen_addr\n'
fi
if systemctl is-enabled --quiet frappe-monitor-stack.service 2>/dev/null \
    || systemctl is-active --quiet frappe-monitor-stack.service 2>/dev/null; then
    check "VictoriaMetrics bundled endpoint" curl -fsS --max-time 3 http://127.0.0.1:8428/health
    check "Loki bundled endpoint" curl -fsS --max-time 3 http://127.0.0.1:3100/ready
else
    printf '  note: bundled VM/Loki checks skipped (external --no-stack tier may be configured)\n'
fi

section "storage"
df -h /var/lib/frappe-monitor /opt/frappe-monitor 2>/dev/null | sed 's/^/  /' || true
du -sh /var/lib/frappe-monitor /opt/frappe-monitor 2>/dev/null | sed 's/^/  /' || true
if command -v sqlite3 >/dev/null 2>&1 && [ -f /var/lib/frappe-monitor/monitor.db ]; then
    integrity=$(sqlite3 /var/lib/frappe-monitor/monitor.db 'PRAGMA quick_check;' 2>&1 || true)
    if [ "$integrity" = "ok" ]; then
        printf '  ok: SQLite quick_check\n'
    else
        printf '  FAIL: SQLite quick_check: %s\n' "$integrity"
    fi
else
    printf '  note: SQLite quick_check skipped (sqlite3 or database missing)\n'
fi

section "containers"
if command -v docker >/dev/null 2>&1; then
    check "Docker daemon" docker info
    docker ps --filter name=frappe-monitor \
        --format '  {{.Names}} status={{.Status}} image={{.Image}}' 2>/dev/null || true
else
    printf '  note: Docker CLI not installed (valid for --no-stack deployments)\n'
fi

section "recent monitor errors"
journalctl -u frappe-monitor.service --since '1 hour ago' -p warning \
    --no-pager -n 80 -o short-iso 2>/dev/null | sed 's/^/  /' || true

printf '\nDiagnostic report complete. No configuration values or credentials were printed.\n'
