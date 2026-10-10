package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"frappe-monitor/scripts"
)

func TestLogDiscoveryIncludesFinalConfiguredBench(t *testing.T) {
	root := t.TempDir()
	benches := make([]string, 0, 3)
	for _, name := range []string{"frappe-13", "frappe-15", "frappe-16"} {
		bench := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Join(bench, "logs"), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(bench, "logs", "worker.log"), nil, 0o600))
		require.NoError(t, os.WriteFile(filepath.Join(bench, "logs", "monitor.json.log"), nil, 0o600))
		benches = append(benches, bench)
	}

	cmd := exec.Command("bash", "-s")
	cmd.Stdin = strings.NewReader(scripts.LogDiscoveryScript)
	cmd.Env = append(os.Environ(), "BENCH_PATHS="+strings.Join(benches, ":"))
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "discovery output:\n%s", out)
	for _, name := range []string{"frappe-13", "frappe-15", "frappe-16"} {
		require.Contains(t, string(out), "|"+name+"|", "missing configured bench %s", name)
	}
	require.Equal(t, 3, strings.Count(string(out), "|monitor|"), "native Frappe monitor logs must have a stable log_type")
}
