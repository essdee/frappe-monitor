package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Server    ServerConfig    `koanf:"server"`
	Database  DatabaseConfig  `koanf:"database"`
	SSH       SSHConfig       `koanf:"ssh"`
	Log       LogConfig       `koanf:"log"`
	Metrics   MetricsConfig   `koanf:"metrics"`
	Logs      LogsConfig      `koanf:"logs"`
	Scheduler SchedulerConfig `koanf:"scheduler"`
	Auth      AuthConfig      `koanf:"auth"`
	Alerts    AlertsConfig    `koanf:"alerts"`
	Streaming StreamingConfig `koanf:"streaming"`
	Realtime  RealtimeConfig  `koanf:"realtime"`
	Control   ControlConfig   `koanf:"control"`
	DBMonitor DBMonitorConfig `koanf:"dbmonitor"`
}

// ControlConfig tunes the Phase-10 control panel (allowlisted bench/service
// commands + site-config edits over SSH). Action timeouts are the important
// knob: long bench update/migrate runs need a generous ceiling.
type ControlConfig struct {
	// ActionTimeoutSeconds bounds a normal action. Default 300 (5 min).
	ActionTimeoutSeconds int `koanf:"action_timeout_seconds"`
	// DangerActionTimeoutSeconds bounds a Dangerous action (bench update).
	// Default 1800 (30 min).
	DangerActionTimeoutSeconds int `koanf:"danger_action_timeout_seconds"`
	// ReadTimeoutSeconds bounds a synchronous read (site-config fetch).
	// Default 20.
	ReadTimeoutSeconds int `koanf:"read_timeout_seconds"`
	// MaxOutputBytes caps stored command output. Default 65536 (64 KiB).
	MaxOutputBytes int `koanf:"max_output_bytes"`
	// MaxConcurrent caps in-flight control runs. Default 4.
	MaxConcurrent int `koanf:"max_concurrent"`
}

// DBMonitorConfig tunes the Phase-9 DB replication monitor.
type DBMonitorConfig struct {
	// IntervalSeconds is the sweep cadence across all targets. Default 60.
	IntervalSeconds int `koanf:"interval_seconds"`
	// MaxParallel caps concurrent target checks per sweep. Default 4.
	MaxParallel int `koanf:"max_parallel"`
	// CommandTimeoutSeconds bounds a single status query. Default 15.
	CommandTimeoutSeconds int `koanf:"command_timeout_seconds"`
}

type ServerConfig struct {
	ListenAddr          string `koanf:"listen_addr"`
	ReadTimeoutSeconds  int    `koanf:"read_timeout_seconds"`
	WriteTimeoutSeconds int    `koanf:"write_timeout_seconds"`
	// MaxBodyBytes caps the request body size accepted by any JSON handler
	// (including the pre-auth /login). Protects against pathological/huge
	// bodies. Default 1 MiB.
	MaxBodyBytes int64 `koanf:"max_body_bytes"`
}

// DatabaseConfig selects and configures the persistent store. The production
// backends are MariaDB/MySQL and PostgreSQL; SQLite remains the zero-config
// default for local dev and the test suite. Every connection field is
// configurable so an operator can point the monitor at an existing DB server.
type DatabaseConfig struct {
	// Driver: "mariadb"/"mysql", "postgres"/"postgresql", or "sqlite".
	Driver string `koanf:"driver"`

	// SQLite only: the database file path.
	Path string `koanf:"path"`

	// Server engines (mariadb / postgres).
	Host     string `koanf:"host"`
	Port     int    `koanf:"port"` // 0 → engine default (3306 / 5432)
	User     string `koanf:"user"`
	Password string `koanf:"password"`
	Name     string `koanf:"name"` // database/schema name
	// SSLMode applies to postgres (disable|require|verify-ca|verify-full);
	// ignored for mysql/mariadb.
	SSLMode string `koanf:"sslmode"`
	// Params appends extra DSN parameters verbatim (advanced; optional).
	Params string `koanf:"params"`

	// Connection pool sizing (0 → driver/engine sensible default).
	MaxOpenConns int `koanf:"max_open_conns"`
	MaxIdleConns int `koanf:"max_idle_conns"`
}

// NormalizedDriver maps the configured driver name to the storage engine key
// ("sqlite" | "mysql" | "postgres"). mariadb is an alias of mysql.
func (d DatabaseConfig) NormalizedDriver() (string, error) {
	switch strings.ToLower(strings.TrimSpace(d.Driver)) {
	case "", "sqlite", "sqlite3":
		return "sqlite", nil
	case "mysql", "mariadb":
		return "mysql", nil
	case "postgres", "postgresql", "pgx", "pg":
		return "postgres", nil
	default:
		return "", fmt.Errorf("unsupported database.driver %q (want mariadb|postgres|sqlite)", d.Driver)
	}
}

// DSN resolves the config into a (driver, dsn) pair for storage.Open. driver is
// the normalized engine key; dsn is the engine-specific connection string.
func (d DatabaseConfig) DSN() (driver, dsn string, err error) {
	drv, err := d.NormalizedDriver()
	if err != nil {
		return "", "", err
	}
	switch drv {
	case "sqlite":
		return drv, SQLiteDSN(d.Path), nil
	case "mysql":
		port := d.Port
		if port == 0 {
			port = 3306
		}
		params := "parseTime=true&loc=UTC&charset=utf8mb4"
		if d.Params != "" {
			params += "&" + d.Params
		}
		// go-sql-driver: user:pass@tcp(host:port)/db?params
		dsn = fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?%s",
			d.User, d.Password, hostOr(d.Host), port, d.Name, params)
		return drv, dsn, nil
	case "postgres":
		port := d.Port
		if port == 0 {
			port = 5432
		}
		ssl := d.SSLMode
		if ssl == "" {
			ssl = "disable"
		}
		u := url.URL{
			Scheme: "postgres",
			User:   url.UserPassword(d.User, d.Password),
			Host:   fmt.Sprintf("%s:%d", hostOr(d.Host), port),
			Path:   "/" + d.Name,
		}
		q := url.Values{}
		q.Set("sslmode", ssl)
		u.RawQuery = q.Encode()
		dsn = u.String()
		if d.Params != "" {
			dsn += "&" + d.Params
		}
		return drv, dsn, nil
	}
	return "", "", fmt.Errorf("unreachable driver %q", drv)
}

func hostOr(h string) string {
	if h == "" {
		return "127.0.0.1"
	}
	return h
}

// SQLiteDSN builds the modernc.org/sqlite DSN with the recommended PRAGMAs
// (WAL, foreign-key enforcement, sane busy timeout + caches).
func SQLiteDSN(path string) string {
	v := url.Values{}
	v.Add("_pragma", "journal_mode(wal)")
	v.Add("_pragma", "synchronous(normal)")
	v.Add("_pragma", "busy_timeout(5000)")
	v.Add("_pragma", "foreign_keys(on)")
	v.Add("_pragma", "temp_store(memory)")
	v.Add("_pragma", "mmap_size(134217728)")
	v.Add("_pragma", "cache_size(-64000)")
	return "file:" + path + "?" + v.Encode()
}

type SSHConfig struct {
	DialTimeoutSeconds    int `koanf:"dial_timeout_seconds"`
	CommandTimeoutSeconds int `koanf:"command_timeout_seconds"`
	// KnownHostsPath is the OpenSSH known_hosts file used to pin/verify remote
	// host keys. Empty → ~/.ssh/known_hosts of the user the monitor runs as
	// (created automatically if absent).
	KnownHostsPath string `koanf:"known_hosts_path"`
	// InsecureSkipHostKeyCheck disables SSH host-key verification entirely.
	// Dangerous (MITM-able) — intended only for dev. Default false, which uses
	// trust-on-first-use: a host's key is pinned on first connect and accepted,
	// and a later CHANGED key is rejected (no manual ssh-keyscan needed).
	InsecureSkipHostKeyCheck bool `koanf:"insecure_skip_host_key_check"`
}

type LogConfig struct {
	Level  string `koanf:"level"`
	Format string `koanf:"format"`
}

// MetricsConfig is the VictoriaMetrics push + query target.
type MetricsConfig struct {
	VMURL               string `koanf:"vm_url"` // e.g. http://127.0.0.1:8428
	PushTimeoutSeconds  int    `koanf:"push_timeout_seconds"`
	QueryTimeoutSeconds int    `koanf:"query_timeout_seconds"` // for /api/v1/metrics/query proxy
}

// LogsConfig is the Loki push + query target.
type LogsConfig struct {
	LokiURL             string `koanf:"loki_url"` // e.g. http://127.0.0.1:3100
	PushTimeoutSeconds  int    `koanf:"push_timeout_seconds"`
	QueryTimeoutSeconds int    `koanf:"query_timeout_seconds"`
}

// SchedulerConfig governs the per-server pull cadence and parallelism.
type SchedulerConfig struct {
	DefaultIntervalSeconds int `koanf:"default_interval_seconds"`
	MaxParallel            int `koanf:"max_parallel"`
	PerJobTimeoutSeconds   int `koanf:"per_job_timeout_seconds"`
}

// AuthConfig is the Phase 7 single-tenant auth knob. Empty Password
// means auth is off (the dashboard + API are open). Non-empty
// password applies HTTP basic auth — browsers prompt for credentials
// natively, so the SPA needs no login page.
type AuthConfig struct {
	Password string `koanf:"password"`
	Realm    string `koanf:"realm"`
}

// AlertsConfig mirrors internal/alerts.Config. Duplicated here as a
// flat struct so koanf binds it from yaml; the alerts package's
// Config has the canonical Validate().
type AlertsConfig struct {
	Enabled                   bool           `koanf:"enabled"`
	EvaluationIntervalSeconds int            `koanf:"evaluation_interval_seconds"`
	NotifyRepeatSeconds       int            `koanf:"notify_repeat_seconds"`
	VMQueryTimeoutSeconds     int            `koanf:"vm_query_timeout_seconds"`
	Telegram                  AlertsTelegram `koanf:"telegram"`
	Rules                     []AlertsRule   `koanf:"rules"`
	DisableDefaults           bool           `koanf:"disable_defaults"`
}

type AlertsTelegram struct {
	BotToken           string   `koanf:"bot_token"`
	ChatIDs            []string `koanf:"chat_ids"`
	SendTimeoutSeconds int      `koanf:"send_timeout_seconds"`
}

type AlertsRule struct {
	Name              string   `koanf:"name"`
	Expr              string   `koanf:"expr"`
	Severity          string   `koanf:"severity"`
	Message           string   `koanf:"message"`
	FingerprintLabels []string `koanf:"fingerprint_labels"`
}

// StreamingConfig mirrors internal/streamer.Config. Keep the koanf
// shape close to the runtime struct so the wiring in main.go is a
// straight field copy.
type StreamingConfig struct {
	Enabled              bool                  `koanf:"enabled"`
	MonitorID            string                `koanf:"monitor_id"`
	ScriptPath           string                `koanf:"script_path"`
	Files                []StreamingFileConfig `koanf:"files"`
	FlushIntervalSeconds int                   `koanf:"flush_interval_seconds"`
	MaxBatchLines        int                   `koanf:"max_batch_lines"`
	PushTimeoutSeconds   int                   `koanf:"push_timeout_seconds"`
	MinBackoffSeconds    int                   `koanf:"min_backoff_seconds"`
	MaxBackoffSeconds    int                   `koanf:"max_backoff_seconds"`
	MaxLineBytes         int                   `koanf:"max_line_bytes"`
}

type StreamingFileConfig struct {
	ID   string `koanf:"id"`
	Path string `koanf:"path"`
}

// RealtimeConfig tunes the WebSocket push layer (the dashboard's
// transport). The WebSocket shares the main HTTP listener, so its "port"
// is server.listen_addr — there is no separate port to configure.
type RealtimeConfig struct {
	// Enabled exposes GET /api/v1/ws. When false the endpoint isn't
	// mounted and the dashboard falls back to manual reloads. Default true.
	Enabled bool `koanf:"enabled"`
	// PingIntervalSeconds is the keepalive cadence per connection.
	PingIntervalSeconds int `koanf:"ping_interval_seconds"`
	// WriteTimeoutSeconds bounds a single frame write / ping.
	WriteTimeoutSeconds int `koanf:"write_timeout_seconds"`
	// SendBuffer is the per-client queue depth before a slow client is
	// dropped. Bounds per-connection memory.
	SendBuffer int `koanf:"send_buffer"`
	// MaxClients caps total concurrent connections (0 = unlimited). A
	// safety bound so a connection flood can't exhaust the monitor host.
	MaxClients int `koanf:"max_clients"`
}

func defaults() *koanf.Koanf {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(map[string]any{
		"server.listen_addr":                    ":8080",
		"server.read_timeout_seconds":           15,
		"server.write_timeout_seconds":          60,
		"server.max_body_bytes":                 1 << 20, // 1 MiB
		"database.driver":                       "sqlite",
		"database.path":                         "./data/monitor.db",
		"ssh.dial_timeout_seconds":              10,
		"ssh.command_timeout_seconds":           30,
		"ssh.known_hosts_path":                  "",
		"ssh.insecure_skip_host_key_check":      false,
		"log.level":                             "info",
		"log.format":                            "json",
		"metrics.vm_url":                        "http://127.0.0.1:8428",
		"metrics.push_timeout_seconds":          5,
		"metrics.query_timeout_seconds":         15,
		"logs.loki_url":                         "http://127.0.0.1:3100",
		"logs.push_timeout_seconds":             5,
		"logs.query_timeout_seconds":            15,
		"scheduler.default_interval_seconds":    900, // 15 min — master plan §5 default
		"scheduler.max_parallel":                10,
		"scheduler.per_job_timeout_seconds":     30,
		"auth.password":                         "",
		"auth.realm":                            "frappe-monitor",
		"alerts.enabled":                        false,
		"alerts.evaluation_interval_seconds":    60,
		"alerts.notify_repeat_seconds":          3600,
		"alerts.vm_query_timeout_seconds":       10,
		"alerts.telegram.send_timeout_seconds":  5,
		"alerts.disable_defaults":               false,
		"streaming.enabled":                     false,
		"streaming.script_path":                 "",
		"streaming.flush_interval_seconds":      1,
		"streaming.max_batch_lines":             500,
		"streaming.push_timeout_seconds":        10,
		"streaming.min_backoff_seconds":         1,
		"streaming.max_backoff_seconds":         60,
		"streaming.max_line_bytes":              1 << 20, // 1 MiB
		"realtime.enabled":                      true,
		"realtime.ping_interval_seconds":        30,
		"realtime.write_timeout_seconds":        10,
		"realtime.send_buffer":                  128,
		"realtime.max_clients":                  512,
		"control.action_timeout_seconds":        300,
		"control.danger_action_timeout_seconds": 1800,
		"control.read_timeout_seconds":          20,
		"control.max_output_bytes":              65536,
		"control.max_concurrent":                4,
		"dbmonitor.interval_seconds":            60,
		"dbmonitor.max_parallel":                4,
		"dbmonitor.command_timeout_seconds":     15,
	}, "."), nil); err != nil {
		panic(fmt.Sprintf("config defaults: %v", err))
	}
	return k
}

func Load(path string) (*Config, error) {
	k := defaults()
	if err := k.Load(file.Provider(path), yaml.Parser()); err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	// Env overrides: MONITOR_SERVER__LISTEN_ADDR -> server.listen_addr
	err := k.Load(env.Provider("MONITOR_", ".", func(s string) string {
		return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(s, "MONITOR_")), "__", ".")
	}), nil)
	if err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}
	var cfg Config
	if err := k.Unmarshal("", &cfg); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Reconcile clamps clampable values to safe equivalents (instead of failing)
// and returns a human-readable warning for each adjustment, so the caller can
// log them. This keeps an in-place binary upgrade from bricking the service:
// a config that predates a bumped default (e.g. write_timeout_seconds: 15 from
// an older release, below the now-30+ command timeout) is raised and logged
// rather than rejected. Call it after Load, then log the returned warnings.
func (c *Config) Reconcile() []string {
	var warns []string
	if c.Server.WriteTimeoutSeconds < c.SSH.CommandTimeoutSeconds {
		warns = append(warns, fmt.Sprintf(
			"server.write_timeout_seconds (%d) is below ssh.command_timeout_seconds (%d); raising it to %d so a slow SSH-backed response isn't severed",
			c.Server.WriteTimeoutSeconds, c.SSH.CommandTimeoutSeconds, c.SSH.CommandTimeoutSeconds))
		c.Server.WriteTimeoutSeconds = c.SSH.CommandTimeoutSeconds
	}
	if c.Streaming.Enabled && c.Streaming.MaxLineBytes > 0 && c.Streaming.MaxLineBytes < 4096 {
		warns = append(warns, fmt.Sprintf(
			"streaming.max_line_bytes (%d) is below the 4096 floor; raising to 1 MiB", c.Streaming.MaxLineBytes))
		c.Streaming.MaxLineBytes = 1 << 20
	}
	return warns
}

// validateDatabase checks the database block against the selected driver:
// sqlite needs a path; the server engines need host/user/name.
func (c *Config) validateDatabase() error {
	drv, err := c.Database.NormalizedDriver()
	if err != nil {
		return err
	}
	switch drv {
	case "sqlite":
		if c.Database.Path == "" {
			return fmt.Errorf("database.path is required for the sqlite driver")
		}
	case "mysql", "postgres":
		if c.Database.Host == "" {
			return fmt.Errorf("database.host is required for the %s driver", c.Database.Driver)
		}
		if c.Database.User == "" {
			return fmt.Errorf("database.user is required for the %s driver", c.Database.Driver)
		}
		if c.Database.Name == "" {
			return fmt.Errorf("database.name is required for the %s driver", c.Database.Driver)
		}
		// A ':' in the MySQL/MariaDB user would corrupt the "user:pass@..."
		// DSN split (go-sql-driver anchors on the first ':'). Postgres is
		// safe (url.UserPassword escapes), so this only matters for mysql.
		if drv == "mysql" && strings.Contains(c.Database.User, ":") {
			return fmt.Errorf("database.user must not contain ':' for the %s driver", c.Database.Driver)
		}
	}
	return nil
}

// validate enforces invariants on the merged config. It guards against
// explicit empty overrides (e.g. `path: ""` in YAML) and out-of-range values
// for fields wired into long-lived runtime components like the SSH pool and
// HTTP server. Absent keys fall back to defaults() and never reach these checks.
func (c *Config) validate() error {
	if err := c.validateDatabase(); err != nil {
		return err
	}
	if c.Server.ListenAddr == "" {
		return fmt.Errorf("server.listen_addr is required")
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level must be one of debug|info|warn|error, got %q", c.Log.Level)
	}
	switch c.Log.Format {
	case "json", "text":
	default:
		return fmt.Errorf("log.format must be one of json|text, got %q", c.Log.Format)
	}

	if c.Server.ReadTimeoutSeconds < 1 {
		return fmt.Errorf("server.read_timeout_seconds must be >= 1, got %d", c.Server.ReadTimeoutSeconds)
	}
	if c.Server.WriteTimeoutSeconds < 1 {
		return fmt.Errorf("server.write_timeout_seconds must be >= 1, got %d", c.Server.WriteTimeoutSeconds)
	}
	if c.Server.MaxBodyBytes < 1 {
		return fmt.Errorf("server.max_body_bytes must be >= 1, got %d", c.Server.MaxBodyBytes)
	}
	if c.SSH.DialTimeoutSeconds < 1 {
		return fmt.Errorf("ssh.dial_timeout_seconds must be >= 1, got %d", c.SSH.DialTimeoutSeconds)
	}
	if c.SSH.CommandTimeoutSeconds < 1 {
		return fmt.Errorf("ssh.command_timeout_seconds must be >= 1, got %d", c.SSH.CommandTimeoutSeconds)
	}
	// NOTE: the server.write_timeout_seconds >= ssh.command_timeout_seconds
	// invariant is enforced by Reconcile() as a clamp+warn, NOT a hard error —
	// an existing deployment whose on-disk config predates the bumped default
	// must keep booting on upgrade (the SSH routes also extend their own write
	// deadline at runtime, so a low static value is non-fatal).

	if c.Metrics.VMURL == "" {
		return fmt.Errorf("metrics.vm_url is required")
	}
	if c.Metrics.PushTimeoutSeconds < 1 {
		return fmt.Errorf("metrics.push_timeout_seconds must be >= 1, got %d", c.Metrics.PushTimeoutSeconds)
	}
	if c.Metrics.QueryTimeoutSeconds < 1 {
		return fmt.Errorf("metrics.query_timeout_seconds must be >= 1, got %d", c.Metrics.QueryTimeoutSeconds)
	}
	if c.Logs.LokiURL == "" {
		return fmt.Errorf("logs.loki_url is required")
	}
	if c.Logs.PushTimeoutSeconds < 1 {
		return fmt.Errorf("logs.push_timeout_seconds must be >= 1, got %d", c.Logs.PushTimeoutSeconds)
	}
	if c.Logs.QueryTimeoutSeconds < 1 {
		return fmt.Errorf("logs.query_timeout_seconds must be >= 1, got %d", c.Logs.QueryTimeoutSeconds)
	}
	if c.Scheduler.DefaultIntervalSeconds < 1 {
		return fmt.Errorf("scheduler.default_interval_seconds must be >= 1, got %d", c.Scheduler.DefaultIntervalSeconds)
	}
	if c.Scheduler.MaxParallel < 1 {
		return fmt.Errorf("scheduler.max_parallel must be >= 1, got %d", c.Scheduler.MaxParallel)
	}
	if c.Scheduler.PerJobTimeoutSeconds < 1 {
		return fmt.Errorf("scheduler.per_job_timeout_seconds must be >= 1, got %d", c.Scheduler.PerJobTimeoutSeconds)
	}

	// Alerts: the alerts package validates the deeper invariants
	// (telegram bot token, chat IDs, per-rule fields). Here we only
	// guard the koanf-bound numeric fields so a typo doesn't divide
	// by zero somewhere downstream.
	if c.Alerts.Enabled {
		if c.Alerts.EvaluationIntervalSeconds < 15 {
			return fmt.Errorf("alerts.evaluation_interval_seconds must be >= 15, got %d", c.Alerts.EvaluationIntervalSeconds)
		}
		if c.Alerts.VMQueryTimeoutSeconds < 1 {
			return fmt.Errorf("alerts.vm_query_timeout_seconds must be >= 1, got %d", c.Alerts.VMQueryTimeoutSeconds)
		}
		if c.Alerts.NotifyRepeatSeconds < 0 {
			return fmt.Errorf("alerts.notify_repeat_seconds must be >= 0, got %d", c.Alerts.NotifyRepeatSeconds)
		}
	}

	// Streaming: the streamer package's own Validate handles ScriptPath
	// + Files; we just keep the numeric guards here so a typo in YAML
	// (push_timeout_seconds: 0) doesn't slip past.
	if c.Streaming.Enabled {
		if c.Streaming.FlushIntervalSeconds < 1 {
			return fmt.Errorf("streaming.flush_interval_seconds must be >= 1, got %d", c.Streaming.FlushIntervalSeconds)
		}
		if c.Streaming.MaxBatchLines < 1 {
			return fmt.Errorf("streaming.max_batch_lines must be >= 1, got %d", c.Streaming.MaxBatchLines)
		}
		if c.Streaming.PushTimeoutSeconds < 1 {
			return fmt.Errorf("streaming.push_timeout_seconds must be >= 1, got %d", c.Streaming.PushTimeoutSeconds)
		}
		if c.Streaming.MinBackoffSeconds < 1 {
			return fmt.Errorf("streaming.min_backoff_seconds must be >= 1, got %d", c.Streaming.MinBackoffSeconds)
		}
		if c.Streaming.MaxBackoffSeconds < c.Streaming.MinBackoffSeconds {
			return fmt.Errorf("streaming.max_backoff_seconds (%d) must be >= streaming.min_backoff_seconds (%d)",
				c.Streaming.MaxBackoffSeconds, c.Streaming.MinBackoffSeconds)
		}
		// streaming.max_line_bytes is clamped (not hard-failed) by Reconcile():
		// 0 falls back to the session default, a too-small positive value is
		// raised to the floor — so a stale override can't block startup.
	}

	// Realtime: numeric guards (the WS shares the HTTP listener, no port
	// of its own). MaxClients == 0 means unlimited, so only reject < 0.
	if c.Realtime.Enabled {
		if c.Realtime.PingIntervalSeconds < 1 {
			return fmt.Errorf("realtime.ping_interval_seconds must be >= 1, got %d", c.Realtime.PingIntervalSeconds)
		}
		if c.Realtime.WriteTimeoutSeconds < 1 {
			return fmt.Errorf("realtime.write_timeout_seconds must be >= 1, got %d", c.Realtime.WriteTimeoutSeconds)
		}
		if c.Realtime.SendBuffer < 1 {
			return fmt.Errorf("realtime.send_buffer must be >= 1, got %d", c.Realtime.SendBuffer)
		}
		if c.Realtime.MaxClients < 0 {
			return fmt.Errorf("realtime.max_clients must be >= 0 (0 = unlimited), got %d", c.Realtime.MaxClients)
		}
	}

	// Control panel (always wired). Action timeouts must be positive and the
	// danger ceiling must be at least the normal one.
	if c.Control.ActionTimeoutSeconds < 1 {
		return fmt.Errorf("control.action_timeout_seconds must be >= 1, got %d", c.Control.ActionTimeoutSeconds)
	}
	if c.Control.DangerActionTimeoutSeconds < c.Control.ActionTimeoutSeconds {
		return fmt.Errorf("control.danger_action_timeout_seconds (%d) must be >= control.action_timeout_seconds (%d)",
			c.Control.DangerActionTimeoutSeconds, c.Control.ActionTimeoutSeconds)
	}
	if c.Control.ReadTimeoutSeconds < 1 {
		return fmt.Errorf("control.read_timeout_seconds must be >= 1, got %d", c.Control.ReadTimeoutSeconds)
	}
	if c.Control.MaxOutputBytes < 1 {
		return fmt.Errorf("control.max_output_bytes must be >= 1, got %d", c.Control.MaxOutputBytes)
	}
	if c.Control.MaxConcurrent < 1 {
		return fmt.Errorf("control.max_concurrent must be >= 1, got %d", c.Control.MaxConcurrent)
	}

	// DB replication monitor (always runs; idle with no targets).
	if c.DBMonitor.IntervalSeconds < 1 {
		return fmt.Errorf("dbmonitor.interval_seconds must be >= 1, got %d", c.DBMonitor.IntervalSeconds)
	}
	if c.DBMonitor.MaxParallel < 1 {
		return fmt.Errorf("dbmonitor.max_parallel must be >= 1, got %d", c.DBMonitor.MaxParallel)
	}
	if c.DBMonitor.CommandTimeoutSeconds < 1 {
		return fmt.Errorf("dbmonitor.command_timeout_seconds must be >= 1, got %d", c.DBMonitor.CommandTimeoutSeconds)
	}

	return nil
}
