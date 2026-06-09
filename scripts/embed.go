// Package scripts embeds shell scripts that the monitoring server ships
// to bench servers. The script files in this directory are the canonical
// versions; the binary carries them via go:embed so deployment doesn't
// depend on any external file path.
package scripts

import (
	_ "embed"
	"strings"
)

//go:embed frappe-monitor-collect.sh
var CollectorScript string

//go:embed frappe-monitor-system.sh
var SystemScript string

//go:embed frappe-monitor-stream.sh
var StreamerScript string

// CollectorVersion returns the VERSION declared in the embedded
// collector script. It scans for the first top-level `VERSION="..."`
// assignment — collectors with long header comments are fine.
// Failure mode: returns "unknown" if the version line is absent — this
// is a build-time invariant violation, but the runtime should not
// crash on a malformed embed.
func CollectorVersion() string {
	return scanVersion(CollectorScript)
}

// StreamerVersion returns the VERSION declared in the embedded
// streamer script. Same protocol as CollectorVersion — first top-level
// `VERSION="..."` line wins.
func StreamerVersion() string {
	return scanVersion(StreamerScript)
}

func scanVersion(script string) string {
	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		const prefix = `VERSION="`
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		rest := strings.TrimPrefix(line, prefix)
		if i := strings.Index(rest, `"`); i > 0 {
			return rest[:i]
		}
	}
	return "unknown"
}
