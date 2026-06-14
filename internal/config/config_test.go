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

func TestReconcile_ClampsLowWriteTimeout(t *testing.T) {
	// Upgrade scenario: an on-disk config from an older release (write=15)
	// loads WITHOUT error (no hard-fail), and Reconcile clamps it up + warns —
	// rather than the monitor refusing to boot and crash-looping.
	cfg := &Config{}
	cfg.Server.WriteTimeoutSeconds = 15
	cfg.SSH.CommandTimeoutSeconds = 30
	warns := cfg.Reconcile()
	require.Equal(t, 30, cfg.Server.WriteTimeoutSeconds, "write_timeout should be clamped up to command_timeout")
	require.Len(t, warns, 1, "clamp should produce one warning")
	require.Contains(t, warns[0], "write_timeout_seconds")
}

func TestReconcile_NoOpWhenConsistent(t *testing.T) {
	cfg := &Config{}
	cfg.Server.WriteTimeoutSeconds = 60
	cfg.SSH.CommandTimeoutSeconds = 30
	require.Empty(t, cfg.Reconcile())
	require.Equal(t, 60, cfg.Server.WriteTimeoutSeconds)
}

func TestReconcile_ClampsLowMaxLineBytes(t *testing.T) {
	cfg := &Config{}
	cfg.Streaming.Enabled = true
	cfg.Streaming.MaxLineBytes = 100 // below the 4096 floor
	warns := cfg.Reconcile()
	require.Equal(t, 1<<20, cfg.Streaming.MaxLineBytes)
	require.Len(t, warns, 1)
}

func TestLoad_LowWriteTimeoutNoLongerFailsToBoot(t *testing.T) {
	// The cross-field invariant is a Reconcile clamp now, not a validate() error.
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
server: {write_timeout_seconds: 15}
ssh: {command_timeout_seconds: 30}
metrics: {vm_url: "http://127.0.0.1:8428"}
logs: {loki_url: "http://127.0.0.1:3100"}
`), 0o644))
	_, err := Load(path)
	require.NoError(t, err, "a write<command config must load (clamped at runtime), not fail")
}
