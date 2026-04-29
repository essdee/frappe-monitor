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
