#!/usr/bin/env bash
# Production install for frappe-monitor.
#
# Idempotent: safe to re-run for upgrades. Re-running:
#   - rebuilds the binary from the current checkout
#   - replaces /usr/local/bin/frappe-monitor (atomic mv)
#   - reinstalls systemd units (only if changed)
#   - leaves /etc/frappe-monitor/monitor.yaml + /var/lib/frappe-monitor alone
#   - restarts frappe-monitor.service so the new binary takes effect
#
# Usage:
#   sudo ./deploy/install.sh                # full install or upgrade
#   sudo ./deploy/install.sh --no-stack     # skip VM+Loki bring-up (split-tier deploys)
#   sudo ./deploy/install.sh --uninstall    # stop, disable, and remove (keeps data)
#
# Layout produced:
#   /usr/local/bin/frappe-monitor              binary (CGO=0, embedded SPA)
#   /etc/frappe-monitor/monitor.yaml           config (created from example on first install)
#   /var/lib/frappe-monitor/                   SQLite DB + per-server state
#   /opt/frappe-monitor/                       repo checkout (for compose + upgrade)
#   /etc/systemd/system/frappe-monitor.service        the binary
#   /etc/systemd/system/frappe-monitor-stack.service  VM + Loki via compose
#
# Requires: bash, sudo/root, Linux with systemd, Go ≥ 1.25, Node ≥ 20,
# npm, Docker ≥ 24 with the compose plugin. The script verifies each
# and bails with a copy-pasteable install hint if anything is missing.
#
# Exits 0 on success, non-zero with a "FAIL:" line on failure.

set -euo pipefail

# ---------------------------------------------------------------------------
# Args + paths
# ---------------------------------------------------------------------------
SKIP_STACK=0
UNINSTALL=0
for arg in "$@"; do
    case "$arg" in
        --no-stack) SKIP_STACK=1 ;;
        --uninstall) UNINSTALL=1 ;;
        -h|--help)
            sed -n '2,/^$/p' "$0" | sed 's/^# \?//'
            exit 0
            ;;
        *) echo "FAIL: unknown arg: $arg (use --help)" >&2; exit 2 ;;
    esac
done

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
INSTALL_USER="frappe-monitor"
INSTALL_GROUP="frappe-monitor"
BIN_DST="/usr/local/bin/frappe-monitor"
ETC_DIR="/etc/frappe-monitor"
STATE_DIR="/var/lib/frappe-monitor"
OPT_DIR="/opt/frappe-monitor"
SYSTEMD_DIR="/etc/systemd/system"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
note() { echo "  >> $*"; }

[ "$(id -u)" -eq 0 ] || fail "must run as root (use sudo)"

# sudo strips PATH and replaces it with secure_path from /etc/sudoers,
# which usually doesn't include /usr/local/go/bin or per-user shim
# directories. This is the most common first-run failure: "go not
# found" even though the invoking user has it installed. Augment PATH
# with the standard install locations relative to the invoking user's
# home so the require_cmd checks below succeed.
if [ -n "${SUDO_USER:-}" ]; then
    USER_HOME=$(getent passwd "$SUDO_USER" 2>/dev/null | cut -d: -f6 || echo "")
    if [ -n "$USER_HOME" ]; then
        # asdf shims must come first so they shadow system Go, etc.
        if [ -d "$USER_HOME/.asdf/shims" ]; then
            PATH="$USER_HOME/.asdf/shims:$PATH"
        fi
        PATH="$PATH:/usr/local/go/bin:$USER_HOME/go/bin:$USER_HOME/.local/bin"
        if [ -d "$USER_HOME/.nvm" ]; then
            # Pick the active nvm-installed node, if any.
            NVM_NODE_BIN=$(ls -1d "$USER_HOME"/.nvm/versions/node/*/bin 2>/dev/null | tail -1 || true)
            [ -n "$NVM_NODE_BIN" ] && PATH="$NVM_NODE_BIN:$PATH"
        fi
    fi
fi
export PATH

# ---------------------------------------------------------------------------
# Uninstall path
# ---------------------------------------------------------------------------
if [ "$UNINSTALL" = "1" ]; then
    echo "==> uninstalling frappe-monitor (data preserved)"
    systemctl stop frappe-monitor.service 2>/dev/null || true
    systemctl disable frappe-monitor.service 2>/dev/null || true
    systemctl stop frappe-monitor-stack.service 2>/dev/null || true
    systemctl disable frappe-monitor-stack.service 2>/dev/null || true
    rm -f "$SYSTEMD_DIR/frappe-monitor.service" "$SYSTEMD_DIR/frappe-monitor-stack.service"
    systemctl daemon-reload
    rm -f "$BIN_DST"
    ok "stopped, disabled, removed binary + units"
    note "config preserved at $ETC_DIR — remove manually if desired"
    note "data preserved at $STATE_DIR — remove manually if desired"
    note "VM + Loki volumes preserved — 'docker volume rm frappe-monitor-vm-data frappe-monitor-loki-data' wipes them"
    exit 0
fi

# ---------------------------------------------------------------------------
# Step 1: prerequisites
# ---------------------------------------------------------------------------
echo "==> checking prerequisites"

require_cmd() {
    local cmd="$1"; local hint="$2"
    if ! command -v "$cmd" >/dev/null 2>&1; then
        cat >&2 <<EOF
FAIL: '$cmd' not found in PATH.
  Searched: $PATH

  If you have $cmd installed as your normal user but sudo can't see
  it, re-run preserving your PATH:

      sudo env "PATH=\$PATH" ./deploy/install.sh

  Otherwise install $cmd system-wide:
      $hint
EOF
        exit 1
    fi
}

require_cmd go    "https://go.dev/doc/install — frappe-monitor needs Go ≥ 1.25"
require_cmd node  "your distro's package manager — Node ≥ 20"
require_cmd npm   "ships with Node"
require_cmd docker "https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || fail "docker compose plugin missing — apt install docker-compose-plugin (or your distro's equivalent)"
require_cmd systemctl "this script targets systemd Linux; bail otherwise"

GO_VERSION=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
[ -n "$GO_VERSION" ] || fail "go env GOVERSION returned empty"
MAJOR_MINOR=$(echo "$GO_VERSION" | awk -F. '{print $1"."$2}')
awk -v v="$MAJOR_MINOR" 'BEGIN { exit !(v+0 >= 1.25) }' || fail "Go $GO_VERSION too old — need ≥ 1.25"
ok "go $GO_VERSION, node $(node --version), docker $(docker --version | awk '{print $3}' | tr -d ,)"

# ---------------------------------------------------------------------------
# Step 2: build
# ---------------------------------------------------------------------------
echo "==> building binary (npm install + vite build + go build)"
cd "$REPO_ROOT"
# Build as the invoking user (not root) so node_modules ownership
# stays sane. -H resets HOME to the target user's home dir — without
# it, HOME stays /root from the parent sudo and Go's build cache
# (~/.cache/go-build) plus npm's cache try to write under /root,
# which fails. Pass PATH explicitly so /usr/local/go/bin and the
# user's per-shell PATH augmentation from above are visible.
SUDO_USER_NAME="${SUDO_USER:-$(logname 2>/dev/null || echo root)}"
if [ "$SUDO_USER_NAME" != "root" ] && id -u "$SUDO_USER_NAME" >/dev/null 2>&1; then
    sudo -u "$SUDO_USER_NAME" -H env "PATH=$PATH" make build >/dev/null
else
    make build >/dev/null
fi
[ -x "$REPO_ROOT/bin/monitor-server" ] || fail "build did not produce $REPO_ROOT/bin/monitor-server"
ok "binary built ($(du -h "$REPO_ROOT/bin/monitor-server" | cut -f1))"

# ---------------------------------------------------------------------------
# Step 3: system user + directories
# ---------------------------------------------------------------------------
echo "==> system user + dirs"

if ! id -u "$INSTALL_USER" >/dev/null 2>&1; then
    useradd --system --shell /usr/sbin/nologin --home-dir "$STATE_DIR" \
        --no-create-home --user-group "$INSTALL_USER"
    ok "created system user $INSTALL_USER"
else
    ok "user $INSTALL_USER already exists"
fi

install -d -o "$INSTALL_USER" -g "$INSTALL_GROUP" -m 0750 "$STATE_DIR"
install -d -o root            -g "$INSTALL_GROUP" -m 0750 "$ETC_DIR"
install -d -o root            -g root             -m 0755 "$OPT_DIR"
ok "directories laid out: $STATE_DIR (data), $ETC_DIR (config), $OPT_DIR (compose)"

# ---------------------------------------------------------------------------
# Step 4: install binary, config, compose, systemd units
# ---------------------------------------------------------------------------
echo "==> install files"

# Atomic binary swap so an in-flight upgrade can't half-deploy.
install -m 0755 "$REPO_ROOT/bin/monitor-server" "$BIN_DST.new"
mv -f "$BIN_DST.new" "$BIN_DST"
ok "installed $BIN_DST"

if [ ! -f "$ETC_DIR/monitor.yaml" ]; then
    install -m 0640 -o root -g "$INSTALL_GROUP" \
        "$REPO_ROOT/deploy/config/monitor.yaml.example" \
        "$ETC_DIR/monitor.yaml"
    # Production state path is /var/lib/...; the example points at ./data/.
    sed -i 's|path: "./data/monitor.db"|path: "/var/lib/frappe-monitor/monitor.db"|' \
        "$ETC_DIR/monitor.yaml"
    ok "wrote default config to $ETC_DIR/monitor.yaml (state path → $STATE_DIR/monitor.db)"
else
    ok "preserving existing $ETC_DIR/monitor.yaml"
fi

# Compose file goes under /opt so the systemd unit's WorkingDirectory
# matches whether or not the original repo checkout still exists.
install -d -m 0755 "$OPT_DIR/deploy"
install -m 0644 "$REPO_ROOT/deploy/docker-compose.prod.yml" "$OPT_DIR/deploy/docker-compose.prod.yml"
ok "installed compose to $OPT_DIR/deploy/docker-compose.prod.yml"

install -m 0644 "$REPO_ROOT/deploy/systemd/frappe-monitor.service"       "$SYSTEMD_DIR/frappe-monitor.service"
install -m 0644 "$REPO_ROOT/deploy/systemd/frappe-monitor-stack.service" "$SYSTEMD_DIR/frappe-monitor-stack.service"
systemctl daemon-reload
ok "installed systemd units"

# ---------------------------------------------------------------------------
# Step 5: start (or restart) services
# ---------------------------------------------------------------------------
echo "==> services"

if [ "$SKIP_STACK" = "0" ]; then
    systemctl enable frappe-monitor-stack.service >/dev/null
    systemctl restart frappe-monitor-stack.service
    ok "frappe-monitor-stack.service (VM + Loki) started"

    # Wait for VM + Loki to become healthy before starting the monitor
    # itself so the first-cycle push doesn't fail.
    for _ in $(seq 1 30); do
        if curl -fsS http://127.0.0.1:8428/health >/dev/null 2>&1 \
           && curl -fsS http://127.0.0.1:3100/ready >/dev/null 2>&1; then
            break
        fi
        sleep 1
    done
    curl -fsS http://127.0.0.1:8428/health >/dev/null 2>&1 || fail "VM health check never succeeded"
    curl -fsS http://127.0.0.1:3100/ready  >/dev/null 2>&1 || fail "Loki readiness never succeeded"
    ok "VM + Loki healthy"
else
    note "--no-stack: skipped VM+Loki bring-up. Configure metrics.vm_url + logs.loki_url in $ETC_DIR/monitor.yaml to point at your existing tier."
fi

systemctl enable frappe-monitor.service >/dev/null
systemctl restart frappe-monitor.service
sleep 1
if systemctl is-active --quiet frappe-monitor.service; then
    ok "frappe-monitor.service is active"
else
    journalctl -u frappe-monitor --no-pager -n 30 >&2
    fail "frappe-monitor.service did not start — see journalctl above"
fi

# ---------------------------------------------------------------------------
# Step 6: post-install summary
# ---------------------------------------------------------------------------
echo
echo "==> install complete"
echo
echo "  Binary:    $BIN_DST"
echo "  Config:    $ETC_DIR/monitor.yaml"
echo "  Data:      $STATE_DIR/"
echo "  Compose:   $OPT_DIR/deploy/docker-compose.prod.yml"
echo
echo "  Status:    sudo systemctl status frappe-monitor"
echo "  Logs:      sudo journalctl -u frappe-monitor -f"
echo "  Stop:      sudo systemctl stop frappe-monitor frappe-monitor-stack"
echo "  Start:     sudo systemctl start frappe-monitor-stack frappe-monitor"
echo
echo "  Dashboard: http://$(hostname):$(grep -E '^\s*listen_addr:' "$ETC_DIR/monitor.yaml" | head -1 | sed 's/.*"\(:[0-9]*\)".*/\1/' | tr -d ':')"
echo
echo "  Next: read docs/guide/usage.md to add your first bench server."
