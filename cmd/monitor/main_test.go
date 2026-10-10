package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckConfigurationAcceptsTrackedExample(t *testing.T) {
	require.NoError(t, checkConfiguration(filepath.Join("..", "..", "deploy", "config", "monitor.yaml.example")))
}

func TestCheckConfigurationRejectsInvalidRuntimeSubsystemBeforeStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
alerts:
  enabled: true
  telegram:
    bot_token: dasda
    chat_ids: [qweq, qweq]
`), 0o600))

	err := checkConfiguration(path)
	require.ErrorContains(t, err, "alerts:")
	require.ErrorContains(t, err, "bot-token format")
}
