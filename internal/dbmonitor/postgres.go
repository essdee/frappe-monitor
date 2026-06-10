package dbmonitor

import (
	"context"
	"strconv"
	"strings"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// checkPostgres reads replication status from a PostgreSQL standby via psql
// over SSH. PostgreSQL has no Slave_IO/SQL threads, so we map its concepts
// onto the engine-agnostic Result:
//
//	IsReplica  = pg_is_in_recovery()                  (is this a standby?)
//	IORunning  = a WAL receiver is "streaming"        (connected to primary)
//	SQLRunning = the standby is in recovery (applying WAL — always true on a standby)
//	LagSeconds = now() - pg_last_xact_replay_timestamp()
//
// Credentials: psql reads ~/.pgpass by default; DefaultsFile overrides it
// via PGPASSFILE. Socket is passed as the host (-h), which may be a socket
// directory.
func (c *Checker) checkPostgres(ctx context.Context, tgt sshpkg.Target, t *storage.DBTarget) Result {
	psql := shellQuote(firstNonEmpty(t.ClientCommand, "psql"))
	env := ""
	if t.DefaultsFile != "" {
		env = "PGPASSFILE=" + shellQuote(t.DefaultsFile) + " "
	}
	host := ""
	if t.Socket != "" {
		host = " -h " + shellQuote(t.Socket)
	}
	base := env + psql + host

	// One round-trip: is_in_recovery | wal_receiver_status | lag_seconds(-1=NULL).
	const q = `SELECT pg_is_in_recovery()::int, ` +
		`COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1),'none'), ` +
		`COALESCE(EXTRACT(EPOCH FROM (now()-pg_last_xact_replay_timestamp()))::bigint,-1)`

	out, err := c.Exec.Run(ctx, tgt, base+" -tA -F'|' -c "+shellQuote(q)+" 2>/dev/null")
	if err != nil {
		return Result{Reachable: false, LastError: cleanErr(err.Error())}
	}
	res := parsePostgresStatus(out)
	res.Reachable = true

	if res.IsReplica && t.HeartbeatEnabled && strings.TrimSpace(t.HeartbeatQuery) != "" {
		hbCmd := base + " -tA -c " + shellQuote(t.HeartbeatQuery) + " 2>/dev/null"
		if hbOut, hbErr := c.Exec.Run(ctx, tgt, hbCmd); hbErr == nil {
			if v, ok := parseSingleFloat(hbOut); ok {
				res.HeartbeatLagSeconds = &v
			}
		}
	}
	return res
}

// parsePostgresStatus parses the pipe-separated single row from the status
// query: "<in_recovery 1|0>|<wal_receiver_status>|<lag_seconds -1=NULL>".
func parsePostgresStatus(out string) Result {
	out = strings.TrimSpace(out)
	if out == "" {
		return Result{IsReplica: false}
	}
	lines := strings.Split(out, "\n")
	parts := strings.Split(strings.TrimSpace(lines[len(lines)-1]), "|")
	if len(parts) < 3 {
		return Result{IsReplica: false}
	}
	inRecovery := strings.TrimSpace(parts[0]) == "1"
	res := Result{IsReplica: inRecovery}
	if !inRecovery {
		return res // a primary, not a standby
	}
	res.IORunning = strings.EqualFold(strings.TrimSpace(parts[1]), "streaming")
	res.SQLRunning = true // a standby is always applying WAL
	if lag, err := strconv.ParseInt(strings.TrimSpace(parts[2]), 10, 64); err == nil && lag >= 0 {
		res.LagSeconds = &lag
	}
	return res
}
