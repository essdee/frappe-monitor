package config

import (
	"fmt"
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
}

type ServerConfig struct {
	ListenAddr          string `koanf:"listen_addr"`
	ReadTimeoutSeconds  int    `koanf:"read_timeout_seconds"`
	WriteTimeoutSeconds int    `koanf:"write_timeout_seconds"`
}

type DatabaseConfig struct {
	Path string `koanf:"path"`
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
	Enabled                   bool             `koanf:"enabled"`
	EvaluationIntervalSeconds int              `koanf:"evaluation_interval_seconds"`
	NotifyRepeatSeconds       int              `koanf:"notify_repeat_seconds"`
	VMQueryTimeoutSeconds     int              `koanf:"vm_query_timeout_seconds"`
	Telegram                  AlertsTelegram   `koanf:"telegram"`
	Rules                     []AlertsRule     `koanf:"rules"`
	DisableDefaults           bool             `koanf:"disable_defaults"`
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

func defaults() *koanf.Koanf {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(map[string]any{
		"server.listen_addr":                 ":8080",
		"server.read_timeout_seconds":        15,
		"server.write_timeout_seconds":       15,
		"database.path":                      "./data/monitor.db",
		"ssh.dial_timeout_seconds":           10,
		"ssh.command_timeout_seconds":        30,
		"ssh.max_connections_per_host":       2,
		"log.level":                          "info",
		"log.format":                         "json",
		"metrics.vm_url":                     "http://127.0.0.1:8428",
		"metrics.push_timeout_seconds":       5,
		"metrics.query_timeout_seconds":      15,
		"logs.loki_url":                      "http://127.0.0.1:3100",
		"logs.push_timeout_seconds":          5,
		"logs.query_timeout_seconds":         15,
		"scheduler.default_interval_seconds": 900, // 15 min — master plan §5 default
		"scheduler.max_parallel":             10,
		"scheduler.per_job_timeout_seconds":  30,
		"auth.password":                      "",
		"auth.realm":                         "frappe-monitor",
		"alerts.enabled":                     false,
		"alerts.evaluation_interval_seconds": 60,
		"alerts.notify_repeat_seconds":       3600,
		"alerts.vm_query_timeout_seconds":    10,
		"alerts.telegram.send_timeout_seconds": 5,
		"alerts.disable_defaults":            false,
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
func (c *Config) validate() error {
	if c.Database.Path == "" {
		return fmt.Errorf("database.path is required")
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

	return nil
}
