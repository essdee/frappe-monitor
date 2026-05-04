package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoad_ReadsYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	yaml := `
server:
  listen_addr: ":9090"
  read_timeout_seconds: 20
  write_timeout_seconds: 20
database:
  path: "/tmp/test.db"
ssh:
  dial_timeout_seconds: 5
  command_timeout_seconds: 15
  max_connections_per_host: 1
log:
  level: "debug"
  format: "text"
`
	require.NoError(t, os.WriteFile(path, []byte(yaml), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, ":9090", cfg.Server.ListenAddr)
	require.Equal(t, 20, cfg.Server.ReadTimeoutSeconds)
	require.Equal(t, "/tmp/test.db", cfg.Database.Path)
	require.Equal(t, 5, cfg.SSH.DialTimeoutSeconds)
	require.Equal(t, "debug", cfg.Log.Level)
}

func TestLoad_EnvOverrides(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`server: {listen_addr: ":8080"}`), 0o600))

	t.Setenv("MONITOR_SERVER__LISTEN_ADDR", ":7777")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, ":7777", cfg.Server.ListenAddr)
}

func TestLoad_Validates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`database: {path: ""}`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "database.path")
}

func TestLoad_RejectsInvalidLogLevel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`log: {level: "warning"}`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "log.level")
	require.Contains(t, err.Error(), "warning")
}

func TestLoad_RejectsInvalidLogFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`log: {format: "yaml"}`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "log.format")
	require.Contains(t, err.Error(), "yaml")
}

func TestLoad_RejectsNonPositiveNumbers(t *testing.T) {
	cases := []struct {
		name      string
		yaml      string
		errSubstr string
	}{
		{
			name:      "zero dial timeout",
			yaml:      `ssh: {dial_timeout_seconds: 0}`,
			errSubstr: "ssh.dial_timeout_seconds",
		},
		{
			name:      "negative command timeout",
			yaml:      `ssh: {command_timeout_seconds: -1}`,
			errSubstr: "ssh.command_timeout_seconds",
		},
		{
			name:      "zero max connections",
			yaml:      `ssh: {max_connections_per_host: 0}`,
			errSubstr: "ssh.max_connections_per_host",
		},
		{
			name:      "zero read timeout",
			yaml:      `server: {read_timeout_seconds: 0}`,
			errSubstr: "server.read_timeout_seconds",
		},
		{
			name:      "zero write timeout",
			yaml:      `server: {write_timeout_seconds: 0}`,
			errSubstr: "server.write_timeout_seconds",
		},
		{
			name:      "metrics push timeout",
			yaml:      `metrics: {push_timeout_seconds: 0}`,
			errSubstr: "metrics.push_timeout_seconds",
		},
		{
			name:      "scheduler default interval",
			yaml:      `scheduler: {default_interval_seconds: 0}`,
			errSubstr: "scheduler.default_interval_seconds",
		},
		{
			name:      "scheduler max parallel",
			yaml:      `scheduler: {max_parallel: 0}`,
			errSubstr: "scheduler.max_parallel",
		},
		{
			name:      "scheduler per-job timeout",
			yaml:      `scheduler: {per_job_timeout_seconds: 0}`,
			errSubstr: "scheduler.per_job_timeout_seconds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "monitor.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tc.yaml), 0o600))

			_, err := Load(path)
			require.Error(t, err)
			require.Contains(t, err.Error(), tc.errSubstr)
		})
	}
}

func TestLoad_RejectsEmptyVMURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`metrics: {vm_url: ""}`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "metrics.vm_url")
}

func TestLoad_NewSectionsHaveDefaults(t *testing.T) {
	// Empty config → defaults populate metrics + scheduler too.
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`# empty`), 0o600))

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "http://127.0.0.1:8428", cfg.Metrics.VMURL)
	require.Equal(t, 5, cfg.Metrics.PushTimeoutSeconds)
	require.Equal(t, 900, cfg.Scheduler.DefaultIntervalSeconds)
	require.Equal(t, 10, cfg.Scheduler.MaxParallel)
	require.Equal(t, 30, cfg.Scheduler.PerJobTimeoutSeconds)
}

func TestLoad_NewSectionsHonorEnvOverrides(t *testing.T) {
	// Env vars should override defaults for the new metrics + scheduler
	// sections through the same MONITOR_<SECTION>__<KEY> transform used
	// for the existing sections. Important for Task 19 (docker-compose
	// will set MONITOR_METRICS__VM_URL=http://victoriametrics:8428).
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`# empty`), 0o600))

	t.Setenv("MONITOR_METRICS__VM_URL", "http://victoriametrics:8428")
	t.Setenv("MONITOR_SCHEDULER__DEFAULT_INTERVAL_SECONDS", "5")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "http://victoriametrics:8428", cfg.Metrics.VMURL)
	require.Equal(t, 5, cfg.Scheduler.DefaultIntervalSeconds)
}
