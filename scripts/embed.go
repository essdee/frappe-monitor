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

// CollectorVersion returns the VERSION declared in the embedded
// collector script. It scans the first ~10 lines for `VERSION="..."`.
// Failure mode: returns "unknown" if the version line is absent — this
// is a build-time invariant violation, but the runtime should not
// crash on a malformed embed.
func CollectorVersion() string {
	for _, line := range strings.SplitN(CollectorScript, "\n", 12) {
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
