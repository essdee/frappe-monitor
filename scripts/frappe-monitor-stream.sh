#!/usr/bin/env bash
# frappe-monitor-stream.sh
#
# Long-lived companion to frappe-monitor-collect.sh. Where the
# collector emits a single snapshot per SSH invocation, the streamer
# follows N log files indefinitely and emits each new line over
# stdout, prefixed with the file id so the monitor can route it.
#
# This script is the bench-side half of Phase 8 (per-event capture).
# Design rationale lives in docs/2026-05-07/2.md — read that first.
#
# Hard rules on this script:
#   - bash + coreutils only. No python, no awk-pipelines that aren't
#     line-buffered. Anything that buffers will hide events for minutes.
#   - reads files Frappe / MariaDB ALREADY write. We don't enable any
#     new logging from here. The operator chooses what to tail via
#     --files; this script just reads.
#   - polite under load. renice +10, ionice idle so we never preempt
#     the bench's actual workload.
#
# Wire protocol (stdout, line-oriented, UTF-8):
#
#   First line:           ##V=<semver>
#   Subsequent lines:     ##F=<file_id>|<raw line content>
#   Read errors:          ##F=<file_id> ##E=<error_token>
#
# The monitor parses by splitting on the first '|'. File ids are
# short alphanumeric tokens (e.g. web, err, slowq). Raw line content
# may itself contain '|' — only the FIRST pipe is the protocol delimiter.
#
# Usage:
#   frappe-monitor-stream.sh \
#       --files=web:/home/frappe/frappe-bench/logs/web.log,err:/home/frappe/frappe-bench/logs/web.error.log \
#       --resume=web:12345,err:0
#
# --resume is optional; missing entries default to "tail from end".
# Pass 0 to start from byte 0.

set -uo pipefail

VERSION="8.0.0"

# Self-renice + ionice. Same pattern as the collector — we never
# outrank the bench's own workloads. Failures here are silently
# swallowed; politeness is best-effort, not load-bearing.
renice +10 -p $$ >/dev/null 2>&1 || true
command -v ionice >/dev/null 2>&1 && ionice -c 2 -n 7 -p $$ >/dev/null 2>&1 || true

# ---------------------------------------------------------------------------
# Argument parsing.
# ---------------------------------------------------------------------------

FILES_ARG=""
RESUME_ARG=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --files=*)  FILES_ARG="${1#--files=}"  ;;
    --resume=*) RESUME_ARG="${1#--resume=}" ;;
    --version)
      printf '%s\n' "$VERSION"
      exit 0
      ;;
    -h|--help)
      sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'
      exit 0
      ;;
    *)
      printf 'frappe-monitor-stream: unknown arg: %s\n' "$1" >&2
      exit 2
      ;;
  esac
  shift
done

if [[ -z "$FILES_ARG" ]]; then
  printf 'frappe-monitor-stream: --files is required (e.g. --files=web:/path/web.log,err:/path/err.log)\n' >&2
  exit 2
fi

# Parse --files into parallel arrays IDS[] and PATHS[].
declare -a IDS=()
declare -a PATHS=()
IFS=',' read -ra _PAIRS <<< "$FILES_ARG"
for pair in "${_PAIRS[@]}"; do
  if [[ "$pair" != *:* ]]; then
    printf 'frappe-monitor-stream: bad --files entry %q (want id:path)\n' "$pair" >&2
    exit 2
  fi
  fid="${pair%%:*}"
  fpath="${pair#*:}"
  if [[ ! "$fid" =~ ^[a-zA-Z0-9_]+$ ]]; then
    printf 'frappe-monitor-stream: file id %q must be alphanumeric/underscore\n' "$fid" >&2
    exit 2
  fi
  IDS+=("$fid")
  PATHS+=("$fpath")
done

# Parse --resume=id:offset,id:offset into parallel arrays. bash 3.2 (the
# default /bin/bash on macOS dev machines) has no associative arrays, so
# we keep a flat lookup and resolve each file's offset by id below. Using
# `declare -A` here previously crashed the whole streamer on bash 3.2.
declare -a RIDS=()
declare -a ROFFS=()
if [[ -n "$RESUME_ARG" ]]; then
  IFS=',' read -ra _RPAIRS <<< "$RESUME_ARG"
  for pair in "${_RPAIRS[@]}"; do
    if [[ "$pair" != *:* ]]; then
      printf 'frappe-monitor-stream: bad --resume entry %q (want id:offset)\n' "$pair" >&2
      exit 2
    fi
    fid="${pair%%:*}"
    off="${pair#*:}"
    if ! [[ "$off" =~ ^[0-9]+$ ]]; then
      printf 'frappe-monitor-stream: --resume offset %q for %q must be a non-negative integer\n' "$off" "$fid" >&2
      exit 2
    fi
    RIDS+=("$fid")
    ROFFS+=("$off")
  done
fi

# resume_for echoes the resume offset for a file id, or nothing if the
# id has no --resume entry (the caller then tails from end-of-file).
resume_for() {
  local want="$1" i
  for i in "${!RIDS[@]}"; do
    if [[ "${RIDS[$i]}" == "$want" ]]; then
      printf '%s' "${ROFFS[$i]}"
      return 0
    fi
  done
}

# ---------------------------------------------------------------------------
# Header. The monitor reads this before launching its parser so it can
# detect a stale streamer (version mismatch → redeploy).
# ---------------------------------------------------------------------------

printf '##V=%s\n' "$VERSION"

# ---------------------------------------------------------------------------
# Trap shutdown — clean up child tail processes so the bench doesn't
# leak followers when the SSH session goes away.
# ---------------------------------------------------------------------------

declare -a CHILDREN=()
cleanup() {
  for pid in "${CHILDREN[@]}"; do
    kill -TERM "$pid" 2>/dev/null || true
  done
  # Brief grace period, then SIGKILL anything still hanging on.
  sleep 0.5 || true
  for pid in "${CHILDREN[@]}"; do
    kill -KILL "$pid" 2>/dev/null || true
  done
}
trap cleanup EXIT TERM INT

# ---------------------------------------------------------------------------
# Per-file streamer. Spawned once per --files entry, runs until the
# process is signaled. Emits ##F=<id>|<line> for every new line in the
# tailed file. On resume, starts at byte offset RESUME[id] (default
# tail-from-end via -n 0).
#
# We use 'tail -F' (capital) which follows by name, so logrotate
# moving the file aside and creating a fresh one is followed without
# losing the new file. Resume from byte offset uses 'tail -c +N' which
# means "start from byte N+1". Note: passing -c +1 means "the whole
# file" (byte 1 onwards), which is what we want when RESUME is unset
# AND a caller explicitly asks for full replay (offset=0 → -c +1).
# ---------------------------------------------------------------------------

stream_one() {
  local fid="$1"
  local fpath="$2"
  local offset="$3"

  # File missing at startup? Emit an error sentinel and exit; the
  # monitor will see ##F=fid ##E=MISSING and decide whether to retry
  # later. Don't crash the whole streamer — other files may be fine.
  if [[ ! -e "$fpath" ]]; then
    printf '##F=%s ##E=MISSING\n' "$fid"
    return 0
  fi
  if [[ ! -r "$fpath" ]]; then
    printf '##F=%s ##E=PERMISSION\n' "$fid"
    return 0
  fi

  local tail_args
  if [[ -n "$offset" ]]; then
    # +1-based byte offset: byte position 0 in the file is "before any
    # data", so -c +$((offset+1)) is "starting at byte index `offset`"
    # in 0-based terms. The monitor stores 0-based offsets in
    # log_cursors, so we add 1 here.
    tail_args=(-c "+$((offset + 1))" -F "$fpath")
  else
    # No resume: start from end-of-file ("only new lines from now").
    # The monitor will record the starting offset on first read.
    tail_args=(-n 0 -F "$fpath")
  fi

  # Per-line prefix in a small bash filter. read -r preserves
  # backslashes; IFS= preserves leading/trailing whitespace. The line
  # content is opaque from this script's point of view — we never
  # parse it.
  tail "${tail_args[@]}" 2>/dev/null | while IFS= read -r line; do
    # printf with a single format string is one write(2) call. POSIX
    # guarantees writes <= PIPE_BUF (4096 on Linux) are atomic across
    # concurrent writers, so concurrent stream_one's interleave at the
    # line boundary, not within a line. Lines longer than PIPE_BUF can
    # theoretically tear; in practice Frappe log lines are well under.
    printf '##F=%s|%s\n' "$fid" "$line"
  done

  # If we got here, tail exited (file unreadable, killed, etc.).
  # Surface the cause so the monitor can act.
  printf '##F=%s ##E=TAIL_EXITED\n' "$fid"
}

# ---------------------------------------------------------------------------
# Fan out: one streamer per file, run concurrently, all writing to our
# stdout. We `wait` so the parent stays alive until SIGTERM/EOF; the
# trap then cleans up children.
# ---------------------------------------------------------------------------

for i in "${!IDS[@]}"; do
  stream_one "${IDS[$i]}" "${PATHS[$i]}" "$(resume_for "${IDS[$i]}")" &
  CHILDREN+=("$!")
done

wait
