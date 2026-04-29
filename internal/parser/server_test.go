package parser

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func tokenize(t *testing.T, input string) Output {
	t.Helper()
	out, err := Tokenize(input)
	require.NoError(t, err)
	return out
}

func TestParseServer_FromGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/server-good.txt")
	require.NoError(t, err)

	out, err := Tokenize(string(raw))
	require.NoError(t, err)

	m, err := ServerFromSections(out)
	require.NoError(t, err)

	// META
	require.Equal(t, "1.0.0", m.CollectorVersion)
	require.Equal(t, "fixture-host", m.Hostname)
	require.Equal(t, int64(1777000000), m.Timestamp.Unix())

	// CPU counters non-zero (real /proc/stat values)
	require.Greater(t, m.CPUUser, int64(0))
	require.Greater(t, m.CPUIdle, int64(0))

	// Memory positive
	require.Greater(t, m.MemTotalKB, int64(0))
	require.Greater(t, m.MemAvailableKB, int64(0))

	// Load is positive
	require.Greater(t, m.Load1, 0.0)

	// Uptime is positive
	require.Greater(t, m.UptimeSeconds, 0.0)

	// Disks: at least one mount, all have non-negative bytes
	require.NotEmpty(t, m.Disks)
	for _, d := range m.Disks {
		require.NotEmpty(t, d.Mount)
		require.GreaterOrEqual(t, d.TotalBytes, int64(0))
		require.GreaterOrEqual(t, d.UsedBytes, int64(0))
	}

	// Net: at least one iface; lo not present (filtered upstream)
	require.NotEmpty(t, m.Net)
	for _, n := range m.Net {
		require.NotEqual(t, "lo", n.Name)
	}
}

func TestParseServer_RejectsZeroMemTotal(t *testing.T) {
	input := buildMinimalServerInput(map[string]string{"mem_total_kb": "0"})
	out := tokenize(t, input)
	_, err := ServerFromSections(out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mem_total_kb")
}

func TestParseServer_RejectsMissingMETA(t *testing.T) {
	input := "###SERVER\ncpu_user=1\n###END\n"
	out := tokenize(t, input)
	_, err := ServerFromSections(out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "META")
}

func TestParseServer_RejectsMissingSERVER(t *testing.T) {
	input := "###META\nversion=1.0.0\nhostname=h\ntimestamp=1\n###END\n"
	out := tokenize(t, input)
	_, err := ServerFromSections(out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "SERVER")
}

func TestParseServer_RejectsMissingExpectedKey(t *testing.T) {
	// SERVER section missing cpu_user (a required key).
	input := buildMinimalServerInput(nil)
	input = strings.Replace(input, "cpu_user=1\n", "", 1)
	out := tokenize(t, input)
	_, err := ServerFromSections(out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cpu_user")
}

func TestParseServer_RejectsEmptyValue(t *testing.T) {
	input := buildMinimalServerInput(map[string]string{"cpu_user": ""})
	out := tokenize(t, input)
	_, err := ServerFromSections(out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cpu_user")
	require.Contains(t, err.Error(), "empty")
}

func TestParseServer_IgnoresUnknownSection(t *testing.T) {
	// A future Phase 3 collector might emit a BENCH section. The Phase 2
	// monitor must skip it without erroring so we don't break old monitors
	// during a rolling collector upgrade.
	input := buildMinimalServerInput(nil) +
		// already emitted ###END — we need to embed BENCH between SERVER and END
		"" // placeholder; building by string manipulation below
	// Rebuild input with a BENCH section between SERVER and END.
	bench := "###BENCH:production\nfrappe_version=15.0.0\n"
	input = strings.Replace(input, "###END", bench+"###END", 1)
	out := tokenize(t, input)
	_, err := ServerFromSections(out)
	require.NoError(t, err, "unknown section names must be skipped, not errored")
}

func TestParseServer_DisksAndNetParsedCorrectly(t *testing.T) {
	// Build SERVER with two disks + two ifaces; verify the lookups are correct.
	body := []string{
		"cpu_user=1", "cpu_nice=1", "cpu_system=1", "cpu_idle=1",
		"cpu_iowait=1", "cpu_irq=0", "cpu_softirq=0", "cpu_steal=0",
		"mem_total_kb=16000000", "mem_available_kb=8000000",
		"mem_free_kb=4000000", "mem_buffers_kb=500000", "mem_cached_kb=2000000",
		"swap_total_kb=1000", "swap_free_kb=500",
		"load_1m=1.0", "load_5m=0.5", "load_15m=0.2",
		"uptime_seconds=42.5",
		`disk_used_bytes{mount="/"}=100`,
		`disk_total_bytes{mount="/"}=1000`,
		`disk_used_bytes{mount="/var"}=200`,
		`disk_total_bytes{mount="/var"}=2000`,
		`net_rx_bytes{iface="eth0"}=11`,
		`net_tx_bytes{iface="eth0"}=12`,
		`net_rx_bytes{iface="eth1"}=21`,
		`net_tx_bytes{iface="eth1"}=22`,
	}
	input := "###META\nversion=1.0.0\nhostname=h\ntimestamp=1\n" +
		"###SERVER\n" + strings.Join(body, "\n") + "\n###END\n"

	out := tokenize(t, input)
	m, err := ServerFromSections(out)
	require.NoError(t, err)

	require.Len(t, m.Disks, 2)
	disksByMount := map[string][2]int64{}
	for _, d := range m.Disks {
		disksByMount[d.Mount] = [2]int64{d.UsedBytes, d.TotalBytes}
	}
	require.Equal(t, [2]int64{100, 1000}, disksByMount["/"])
	require.Equal(t, [2]int64{200, 2000}, disksByMount["/var"])

	require.Len(t, m.Net, 2)
	netByIface := map[string][2]int64{}
	for _, n := range m.Net {
		netByIface[n.Name] = [2]int64{n.RxBytes, n.TxBytes}
	}
	require.Equal(t, [2]int64{11, 12}, netByIface["eth0"])
	require.Equal(t, [2]int64{21, 22}, netByIface["eth1"])
}

func TestParseServer_RejectsHalfPairDisk(t *testing.T) {
	// disk_used without matching disk_total → torn output.
	body := minimalSeverBodyMap(nil)
	delete(body, `disk_used_bytes{mount="/"}`)
	delete(body, `disk_total_bytes{mount="/"}`)
	body[`disk_used_bytes{mount="/"}`] = "100"
	// no disk_total_bytes for "/" — that's the bug we want to catch.
	input := buildServerFromBodyMap(body)
	out := tokenize(t, input)
	_, err := ServerFromSections(out)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing")
}

// --- helpers ---

func minimalSeverBodyMap(overrides map[string]string) map[string]string {
	body := map[string]string{
		"cpu_user": "1", "cpu_nice": "1", "cpu_system": "1", "cpu_idle": "1",
		"cpu_iowait": "1", "cpu_irq": "0", "cpu_softirq": "0", "cpu_steal": "0",
		"mem_total_kb": "16000000", "mem_available_kb": "8000000",
		"mem_free_kb": "4000000", "mem_buffers_kb": "500000", "mem_cached_kb": "2000000",
		"swap_total_kb": "1000", "swap_free_kb": "500",
		"load_1m": "1.0", "load_5m": "0.5", "load_15m": "0.2",
		"uptime_seconds": "42.5",
		`disk_used_bytes{mount="/"}`:  "100",
		`disk_total_bytes{mount="/"}`: "1000",
		`net_rx_bytes{iface="eth0"}`:  "11",
		`net_tx_bytes{iface="eth0"}`:  "12",
	}
	for k, v := range overrides {
		body[k] = v
	}
	return body
}

func buildServerFromBodyMap(body map[string]string) string {
	var sb strings.Builder
	sb.WriteString("###META\nversion=1.0.0\nhostname=h\ntimestamp=1\n###SERVER\n")
	for k, v := range body {
		sb.WriteString(k)
		sb.WriteString("=")
		sb.WriteString(v)
		sb.WriteString("\n")
	}
	sb.WriteString("###END\n")
	return sb.String()
}

func buildMinimalServerInput(overrides map[string]string) string {
	return buildServerFromBodyMap(minimalSeverBodyMap(overrides))
}
