#!/usr/bin/env bash
# Production install for frappe-monitor.
#
# Idempotent: safe to re-run for upgrades. Re-running:
#   - rebuilds the binary from the current checkout
#   - replaces /usr/local/bin/frappe-monitor (atomic mv)
#   - keeps the last known-good binary/compose/units and restores them if
#     the upgraded services fail their startup checks
#   - reinstalls systemd units (only if changed)
#   - leaves /etc/frappe-monitor/monitor.yaml + /var/lib/frappe-monitor alone
#   - restarts frappe-monitor.service so the new binary takes effect
#
# Usage:
#   sudo ./deploy/install.sh                # full install or upgrade (prompts for
#                                           # dashboard password + alerts on first run)
#   sudo ./deploy/install.sh --no-stack     # skip VM+Loki bring-up (split-tier deploys)
#   sudo ./deploy/install.sh --uninstall    # stop, disable, remove binary + units
#                                           # (keeps /etc/frappe-monitor + data)
#   sudo ./deploy/install.sh --purge        # --uninstall + nuke /etc/frappe-monitor,
#                                           # /var/lib/frappe-monitor, docker volumes,
#                                           # and the frappe-monitor user. Goes back
#                                           # to a clean slate; next install re-prompts.
#
# Non-interactive override (skip prompts) — pass via env or args:
#   sudo MONITOR_PASSWORD=hunter2 ./deploy/install.sh
#   sudo ./deploy/install.sh --password=hunter2
#   sudo ./deploy/install.sh --password=hunter2 --enable-alerts \
#         --bot-token=<TOKEN> --chat-ids=12345,67890
#
# If --password isn't provided and stdin isn't a TTY, the script auto-
# generates a random password and prints it. Existing /etc/frappe-monitor/
# monitor.yaml is preserved on re-runs (delete it or use --purge to
# re-prompt).
#
# Layout produced:
#   /usr/local/bin/frappe-monitor              binary (CGO=0, embedded SPA)
#   /etc/frappe-monitor/monitor.yaml           config (created from example on first install)
#   /var/lib/frappe-monitor/                   SQLite DB + per-server state
#   /opt/frappe-monitor/                       repo checkout (for compose + upgrade)
#   /etc/systemd/system/frappe-monitor.service        the binary
#   /etc/systemd/system/frappe-monitor-stack.service  VM + Loki via compose
#
# Requires: bash, sudo/root, Linux with systemd, Go ≥ 1.26.5, Node ≥ 20.19,
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
PURGE=0
# Config knobs — env wins over arg when both supplied. All blank-default
# so the prompt-on-first-install flow stays the dominant UX.
MONITOR_PASSWORD="${MONITOR_PASSWORD:-}"
MONITOR_ALERTS_ENABLED="${MONITOR_ALERTS_ENABLED:-}"        # "true" | "false"
MONITOR_TELEGRAM_BOT_TOKEN="${MONITOR_TELEGRAM_BOT_TOKEN:-}"
MONITOR_TELEGRAM_CHAT_IDS="${MONITOR_TELEGRAM_CHAT_IDS:-}"  # comma-separated

for arg in "$@"; do
    case "$arg" in
        --no-stack) SKIP_STACK=1 ;;
        --uninstall) UNINSTALL=1 ;;
        --purge) UNINSTALL=1; PURGE=1 ;;
        --password=*) MONITOR_PASSWORD="${arg#--password=}" ;;
        --enable-alerts) MONITOR_ALERTS_ENABLED=true ;;
        --no-alerts) MONITOR_ALERTS_ENABLED=false ;;
        --bot-token=*) MONITOR_TELEGRAM_BOT_TOKEN="${arg#--bot-token=}" ;;
        --chat-ids=*) MONITOR_TELEGRAM_CHAT_IDS="${arg#--chat-ids=}" ;;
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
ROLLBACK_DIR="$STATE_DIR/install-rollback"

HAD_PREVIOUS_BINARY=0
HAD_PREVIOUS_COMPOSE=0
HAD_PREVIOUS_MONITOR_UNIT=0
HAD_PREVIOUS_STACK_UNIT=0
PREVIOUS_MONITOR_ACTIVE=0
PREVIOUS_MONITOR_ENABLED=0
PREVIOUS_STACK_ACTIVE=0
PREVIOUS_STACK_ENABLED=0

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
note() { echo "  >> $*"; }

backup_install_state() {
    install -d -o root -g root -m 0700 "$ROLLBACK_DIR"
    if [ -f "$BIN_DST" ]; then
        install -m 0755 "$BIN_DST" "$ROLLBACK_DIR/frappe-monitor"
        HAD_PREVIOUS_BINARY=1
    fi
    if [ -f "$OPT_DIR/deploy/docker-compose.prod.yml" ]; then
        install -m 0644 "$OPT_DIR/deploy/docker-compose.prod.yml" "$ROLLBACK_DIR/docker-compose.prod.yml"
        HAD_PREVIOUS_COMPOSE=1
    fi
    if [ -f "$SYSTEMD_DIR/frappe-monitor.service" ]; then
        install -m 0644 "$SYSTEMD_DIR/frappe-monitor.service" "$ROLLBACK_DIR/frappe-monitor.service"
        HAD_PREVIOUS_MONITOR_UNIT=1
    fi
    if [ -f "$SYSTEMD_DIR/frappe-monitor-stack.service" ]; then
        install -m 0644 "$SYSTEMD_DIR/frappe-monitor-stack.service" "$ROLLBACK_DIR/frappe-monitor-stack.service"
        HAD_PREVIOUS_STACK_UNIT=1
    fi
    if systemctl is-active --quiet frappe-monitor.service 2>/dev/null; then
        PREVIOUS_MONITOR_ACTIVE=1
    fi
    if systemctl is-enabled --quiet frappe-monitor.service 2>/dev/null; then
        PREVIOUS_MONITOR_ENABLED=1
    fi
    if systemctl is-active --quiet frappe-monitor-stack.service 2>/dev/null; then
        PREVIOUS_STACK_ACTIVE=1
    fi
    if systemctl is-enabled --quiet frappe-monitor-stack.service 2>/dev/null; then
        PREVIOUS_STACK_ENABLED=1
    fi
}

restore_unit_state() {
    local unit="$1" had_unit="$2" was_enabled="$3" was_active="$4"

    if [ "$had_unit" != "1" ]; then
        systemctl disable "$unit" >/dev/null 2>&1 || true
        systemctl stop "$unit" >/dev/null 2>&1 || true
        systemctl reset-failed "$unit" >/dev/null 2>&1 || true
        return
    fi

    if [ "$was_enabled" = "1" ]; then
        systemctl enable "$unit" >/dev/null 2>&1 || true
    else
        systemctl disable "$unit" >/dev/null 2>&1 || true
    fi
    if [ "$was_active" = "1" ]; then
        systemctl restart "$unit" >/dev/null 2>&1 || true
    else
        systemctl stop "$unit" >/dev/null 2>&1 || true
    fi
}

rollback_install() {
    local cause="$1"
    note "upgrade failed: $cause"
    note "restoring last known-good installation"
    set +e

    # Stop only the candidate services that this install path may have
    # started. Their exact pre-upgrade states are restored below.
    systemctl stop frappe-monitor.service >/dev/null 2>&1 || true
    if [ "$SKIP_STACK" = "0" ]; then
        systemctl stop frappe-monitor-stack.service >/dev/null 2>&1 || true
    fi

    if [ "$HAD_PREVIOUS_BINARY" = "1" ]; then
        install -m 0755 "$ROLLBACK_DIR/frappe-monitor" "$BIN_DST.restore"
        mv -f "$BIN_DST.restore" "$BIN_DST"
    else
        rm -f "$BIN_DST" "$BIN_DST.restore"
    fi
    if [ "$HAD_PREVIOUS_COMPOSE" = "1" ]; then
        install -m 0644 "$ROLLBACK_DIR/docker-compose.prod.yml" "$OPT_DIR/deploy/docker-compose.prod.yml"
    else
        rm -f "$OPT_DIR/deploy/docker-compose.prod.yml"
    fi
    if [ "$HAD_PREVIOUS_MONITOR_UNIT" = "1" ]; then
        install -m 0644 "$ROLLBACK_DIR/frappe-monitor.service" "$SYSTEMD_DIR/frappe-monitor.service"
    else
        rm -f "$SYSTEMD_DIR/frappe-monitor.service"
    fi
    if [ "$HAD_PREVIOUS_STACK_UNIT" = "1" ]; then
        install -m 0644 "$ROLLBACK_DIR/frappe-monitor-stack.service" "$SYSTEMD_DIR/frappe-monitor-stack.service"
    else
        rm -f "$SYSTEMD_DIR/frappe-monitor-stack.service"
    fi

    systemctl daemon-reload
    if [ "$SKIP_STACK" = "0" ]; then
        restore_unit_state frappe-monitor-stack.service "$HAD_PREVIOUS_STACK_UNIT" \
            "$PREVIOUS_STACK_ENABLED" "$PREVIOUS_STACK_ACTIVE"
    fi
    restore_unit_state frappe-monitor.service "$HAD_PREVIOUS_MONITOR_UNIT" \
        "$PREVIOUS_MONITOR_ENABLED" "$PREVIOUS_MONITOR_ACTIVE"
    set -e
    note "rollback attempted; inspect systemctl status and journalctl before retrying"
}

configured_health_url() {
    local listen_addr
    listen_addr=$(awk '
        /^server:[[:space:]]*$/ { in_server=1; next }
        in_server && /^[^[:space:]]/ { in_server=0 }
        in_server && /^[[:space:]]+listen_addr:/ {
            sub(/^[[:space:]]+listen_addr:[[:space:]]*/, "")
            gsub(/^["\047 ]+|["\047 ]+$/, "")
            print
            exit
        }
    ' "$ETC_DIR/monitor.yaml")
    case "$listen_addr" in
        :*) printf 'http://127.0.0.1:%s/healthz' "${listen_addr#:}" ;;
        0.0.0.0:*|\[::\]:*) printf 'http://127.0.0.1:%s/healthz' "${listen_addr##*:}" ;;
        \[*\]:*) printf 'http://%s/healthz' "$listen_addr" ;;
        *:*) printf 'http://%s/healthz' "$listen_addr" ;;
        *) return 1 ;;
    esac
}

trim() {
    local value="$1"
    value="${value#"${value%%[![:space:]]*}"}"
    value="${value%"${value##*[![:space:]]}"}"
    printf '%s' "$value"
}

# Telegram accepts numeric user/group/channel IDs. Validate the first-run
# answers here instead of installing a config that will make systemd restart
# forever. The runtime performs the same validation for edited configs.
validate_telegram_credentials() {
    local token raw_id chat_id normalized=""
    local -A seen=()

    token=$(trim "$MONITOR_TELEGRAM_BOT_TOKEN")
    if [[ ! "$token" =~ ^[0-9]{6,}:[A-Za-z0-9_-]{20,}$ ]]; then
        TELEGRAM_VALIDATION_ERROR="bot token must look like 123456789:ABC... (copy it from @BotFather)"
        return 1
    fi

    IFS=',' read -ra _chat_ids <<< "$MONITOR_TELEGRAM_CHAT_IDS"
    for raw_id in "${_chat_ids[@]}"; do
        chat_id=$(trim "$raw_id")
        if [ -z "$chat_id" ]; then
            TELEGRAM_VALIDATION_ERROR="chat IDs must not contain empty entries"
            return 1
        fi
        if [[ ! "$chat_id" =~ ^-?[0-9]+$ ]]; then
            TELEGRAM_VALIDATION_ERROR="chat ID '$chat_id' is not numeric (groups usually start with -100)"
            return 1
        fi
        if [[ -n "${seen[$chat_id]+present}" ]]; then
            TELEGRAM_VALIDATION_ERROR="chat ID '$chat_id' is duplicated"
            return 1
        fi
        seen["$chat_id"]=1
        if [ -n "$normalized" ]; then normalized+=","; fi
        normalized+="$chat_id"
    done
    if [ -z "$normalized" ]; then
        TELEGRAM_VALIDATION_ERROR="at least one Telegram chat ID is required"
        return 1
    fi

    MONITOR_TELEGRAM_BOT_TOKEN="$token"
    MONITOR_TELEGRAM_CHAT_IDS="$normalized"
    return 0
}

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
    if [ "$PURGE" = "1" ]; then
        echo "==> purging frappe-monitor (everything goes — config, data, volumes, user)"
    else
        echo "==> uninstalling frappe-monitor (config + data preserved)"
    fi

    # Stop + disable systemd units first so docker volumes and dirs
    # aren't busy when we try to delete them.
    systemctl stop frappe-monitor.service       2>/dev/null || true
    systemctl disable frappe-monitor.service    2>/dev/null || true
    systemctl stop frappe-monitor-stack.service 2>/dev/null || true
    systemctl disable frappe-monitor-stack.service 2>/dev/null || true
    rm -f "$SYSTEMD_DIR/frappe-monitor.service" "$SYSTEMD_DIR/frappe-monitor-stack.service"
    systemctl daemon-reload
    rm -f "$BIN_DST"
    ok "stopped, disabled, removed binary + units"

    if [ "$PURGE" = "1" ]; then
        rm -rf "$ETC_DIR" "$STATE_DIR" "$OPT_DIR"
        ok "removed $ETC_DIR, $STATE_DIR, $OPT_DIR"
        if id -u "$INSTALL_USER" >/dev/null 2>&1; then
            userdel  "$INSTALL_USER"  2>/dev/null || true
            groupdel "$INSTALL_GROUP" 2>/dev/null || true
            ok "removed system user $INSTALL_USER"
        fi
        docker volume rm frappe-monitor-vm-data   2>/dev/null \
            && ok "removed docker volume frappe-monitor-vm-data"   || true
        docker volume rm frappe-monitor-loki-data 2>/dev/null \
            && ok "removed docker volume frappe-monitor-loki-data" || true
        echo
        echo "==> purge complete. Re-run 'sudo ./deploy/install.sh' for a clean install."
    else
        note "config preserved at $ETC_DIR (delete manually or use --purge)"
        note "data preserved at $STATE_DIR (delete manually or use --purge)"
        note "VM + Loki volumes preserved (use --purge to wipe everything)"
    fi
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

require_cmd go    "https://go.dev/doc/install — frappe-monitor needs Go ≥ 1.26.5"
require_cmd node  "your distro's package manager — Node ≥ 20.19"
require_cmd npm   "ships with Node"
require_cmd curl  "your distro's curl package"
require_cmd openssl "your distro's openssl package"
require_cmd ssh-keygen "your distro's OpenSSH client package"
if [ "$SKIP_STACK" = "0" ]; then
    require_cmd docker "https://docs.docker.com/engine/install/"
    docker compose version >/dev/null 2>&1 || fail "docker compose plugin missing — apt install docker-compose-plugin (or your distro's equivalent)"
    docker info >/dev/null 2>&1 || fail "Docker CLI is installed but the daemon is unavailable. Start it with 'systemctl enable --now docker' (apt/upstream) or 'snap start docker' (Snap), then retry"
fi
require_cmd systemctl "this script targets systemd Linux; bail otherwise"

GO_VERSION=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
[ -n "$GO_VERSION" ] || fail "go env GOVERSION returned empty"
version_at_least() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -1)" = "$2" ]; }
version_at_least "$GO_VERSION" "1.26.5" || fail "Go $GO_VERSION too old — need ≥ 1.26.5"
NODE_VERSION=$(node --version | sed 's/^v//')
version_at_least "$NODE_VERSION" "20.19.0" || fail "Node $NODE_VERSION too old — need ≥ 20.19"
if [ "$SKIP_STACK" = "0" ]; then
    DOCKER_VERSION=$(docker version --format '{{.Server.Version}}' 2>/dev/null)
    COMPOSE_VERSION=$(docker compose version --short 2>/dev/null | sed 's/^v//')
    version_at_least "$DOCKER_VERSION" "24.0.0" || fail "Docker $DOCKER_VERSION too old — need ≥ 24"
    version_at_least "$COMPOSE_VERSION" "2.23.1" || fail "Docker Compose $COMPOSE_VERSION too old — need ≥ 2.23.1 (embedded Loki config support)"
    ok "go $GO_VERSION, node $(node --version), docker $DOCKER_VERSION, compose $COMPOSE_VERSION"
else
    ok "go $GO_VERSION, node $(node --version), external metrics/logs tier (--no-stack)"
fi

# ---------------------------------------------------------------------------
# Step 1.5: collect credentials BEFORE the long build, so the operator
# types in their answers up-front and can walk away.
#
# Skipped entirely if /etc/frappe-monitor/monitor.yaml already exists
# (re-runs preserve the operator's edits — use --purge to start fresh).
# ---------------------------------------------------------------------------
if [ ! -f "$ETC_DIR/monitor.yaml" ]; then
    echo "==> configuring monitor.yaml"

    if [ -z "$MONITOR_PASSWORD" ]; then
        if [ -t 0 ] && [ -t 1 ]; then
            echo
            echo "  Dashboard requires a password. Anyone with the password +"
            echo "  network access to this host can see all monitored servers."
            echo "  Press Enter alone to auto-generate a 32-char random password."
            echo
            read -r -s -p "  Dashboard password: " MONITOR_PASSWORD
            echo
            echo
        fi
        if [ -z "$MONITOR_PASSWORD" ]; then
            MONITOR_PASSWORD=$(openssl rand -base64 24 | tr -d '\n')
            echo "  Auto-generated password:"
            echo "      $MONITOR_PASSWORD"
            echo "  ^^ WRITE THIS DOWN ^^   (you'll need it to log into the dashboard)"
            echo
        fi
    fi

    if [ -z "$MONITOR_ALERTS_ENABLED" ]; then
        if [ -t 0 ] && [ -t 1 ]; then
            read -r -p "  Enable Telegram alerts? [y/N]: " _ans
            case "${_ans:-n}" in
                [Yy]*) MONITOR_ALERTS_ENABLED=true ;;
                *)     MONITOR_ALERTS_ENABLED=false ;;
            esac
        else
            MONITOR_ALERTS_ENABLED=false
        fi
    fi

    case "$MONITOR_ALERTS_ENABLED" in
        true|false) ;;
        *) fail "MONITOR_ALERTS_ENABLED must be 'true' or 'false', got '$MONITOR_ALERTS_ENABLED'" ;;
    esac

    if [ "$MONITOR_ALERTS_ENABLED" = "true" ]; then
        while true; do
            if [ -z "$MONITOR_TELEGRAM_BOT_TOKEN" ] && [ -t 0 ] && [ -t 1 ]; then
                read -r -s -p "  Telegram bot token (from @BotFather): " MONITOR_TELEGRAM_BOT_TOKEN
                echo
            fi
            if [ -z "$MONITOR_TELEGRAM_CHAT_IDS" ] && [ -t 0 ] && [ -t 1 ]; then
                read -r -p "  Telegram chat IDs (comma-separated): " MONITOR_TELEGRAM_CHAT_IDS
            fi
            if validate_telegram_credentials; then
                break
            fi
            if [ -t 0 ] && [ -t 1 ]; then
                note "invalid Telegram settings: $TELEGRAM_VALIDATION_ERROR"
                note "enter them again, or press Ctrl-C and rerun with --no-alerts"
                MONITOR_TELEGRAM_BOT_TOKEN=""
                MONITOR_TELEGRAM_CHAT_IDS=""
            else
                fail "invalid Telegram settings: $TELEGRAM_VALIDATION_ERROR"
            fi
        done
    fi
    ok "credentials collected"
fi

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
install -d -o "$INSTALL_USER" -g "$INSTALL_GROUP" -m 0700 "$STATE_DIR/.ssh"
if [ ! -f "$STATE_DIR/.ssh/known_hosts" ]; then
    install -o "$INSTALL_USER" -g "$INSTALL_GROUP" -m 0600 /dev/null "$STATE_DIR/.ssh/known_hosts"
fi
MONITOR_KEY_PATH="$STATE_DIR/.ssh/id_ed25519"
if [ ! -f "$MONITOR_KEY_PATH" ]; then
    # A dedicated identity avoids exposing an operator's personal ~/.ssh to
    # the locked-down service (ProtectHome=true). It is not authorized on any
    # target automatically; the operator copies only the .pub key there.
    (umask 077; ssh-keygen -q -t ed25519 -N "" -C "$INSTALL_USER" -f "$MONITOR_KEY_PATH")
    chown "$INSTALL_USER:$INSTALL_GROUP" "$MONITOR_KEY_PATH" "$MONITOR_KEY_PATH.pub"
    chmod 0600 "$MONITOR_KEY_PATH"
    chmod 0644 "$MONITOR_KEY_PATH.pub"
    ok "generated dedicated SSH identity at $MONITOR_KEY_PATH"
else
    chown "$INSTALL_USER:$INSTALL_GROUP" "$MONITOR_KEY_PATH"
    chmod 0600 "$MONITOR_KEY_PATH"
    ok "preserving dedicated SSH identity at $MONITOR_KEY_PATH"
fi
# The authenticated Settings UI atomically rewrites monitor.yaml and its .bak
# sibling. Keep ownership rooted while granting only the service group write
# access to this one configuration directory.
install -d -o root            -g "$INSTALL_GROUP" -m 0770 "$ETC_DIR"
install -d -o root            -g root             -m 0755 "$OPT_DIR"
ok "directories laid out: $STATE_DIR (data), $ETC_DIR (config), $OPT_DIR (compose)"

# ---------------------------------------------------------------------------
# Step 4: install config, validate it, then atomically install files
# ---------------------------------------------------------------------------
echo "==> install files"

CONFIG_CREATED=0
if [ ! -f "$ETC_DIR/monitor.yaml" ]; then
    # Escape values for embedding in a double-quoted YAML string:
    # backslash and double-quote become \\ and \" respectively.
    yaml_dq() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

    PW_YAML=$(yaml_dq "$MONITOR_PASSWORD")
    BOT_YAML=$(yaml_dq "$MONITOR_TELEGRAM_BOT_TOKEN")
	MONITOR_TIMEZONE=$(timedatectl show --property=Timezone --value 2>/dev/null || true)
	if [ -z "$MONITOR_TIMEZONE" ] || { [ "$MONITOR_TIMEZONE" != "UTC" ] && [ ! -r "/usr/share/zoneinfo/$MONITOR_TIMEZONE" ]; }; then
		MONITOR_TIMEZONE="UTC"
	fi
	TIMEZONE_YAML=$(yaml_dq "$MONITOR_TIMEZONE")

    # Build the chat_ids YAML array from the comma-separated input.
    CHAT_IDS_YAML="[]"
    if [ -n "$MONITOR_TELEGRAM_CHAT_IDS" ]; then
        CHAT_IDS_YAML="["
        _first=1
        IFS=',' read -ra _ids <<< "$MONITOR_TELEGRAM_CHAT_IDS"
        for _id in "${_ids[@]}"; do
            _id="${_id## }"; _id="${_id%% }"  # trim leading + trailing space
            [ -z "$_id" ] && continue
            if [ "$_first" = 1 ]; then _first=0; else CHAT_IDS_YAML+=", "; fi
            CHAT_IDS_YAML+="\"$(yaml_dq "$_id")\""
        done
        CHAT_IDS_YAML+="]"
    fi

    # Write the config from a heredoc — no sed, no escaping landmines.
    # Permissions: 0640 root:frappe-monitor so the service can read it
    # but other users on the host can't grep out the password.
    umask 027
    cat > "$ETC_DIR/monitor.yaml" <<EOF
# frappe-monitor configuration.
# Generated by deploy/install.sh on $(date -Iseconds).
# Edit this file directly, then run:
#     sudo systemctl restart frappe-monitor
# to apply. Full reference: docs/guide/configuration.md.

server:
  listen_addr: ":8080"
  read_timeout_seconds: 15
  write_timeout_seconds: 15
  trusted_proxy_cidrs: ["127.0.0.1/32", "::1/128"]

display:
  timezone: "$TIMEZONE_YAML"

database:
  path: "$STATE_DIR/monitor.db"

ssh:
  dial_timeout_seconds: 10
  command_timeout_seconds: 30
  max_connections_per_host: 2
  known_hosts_path: "$STATE_DIR/.ssh/known_hosts"

log:
  level: "info"
  format: "json"

metrics:
  vm_url: "http://127.0.0.1:8428"
  push_timeout_seconds: 5
  query_timeout_seconds: 15

logs:
  loki_url: "http://127.0.0.1:3100"
  push_timeout_seconds: 5
  query_timeout_seconds: 15

scheduler:
  default_interval_seconds: 60
  max_parallel: 10
  per_job_timeout_seconds: 45

public_probes:
  enabled: false
  auto_discover: true
  default_scheme: "https"
  path: "/api/method/ping"
  timeout_seconds: 5
  max_parallel: 10
  allow_private_targets: false
  require_pong: true
  targets: []

# Set during install (sudo ./deploy/install.sh prompts for these).
# Re-prompt by deleting this file or running 'sudo ./deploy/install.sh --purge'.
auth:
  password: "$PW_YAML"
  realm: "frappe-monitor"

alerts:
  enabled: $MONITOR_ALERTS_ENABLED
  evaluation_interval_seconds: 60
  notify_repeat_seconds: 3600
  vm_query_timeout_seconds: 10
  telegram:
    bot_token: "$BOT_YAML"
    chat_ids: $CHAT_IDS_YAML
    send_timeout_seconds: 5
  disable_defaults: false
  rules: []

# Deploy collector installs the stream script too. Configure files and set
# enabled=true when live Loki ingestion is wanted.
streaming:
  enabled: false
  monitor_id: ""
  script_path: ".frappe-monitor/frappe-monitor-stream.sh"
  files: []
  flush_interval_seconds: 1
  max_batch_lines: 500
  max_buffered_lines: 5000
  max_buffered_bytes: 33554432
  push_timeout_seconds: 10
  min_backoff_seconds: 1
  max_backoff_seconds: 60
EOF
    chmod 0640 "$ETC_DIR/monitor.yaml"
    chown root:"$INSTALL_GROUP" "$ETC_DIR/monitor.yaml"
    CONFIG_CREATED=1
    ok "wrote $ETC_DIR/monitor.yaml (auth + alerts populated from your answers)"
else
    ok "preserving existing $ETC_DIR/monitor.yaml"
fi

# Validate with the candidate binary before replacing the known-good binary or
# touching systemd. This has no database, scheduler, network, or SSH side effects.
if ! _config_check=$("$REPO_ROOT/bin/monitor-server" --config "$ETC_DIR/monitor.yaml" --check-config 2>&1); then
    if [ "$CONFIG_CREATED" = "1" ]; then
        rm -f "$ETC_DIR/monitor.yaml"
        note "removed the invalid newly generated config; the next run will prompt again"
    fi
    fail "configuration validation failed: $_config_check"
fi
ok "configuration preflight passed"

# Preserve the exact files currently running before replacing any of them.
# This is intentionally after candidate build/config validation: failed
# preflight work should not disturb the last rollback snapshot.
backup_install_state
if [ "$HAD_PREVIOUS_BINARY" = "1" ]; then
    ok "saved last known-good installation in $ROLLBACK_DIR"
fi

# Atomic binary swap so an in-flight upgrade can't half-deploy.
if ! install -m 0755 "$REPO_ROOT/bin/monitor-server" "$BIN_DST.new" \
    || ! mv -f "$BIN_DST.new" "$BIN_DST"; then
    rollback_install "candidate binary installation"
    fail "could not install candidate binary; previous installation restored"
fi
ok "installed $BIN_DST"

# Compose file goes under /opt so the systemd unit's WorkingDirectory
# matches whether or not the original repo checkout still exists.
if ! install -d -m 0755 "$OPT_DIR/deploy" \
    || ! install -m 0644 "$REPO_ROOT/deploy/docker-compose.prod.yml" "$OPT_DIR/deploy/docker-compose.prod.yml"; then
    rollback_install "backing-stack configuration installation"
    fail "could not install backing-stack configuration; previous installation restored"
fi
ok "installed bounded VM/Loki configuration to $OPT_DIR/deploy/docker-compose.prod.yml"

if ! install -m 0644 "$REPO_ROOT/deploy/systemd/frappe-monitor.service" "$SYSTEMD_DIR/frappe-monitor.service" \
    || ! install -m 0644 "$REPO_ROOT/deploy/systemd/frappe-monitor-stack.service" "$SYSTEMD_DIR/frappe-monitor-stack.service" \
    || ! systemctl daemon-reload; then
    rollback_install "systemd unit installation"
    fail "could not install systemd units; previous installation restored"
fi
ok "installed systemd units"

# ---------------------------------------------------------------------------
# Step 5: start (or restart) services
# ---------------------------------------------------------------------------
echo "==> services"

if [ "$SKIP_STACK" = "0" ]; then
    if ! systemctl enable frappe-monitor-stack.service >/dev/null \
        || ! systemctl restart frappe-monitor-stack.service; then
        rollback_install "backing stack restart"
        fail "frappe-monitor-stack.service did not restart; previous installation restored"
    fi
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
    if ! curl -fsS http://127.0.0.1:8428/health >/dev/null 2>&1 \
        || ! curl -fsS http://127.0.0.1:3100/ready >/dev/null 2>&1; then
        rollback_install "backing stack health check"
        fail "VM/Loki health check failed; previous installation restored"
    fi
    ok "VM + Loki healthy"
else
    note "--no-stack: skipped VM+Loki bring-up. Configure metrics.vm_url + logs.loki_url in $ETC_DIR/monitor.yaml to point at your existing tier."
fi

if ! systemctl enable frappe-monitor.service >/dev/null \
    || ! systemctl restart frappe-monitor.service; then
    rollback_install "monitor service restart"
    fail "frappe-monitor.service did not restart; previous installation restored"
fi
MONITOR_HEALTH_URL=$(configured_health_url) \
    || { rollback_install "invalid server.listen_addr health endpoint"; fail "could not derive health endpoint from YAML server.listen_addr"; }
for _ in $(seq 1 15); do
    if systemctl is-active --quiet frappe-monitor.service \
        && curl -fsS --max-time 2 "$MONITOR_HEALTH_URL" >/dev/null 2>&1; then
        break
    fi
    sleep 1
done
if ! systemctl is-active --quiet frappe-monitor.service \
    || ! curl -fsS --max-time 2 "$MONITOR_HEALTH_URL" >/dev/null 2>&1; then
    journalctl -u frappe-monitor --no-pager -n 30 >&2
    rollback_install "monitor service health check"
    fail "frappe-monitor.service did not become healthy at the YAML listen_addr; previous installation restored"
fi
ok "frappe-monitor.service is active and healthy"

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
echo "  SSH key:   $MONITOR_KEY_PATH (authorize $MONITOR_KEY_PATH.pub on each target)"
echo
echo "  Status:    sudo systemctl status frappe-monitor"
echo "  Logs:      sudo journalctl -u frappe-monitor -f"
echo "  Stop:      sudo systemctl stop frappe-monitor frappe-monitor-stack"
echo "  Start:     sudo systemctl start frappe-monitor-stack frappe-monitor"
echo
listen_addr=$(awk '$1 == "listen_addr:" { value=$2; gsub(/["\047]/, "", value); print value; exit }' "$ETC_DIR/monitor.yaml")
case "$listen_addr" in
    :*) dashboard_addr="$(hostname):${listen_addr#:}" ;;
    0.0.0.0:*|\[::\]:*) dashboard_addr="$(hostname):${listen_addr##*:}" ;;
    *) dashboard_addr="$listen_addr" ;;
esac
echo "  Dashboard: http://$dashboard_addr"
echo
echo "  Next: read docs/guide/usage.md to add your first bench server."
