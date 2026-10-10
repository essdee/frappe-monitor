#!/usr/bin/env bash
# frappe-monitor-stream.sh
#
# Agentless, resumable multi-file log follower. The monitor supplies this
# embedded wrapper through SSH stdin; it is never installed on the target.
# One Python process polls every selected file, avoiding the old one-shell plus
# one-tail-per-file design that became expensive on benches with many site logs.
# Python's standard library is sufficient; no package or target change occurs.
#
# Wire protocol:
#   ##V=<semver>
#   ##O=<file_id>|<device-inode>|<start_offset>
#   ##F=<file_id>|<device-inode>|<exclusive_end_offset>|<line>
#   ##F=<file_id> ##E=<error_token>

set -euo pipefail

VERSION="9.2.0"

PYTHON_BIN=$(command -v python3 || true)
if [ -z "$PYTHON_BIN" ]; then
  # A Frappe bench always has a Python environment, even on minimal targets
  # where python3 is not globally visible. Derive its root from the first log.
  files_arg=""
  for arg in "$@"; do
    case "$arg" in --files=*) files_arg=${arg#--files=} ;; esac
  done
  first_pair=${files_arg%%,*}
  first_path=${first_pair#*:}
  bench_root=""
  case "$first_path" in
    */sites/*/logs/*) bench_root=${first_path%%/sites/*} ;;
    */logs/*) bench_root=${first_path%%/logs/*} ;;
  esac
  if [ -n "$bench_root" ] && [ -x "$bench_root/env/bin/python" ]; then
    PYTHON_BIN="$bench_root/env/bin/python"
  fi
fi
if [ -z "$PYTHON_BIN" ]; then
  printf 'frappe-monitor-stream: python3 or a bench env/bin/python is required\n' >&2
  exit 127
fi

# Keep the single follower below application workloads under contention.
renice +10 -p $$ >/dev/null 2>&1 || true
command -v ionice >/dev/null 2>&1 && ionice -c 3 -p $$ >/dev/null 2>&1 || true

exec "$PYTHON_BIN" -u - "$@" <<'PY'
import os
import re
import sys
import time

VERSION = "9.2.0"
POLL_SECONDS = 0.25
READ_SIZE = 65536
MAX_PENDING = 65536


def fail(message):
    print("frappe-monitor-stream: " + message, file=sys.stderr, flush=True)
    raise SystemExit(2)


files_arg = ""
resume_arg = ""
for arg in sys.argv[1:]:
    if arg.startswith("--files="):
        files_arg = arg[len("--files="):]
    elif arg.startswith("--resume="):
        resume_arg = arg[len("--resume="):]
    elif arg == "--version":
        print(VERSION)
        raise SystemExit(0)
    elif arg in ("-h", "--help"):
        print("usage: frappe-monitor-stream.sh --files=id:/absolute/path[,id:path] [--resume=id:device-inode:offset]")
        raise SystemExit(0)
    else:
        fail("unknown arg: " + arg)

if not files_arg:
    fail("--files is required (want id:path)")

files = []
for pair in files_arg.split(","):
    if ":" not in pair:
        fail("bad --files entry %r (want id:path)" % pair)
    file_id, path = pair.split(":", 1)
    if not re.fullmatch(r"[A-Za-z0-9_-]+", file_id):
        fail("file id %r must be alphanumeric, underscore, or hyphen" % file_id)
    if not path:
        fail("path for %r is empty" % file_id)
    files.append((file_id, path))

resume = {}
if resume_arg:
    for pair in resume_arg.split(","):
        parts = pair.split(":", 2)
        if len(parts) != 3:
            fail("bad --resume entry %r (want id:identity:offset)" % pair)
        file_id, identity, raw_offset = parts
        if not re.fullmatch(r"[A-Za-z0-9_-]+", identity):
            fail("--resume identity %r for %r is invalid" % (identity, file_id))
        if not raw_offset.isdigit():
            fail("--resume offset %r for %r must be a non-negative integer" % (raw_offset, file_id))
        resume[file_id] = (identity, int(raw_offset))


def identity_for(stat_result):
    return "%d-%d" % (stat_result.st_dev, stat_result.st_ino)


class FileFollower:
    def __init__(self, file_id, path):
        self.file_id = file_id
        self.path = path
        self.fd = None
        self.identity = ""
        self.read_offset = 0
        self.pending = b""
        self.pending_start = 0
        self.initialized = False
        self.last_error = ""

    def emit_error(self, token):
        if self.last_error != token:
            print("##F=%s ##E=%s" % (self.file_id, token), flush=True)
            self.last_error = token

    def close(self):
        if self.fd is not None:
            try:
                os.close(self.fd)
            except OSError:
                pass
        self.fd = None

    def flush_partial(self):
        if not self.pending or not self.identity:
            self.pending = b""
            return
        end = self.pending_start + len(self.pending)
        content = self.pending.rstrip(b"\r").decode("utf-8", "replace")
        print("##F=%s|%s|%d|%s" % (self.file_id, self.identity, end, content), flush=True)
        self.pending = b""
        self.pending_start = end

    def open_current(self, start_at_zero=False):
        try:
            stat_result = os.stat(self.path)
            fd = os.open(self.path, os.O_RDONLY)
        except FileNotFoundError:
            self.emit_error("MISSING")
            return False
        except PermissionError:
            self.emit_error("PERMISSION")
            return False
        except OSError:
            self.emit_error("OPEN")
            return False

        identity = identity_for(stat_result)
        if start_at_zero:
            offset = 0
        elif not self.initialized:
            previous = resume.get(self.file_id)
            offset = min(previous[1], stat_result.st_size) if previous and previous[0] == identity else stat_result.st_size
        else:
            offset = 0

        self.close()
        self.fd = fd
        self.identity = identity
        self.read_offset = offset
        self.pending = b""
        self.pending_start = offset
        self.initialized = True
        self.last_error = ""
        os.lseek(fd, offset, os.SEEK_SET)
        print("##O=%s|%s|%d" % (self.file_id, identity, offset), flush=True)
        return True

    def emit_complete_lines(self):
        while True:
            newline = self.pending.find(b"\n")
            if newline < 0:
                break
            raw = self.pending[:newline]
            consumed = newline + 1
            end = self.pending_start + consumed
            content = raw.rstrip(b"\r").decode("utf-8", "replace")
            print("##F=%s|%s|%d|%s" % (self.file_id, self.identity, end, content), flush=True)
            self.pending = self.pending[consumed:]
            self.pending_start = end
        # Bound a pathological unterminated line without dropping its bytes.
        if len(self.pending) > MAX_PENDING:
            raw = self.pending[:MAX_PENDING]
            end = self.pending_start + len(raw)
            print("##F=%s|%s|%d|%s" % (
                self.file_id, self.identity, end, raw.decode("utf-8", "replace")
            ), flush=True)
            self.pending = self.pending[MAX_PENDING:]
            self.pending_start = end

    def poll(self):
        try:
            stat_result = os.stat(self.path)
        except FileNotFoundError:
            self.close()
            self.emit_error("MISSING")
            return
        except PermissionError:
            self.close()
            self.emit_error("PERMISSION")
            return
        except OSError:
            self.close()
            self.emit_error("STAT")
            return

        current_identity = identity_for(stat_result)
        if self.fd is None:
            if not self.open_current():
                return
        elif current_identity != self.identity:
            self.flush_partial()
            if not self.open_current(start_at_zero=True):
                return
        elif stat_result.st_size < self.read_offset:
            self.flush_partial()
            if not self.open_current(start_at_zero=True):
                return

        while True:
            try:
                chunk = os.read(self.fd, READ_SIZE)
            except PermissionError:
                self.close()
                self.emit_error("PERMISSION")
                return
            except OSError:
                self.close()
                self.emit_error("READ")
                return
            if not chunk:
                break
            if not self.pending:
                self.pending_start = self.read_offset
            self.read_offset += len(chunk)
            self.pending += chunk
            self.emit_complete_lines()


followers = [FileFollower(file_id, path) for file_id, path in files]
print("##V=" + VERSION, flush=True)
try:
    while True:
        for follower in followers:
            follower.poll()
        time.sleep(POLL_SECONDS)
except (KeyboardInterrupt, SystemExit):
    pass
finally:
    for follower in followers:
        follower.close()
PY
