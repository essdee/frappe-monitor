package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSafeToPurge(t *testing.T) {
	require.Error(t, safeToPurge(""), "empty path")
	require.Error(t, safeToPurge(string(filepath.Separator)), "filesystem root")
	if home, err := os.UserHomeDir(); err == nil {
		require.Error(t, safeToPurge(home), "home directory")
	}

	// A directory that doesn't look like an install must be refused.
	tmp := t.TempDir()
	require.Error(t, safeToPurge(tmp), "no monitor.yaml or bin/ → refuse")

	// Once it looks like an install, it's allowed.
	require.NoError(t, os.WriteFile(filepath.Join(tmp, "monitor.yaml"), []byte("x"), 0o600))
	require.NoError(t, safeToPurge(tmp))
}

func TestRandomPassword(t *testing.T) {
	a, err := randomPassword()
	require.NoError(t, err)
	require.NotEmpty(t, a)
	b, err := randomPassword()
	require.NoError(t, err)
	require.NotEqual(t, a, b, "passwords must differ")
}
