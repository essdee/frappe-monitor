package scripts

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCollectorScript_Embedded(t *testing.T) {
	require.NotEmpty(t, CollectorScript, "collector script must be embedded into the binary")
	require.True(t, strings.HasPrefix(CollectorScript, "#!/usr/bin/env bash"),
		"embedded script should start with bash shebang")
	require.Contains(t, CollectorScript, "###META", "must contain META section marker")
	require.Contains(t, CollectorScript, "###SERVER", "must contain SERVER section marker")
	require.Contains(t, CollectorScript, "###END", "must contain END marker")
}

func TestCollectorVersion_ReadsVersionLine(t *testing.T) {
	v := CollectorVersion()
	require.NotEqual(t, "unknown", v, "version line must be parseable")
	require.Regexp(t, `^\d+\.\d+\.\d+$`, v, "expected semver-style version")
}
