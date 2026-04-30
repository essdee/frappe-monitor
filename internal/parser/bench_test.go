package parser

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// loadFullHierarchy returns the v2 captured fixture tokenized.
func loadFullHierarchy(t *testing.T) Output {
	t.Helper()
	raw, err := os.ReadFile("testdata/full-hierarchy.txt")
	require.NoError(t, err)
	out, err := Tokenize(string(raw))
	require.NoError(t, err)
	return out
}

func TestBenchNames_FromGolden(t *testing.T) {
	out := loadFullHierarchy(t)
	names := BenchNames(out)
	require.NotEmpty(t, names)
	require.Contains(t, names, "frappe-bench")
}

func TestSiteNamesFor_FromGolden(t *testing.T) {
	out := loadFullHierarchy(t)
	pairs := SiteNamesFor(out)
	require.NotEmpty(t, pairs)
	// Every pair has a non-empty bench and site.
	for _, p := range pairs {
		require.NotEmpty(t, p[0], "bench name")
		require.NotEmpty(t, p[1], "site name")
		require.Equal(t, "frappe-bench", p[0])
	}
}

func TestBenchFromSections_FromGolden(t *testing.T) {
	out := loadFullHierarchy(t)
	m, err := BenchFromSections(out, "frappe-bench")
	require.NoError(t, err)
	require.Equal(t, "frappe-bench", m.Bench)
	require.Equal(t, "15.97.0", m.FrappeVersion)
	require.Greater(t, m.AppsCount, int64(0))
	require.GreaterOrEqual(t, m.SupervisorRun, int64(0))
	require.GreaterOrEqual(t, m.SupervisorTotal, int64(0))

	// Three redis queues expected (short/default/long).
	queueNames := map[string]bool{}
	for _, q := range m.RedisQueues {
		queueNames[q.Name] = true
	}
	require.True(t, queueNames["short"], "short queue missing: %v", queueNames)
	require.True(t, queueNames["default"], "default queue missing")
	require.True(t, queueNames["long"], "long queue missing")
}

func TestSiteFromSections_FromGolden(t *testing.T) {
	out := loadFullHierarchy(t)
	pairs := SiteNamesFor(out)
	require.NotEmpty(t, pairs)

	// First-pair smoke: parse fields cleanly.
	bench, site := pairs[0][0], pairs[0][1]
	m, err := SiteFromSections(out, bench, site)
	require.NoError(t, err)
	require.Equal(t, bench, m.Bench)
	require.Equal(t, site, m.Site)
	require.GreaterOrEqual(t, m.HTTPStatusCode, int64(0))
	require.GreaterOrEqual(t, m.HTTPResponseMs, 0.0)
	require.True(t, m.IsHealthy == 0 || m.IsHealthy == 1)
}

func TestBenchFromSections_MissingSection(t *testing.T) {
	out := loadFullHierarchy(t)
	_, err := BenchFromSections(out, "no-such-bench")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing")
	require.Contains(t, err.Error(), "BENCH:no-such-bench")
}

func TestSiteFromSections_MissingSection(t *testing.T) {
	out := loadFullHierarchy(t)
	_, err := SiteFromSections(out, "frappe-bench", "no-such-site")
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing")
	require.Contains(t, err.Error(), "SITE:frappe-bench:no-such-site")
}

func TestBenchFromSections_RejectsMalformedAppsCount(t *testing.T) {
	input := "###META\nversion=2.0.0\nhostname=h\ntimestamp=1\n" +
		"###BENCH:bad\napps_count=NotANumber\nsupervisor_running=0\nsupervisor_total=0\n" +
		"###END\n"
	out, err := Tokenize(input)
	require.NoError(t, err)
	_, err = BenchFromSections(out, "bad")
	require.Error(t, err)
	require.Contains(t, err.Error(), "apps_count")
}

func TestMetaTimestamp_FromGolden(t *testing.T) {
	out := loadFullHierarchy(t)
	ts, err := MetaTimestamp(out)
	require.NoError(t, err)
	require.Equal(t, int64(1777000000), ts.Unix())
}

func TestBenchFromSections_HandlesNoFrappeVersion(t *testing.T) {
	// frappe-bench section without info{...}=1 → FrappeVersion should be "".
	input := "###META\nversion=2.0.0\nhostname=h\ntimestamp=1\n" +
		"###BENCH:b1\napps_count=5\nsupervisor_running=2\nsupervisor_total=4\n" +
		`redis_queue_depth{queue="short"}=0` + "\n" +
		`redis_queue_depth{queue="default"}=0` + "\n" +
		`redis_queue_depth{queue="long"}=0` + "\n" +
		"###END\n"
	out, err := Tokenize(input)
	require.NoError(t, err)
	m, err := BenchFromSections(out, "b1")
	require.NoError(t, err)
	require.Equal(t, "", m.FrappeVersion)
	require.Equal(t, int64(5), m.AppsCount)
}

func TestSiteFromSections_FloatResponseMs(t *testing.T) {
	input := "###META\nversion=2.0.0\nhostname=h\ntimestamp=1\n" +
		"###BENCH:b1\napps_count=1\nsupervisor_running=0\nsupervisor_total=0\n" +
		`redis_queue_depth{queue="short"}=0` + "\n" +
		`redis_queue_depth{queue="default"}=0` + "\n" +
		`redis_queue_depth{queue="long"}=0` + "\n" +
		"###SITE:b1:s1.example.com\n" +
		"http_status_code=200\nhttp_response_ms=12.345\nis_healthy=1\n" +
		"###END\n"
	_ = strings.Repeat("", 0) // (keep strings import)
	out, err := Tokenize(input)
	require.NoError(t, err)
	m, err := SiteFromSections(out, "b1", "s1.example.com")
	require.NoError(t, err)
	require.Equal(t, int64(200), m.HTTPStatusCode)
	require.InDelta(t, 12.345, m.HTTPResponseMs, 0.001)
	require.Equal(t, int64(1), m.IsHealthy)
}
