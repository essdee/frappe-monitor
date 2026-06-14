#!/usr/bin/env bash
# Cross-platform install for frappe-monitor.
#
#   Linux  → systemd services + a dedicated frappe-monitor system user
#            (production server install; run with sudo).
#   macOS  → a launchd LaunchAgent under your user, everything in
#            ~/.frappe-monitor (dev / local install; run WITHOUT sudo).
#
# The OS is auto-detected. Idempotent: safe to re-run for upgrades —
# it rebuilds the binary, swaps it in atomically, refreshes the service
# definition, and leaves your config + data alone.
#
# Usage:
#   Linux:  sudo ./deploy/install.sh            # install / upgrade
#   macOS:       ./deploy/install.sh            # install / upgrade (no sudo)
#
#   ./deploy/install.sh --no-stack              # skip VM+Loki bring-up
#   ./deploy/install.sh --uninstall             # stop + remove service + binary
#   ./deploy/install.sh --purge                 # --uninstall + wipe config/data/volumes
#
# Non-interactive (skip prompts), via env or args:
#   MONITOR_PASSWORD=hunter2 ./deploy/install.sh
#   ./deploy/install.sh --password=hunter2 --enable-alerts \
#         --bot-token=<TOKEN> --chat-ids=12345,67890
#
# Requires (both OSes): bash, Go ≥ 1.25, Node ≥ 20, npm, curl, and
# Docker ≥ 24 with EITHER `docker compose` (v2 plugin) or `docker-compose`
# (standalone). On macOS the Docker engine (Docker Desktop / colima /
# OrbStack) must be running. The script verifies each and bails with a
# copy-pasteable hint if anything is missing.
#
# Exits 0 on success, non-zero with a "FAIL:" line on failure.

set -euo pipefail

# ---------------------------------------------------------------------------
# Args
# ---------------------------------------------------------------------------
SKIP_STACK=0
UNINSTALL=0
PURGE=0
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

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
note() { echo "  >> $*"; }

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

# ---------------------------------------------------------------------------
# OS detection + platform layout
# ---------------------------------------------------------------------------
case "$(uname -s)" in
    Linux)  IS_MAC=0 ;;
    Darwin) IS_MAC=1 ;;
    *) fail "unsupported OS '$(uname -s)' — this installer targets Linux or macOS" ;;
esac

# compose() runs the available Compose implementation (v2 plugin or the
# standalone). have_compose reports whether either exists.
have_compose() { docker compose version >/dev/null 2>&1 || command -v docker-compose >/dev/null 2>&1; }
compose() {
    if docker compose version >/dev/null 2>&1; then
        docker compose "$@"
    else
        docker-compose "$@"
    fi
}

if [ "$IS_MAC" = 1 ]; then
    # macOS: user-space install under the current user's home. No sudo,
    # no system user; launchd LaunchAgent keeps the binary running.
    [ "$(id -u)" -ne 0 ] || fail "on macOS, run WITHOUT sudo (user-space install): ./deploy/install.sh"
    BASE_DIR="$HOME/.frappe-monitor"
    BIN_DST="$BASE_DIR/bin/frappe-monitor"
    CONF_FILE="$BASE_DIR/monitor.yaml"
    STATE_DIR="$BASE_DIR/data"
    DEPLOY_DIR="$BASE_DIR/deploy"
    LOG_DIR="$BASE_DIR/logs"
    LAUNCH_DIR="$HOME/Library/LaunchAgents"
    PLIST_LABEL="com.frappe-monitor"
    PLIST="$LAUNCH_DIR/$PLIST_LABEL.plist"
else
    # Linux: production install with systemd + a dedicated system user.
    [ "$(id -u)" -eq 0 ] || fail "on Linux, run as root: sudo ./deploy/install.sh"
    INSTALL_USER="frappe-monitor"
    INSTALL_GROUP="frappe-monitor"
    BIN_DST="/usr/local/bin/frappe-monitor"
    ETC_DIR="/etc/frappe-monitor"
    CONF_FILE="$ETC_DIR/monitor.yaml"
    STATE_DIR="/var/lib/frappe-monitor"
    OPT_DIR="/opt/frappe-monitor"
    DEPLOY_DIR="$OPT_DIR/deploy"
    SYSTEMD_DIR="/etc/systemd/system"
fi

# On Linux, sudo strips PATH; re-add the common Go/Node install dirs of the
# invoking user so the require_cmd checks find them. (No-op on macOS.)
if [ "$IS_MAC" = 0 ] && [ -n "${SUDO_USER:-}" ]; then
    USER_HOME=$(getent passwd "$SUDO_USER" 2>/dev/null | cut -d: -f6 || echo "")
    if [ -n "$USER_HOME" ]; then
        if [ -d "$USER_HOME/.asdf/shims" ]; then
            PATH="$USER_HOME/.asdf/shims:$PATH"
        fi
        PATH="$PATH:/usr/local/go/bin:$USER_HOME/go/bin:$USER_HOME/.local/bin"
        if [ -d "$USER_HOME/.nvm" ]; then
            NVM_NODE_BIN=$(ls -1d "$USER_HOME"/.nvm/versions/node/*/bin 2>/dev/null | tail -1 || true)
            [ -n "$NVM_NODE_BIN" ] && PATH="$NVM_NODE_BIN:$PATH"
        fi
    fi
    export PATH
fi

# ---------------------------------------------------------------------------
# Uninstall path
# ---------------------------------------------------------------------------
if [ "$UNINSTALL" = "1" ]; then
    if [ "$PURGE" = "1" ]; then
        echo "==> purging frappe-monitor (config, data, volumes — everything)"
    else
        echo "==> uninstalling frappe-monitor (config + data preserved)"
    fi

    if [ "$IS_MAC" = 1 ]; then
        launchctl unload "$PLIST" 2>/dev/null || true
        rm -f "$PLIST"
        compose -f "$DEPLOY_DIR/docker-compose.prod.yml" down 2>/dev/null || true
        rm -f "$BIN_DST"
        ok "unloaded LaunchAgent, stopped stack, removed binary"
        if [ "$PURGE" = "1" ]; then
            rm -rf "$BASE_DIR"
            docker volume rm frappe-monitor-vm-data   2>/dev/null && ok "removed volume frappe-monitor-vm-data"   || true
            docker volume rm frappe-monitor-loki-data 2>/dev/null && ok "removed volume frappe-monitor-loki-data" || true
            ok "removed $BASE_DIR"
            echo; echo "==> purge complete. Re-run ./deploy/install.sh for a clean install."
        else
            note "config + data preserved at $BASE_DIR (use --purge to wipe)"
        fi
        exit 0
    fi

    # Linux uninstall.
    systemctl stop frappe-monitor.service          2>/dev/null || true
    systemctl disable frappe-monitor.service       2>/dev/null || true
    systemctl stop frappe-monitor-stack.service    2>/dev/null || true
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
        docker volume rm frappe-monitor-vm-data   2>/dev/null && ok "removed docker volume frappe-monitor-vm-data"   || true
        docker volume rm frappe-monitor-loki-data 2>/dev/null && ok "removed docker volume frappe-monitor-loki-data" || true
        echo; echo "==> purge complete. Re-run 'sudo ./deploy/install.sh' for a clean install."
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
echo "==> checking prerequisites ($(uname -s))"

require_cmd() {
    local cmd="$1"; local hint="$2"
    if ! command -v "$cmd" >/dev/null 2>&1; then
        cat >&2 <<EOF
FAIL: '$cmd' not found in PATH.
  Searched: $PATH

  Install $cmd, then re-run:
      $hint
EOF
        exit 1
    fi
}

require_cmd go     "https://go.dev/doc/install — frappe-monitor needs Go ≥ 1.25"
require_cmd node   "Node ≥ 20 (nodejs.org, or brew install node on macOS)"
require_cmd npm    "ships with Node"
require_cmd curl   "your package manager (preinstalled on macOS)"
require_cmd docker "https://docs.docker.com/get-docker/ (Docker Desktop / colima / OrbStack on macOS)"
have_compose || fail "Docker Compose not found — need 'docker compose' (v2 plugin) OR 'docker-compose' (standalone). On macOS, Docker Desktop includes it."

if [ "$IS_MAC" = 1 ]; then
    # The running engine is only needed when we bring up the stack here.
    if [ "$SKIP_STACK" = "0" ]; then
        docker info >/dev/null 2>&1 || fail "Docker engine not running — start Docker Desktop (open -a Docker), then re-run. (Or pass --no-stack to point at an external VM/Loki.)"
    fi
else
    require_cmd systemctl "this Linux path targets systemd"
fi

GO_VERSION=$(go env GOVERSION 2>/dev/null | sed 's/^go//')
[ -n "$GO_VERSION" ] || fail "go env GOVERSION returned empty"
MAJOR_MINOR=$(echo "$GO_VERSION" | awk -F. '{print $1"."$2}')
awk -v v="$MAJOR_MINOR" 'BEGIN { exit !(v+0 >= 1.25) }' || fail "Go $GO_VERSION too old — need ≥ 1.25"
ok "go $GO_VERSION, node $(node --version), docker $(docker --version | awk '{print $3}' | tr -d ,)"

# ---------------------------------------------------------------------------
# Step 1.5: collect credentials before the long build (skipped if config
# already exists — re-runs preserve your edits; use --purge to re-prompt).
# ---------------------------------------------------------------------------
if [ -f "$CONF_FILE" ]; then
    # Re-run/upgrade: config is preserved. Warn if config-only flags were passed
    # so the operator isn't surprised they had no effect (edit $CONF_FILE instead).
    if [ -n "$MONITOR_PASSWORD" ] || [ -n "$MONITOR_ALERTS_ENABLED" ] || \
       [ -n "$MONITOR_TELEGRAM_BOT_TOKEN" ] || [ -n "$MONITOR_TELEGRAM_CHAT_IDS" ]; then
        note "existing $CONF_FILE kept — password/alerts flags IGNORED on a re-run."
        note "   edit $CONF_FILE then restart, or --purge for a clean re-config."
    fi
fi

if [ ! -f "$CONF_FILE" ]; then
    echo "==> configuring monitor.yaml"

    if [ -z "$MONITOR_PASSWORD" ]; then
        if [ -t 0 ] && [ -t 1 ]; then
            echo
            echo "  Dashboard requires a password. Anyone with the password +"
            echo "  network access to this host can see all monitored servers."
            echo "  Press Enter alone to auto-generate a 32-char random password."
            echo
            read -r -p "  Dashboard password: " MONITOR_PASSWORD
            echo
        fi
        if [ -z "$MONITOR_PASSWORD" ]; then
            MONITOR_PASSWORD=$(head -c 18 /dev/urandom | base64 | tr -d '\n')
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

    if [ "$MONITOR_ALERTS_ENABLED" = "true" ]; then
        if [ -z "$MONITOR_TELEGRAM_BOT_TOKEN" ] && [ -t 0 ] && [ -t 1 ]; then
            read -r -p "  Telegram bot token (from @BotFather): " MONITOR_TELEGRAM_BOT_TOKEN
        fi
        if [ -z "$MONITOR_TELEGRAM_CHAT_IDS" ] && [ -t 0 ] && [ -t 1 ]; then
            read -r -p "  Telegram chat IDs (comma-separated): " MONITOR_TELEGRAM_CHAT_IDS
        fi
        if [ -z "$MONITOR_TELEGRAM_BOT_TOKEN" ] || [ -z "$MONITOR_TELEGRAM_CHAT_IDS" ]; then
            note "alerts requested but bot_token or chat_ids missing — leaving disabled."
            note "   set them later by editing $CONF_FILE; restart the service."
            MONITOR_ALERTS_ENABLED=false
        fi
    fi
    ok "credentials collected"
fi

# ---------------------------------------------------------------------------
# Step 2: build
# ---------------------------------------------------------------------------
echo "==> building binary (npm install + vite build + go build)"
cd "$REPO_ROOT"
if [ "$IS_MAC" = 0 ]; then
    # Linux: build as the invoking user so node_modules ownership stays
    # sane and the Go/npm caches land in that user's home, not /root.
    SUDO_USER_NAME="${SUDO_USER:-$(logname 2>/dev/null || echo root)}"
    if [ "$SUDO_USER_NAME" != "root" ] && id -u "$SUDO_USER_NAME" >/dev/null 2>&1; then
        sudo -u "$SUDO_USER_NAME" -H env "PATH=$PATH" make build >/dev/null
    else
        make build >/dev/null
    fi
else
    # macOS: we're already the target user.
    make build >/dev/null
fi
[ -x "$REPO_ROOT/bin/monitor-server" ] || fail "build did not produce $REPO_ROOT/bin/monitor-server"
ok "binary built ($(du -h "$REPO_ROOT/bin/monitor-server" | cut -f1 | tr -d ' '))"

# ---------------------------------------------------------------------------
# Step 3: directories (+ system user on Linux)
# ---------------------------------------------------------------------------
echo "==> directories"
if [ "$IS_MAC" = 1 ]; then
    mkdir -p "$BASE_DIR/bin" "$STATE_DIR" "$DEPLOY_DIR" "$LOG_DIR" "$LAUNCH_DIR"
    chmod 0700 "$BASE_DIR" # config holds the dashboard password
    ok "laid out $BASE_DIR (bin, data, deploy, logs) + $LAUNCH_DIR"
else
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
    install -d -m 0755 "$DEPLOY_DIR"
    ok "directories: $STATE_DIR (data), $ETC_DIR (config), $OPT_DIR (compose)"
fi

# Provision the SSH identity the monitor uses to reach bench servers. It MUST
# live under the state dir (the hardened systemd unit's ProtectHome=true masks
# /home + /root). Idempotent: an existing key is preserved on re-run/upgrade.
SSH_DIR="$STATE_DIR/.ssh"
SSH_KEY="$SSH_DIR/id_ed25519"
if [ "$IS_MAC" = 1 ]; then
    mkdir -p "$SSH_DIR"; chmod 0700 "$SSH_DIR"
    [ -f "$SSH_KEY" ] || ssh-keygen -t ed25519 -N '' -f "$SSH_KEY" -C frappe-monitor >/dev/null
else
    install -d -o "$INSTALL_USER" -g "$INSTALL_GROUP" -m 0700 "$SSH_DIR"
    if [ ! -f "$SSH_KEY" ]; then
        sudo -u "$INSTALL_USER" ssh-keygen -t ed25519 -N '' -f "$SSH_KEY" -C frappe-monitor >/dev/null
    fi
fi
ok "ssh key: $SSH_KEY (use this path when adding a server; authorize $SSH_KEY.pub on each bench)"

# ---------------------------------------------------------------------------
# Step 4: install binary + config + compose
# ---------------------------------------------------------------------------
echo "==> install files"

# Atomic binary swap so an in-flight upgrade can't half-deploy.
install -m 0755 "$REPO_ROOT/bin/monitor-server" "$BIN_DST.new"
mv -f "$BIN_DST.new" "$BIN_DST"
ok "installed $BIN_DST"

if [ ! -f "$CONF_FILE" ]; then
    # Escape values for a double-quoted YAML string.
    yaml_dq() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }
    PW_YAML=$(yaml_dq "$MONITOR_PASSWORD")
    BOT_YAML=$(yaml_dq "$MONITOR_TELEGRAM_BOT_TOKEN")

    CHAT_IDS_YAML="[]"
    if [ -n "$MONITOR_TELEGRAM_CHAT_IDS" ]; then
        CHAT_IDS_YAML="["
        _first=1
        IFS=',' read -ra _ids <<< "$MONITOR_TELEGRAM_CHAT_IDS"
        for _id in "${_ids[@]}"; do
            _id="${_id## }"; _id="${_id%% }"
            [ -z "$_id" ] && continue
            if [ "$_first" = 1 ]; then _first=0; else CHAT_IDS_YAML+=", "; fi
            CHAT_IDS_YAML+="\"$(yaml_dq "$_id")\""
        done
        CHAT_IDS_YAML+="]"
    fi

    umask 077
    cat > "$CONF_FILE" <<EOF
# frappe-monitor configuration.
# Generated by deploy/install.sh on $(date +"%Y-%m-%dT%H:%M:%S%z").
# Edit, then restart the service to apply. Reference: docs/guide/configuration.md.

server:
  listen_addr: ":8080"          # all interfaces; put a TLS reverse proxy (Caddy) in front for prod
  read_timeout_seconds: 15
  write_timeout_seconds: 60      # must be >= ssh.command_timeout_seconds
  max_body_bytes: 1048576        # request-body cap (1 MiB)

database:
  path: "$STATE_DIR/monitor.db"

ssh:
  dial_timeout_seconds: 10
  command_timeout_seconds: 30
  # Host-key handling is trust-on-first-use: the host's key is pinned on first
  # connect and a later CHANGED key is rejected. The bench SSH key the monitor
  # uses is $SSH_KEY (auto-generated by the installer).
  known_hosts_path: "$SSH_DIR/known_hosts"
  insecure_skip_host_key_check: false   # true disables verification (DEV ONLY)

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
  default_interval_seconds: 900
  max_parallel: 10
  per_job_timeout_seconds: 30

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

realtime:
  enabled: true
  ping_interval_seconds: 30
  write_timeout_seconds: 10
  send_buffer: 128
  max_clients: 512

# Control panel: allowlisted bench/service commands over SSH. Long actions
# (bench update/migrate) need a generous ceiling, separate from ssh.command_timeout.
control:
  action_timeout_seconds: 300
  danger_action_timeout_seconds: 1800
  read_timeout_seconds: 20
  max_output_bytes: 65536
  max_concurrent: 4

# DB replication monitor (idle until you add targets in the dashboard).
dbmonitor:
  interval_seconds: 60
  max_parallel: 4
  command_timeout_seconds: 15

# Per-event log streaming to Loki (off by default; enable + deploy the stream
# script on each bench, then list the files to tail).
streaming:
  enabled: false
  monitor_id: ""
  script_path: ""
  files: []
  flush_interval_seconds: 1
  max_batch_lines: 500
  push_timeout_seconds: 10
  min_backoff_seconds: 1
  max_backoff_seconds: 60
  max_line_bytes: 1048576
EOF
    chmod 0640 "$CONF_FILE"
    if [ "$IS_MAC" = 0 ]; then
        chown root:"$INSTALL_GROUP" "$CONF_FILE"
    fi
    ok "wrote $CONF_FILE (auth + alerts populated from your answers)"
else
    ok "preserving existing $CONF_FILE"
fi

install -m 0644 "$REPO_ROOT/deploy/docker-compose.prod.yml" "$DEPLOY_DIR/docker-compose.prod.yml"
# Loki retention config (the compose mounts ./config/loki.yaml into the container).
install -d -m 0755 "$DEPLOY_DIR/config"
install -m 0644 "$REPO_ROOT/deploy/config/loki.yaml" "$DEPLOY_DIR/config/loki.yaml"
# Caddyfile example for optional TLS reverse proxy (referenced by deployment.md).
[ -f "$REPO_ROOT/deploy/Caddyfile.example" ] && install -m 0644 "$REPO_ROOT/deploy/Caddyfile.example" "$DEPLOY_DIR/Caddyfile.example"
ok "installed compose + loki config to $DEPLOY_DIR"

# ---------------------------------------------------------------------------
# Step 5: install + start the service
# ---------------------------------------------------------------------------
echo "==> service + stack"

wait_for_backends() {
    for _ in $(seq 1 30); do
        if curl -fsS http://127.0.0.1:8428/health >/dev/null 2>&1 \
           && curl -fsS http://127.0.0.1:3100/ready >/dev/null 2>&1; then
            return 0
        fi
        sleep 1
    done
    curl -fsS http://127.0.0.1:8428/health >/dev/null 2>&1 || fail "VM health check never succeeded"
    curl -fsS http://127.0.0.1:3100/ready  >/dev/null 2>&1 || fail "Loki readiness never succeeded"
}

if [ "$IS_MAC" = 1 ]; then
    # Bring up VM + Loki (restart: unless-stopped keeps them alive across
    # Docker restarts; no separate stack agent needed).
    if [ "$SKIP_STACK" = "0" ]; then
        ( cd "$DEPLOY_DIR" && compose -f docker-compose.prod.yml up -d ) \
            || fail "docker compose up failed (is the Docker engine running?)"
        ok "VM + Loki started (docker compose up -d)"
        wait_for_backends
        ok "VM + Loki healthy"
    else
        note "--no-stack: point metrics.vm_url + logs.loki_url in $CONF_FILE at your existing tier."
    fi

    # launchd LaunchAgent for the binary.
    cat > "$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>$PLIST_LABEL</string>
  <key>ProgramArguments</key>
  <array>
    <string>$BIN_DST</string>
    <string>--config</string>
    <string>$CONF_FILE</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>WorkingDirectory</key><string>$BASE_DIR</string>
  <key>StandardOutPath</key><string>$LOG_DIR/monitor.log</string>
  <key>StandardErrorPath</key><string>$LOG_DIR/monitor.log</string>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string></dict>
</dict>
</plist>
EOF
    chmod 0644 "$PLIST"
    launchctl unload "$PLIST" 2>/dev/null || true
    launchctl load -w "$PLIST"
    ok "loaded LaunchAgent $PLIST_LABEL"
    _up=0
    for _ in $(seq 1 15); do
        if curl -fsS "http://127.0.0.1:8080/healthz" >/dev/null 2>&1; then _up=1; break; fi
        sleep 1
    done
    if [ "$_up" = 1 ]; then
        ok "frappe-monitor is responding on :8080"
    else
        note "service loaded but /healthz not up after 15s — tail logs: tail -f $LOG_DIR/monitor.log"
    fi
else
    # Linux: systemd units.
    install -m 0644 "$REPO_ROOT/deploy/systemd/frappe-monitor.service"       "$SYSTEMD_DIR/frappe-monitor.service"

    # The stack unit is GENERATED (not copied) so ExecStart/ExecStop match the
    # compose flavor actually present on this host: the static unit hardcoded
    # `/usr/bin/docker compose` (v2 plugin), which fails on reboot on a host
    # that only has standalone `docker-compose`. Resolve the same way the
    # compose() wrapper does.
    DOCKER_BIN="$(command -v docker || echo /usr/bin/docker)"
    if docker compose version >/dev/null 2>&1; then
        STACK_START="$DOCKER_BIN compose -f docker-compose.prod.yml up -d"
        STACK_STOP="$DOCKER_BIN compose -f docker-compose.prod.yml down"
    else
        DC_BIN="$(command -v docker-compose)"
        STACK_START="$DC_BIN -f docker-compose.prod.yml up -d"
        STACK_STOP="$DC_BIN -f docker-compose.prod.yml down"
    fi
    cat > "$SYSTEMD_DIR/frappe-monitor-stack.service" <<EOF
[Unit]
Description=Frappe Monitor backing stack (VictoriaMetrics + Loki)
Documentation=https://github.com/sakthikumarp/frappe_monitor
Wants=docker.service network-online.target
After=docker.service network-online.target

[Service]
Type=oneshot
RemainAfterExit=true
WorkingDirectory=$DEPLOY_DIR
ExecStart=$STACK_START
ExecStop=$STACK_STOP
TimeoutStartSec=120s

[Install]
WantedBy=multi-user.target
EOF
    chmod 0644 "$SYSTEMD_DIR/frappe-monitor-stack.service"
    systemctl daemon-reload
    ok "installed systemd units"

    if [ "$SKIP_STACK" = "0" ]; then
        systemctl enable frappe-monitor-stack.service >/dev/null
        systemctl restart frappe-monitor-stack.service
        ok "frappe-monitor-stack.service (VM + Loki) started"
        wait_for_backends
        ok "VM + Loki healthy"
    else
        note "--no-stack: point metrics.vm_url + logs.loki_url in $CONF_FILE at your existing tier."
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
fi

# ---------------------------------------------------------------------------
# Step 6: post-install summary
# ---------------------------------------------------------------------------
echo
echo "==> install complete"
echo
echo "  Binary:    $BIN_DST"
echo "  Config:    $CONF_FILE"
echo "  Data:      $STATE_DIR/"
echo "  Compose:   $DEPLOY_DIR/docker-compose.prod.yml"
echo
if [ "$IS_MAC" = 1 ]; then
    echo "  Status:    launchctl list | grep frappe-monitor"
    echo "  Logs:      tail -f $LOG_DIR/monitor.log"
    echo "  Stop:      launchctl unload $PLIST"
    echo "  Start:     launchctl load -w $PLIST"
    echo "  Stack:     cd $DEPLOY_DIR && docker compose -f docker-compose.prod.yml {ps,down,up -d}"
    echo
    echo "  Dashboard: http://localhost:8080"
else
    echo "  Status:    sudo systemctl status frappe-monitor"
    echo "  Logs:      sudo journalctl -u frappe-monitor -f"
    echo "  Stop:      sudo systemctl stop frappe-monitor frappe-monitor-stack"
    echo "  Start:     sudo systemctl start frappe-monitor-stack frappe-monitor"
    echo
    echo "  Dashboard: http://$(hostname):8080"
fi
echo
echo "  SSH key:   $SSH_KEY  (use this path when adding a server)"
echo "             authorize it on each bench:  ssh-copy-id -i $SSH_KEY.pub <user>@<bench-host>"
echo
echo "  SECURITY:  the dashboard serves plain HTTP on :8080 (all interfaces)."
echo "             For production, restrict the port with a firewall AND put TLS"
echo "             in front — see $DEPLOY_DIR/Caddyfile.example + docs/guide/deployment.md."
echo
echo "  Next: open the dashboard, log in, and add your first bench server (key path above)."
