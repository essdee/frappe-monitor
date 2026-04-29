package metrics

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestServerMetrics_ToLineProtocol(t *testing.T) {
	m := ServerMetrics{
		Timestamp:      time.Unix(1714363200, 0).UTC(),
		Hostname:       "prod1.example.com",
		Load1:          1.5,
		Load5:          0.5,
		Load15:         0.2,
		UptimeSeconds:  42.5,
		MemTotalKB:     16_000_000,
		MemAvailableKB: 8_000_000,
		MemFreeKB:      4_000_000,
		MemBuffersKB:   500_000,
		MemCachedKB:    2_000_000,
		SwapTotalKB:    1_000,
		SwapFreeKB:     500,
		CPUUser:        100,
		CPUNice:        1,
		CPUSystem:      2,
		CPUIdle:        3,
		CPUIOWait:      4,
		CPUIRQ:         5,
		CPUSoftIRQ:     6,
		CPUSteal:       7,
		Disks: []DiskMount{
			{Mount: "/", UsedBytes: 100, TotalBytes: 1000},
			{Mount: "/boot/efi", UsedBytes: 50, TotalBytes: 500},
		},
		Net: []NetIface{
			{Name: "eth0", RxBytes: 11, TxBytes: 12},
		},
	}
	out := m.LineProtocol("prod-1")

	// Float gauge — load_1m.
	require.Contains(t, out,
		"frappe_server_load_1m,server=prod-1 value=1.5 1714363200000000000\n")

	// kB → bytes conversion (×1024).
	require.Contains(t, out,
		"frappe_server_mem_total_bytes,server=prod-1 value=16384000000 1714363200000000000\n")

	// CPU counter — emitted as plain integer.
	require.Contains(t, out,
		"frappe_server_cpu_user,server=prod-1 value=100 1714363200000000000\n")

	// Multi-instance — disk_used_bytes for "/" mount.
	require.Contains(t, out,
		"frappe_server_disk_used_bytes,server=prod-1,mount=/ value=100 1714363200000000000\n")

	// Multi-instance — disk for nested mount path.
	require.Contains(t, out,
		"frappe_server_disk_used_bytes,server=prod-1,mount=/boot/efi value=50 1714363200000000000\n")

	// Network iface tag.
	require.Contains(t, out,
		"frappe_server_net_rx_bytes,server=prod-1,iface=eth0 value=11 1714363200000000000\n")

	// Sanity: every line ends with the same nanosecond timestamp + newline,
	// i.e., the body parses as a sequence of complete lines.
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		require.True(t, strings.HasSuffix(line, "1714363200000000000"),
			"every line should end with the unix-ns timestamp: %q", line)
	}
}

func TestEscapeTag_HandlesSpecialChars(t *testing.T) {
	require.Equal(t, `prod-1`, escapeTag("prod-1"))
	require.Equal(t, `with\ space`, escapeTag("with space"))
	require.Equal(t, `a\,b`, escapeTag("a,b"))
	require.Equal(t, `k\=v`, escapeTag("k=v"))
	require.Equal(t, `/boot/efi`, escapeTag("/boot/efi"), "slash is not special")
	require.Equal(t, `a\\b`, escapeTag(`a\b`), "backslash must be escaped first")
}

func TestServerMetrics_LineCountMatches(t *testing.T) {
	// 8 cpu + 7 mem (5 + 2 swap) + 4 (3 load + uptime) = 19 single-instance.
	// Plus 2 lines per disk and 2 lines per iface.
	m := ServerMetrics{
		Timestamp:  time.Unix(1, 0).UTC(),
		MemTotalKB: 1, // any positive value
		Disks: []DiskMount{
			{Mount: "/", UsedBytes: 0, TotalBytes: 1},
			{Mount: "/var", UsedBytes: 0, TotalBytes: 1},
		},
		Net: []NetIface{
			{Name: "eth0"},
			{Name: "eth1"},
			{Name: "eth2"},
		},
	}
	out := m.LineProtocol("h")
	lines := strings.Count(strings.TrimRight(out, "\n"), "\n") + 1

	expected := 19 + 2*len(m.Disks) + 2*len(m.Net)
	require.Equal(t, expected, lines, "unexpected line count")
}
