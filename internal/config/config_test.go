package config

import (
	"os"
	"path/filepath"
	"strings"
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
	require.Equal(t, "UTC", cfg.Display.Timezone)
}

func TestLoad_ValidatesDisplayTimezone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`display: {timezone: "Asia/Kolkata"}`), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "Asia/Kolkata", cfg.Display.Timezone)

	require.NoError(t, os.WriteFile(path, []byte(`display: {timezone: "Mars/Olympus"}`), 0o600))
	_, err = Load(path)
	require.ErrorContains(t, err, "display.timezone")
}

func TestLoad_YAMLIsNotOverriddenByEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`server: {listen_addr: ":8080"}`), 0o600))

	t.Setenv("MONITOR_SERVER__LISTEN_ADDR", ":7777")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, ":8080", cfg.Server.ListenAddr)
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
	require.Equal(t, 60, cfg.Scheduler.DefaultIntervalSeconds)
	require.Equal(t, 10, cfg.Scheduler.MaxParallel)
	require.Equal(t, 45, cfg.Scheduler.PerJobTimeoutSeconds)
	require.Equal(t, "~/.ssh/known_hosts", cfg.SSH.KnownHostsPath)
	require.Equal(t, []string{"127.0.0.1/32", "::1/128"}, cfg.Server.TrustedProxyCIDRs)
	require.Equal(t, 5000, cfg.Streaming.MaxBufferedLines)
	require.Equal(t, int64(32*1024*1024), cfg.Streaming.MaxBufferedBytes)
}

func TestLoad_RejectsUnsafeStreamingGlobalBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
streaming:
  enabled: true
  max_batch_lines: 500
  max_buffered_lines: 100
`), 0o600))
	_, err := Load(path)
	require.ErrorContains(t, err, "max_buffered_lines must be >= max_batch_lines")

	require.NoError(t, os.WriteFile(path, []byte(`
streaming:
  enabled: true
  max_buffered_bytes: 1024
`), 0o600))
	_, err = Load(path)
	require.ErrorContains(t, err, "max_buffered_bytes must be >= 1048576")
}

func TestLoad_RejectsInvalidTrustedProxyCIDR(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`server: {trusted_proxy_cidrs: ["not-a-cidr"]}`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "server.trusted_proxy_cidrs")
}

func TestLoad_StreamingNeedsTwoSSHSessionSlots(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
ssh: {max_connections_per_host: 1}
streaming:
  enabled: true
  script_path: /tmp/stream.sh
  files: [{id: error, path: /tmp/error.log}]
`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be >= 2 when streaming is enabled")
}

func TestLoad_RejectsJobTimeoutBelowPipelineTimeouts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
ssh:
  dial_timeout_seconds: 10
  command_timeout_seconds: 60
scheduler:
  per_job_timeout_seconds: 45
`), 0o600))

	_, err := Load(path)
	require.Error(t, err)
	require.Contains(t, err.Error(), "ssh.dial_timeout_seconds + ssh.command_timeout_seconds")
}

func TestLoad_PublicProbeDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`public_probes: {enabled: true}`), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	require.True(t, cfg.PublicProbes.AutoDiscover)
	require.Equal(t, "https", cfg.PublicProbes.DefaultScheme)
	require.Equal(t, "/api/method/ping", cfg.PublicProbes.Path)
	require.Equal(t, 5, cfg.PublicProbes.TimeoutSeconds)

	require.NoError(t, os.WriteFile(path, []byte(`
public_probes:
  enabled: true
  targets:
    - {server: prod, bench: bench, site: erp.test, url: "file:///etc/passwd"}
`), 0o600))
	_, err = Load(path)
	require.ErrorContains(t, err, "absolute http or https URL")
}

func TestLoad_PublicProbeRejectsDuplicateIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
public_probes:
  enabled: true
  targets:
    - {server: prod, bench: bench, site: erp.test, url: "https://erp.test/api/method/ping"}
    - {server: prod, bench: bench, site: erp.test, disabled: true}
`), 0o600))
	_, err := Load(path)
	require.ErrorContains(t, err, "duplicate")
}

func TestLoad_AllRuntimeSettingsComeFromYAMLOrDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
metrics:
  vm_url: http://yaml-vm:8428
scheduler:
  default_interval_seconds: 120
`), 0o600))

	t.Setenv("MONITOR_METRICS__VM_URL", "http://victoriametrics:8428")
	t.Setenv("MONITOR_SCHEDULER__DEFAULT_INTERVAL_SECONDS", "5")

	cfg, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, "http://yaml-vm:8428", cfg.Metrics.VMURL)
	require.Equal(t, 120, cfg.Scheduler.DefaultIntervalSeconds)
}

func TestTrackedExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "deploy", "config", "monitor.yaml.example"))
	require.NoError(t, err)
	require.Equal(t, 60, cfg.Scheduler.DefaultIntervalSeconds)
	require.Equal(t, ".frappe-monitor/frappe-monitor-stream.sh", cfg.Streaming.ScriptPath)
}

func TestSaveUI_PreservesSecretsUnknownKeysAndCreatesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	original := `
auth:
  password: original-password
alerts:
  telegram:
    bot_token: original-token
custom_plugin:
  retained: true
`
	require.NoError(t, os.WriteFile(path, []byte(original), 0o640))
	cfg, err := Load(path)
	require.NoError(t, err)
	cfg.Server.ListenAddr = ":9099"

	require.NoError(t, SaveUI(path, cfg, SecretUpdates{}))
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	text := string(saved)
	require.Contains(t, text, "original-password")
	require.Contains(t, text, "original-token")
	require.Contains(t, text, "custom_plugin:")
	require.Contains(t, text, "retained: true")

	reloaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, ":9099", reloaded.Server.ListenAddr)
	require.Equal(t, "original-password", reloaded.Auth.Password)
	require.Equal(t, "original-token", reloaded.Alerts.Telegram.BotToken)

	backup, err := os.ReadFile(path + ".bak")
	require.NoError(t, err)
	require.Equal(t, original, string(backup))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm())
}

func TestSaveUI_ExplicitSecretUpdatesAndClear(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
auth: {password: old}
alerts: {telegram: {bot_token: old-token}}
`), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	password := "new-password"
	token := ""
	cfg.Auth.Password = password
	cfg.Alerts.Telegram.BotToken = token
	require.NoError(t, SaveUI(path, cfg, SecretUpdates{
		AuthPassword: &password, TelegramBotToken: &token,
	}))

	reloaded, err := Load(path)
	require.NoError(t, err)
	require.Equal(t, password, reloaded.Auth.Password)
	require.Empty(t, reloaded.Alerts.Telegram.BotToken)
}

func TestSaveUI_InvalidConfigDoesNotTouchFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	original := "# retain me\n"
	require.NoError(t, os.WriteFile(path, []byte(original), 0o600))
	cfg, err := Load(path)
	require.NoError(t, err)
	cfg.Metrics.VMURL = ""

	err = SaveUI(path, cfg, SecretUpdates{})
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "metrics.vm_url"))
	after, readErr := os.ReadFile(path)
	require.NoError(t, readErr)
	require.Equal(t, original, string(after))
	_, statErr := os.Stat(path + ".bak")
	require.True(t, os.IsNotExist(statErr))
}
