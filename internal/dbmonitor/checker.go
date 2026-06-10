package dbmonitor

import (
	"context"
	"fmt"
	"strings"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// Checker runs one replication check over SSH.
type Checker struct {
	Exec sshpkg.Executor
}

// Result is the outcome of checking one target.
type Result struct {
	Reachable           bool
	IsReplica           bool
	IORunning           bool
	SQLRunning          bool
	LagSeconds          *int64
	HeartbeatLagSeconds *float64
	LastError           string
}

// Check SSHes into the server and reads replication status (+ optional
// heartbeat lag). It never returns an error — a failure becomes an
// unreachable Result with LastError set.
func (c *Checker) Check(ctx context.Context, tgt sshpkg.Target, t *storage.DBTarget) Result {
	base := shellQuote(firstNonEmpty(t.MySQLCommand, "mysql"))
	if t.DefaultsFile != "" {
		// --defaults-file must come first.
		base = shellQuote(firstNonEmpty(t.MySQLCommand, "mysql")) + " --defaults-file=" + shellQuote(t.DefaultsFile)
	}
	if t.Socket != "" {
		base += " --socket=" + shellQuote(t.Socket)
	}

	// Try the modern REPLICA wording (MySQL 8+/MariaDB 10.5+) and fall
	// back to SLAVE (older). Single-quoted SQL so `\G` survives the shell.
	statusCmd := fmt.Sprintf(`%s -e 'SHOW REPLICA STATUS\G' 2>/dev/null || %s -e 'SHOW SLAVE STATUS\G'`, base, base)

	out, err := c.Exec.Run(ctx, tgt, statusCmd)
	if err != nil {
		return Result{Reachable: false, LastError: cleanErr(err.Error())}
	}

	st := parseReplStatus(out)
	res := Result{
		Reachable:  true,
		IsReplica:  st.IsReplica,
		IORunning:  st.IORunning,
		SQLRunning: st.SQLRunning,
		LagSeconds: st.LagSeconds,
		LastError:  st.LastError,
	}

	// Heartbeat only makes sense on an actual replica.
	if st.IsReplica && t.HeartbeatEnabled && strings.TrimSpace(t.HeartbeatQuery) != "" {
		// -N (skip column names) -B (tab-separated). 2>/dev/null so a mysql
		// warning on stderr can't be parsed as the lag value (Exec.Run
		// returns combined stdout+stderr).
		hbCmd := base + " -N -B -e " + shellQuote(t.HeartbeatQuery) + " 2>/dev/null"
		if hbOut, hbErr := c.Exec.Run(ctx, tgt, hbCmd); hbErr == nil {
			if v, ok := parseSingleFloat(hbOut); ok {
				res.HeartbeatLagSeconds = &v
			}
		}
	}
	return res
}

// effectiveLag returns the lag to compare against the threshold, in
// seconds, preferring the (more accurate) heartbeat value.
func (r Result) effectiveLag() (float64, bool) {
	if r.HeartbeatLagSeconds != nil {
		return *r.HeartbeatLagSeconds, true
	}
	if r.LagSeconds != nil {
		return float64(*r.LagSeconds), true
	}
	return 0, false
}

// deriveStatus maps a Result + threshold to a persisted status string +
// a human last-error.
func deriveStatus(r Result, thresholdSeconds int) (status, lastErr string) {
	switch {
	case !r.Reachable:
		return "unreachable", r.LastError
	case !r.IsReplica:
		return "broken", "replication not configured (SHOW SLAVE/REPLICA STATUS returned no rows)"
	case !r.IORunning || !r.SQLRunning:
		msg := r.LastError
		if msg == "" {
			msg = fmt.Sprintf("replication thread stopped (IO=%s, SQL=%s)", yesNo(r.IORunning), yesNo(r.SQLRunning))
		}
		return "broken", msg
	}
	if lag, ok := r.effectiveLag(); ok && lag > float64(thresholdSeconds) {
		return "lagging", r.LastError
	}
	return "healthy", r.LastError
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// shellQuote wraps s in single quotes for safe interpolation into a remote
// shell command. Embedded single quotes use the standard '\” trick.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// cleanErr trims an SSH error to something short for the status row.
func cleanErr(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 500 {
		s = s[:500]
	}
	return s
}
