package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	yamlv3 "go.yaml.in/yaml/v3"
)

type Config struct {
	Server       ServerConfig       `koanf:"server"`
	Display      DisplayConfig      `koanf:"display"`
	Database     DatabaseConfig     `koanf:"database"`
	SSH          SSHConfig          `koanf:"ssh"`
	Log          LogConfig          `koanf:"log"`
	Metrics      MetricsConfig      `koanf:"metrics"`
	Logs         LogsConfig         `koanf:"logs"`
	Scheduler    SchedulerConfig    `koanf:"scheduler"`
	Auth         AuthConfig         `koanf:"auth"`
	Alerts       AlertsConfig       `koanf:"alerts"`
	Streaming    StreamingConfig    `koanf:"streaming"`
	PublicProbes PublicProbesConfig `koanf:"public_probes"`
}

// DisplayConfig controls presentation only. Metrics, logs, incidents, and API
// timestamps remain UTC/Unix time so data from servers in different timezones
// stays comparable and daylight-saving changes cannot corrupt time ranges.
type DisplayConfig struct {
	Timezone string `koanf:"timezone" json:"timezone"`
}

type ServerConfig struct {
	ListenAddr          string   `koanf:"listen_addr" json:"listen_addr"`
	ReadTimeoutSeconds  int      `koanf:"read_timeout_seconds" json:"read_timeout_seconds"`
	WriteTimeoutSeconds int      `koanf:"write_timeout_seconds" json:"write_timeout_seconds"`
	TrustedProxyCIDRs   []string `koanf:"trusted_proxy_cidrs" json:"trusted_proxy_cidrs"`
}

type DatabaseConfig struct {
	Path string `koanf:"path" json:"path"`
}

type SSHConfig struct {
	DialTimeoutSeconds    int    `koanf:"dial_timeout_seconds" json:"dial_timeout_seconds"`
	CommandTimeoutSeconds int    `koanf:"command_timeout_seconds" json:"command_timeout_seconds"`
	MaxConnectionsPerHost int    `koanf:"max_connections_per_host" json:"max_connections_per_host"`
	KnownHostsPath        string `koanf:"known_hosts_path" json:"known_hosts_path"`
}

type LogConfig struct {
	Level  string `koanf:"level" json:"level"`
	Format string `koanf:"format" json:"format"`
}

// MetricsConfig is the VictoriaMetrics push + query target.
type MetricsConfig struct {
	VMURL               string `koanf:"vm_url" json:"vm_url"` // e.g. http://127.0.0.1:8428
	PushTimeoutSeconds  int    `koanf:"push_timeout_seconds" json:"push_timeout_seconds"`
	QueryTimeoutSeconds int    `koanf:"query_timeout_seconds" json:"query_timeout_seconds"` // for /api/v1/metrics/query proxy
}

// LogsConfig is the Loki push + query target.
type LogsConfig struct {
	LokiURL             string `koanf:"loki_url" json:"loki_url"` // e.g. http://127.0.0.1:3100
	PushTimeoutSeconds  int    `koanf:"push_timeout_seconds" json:"push_timeout_seconds"`
	QueryTimeoutSeconds int    `koanf:"query_timeout_seconds" json:"query_timeout_seconds"`
}

// SchedulerConfig governs the per-server pull cadence and parallelism.
type SchedulerConfig struct {
	DefaultIntervalSeconds int `koanf:"default_interval_seconds" json:"default_interval_seconds"`
	MaxParallel            int `koanf:"max_parallel" json:"max_parallel"`
	PerJobTimeoutSeconds   int `koanf:"per_job_timeout_seconds" json:"per_job_timeout_seconds"`
}

// PublicProbesConfig controls browser-path checks performed by the monitor
// itself. The SSH collector's localhost probe remains the internal signal;
// these checks verify DNS, TLS, the reverse proxy, and Frappe from the same
// network edge as the monitor UI.
type PublicProbesConfig struct {
	Enabled             bool                `koanf:"enabled" json:"enabled"`
	AutoDiscover        bool                `koanf:"auto_discover" json:"auto_discover"`
	DefaultScheme       string              `koanf:"default_scheme" json:"default_scheme"`
	Path                string              `koanf:"path" json:"path"`
	TimeoutSeconds      int                 `koanf:"timeout_seconds" json:"timeout_seconds"`
	MaxParallel         int                 `koanf:"max_parallel" json:"max_parallel"`
	AllowPrivateTargets bool                `koanf:"allow_private_targets" json:"allow_private_targets"`
	RequirePong         bool                `koanf:"require_pong" json:"require_pong"`
	Targets             []PublicProbeTarget `koanf:"targets" json:"targets"`
}

// PublicProbeTarget overrides one discovered site's URL, or disables its
// public check. Server, bench, and site form an exact, stable identity.
type PublicProbeTarget struct {
	Server   string `koanf:"server" json:"server"`
	Bench    string `koanf:"bench" json:"bench"`
	Site     string `koanf:"site" json:"site"`
	URL      string `koanf:"url" json:"url"`
	Disabled bool   `koanf:"disabled" json:"disabled"`
}

// AuthConfig is the single-tenant auth knob. Empty Password means auth is
// off. A non-empty password enables the SPA login page and signed session
// cookie; HTTP basic credentials remain accepted for API automation.
type AuthConfig struct {
	Password string `koanf:"password" json:"-"`
	Realm    string `koanf:"realm" json:"realm"`
}

// AlertsConfig mirrors internal/alerts.Config. Duplicated here as a
// flat struct so koanf binds it from yaml; the alerts package's
// Config has the canonical Validate().
type AlertsConfig struct {
	Enabled                   bool           `koanf:"enabled" json:"enabled"`
	EvaluationIntervalSeconds int            `koanf:"evaluation_interval_seconds" json:"evaluation_interval_seconds"`
	NotifyRepeatSeconds       int            `koanf:"notify_repeat_seconds" json:"notify_repeat_seconds"`
	VMQueryTimeoutSeconds     int            `koanf:"vm_query_timeout_seconds" json:"vm_query_timeout_seconds"`
	Telegram                  AlertsTelegram `koanf:"telegram" json:"telegram"`
	Rules                     []AlertsRule   `koanf:"rules" json:"rules"`
	DisableDefaults           bool           `koanf:"disable_defaults" json:"disable_defaults"`
}

type AlertsTelegram struct {
	BotToken           string   `koanf:"bot_token" json:"-"`
	ChatIDs            []string `koanf:"chat_ids" json:"chat_ids"`
	SendTimeoutSeconds int      `koanf:"send_timeout_seconds" json:"send_timeout_seconds"`
}

type AlertsRule struct {
	Name              string   `koanf:"name" json:"name"`
	Expr              string   `koanf:"expr" json:"expr"`
	Severity          string   `koanf:"severity" json:"severity"`
	Message           string   `koanf:"message" json:"message"`
	FingerprintLabels []string `koanf:"fingerprint_labels" json:"fingerprint_labels"`
}

// StreamingConfig mirrors internal/streamer.Config. Keep the koanf
// shape close to the runtime struct so the wiring in main.go is a
// straight field copy.
type StreamingConfig struct {
	Enabled              bool                  `koanf:"enabled" json:"enabled"`
	MonitorID            string                `koanf:"monitor_id" json:"monitor_id"`
	ScriptPath           string                `koanf:"script_path" json:"script_path"`
	Files                []StreamingFileConfig `koanf:"files" json:"files"`
	FlushIntervalSeconds int                   `koanf:"flush_interval_seconds" json:"flush_interval_seconds"`
	MaxBatchLines        int                   `koanf:"max_batch_lines" json:"max_batch_lines"`
	MaxBufferedLines     int                   `koanf:"max_buffered_lines" json:"max_buffered_lines"`
	MaxBufferedBytes     int64                 `koanf:"max_buffered_bytes" json:"max_buffered_bytes"`
	PushTimeoutSeconds   int                   `koanf:"push_timeout_seconds" json:"push_timeout_seconds"`
	MinBackoffSeconds    int                   `koanf:"min_backoff_seconds" json:"min_backoff_seconds"`
	MaxBackoffSeconds    int                   `koanf:"max_backoff_seconds" json:"max_backoff_seconds"`
}

type StreamingFileConfig struct {
	ServerID int    `koanf:"server_id" json:"server_id"`
	ID       string `koanf:"id" json:"id"`
	Path     string `koanf:"path" json:"path"`
	LogType  string `koanf:"log_type" json:"log_type"`
	Bench    string `koanf:"bench" json:"bench"`
	Site     string `koanf:"site" json:"site"`
}

func defaults() *koanf.Koanf {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(map[string]any{
		"server.listen_addr":                   ":8080",
		"server.read_timeout_seconds":          15,
		"server.write_timeout_seconds":         15,
		"server.trusted_proxy_cidrs":           []string{"127.0.0.1/32", "::1/128"},
		"display.timezone":                     "UTC",
		"database.path":                        "./data/monitor.db",
		"ssh.dial_timeout_seconds":             10,
		"ssh.command_timeout_seconds":          30,
		"ssh.max_connections_per_host":         2,
		"ssh.known_hosts_path":                 "~/.ssh/known_hosts",
		"log.level":                            "info",
		"log.format":                           "json",
		"metrics.vm_url":                       "http://127.0.0.1:8428",
		"metrics.push_timeout_seconds":         5,
		"metrics.query_timeout_seconds":        15,
		"logs.loki_url":                        "http://127.0.0.1:3100",
		"logs.push_timeout_seconds":            5,
		"logs.query_timeout_seconds":           15,
		"scheduler.default_interval_seconds":   60,
		"scheduler.max_parallel":               10,
		"scheduler.per_job_timeout_seconds":    45,
		"public_probes.enabled":                false,
		"public_probes.auto_discover":          true,
		"public_probes.default_scheme":         "https",
		"public_probes.path":                   "/api/method/ping",
		"public_probes.timeout_seconds":        5,
		"public_probes.max_parallel":           10,
		"public_probes.allow_private_targets":  false,
		"public_probes.require_pong":           true,
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
		"streaming.max_buffered_lines":         5000,
		"streaming.max_buffered_bytes":         32 * 1024 * 1024,
		"streaming.push_timeout_seconds":       10,
		"streaming.min_backoff_seconds":        1,
		"streaming.max_backoff_seconds":        60,
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
	if strings.TrimSpace(c.Display.Timezone) == "" {
		return fmt.Errorf("display.timezone is required")
	}
	if _, err := time.LoadLocation(c.Display.Timezone); err != nil {
		return fmt.Errorf("display.timezone must be a valid IANA timezone such as UTC or Asia/Kolkata, got %q: %w", c.Display.Timezone, err)
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
	for _, cidr := range c.Server.TrustedProxyCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("server.trusted_proxy_cidrs contains invalid CIDR %q: %w", cidr, err)
		}
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
	if strings.TrimSpace(c.SSH.KnownHostsPath) == "" {
		return fmt.Errorf("ssh.known_hosts_path is required")
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
	minimumJobTimeout := c.SSH.DialTimeoutSeconds + c.SSH.CommandTimeoutSeconds + c.Metrics.PushTimeoutSeconds
	if c.Scheduler.PerJobTimeoutSeconds < minimumJobTimeout {
		return fmt.Errorf(
			"scheduler.per_job_timeout_seconds must be >= ssh.dial_timeout_seconds + ssh.command_timeout_seconds + metrics.push_timeout_seconds (%d), got %d",
			minimumJobTimeout, c.Scheduler.PerJobTimeoutSeconds,
		)
	}
	if c.PublicProbes.Enabled {
		if c.PublicProbes.DefaultScheme != "http" && c.PublicProbes.DefaultScheme != "https" {
			return fmt.Errorf("public_probes.default_scheme must be http or https, got %q", c.PublicProbes.DefaultScheme)
		}
		if !strings.HasPrefix(c.PublicProbes.Path, "/") || strings.HasPrefix(c.PublicProbes.Path, "//") {
			return fmt.Errorf("public_probes.path must start with one slash")
		}
		if c.PublicProbes.TimeoutSeconds < 1 || c.PublicProbes.TimeoutSeconds > 60 {
			return fmt.Errorf("public_probes.timeout_seconds must be between 1 and 60, got %d", c.PublicProbes.TimeoutSeconds)
		}
		if c.PublicProbes.MaxParallel < 1 || c.PublicProbes.MaxParallel > 100 {
			return fmt.Errorf("public_probes.max_parallel must be between 1 and 100, got %d", c.PublicProbes.MaxParallel)
		}
		seenTargets := make(map[string]struct{}, len(c.PublicProbes.Targets))
		for i, target := range c.PublicProbes.Targets {
			server := strings.TrimSpace(target.Server)
			bench := strings.TrimSpace(target.Bench)
			site := strings.TrimSpace(target.Site)
			if server == "" || bench == "" || site == "" {
				return fmt.Errorf("public_probes.targets[%d] requires server, bench, and site", i)
			}
			key := server + "\x00" + bench + "\x00" + site
			if _, exists := seenTargets[key]; exists {
				return fmt.Errorf("public_probes.targets contains duplicate %q/%q/%q", server, bench, site)
			}
			seenTargets[key] = struct{}{}
			if target.Disabled {
				continue
			}
			u, err := url.Parse(strings.TrimSpace(target.URL))
			if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				return fmt.Errorf("public_probes.targets[%d].url must be an absolute http or https URL without credentials", i)
			}
		}
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

	// Streaming: the streamer package validates any explicit file specs;
	// we keep the numeric guards here so a typo in YAML
	// (push_timeout_seconds: 0) doesn't slip past.
	if c.Streaming.Enabled {
		if c.SSH.MaxConnectionsPerHost < 2 {
			return fmt.Errorf("ssh.max_connections_per_host must be >= 2 when streaming is enabled")
		}
		if c.Streaming.FlushIntervalSeconds < 1 {
			return fmt.Errorf("streaming.flush_interval_seconds must be >= 1, got %d", c.Streaming.FlushIntervalSeconds)
		}
		if c.Streaming.MaxBatchLines < 1 {
			return fmt.Errorf("streaming.max_batch_lines must be >= 1, got %d", c.Streaming.MaxBatchLines)
		}
		if c.Streaming.MaxBufferedLines < c.Streaming.MaxBatchLines {
			return fmt.Errorf("streaming.max_buffered_lines must be >= max_batch_lines (%d), got %d", c.Streaming.MaxBatchLines, c.Streaming.MaxBufferedLines)
		}
		if c.Streaming.MaxBufferedBytes < 1024*1024 {
			return fmt.Errorf("streaming.max_buffered_bytes must be >= 1048576, got %d", c.Streaming.MaxBufferedBytes)
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

	return nil
}

// Validate exposes the same invariant checks used at startup to the local
// administration API. UI-saved configuration must never be able to write a
// file that the next process start cannot load.
func (c *Config) Validate() error { return c.validate() }

// SecretUpdates distinguishes an omitted secret (preserve the value already
// in the YAML file) from an explicit empty value (clear it), so changing an
// unrelated setting never clears or rewrites a secret by accident.
type SecretUpdates struct {
	AuthPassword     *string
	TelegramBotToken *string
}

// SaveUI atomically persists the UI-manageable configuration sections while
// retaining unknown YAML keys and omitted secrets. A sibling .bak file keeps
// the immediately previous version recoverable. Monitored hosts are not
// touched; this writes only the monitor's local bootstrap configuration.
func SaveUI(path string, c *Config, secrets SecretUpdates) error {
	if err := c.Validate(); err != nil {
		return err
	}
	raw := map[string]any{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read existing config: %w", err)
	}
	if len(data) > 0 {
		if err := yamlv3.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse existing config: %w", err)
		}
	}
	if raw == nil {
		raw = map[string]any{}
	}
	section := func(name string) map[string]any {
		value, ok := raw[name].(map[string]any)
		if !ok {
			value = map[string]any{}
			raw[name] = value
		}
		return value
	}

	server := section("server")
	server["listen_addr"] = c.Server.ListenAddr
	server["read_timeout_seconds"] = c.Server.ReadTimeoutSeconds
	server["write_timeout_seconds"] = c.Server.WriteTimeoutSeconds
	server["trusted_proxy_cidrs"] = c.Server.TrustedProxyCIDRs
	display := section("display")
	display["timezone"] = c.Display.Timezone
	database := section("database")
	database["path"] = c.Database.Path

	ssh := section("ssh")
	ssh["dial_timeout_seconds"] = c.SSH.DialTimeoutSeconds
	ssh["command_timeout_seconds"] = c.SSH.CommandTimeoutSeconds
	ssh["max_connections_per_host"] = c.SSH.MaxConnectionsPerHost
	ssh["known_hosts_path"] = c.SSH.KnownHostsPath

	logCfg := section("log")
	logCfg["level"] = c.Log.Level
	logCfg["format"] = c.Log.Format

	metrics := section("metrics")
	metrics["vm_url"] = c.Metrics.VMURL
	metrics["push_timeout_seconds"] = c.Metrics.PushTimeoutSeconds
	metrics["query_timeout_seconds"] = c.Metrics.QueryTimeoutSeconds

	logs := section("logs")
	logs["loki_url"] = c.Logs.LokiURL
	logs["push_timeout_seconds"] = c.Logs.PushTimeoutSeconds
	logs["query_timeout_seconds"] = c.Logs.QueryTimeoutSeconds

	scheduler := section("scheduler")
	scheduler["default_interval_seconds"] = c.Scheduler.DefaultIntervalSeconds
	scheduler["max_parallel"] = c.Scheduler.MaxParallel
	scheduler["per_job_timeout_seconds"] = c.Scheduler.PerJobTimeoutSeconds

	publicProbes := section("public_probes")
	publicProbes["enabled"] = c.PublicProbes.Enabled
	publicProbes["auto_discover"] = c.PublicProbes.AutoDiscover
	publicProbes["default_scheme"] = c.PublicProbes.DefaultScheme
	publicProbes["path"] = c.PublicProbes.Path
	publicProbes["timeout_seconds"] = c.PublicProbes.TimeoutSeconds
	publicProbes["max_parallel"] = c.PublicProbes.MaxParallel
	publicProbes["allow_private_targets"] = c.PublicProbes.AllowPrivateTargets
	publicProbes["require_pong"] = c.PublicProbes.RequirePong
	probeTargets := make([]map[string]any, 0, len(c.PublicProbes.Targets))
	for _, target := range c.PublicProbes.Targets {
		probeTargets = append(probeTargets, map[string]any{
			"server": target.Server, "bench": target.Bench, "site": target.Site,
			"url": target.URL, "disabled": target.Disabled,
		})
	}
	publicProbes["targets"] = probeTargets

	auth := section("auth")
	auth["realm"] = c.Auth.Realm
	if secrets.AuthPassword != nil {
		auth["password"] = *secrets.AuthPassword
	}

	alerts := section("alerts")
	alerts["enabled"] = c.Alerts.Enabled
	alerts["evaluation_interval_seconds"] = c.Alerts.EvaluationIntervalSeconds
	alerts["notify_repeat_seconds"] = c.Alerts.NotifyRepeatSeconds
	alerts["vm_query_timeout_seconds"] = c.Alerts.VMQueryTimeoutSeconds
	alerts["disable_defaults"] = c.Alerts.DisableDefaults
	rules := make([]map[string]any, 0, len(c.Alerts.Rules))
	for _, rule := range c.Alerts.Rules {
		rules = append(rules, map[string]any{
			"name": rule.Name, "expr": rule.Expr, "severity": rule.Severity,
			"message": rule.Message, "fingerprint_labels": rule.FingerprintLabels,
		})
	}
	alerts["rules"] = rules
	telegram, ok := alerts["telegram"].(map[string]any)
	if !ok {
		telegram = map[string]any{}
		alerts["telegram"] = telegram
	}
	telegram["chat_ids"] = c.Alerts.Telegram.ChatIDs
	telegram["send_timeout_seconds"] = c.Alerts.Telegram.SendTimeoutSeconds
	if secrets.TelegramBotToken != nil {
		telegram["bot_token"] = *secrets.TelegramBotToken
	}

	streaming := section("streaming")
	streaming["enabled"] = c.Streaming.Enabled
	streaming["monitor_id"] = c.Streaming.MonitorID
	streaming["script_path"] = c.Streaming.ScriptPath
	streaming["flush_interval_seconds"] = c.Streaming.FlushIntervalSeconds
	streaming["max_batch_lines"] = c.Streaming.MaxBatchLines
	streaming["max_buffered_lines"] = c.Streaming.MaxBufferedLines
	streaming["max_buffered_bytes"] = c.Streaming.MaxBufferedBytes
	streaming["push_timeout_seconds"] = c.Streaming.PushTimeoutSeconds
	streaming["min_backoff_seconds"] = c.Streaming.MinBackoffSeconds
	streaming["max_backoff_seconds"] = c.Streaming.MaxBackoffSeconds
	files := make([]map[string]any, 0, len(c.Streaming.Files))
	for _, file := range c.Streaming.Files {
		files = append(files, map[string]any{
			"server_id": file.ServerID, "id": file.ID, "path": file.Path,
			"log_type": file.LogType, "bench": file.Bench, "site": file.Site,
		})
	}
	streaming["files"] = files

	out, err := yamlv3.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	mode := os.FileMode(0o600)
	if info, statErr := os.Stat(path); statErr == nil {
		mode = info.Mode().Perm()
		if len(data) > 0 {
			if err := os.WriteFile(path+".bak", data, mode); err != nil {
				return fmt.Errorf("write config backup: %w", err)
			}
		}
	}
	tmp, err := os.CreateTemp(dir, ".monitor.yaml.*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temporary config: %w", err)
	}
	if _, err := tmp.Write(out); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}
