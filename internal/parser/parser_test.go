package parser

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTokenize_GoldenServer(t *testing.T) {
	raw, err := os.ReadFile("testdata/server-good.txt")
	require.NoError(t, err)

	out, err := Tokenize(string(raw))
	require.NoError(t, err)

	require.Len(t, out.Sections, 2, "expect META + SERVER")
	require.Equal(t, "META", out.Sections[0].Name)
	require.Equal(t, "1.0.0", out.Sections[0].KVs["version"])
	require.Equal(t, "fixture-host", out.Sections[0].KVs["hostname"])
	require.Equal(t, "1777000000", out.Sections[0].KVs["timestamp"])

	require.Equal(t, "SERVER", out.Sections[1].Name)
	server := out.Sections[1].KVs

	// Single-instance metrics
	require.Contains(t, server, "load_1m")
	require.Contains(t, server, "load_5m")
	require.Contains(t, server, "load_15m")
	require.Contains(t, server, "uptime_seconds")
	require.Contains(t, server, "mem_total_kb")
	require.Contains(t, server, "cpu_user")

	// Multi-instance metrics: keys preserve the inline label braces.
	// Task 3 (ServerFromSections) splits the labels.
	require.Contains(t, server, `disk_used_bytes{mount="/"}`)
	require.Contains(t, server, `disk_total_bytes{mount="/"}`)
	require.Contains(t, server, `disk_used_bytes{mount="/boot/efi"}`)

	// Network interface names with hyphens, dots, digits round-trip cleanly.
	require.Contains(t, server, `net_rx_bytes{iface="br-5a9e56998edb"}`)
	require.Contains(t, server, `net_rx_bytes{iface="ztuzesl7e3"}`)

	// Loopback was filtered by the collector — must NOT appear.
	require.NotContains(t, server, `net_rx_bytes{iface="lo"}`)
}

func TestTokenize_MissingEnd(t *testing.T) {
	raw, err := os.ReadFile("testdata/server-malformed.txt")
	require.NoError(t, err)
	_, err = Tokenize(string(raw))
	require.Error(t, err)
	require.Contains(t, err.Error(), "###END")
}

func TestTokenize_RejectsDuplicateSection(t *testing.T) {
	input := "###META\nversion=1\n###META\nversion=2\n###END\n"
	_, err := Tokenize(input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "duplicate")
	require.Contains(t, err.Error(), "META")
}

func TestTokenize_RejectsContentBeforeFirstSection(t *testing.T) {
	input := "stray=1\n###META\nversion=1\n###END\n"
	_, err := Tokenize(input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "outside section")
}

func TestTokenize_RejectsMalformedKVLine(t *testing.T) {
	input := "###META\nbad-line-no-equals\n###END\n"
	_, err := Tokenize(input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no '='")
}

func TestTokenize_HandlesInlineLabelsWithEqualsInside(t *testing.T) {
	// The inline label like {mount="/"} contains an `=`; the parser must
	// find the `=` at brace-depth zero, not the first `=` it sees.
	input := "###SERVER\n" +
		`disk_used_bytes{mount="/"}=1234` + "\n" +
		"###END\n"
	out, err := Tokenize(input)
	require.NoError(t, err)
	require.Len(t, out.Sections, 1)
	require.Equal(t, "1234", out.Sections[0].KVs[`disk_used_bytes{mount="/"}`])
}

func TestTokenize_PreservesSectionsWithColons(t *testing.T) {
	// Phase 3 will introduce ###BENCH:bench_name — make sure tokenizer
	// keeps section names with colons intact (no splitting on colon).
	input := "###BENCH:production-bench\n" +
		"frappe_version=15.0.0\n" +
		"###END\n"
	out, err := Tokenize(input)
	require.NoError(t, err)
	require.Len(t, out.Sections, 1)
	require.Equal(t, "BENCH:production-bench", out.Sections[0].Name)
	require.Equal(t, "15.0.0", out.Sections[0].KVs["frappe_version"])
}

func TestTokenize_EmptyInputErrors(t *testing.T) {
	_, err := Tokenize("")
	require.Error(t, err)
	require.Contains(t, err.Error(), "###END")
}
