// Package dbmonitor watches MySQL/MariaDB replicas for master/slave
// replication health. It SSHes into each target's registered server
// (reusing that server's SSH credentials), runs the mysql client there to
// read SHOW SLAVE/REPLICA STATUS (and an optional heartbeat query), parses
// the result, persists the status, pushes metrics to VictoriaMetrics, and
// pushes live updates to the dashboard over WebSocket. No new DB port is
// exposed — everything rides the existing SSH path.
package dbmonitor

import (
	"strconv"
	"strings"
)

// ReplStatus is the parsed result of SHOW SLAVE STATUS / SHOW REPLICA
// STATUS. Field names differ across MySQL 8+ (REPLICA / Source) and
// MariaDB (SLAVE / Master); the parser accepts both.
type ReplStatus struct {
	// IsReplica is false when the status output is empty — i.e. the
	// server isn't configured as a replica.
	IsReplica  bool
	IORunning  bool
	SQLRunning bool
	// LagSeconds is Seconds_Behind_Master/Source. nil when the value is
	// NULL (replica stopped) or unparseable.
	LagSeconds *int64
	// LastError is the first non-empty of Last_IO_Error / Last_SQL_Error.
	LastError string
}

// parseReplStatus parses the vertical (`\G`) output of SHOW SLAVE/REPLICA
// STATUS into a ReplStatus.
func parseReplStatus(out string) ReplStatus {
	kv := parseVertical(out)
	if len(kv) == 0 {
		return ReplStatus{IsReplica: false}
	}
	st := ReplStatus{IsReplica: true}
	st.IORunning = strings.EqualFold(firstKey(kv, "Slave_IO_Running", "Replica_IO_Running"), "Yes")
	st.SQLRunning = strings.EqualFold(firstKey(kv, "Slave_SQL_Running", "Replica_SQL_Running"), "Yes")

	secs := firstKey(kv, "Seconds_Behind_Master", "Seconds_Behind_Source")
	if secs != "" && !strings.EqualFold(secs, "NULL") {
		if n, err := strconv.ParseInt(secs, 10, 64); err == nil {
			st.LagSeconds = &n
		}
	}
	if ioErr := firstKey(kv, "Last_IO_Error", "Last_Io_Error"); ioErr != "" {
		st.LastError = ioErr
	} else if sqlErr := firstKey(kv, "Last_SQL_Error", "Last_Sql_Error"); sqlErr != "" {
		st.LastError = sqlErr
	}
	return st
}

// parseVertical turns `Key: value` lines (the mysql `\G` format) into a
// map. Splits on the FIRST colon so values containing ':' survive.
func parseVertical(out string) map[string]string {
	kv := make(map[string]string)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "***") {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])
		if key != "" {
			kv[key] = val
		}
	}
	return kv
}

func firstKey(kv map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := kv[k]; ok {
			return v
		}
	}
	return ""
}

// parseSingleFloat extracts the heartbeat lag value from the query output.
// It scans from the END so a stray leading token (a warning that slipped
// through, an extra column) can't be mistaken for the lag value — the real
// number is the last field a `-N -B` single-value query prints.
func parseSingleFloat(out string) (float64, bool) {
	fields := strings.Fields(out)
	for i := len(fields) - 1; i >= 0; i-- {
		if v, err := strconv.ParseFloat(fields[i], 64); err == nil {
			return v, true
		}
	}
	return 0, false
}
