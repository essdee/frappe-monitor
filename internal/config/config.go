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
}

type ServerConfig struct {
	ListenAddr          string `koanf:"listen_addr"`
	ReadTimeoutSeconds  int    `koanf:"read_timeout_seconds"`
	WriteTimeoutSeconds int    `koanf:"write_timeout_seconds"`
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
	MaxConnectionsPerHost int `koanf:"max_connections_per_host"`
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
		"server.listen_addr":                   ":8080",
		"server.read_timeout_seconds":          15,
		"server.write_timeout_seconds":         15,
		"database.driver":                      "sqlite",
		"database.path":                        "./data/monitor.db",
		"ssh.dial_timeout_seconds":             10,
		"ssh.command_timeout_seconds":          30,
		"ssh.max_connections_per_host":         2,
		"log.level":                            "info",
		"log.format":                           "json",
		"metrics.vm_url":                       "http://127.0.0.1:8428",
		"metrics.push_timeout_seconds":         5,
		"metrics.query_timeout_seconds":        15,
		"logs.loki_url":                        "http://127.0.0.1:3100",
		"logs.push_timeout_seconds":            5,
		"logs.query_timeout_seconds":           15,
		"scheduler.default_interval_seconds":   900, // 15 min — master plan §5 default
		"scheduler.max_parallel":               10,
		"scheduler.per_job_timeout_seconds":    30,
		"auth.password":                        "",
		"auth.realm":                           "frappe-monitor",
		"alerts.enabled":                       false,
		"alerts.evaluation_interval_seconds":   60,
		"alerts.notify_repeat_seconds":         3600,
		"alerts.vm_query_timeout_seconds":      10,
		"alerts.telegram.send_timeout_seconds": 5,
		"alerts.disable_defaults":              false,
		"streaming.enabled":                    false,
		"streaming.script_path":                "",
		"streaming.flush_interval_seconds":     1,
		"streaming.max_batch_lines":            500,
		"streaming.push_timeout_seconds":       10,
		"streaming.min_backoff_seconds":        1,
		"streaming.max_backoff_seconds":        60,
		"realtime.enabled":                     true,
		"realtime.ping_interval_seconds":       30,
		"realtime.write_timeout_seconds":       10,
		"realtime.send_buffer":                 128,
		"realtime.max_clients":                 512,
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

// validate enforces invariants on the merged config. It guards against
// explicit empty overrides (e.g. `path: ""` in YAML) and out-of-range
// values for fields wired into long-lived runtime components like the
// SSH pool and HTTP server. Absent keys fall back to defaults() and
// never reach these checks.
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
	}
	return nil
}

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
	if c.SSH.DialTimeoutSeconds < 1 {
		return fmt.Errorf("ssh.dial_timeout_seconds must be >= 1, got %d", c.SSH.DialTimeoutSeconds)
	}
	if c.SSH.CommandTimeoutSeconds < 1 {
		return fmt.Errorf("ssh.command_timeout_seconds must be >= 1, got %d", c.SSH.CommandTimeoutSeconds)
	}
	if c.SSH.MaxConnectionsPerHost < 1 {
		return fmt.Errorf("ssh.max_connections_per_host must be >= 1, got %d", c.SSH.MaxConnectionsPerHost)
	}

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

	return nil
}
