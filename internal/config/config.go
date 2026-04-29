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
	Server   ServerConfig   `koanf:"server"`
	Database DatabaseConfig `koanf:"database"`
	SSH      SSHConfig      `koanf:"ssh"`
	Log      LogConfig      `koanf:"log"`
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

func defaults() *koanf.Koanf {
	k := koanf.New(".")
	if err := k.Load(confmap.Provider(map[string]any{
		"server.listen_addr":           ":8080",
		"server.read_timeout_seconds":  15,
		"server.write_timeout_seconds": 15,
		"database.path":                "./data/monitor.db",
		"ssh.dial_timeout_seconds":     10,
		"ssh.command_timeout_seconds":  30,
		"ssh.max_connections_per_host": 2,
		"log.level":                    "info",
		"log.format":                   "json",
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

	return nil
}
