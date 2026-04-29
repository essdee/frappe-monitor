#!/usr/bin/env bash
# frappe-monitor-collect.sh
#
# Outputs frappe-monitor metric sections to stdout.
# Phase 2 (v1.0.0): server section only. Bench/site sections come in Phase 3.
#
# The version line below is the canonical source of truth — the monitoring
# server reads it (via `head -3`) and re-deploys this script on mismatch.

set -euo pipefail

VERSION="1.0.0"

emit_meta() {
  echo "###META"
  echo "version=$VERSION"
  echo "timestamp=$(date +%s)"
  echo "hostname=$(hostname)"
  echo
}

emit_server() {
  echo "###SERVER"

  # CPU — first line of /proc/stat: aggregate jiffies counters.
  # Layout: cpu user nice system idle iowait irq softirq steal guest guest_nice
  read -r _ user nice system idle iowait irq softirq steal _ < /proc/stat
  echo "cpu_user=$user"
  echo "cpu_nice=$nice"
  echo "cpu_system=$system"
  echo "cpu_idle=$idle"
  echo "cpu_iowait=$iowait"
  echo "cpu_irq=$irq"
  echo "cpu_softirq=$softirq"
  echo "cpu_steal=$steal"

  # Memory — selected fields from /proc/meminfo (kB units).
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

  # Disk — one used/total pair per real filesystem.
  # Skip pseudo filesystems (tmpfs/devtmpfs/squashfs/overlay).
  # Output uses inline label braces; the parser splits these.
  df -B1 --output=target,used,size -x tmpfs -x devtmpfs -x squashfs -x overlay 2>/dev/null \
    | tail -n +2 \
    | while read -r mount used total; do
        [ -z "$mount" ] && continue
        echo "disk_used_bytes{mount=\"$mount\"}=$used"
        echo "disk_total_bytes{mount=\"$mount\"}=$total"
      done

  # Network — RX/TX bytes per interface from /proc/net/dev.
  # Skip the "lo" loopback and headers. Field 2 is rx_bytes; field 10 is tx_bytes.
  awk 'NR>2 {
        gsub(":", "", $1)
        if ($1 == "lo") next
        printf "net_rx_bytes{iface=\"%s\"}=%s\n", $1, $2
        printf "net_tx_bytes{iface=\"%s\"}=%s\n", $1, $10
       }' /proc/net/dev

  echo
}

emit_meta
emit_server
echo "###END"
