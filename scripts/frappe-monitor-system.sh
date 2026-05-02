#!/usr/bin/env bash
# frappe-monitor-system.sh
#
# Outputs a JSON snapshot of the server's "system manager"-style
# state to stdout. Captured on demand by the monitor (POST
# /api/v1/servers/{id}/refresh-system). NOT a metrics pipeline —
# this is a one-shot inventory: OS, kernel, CPU, memory, swap,
# per-mount disk, and the top processes by CPU + RAM.
#
# Requires: bash + python3 (every modern Linux ships it). The bash
# wrapper is here so the monitor can invoke it via the same SSH
# pipeline as the metrics collector — the actual work happens in
# the embedded python3 block.

set -uo pipefail

VERSION="1.0.0"

if ! command -v python3 >/dev/null 2>&1; then
    echo '{"error":"python3 not found on this host — install it for system snapshots"}' >&2
    exit 1
fi

python3 - <<'PY'
import json, os, subprocess, time

VERSION = "1.0.0"

def run(cmd):
    """Run a shell command and return stdout (stripped). Empty on failure."""
    try:
        return subprocess.check_output(
            cmd, shell=True, text=True, stderr=subprocess.DEVNULL
        ).strip()
    except Exception:
        return ""

def read_file(path):
    try:
        with open(path) as f:
            return f.read()
    except Exception:
        return ""

# -------- system identity --------
os_name = ""
osr = read_file("/etc/os-release")
for line in osr.splitlines():
    if line.startswith("PRETTY_NAME="):
        os_name = line.split("=", 1)[1].strip().strip('"')
        break

uptime_s = 0.0
try:
    uptime_s = float(read_file("/proc/uptime").split()[0])
except Exception:
    pass

system = {
    "os":            os_name,
    "kernel":        run("uname -r"),
    "arch":          run("uname -m"),
    "hostname":      run("hostname"),
    "uptime_seconds": int(uptime_s),
    "boot_time":     int(time.time() - uptime_s),
}

# -------- CPU --------
cpu_model = ""
cpuinfo = read_file("/proc/cpuinfo")
for line in cpuinfo.splitlines():
    if line.startswith("model name"):
        cpu_model = line.split(":", 1)[1].strip()
        break

try:
    cores = int(run("nproc") or "0")
except ValueError:
    cores = 0

cpu = {"model": cpu_model, "cores": cores}

# -------- memory + swap (from /proc/meminfo, kB) --------
meminfo = {}
for line in read_file("/proc/meminfo").splitlines():
    if ":" in line:
        k, v = line.split(":", 1)
        v = v.strip().split()
        if v and v[0].isdigit():
            meminfo[k.strip()] = int(v[0]) * 1024  # → bytes

memory = {
    "total_bytes":      meminfo.get("MemTotal", 0),
    "available_bytes":  meminfo.get("MemAvailable", 0),
    "free_bytes":       meminfo.get("MemFree", 0),
    "buffers_bytes":    meminfo.get("Buffers", 0),
    "cached_bytes":     meminfo.get("Cached", 0),
    "swap_total_bytes": meminfo.get("SwapTotal", 0),
    "swap_free_bytes":  meminfo.get("SwapFree", 0),
}

# -------- disks (df, real filesystems only) --------
disks = []
df_out = run("df -B1 -x tmpfs -x devtmpfs -x squashfs -x overlay -x udev -x snapfuse")
for line in df_out.splitlines()[1:]:  # skip header
    parts = line.split(None, 5)
    if len(parts) >= 6:
        try:
            disks.append({
                "device":          parts[0],
                "total_bytes":     int(parts[1]),
                "used_bytes":      int(parts[2]),
                "available_bytes": int(parts[3]),
                "use_pct":         parts[4].rstrip("%"),
                "mount":           parts[5],
            })
        except ValueError:
            continue

# -------- top processes --------
def top_procs(sort_key, limit=10):
    out = run(f"ps -eo pid,user,pcpu,pmem,rss,comm --sort=-{sort_key} | head -n {limit + 1}")
    rows = []
    for line in out.splitlines()[1:]:  # skip header
        parts = line.split(None, 5)
        if len(parts) == 6:
            try:
                rows.append({
                    "pid":      int(parts[0]),
                    "user":     parts[1],
                    "cpu_pct":  float(parts[2]),
                    "mem_pct":  float(parts[3]),
                    "rss_kb":   int(parts[4]),
                    "command":  parts[5],
                })
            except ValueError:
                continue
    return rows

top_cpu = top_procs("pcpu")
top_mem = top_procs("rss")  # sort by resident set size (most-RAM-using)

# -------- load averages (from /proc/loadavg) --------
load = {"1m": 0.0, "5m": 0.0, "15m": 0.0}
try:
    parts = read_file("/proc/loadavg").split()
    load = {"1m": float(parts[0]), "5m": float(parts[1]), "15m": float(parts[2])}
except Exception:
    pass

snapshot = {
    "schema_version": VERSION,
    "captured_at":    int(time.time()),
    "system":         system,
    "cpu":            cpu,
    "memory":         memory,
    "disks":          disks,
    "load":           load,
    "top_cpu":        top_cpu,
    "top_mem":        top_mem,
}

print(json.dumps(snapshot, separators=(",", ":")))
PY
