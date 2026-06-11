# Getting started — from build to a verified install

A step-by-step runbook to stand up frappe-monitor and check every part works,
in order. Two tracks: a **quick local check** (5 minutes, SQLite, no service)
to see it run, then a **production install** (MariaDB + metrics/logs + a managed
service). Finish by exercising each feature.

---

## 0. Prerequisites

```bash
go version              # need >= 1.25
node --version          # need >= 20  (only to build the UI bundle)
docker compose version  # for VictoriaMetrics + Loki (and optional test DBs)
```

On macOS, start Docker Desktop and wait until `docker ps` works.

---

## 1. Build the binary

```bash
cd frappe-monitor
make build               # builds the UI, then the Go binary with it embedded
ls -lh bin/monitor-server
```

`bin/monitor-server` is the whole app — server, installer, and the dashboard UI
all in one file. Everything below uses it.

---

## What happens when it boots (the "initialization" sequence)

Each start runs, in order:

1. **Load config** (`--config <file>`), apply defaults, validate.
2. **Resolve the database** driver + DSN from the `database:` block.
3. **Open + auto-migrate**: connect to the DB, then ent creates/alters tables
   to match the schema (additive — like `bench migrate`'s DDL step).
4. **Run data migrations**: apply any pending one-time **patches** (recorded in
   `patch_logs`, exactly-once), then idempotent **seeds**.
5. **Reconcile** any control-panel runs left mid-flight by a previous crash.
6. **Start services**: SSH pool, realtime WebSocket hub, metrics push, the
   scheduler, alerts, log streamer, DB monitor, control panel.
7. **Serve HTTP** on `server.listen_addr` (dashboard + API + `/healthz`).

You can watch this in the logs (step 5 / 7 below).

---

## 2. Track A — quick local check (SQLite, foreground)

Fastest way to see it run. No DB server, no metrics tier, no service.

```bash
mkdir -p /tmp/fm-check
cat > /tmp/fm-check/monitor.yaml <<'EOF'
server:
  listen_addr: "127.0.0.1:8080"
database:
  driver: "sqlite"
  path: "/tmp/fm-check/monitor.db"
auth:
  password: "check123"
EOF

./bin/monitor-server --config /tmp/fm-check/monitor.yaml
```

In the log you should see `database connected driver=sqlite` then
`http listening addr=127.0.0.1:8080`. In another terminal:

```bash
curl -s http://127.0.0.1:8080/healthz                 # {"status":"ok"}
curl -s -o /dev/null -w "%{http_code}\n" \
     http://127.0.0.1:8080/api/v1/whoami              # 401 (auth required)
curl -s -u admin:check123 http://127.0.0.1:8080/api/v1/whoami   # {"ok":true}
open http://127.0.0.1:8080                            # log in: any user + check123
```

`Ctrl-C` to stop. (Charts stay empty here — metrics need the tier in Track B and
a collected server. This track just proves the app + DB + auth + UI work.)

---

## 3. Track B — production install (MariaDB + metrics/logs + service)

### 3a. Create the database

Use any MariaDB/MySQL (your bench's MariaDB, a server, or Docker). Create a DB
and a user for the monitor:

```sql
CREATE DATABASE frappe_monitor CHARACTER SET utf8mb4;
CREATE USER 'monitor'@'%' IDENTIFIED BY 'a-strong-db-password';
GRANT ALL PRIVILEGES ON frappe_monitor.* TO 'monitor'@'%';
FLUSH PRIVILEGES;
```

(PostgreSQL works too — use `--db postgres` below and `createdb frappe_monitor`
+ a role.) The monitor creates its own tables on first boot; you only provide
an empty database.

### 3b. Bring up the metrics + logs tier

```bash
docker compose -f deploy/docker-compose.prod.yml up -d
curl -fsS http://127.0.0.1:8428/health && echo " VM ok"
curl -fsS http://127.0.0.1:3100/ready  && echo " Loki ok"
```

Both bind to `127.0.0.1` only. In Docker Desktop settings, enable
"start at login" so they survive a reboot.

### 3c. Install (cross-platform)

`install` lays out the binary + config + a service file in a root you choose.
It works the same on Windows, Linux, and macOS.

```bash
./bin/monitor-server install \
  --root ~/.frappe-monitor \
  --listen 127.0.0.1:8080 \
  --db mariadb --db-host 127.0.0.1 --db-port 3306 \
  --db-user monitor --db-password 'a-strong-db-password' --db-name frappe_monitor \
  --password 'your-dashboard-password' \
  --service launchd
```

Omit any flag to be prompted for it (just `./bin/monitor-server install`).
Omit `--password` to auto-generate one (printed once — save it). It prints the
exact commands to enable the service.

### 3d. Enable the service

It prints these; for launchd on macOS:

```bash
cp ~/.frappe-monitor/com.frappe-monitor.plist ~/Library/LaunchAgents/
launchctl load -w ~/Library/LaunchAgents/com.frappe-monitor.plist
```

(Linux → `systemd`, prints `systemctl enable --now`; or `--service supervisor` /
`--service pm2` for those managers. On Windows use `--service pm2`.)

### 3e. Verify

```bash
curl -s http://127.0.0.1:8080/healthz
tail -f ~/.frappe-monitor/logs/monitor.log     # watch the boot sequence above
open http://127.0.0.1:8080
```

---

## 4. First login

Open the dashboard, log in with any username + your dashboard password. You land
on the Servers page (empty until you add one).

---

## 5. Exercise the features, one by one

### 5a. Add a monitored server (Servers)
**Servers → Add.** Give it the SSH host, user, key path, and (optional) bench
paths. The monitor SSHes out on a schedule (default 15 min) to collect metrics +
inventory. Use **Test SSH** to check reachability immediately. Status flips to
`reachable` once a pull succeeds.

> The monitor only needs read-only SSH for collection. The Control panel (5e)
> needs the SSH user to be able to run `bench`/`supervisorctl`.

### 5b. Metrics + System details (Server detail)
Click a server. After the first collection you'll see CPU/memory/disk charts
(from VictoriaMetrics) and a System details card (OS, RAM, services). Use the
timeline filter (top bar) to change the range.

### 5c. Benches & Sites
**Benches** and **Sites** list everything discovered across your servers. Click
into a site for its per-site metrics. (These derive from the collected metrics,
so they populate after the first pulls.)

### 5d. DB Monitor (Databases)
**Databases → Add database.** Point it at a MariaDB/MySQL **or** PostgreSQL
replica (engine dropdown), give the client command + creds file, set a lag
threshold. The monitor checks replication health over SSH and alerts when a
replica falls behind or replication stops. **Check now** runs an immediate check.

### 5e. Control panel (Control)
**Control.** Pick a server + an allowlisted action (bench migrate / clear-cache /
build / update / restart, or supervisor restart/status) and run it — each run is
confirmed and recorded in the live **History** feed (status, duration, output).
The **Edit site config** panel loads a site's `site_config.json`, lets you edit
it (JSON-validated), and optionally restarts the bench. No arbitrary commands;
no site create/rename/drop.

### 5f. Alerts
**Alerts** shows firing/resolved alerts. Default rules cover unreachable servers,
disk almost full, unhealthy sites, replication lag, etc. To get Telegram pushes,
set `alerts.enabled: true` + `alerts.telegram.bot_token` + `chat_ids` in the
config and restart.

---

## 6. Day-2 operations

```bash
# logs
tail -f ~/.frappe-monitor/logs/monitor.log

# change a setting: edit the config, then restart the service
$EDITOR ~/.frappe-monitor/monitor.yaml
launchctl kickstart -k gui/$(id -u)/com.frappe-monitor     # macOS restart

# upgrade: rebuild, then re-run install (idempotent — keeps your config)
make build && ./bin/monitor-server install --root ~/.frappe-monitor --yes

# metrics tier
docker compose -f deploy/docker-compose.prod.yml ps
docker compose -f deploy/docker-compose.prod.yml down       # stop (data persists)

# remove
./bin/monitor-server uninstall --root ~/.frappe-monitor            # keep data
./bin/monitor-server uninstall --root ~/.frappe-monitor --purge    # delete everything
```

---

## 7. Where things live (after a Track B install)

```
~/.frappe-monitor/
  bin/frappe-monitor          the binary (UI embedded)
  monitor.yaml                your config (edit + restart to apply)
  data/                       sqlite file IF driver=sqlite (none for mariadb/postgres)
  logs/monitor.log            stdout/stderr
  com.frappe-monitor.plist    the generated service file
```

The database (MariaDB/Postgres) holds all state — servers, DB targets, alert
state, and the control-panel audit log. Metrics live in VictoriaMetrics, logs in
Loki (Docker volumes `frappe-monitor-vm-data` / `frappe-monitor-loki-data`).
