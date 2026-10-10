package scripts_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	monitorMetrics "frappe-monitor/internal/metrics"
	"frappe-monitor/internal/parser"
	"frappe-monitor/scripts"
)

func TestCollectorProbesSitesConcurrentlyAndReportsFailureReason(t *testing.T) {
	tmp := t.TempDir()
	bench := filepath.Join(tmp, "bench-prod")
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "apps"), 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(bench, "sites"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "sites", "apps.txt"), []byte("frappe\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(bench, "sites", "common_site_config.json"), []byte(`{"monitor": 1, "db_password": "common-never-emit"}`), 0o600))
	const siteCount = 12
	expectedDatabases := make(map[string]string, siteCount)
	expectedUsers := make(map[string]string, siteCount)
	for i := 0; i < siteCount; i++ {
		siteName := fmt.Sprintf("site-%02d.test", i)
		siteDir := filepath.Join(bench, "sites", siteName)
		require.NoError(t, os.MkdirAll(siteDir, 0o755))
		database := fmt.Sprintf("secret_database_%02d", i)
		databaseUser := fmt.Sprintf("secret_user_%02d", i)
		expectedDatabases[siteName] = monitorMetrics.DatabaseIdentityHash(database)
		expectedUsers[siteName] = monitorMetrics.DatabaseIdentityHash(databaseUser)
		monitor := 1
		if i == 0 {
			monitor = 0
		}
		config := []byte(fmt.Sprintf(`{"monitor": %d, "db_name": %q, "db_user": %q, "db_password": "site-never-emit"}`, monitor, database, databaseUser))
		require.NoError(t, os.WriteFile(filepath.Join(siteDir, "site_config.json"), config, 0o600))
	}

	binDir := filepath.Join(tmp, "bin")
	require.NoError(t, os.Mkdir(binDir, 0o755))
	fakeCurl := `#!/usr/bin/env bash
sleep 0.15
case "$*" in
  *time_total*) printf '503 0.150' ;;
  *) printf '503' ;;
esac
`
	require.NoError(t, os.WriteFile(filepath.Join(binDir, "curl"), []byte(fakeCurl), 0o755))
	scriptPath := filepath.Join(tmp, "collector.sh")
	require.NoError(t, os.WriteFile(scriptPath, []byte(scripts.CollectorScript), 0o755))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	cmd.Env = append(os.Environ(),
		"PATH="+binDir+":"+os.Getenv("PATH"),
		"BENCH_PATHS="+bench,
		"SITE_PROBE_BUDGET_S=8",
		"SITE_PROBE_CONCURRENCY=8",
	)
	started := time.Now()
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "collector output:\n%s", out)
	require.NotContains(t, string(out), "secret_database_")
	require.NotContains(t, string(out), "secret_user_")
	require.NotContains(t, string(out), "never-emit")
	require.Less(t, time.Since(started), 2*time.Second,
		"site probes appear sequential instead of concurrency-bounded")

	parsed, err := parser.Tokenize(string(out))
	require.NoError(t, err)
	sites := parser.SiteNamesFor(parsed)
	require.Len(t, sites, siteCount)
	for _, pair := range sites {
		metric, err := parser.SiteFromSections(parsed, pair[0], pair[1])
		require.NoError(t, err)
		require.Equal(t, int64(503), metric.HTTPStatusCode)
		require.Equal(t, int64(0), metric.IsHealthy)
		require.Equal(t, int64(3), metric.FailureReasonCode)
		require.Equal(t, expectedDatabases[pair[1]], metric.DatabaseHash)
		require.Equal(t, expectedUsers[pair[1]], metric.DatabaseUserHash)
		if pair[1] == "site-00.test" {
			require.Equal(t, int64(0), metric.NativeMonitorEnabled)
		} else {
			require.Equal(t, int64(1), metric.NativeMonitorEnabled)
		}
	}
}
