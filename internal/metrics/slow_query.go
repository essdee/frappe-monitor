package metrics

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const maxSlowQueryStatementBytes = 64 * 1024

var (
	mysqlQueryTimePattern = regexp.MustCompile(`(?i)^#\s*Query_time:\s*([0-9.]+)\s+Lock_time:\s*([0-9.]+)\s+Rows_sent:\s*([0-9]+)\s+Rows_examined:\s*([0-9]+)`)
	mysqlUserPattern      = regexp.MustCompile(`(?i)^#\s*User@Host:\s*([^\s\[]+)`)
	mysqlUsePattern       = regexp.MustCompile(`(?i)^use\s+[` + "`" + `"]?([^` + "`" + `";\s]+)`)
	mysqlSetTimePattern   = regexp.MustCompile(`(?i)^SET\s+timestamp=([0-9]+)`)
	postgresDuration      = regexp.MustCompile(`(?i)\bduration:\s*([0-9.]+)\s*ms\s+(?:statement|execute\s+[^:]*):\s*(.*)$`)
	postgresDBPattern     = regexp.MustCompile(`(?:^|[,\s])db=([^,\s]+)`)
	postgresUserPattern   = regexp.MustCompile(`(?:^|[,\s])user=([^,\s]+)`)
	traceIDPattern        = regexp.MustCompile(`(?i)FRAPPE_TRACE_ID:\s*([A-Za-z0-9_-]{1,128})`)
)

// SlowQueryEvent is a safe, bounded-cardinality projection of one database
// slow-query record. Query text, database names, usernames, and trace IDs stay
// in Loki; VictoriaMetrics receives only duration/cost and a one-way identity
// hash that can join to the site's equally hashed configuration identity.
type SlowQueryEvent struct {
	Timestamp    time.Time
	Engine       string
	Duration     float64
	LockSeconds  float64
	RowsSent     int64
	RowsExamined int64
	IdentityHash string
	IdentityKind string
	Operation    string
	TraceID      string
}

// LogSummary returns a structured, safe-to-index projection for Loki. The
// original database log remains available in its source stream for exact SQL
// inspection; this correlated event intentionally omits SQL and database/user
// names so site dashboards can stay useful without copying sensitive text.
func (e SlowQueryEvent) LogSummary() string {
	value := struct {
		Engine       string  `json:"engine"`
		Operation    string  `json:"operation"`
		Duration     float64 `json:"duration_seconds"`
		LockSeconds  float64 `json:"lock_seconds"`
		RowsSent     int64   `json:"rows_sent"`
		RowsExamined int64   `json:"rows_examined"`
		TraceID      string  `json:"trace_id,omitempty"`
	}{
		Engine: e.Engine, Operation: e.Operation, Duration: e.Duration,
		LockSeconds: e.LockSeconds, RowsSent: e.RowsSent,
		RowsExamined: e.RowsExamined, TraceID: e.TraceID,
	}
	body, err := json.Marshal(value)
	if err != nil {
		return "{}"
	}
	return string(body)
}

// DatabaseIdentityHash returns a stable non-reversible 96-bit identifier.
// The truncated SHA-256 value is adequate for equality joins while avoiding
// disclosure of tenant database names or users in metric labels.
func DatabaseIdentityHash(identity string) string {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:12])
}

// SlowQueryParser incrementally parses MySQL/MariaDB multi-line slow logs and
// common PostgreSQL duration lines. One parser is owned per streamed file.
type SlowQueryParser struct {
	mysql slowMySQLState
}

type slowMySQLState struct {
	timestamp    time.Time
	user         string
	database     string
	duration     float64
	lockSeconds  float64
	rowsSent     int64
	rowsExamined int64
	statement    strings.Builder
	hasTiming    bool
}

// Consume accepts one raw log line and returns zero or more completed events.
// State is capped at 64 KiB even for malformed or unterminated SQL.
func (p *SlowQueryParser) Consume(line string, observedAt time.Time) []SlowQueryEvent {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}

	if matches := postgresDuration.FindStringSubmatch(line); len(matches) == 3 {
		durationMS, err := strconv.ParseFloat(matches[1], 64)
		if err == nil && durationMS >= 0 {
			identity, kind := postgresIdentity(line)
			return []SlowQueryEvent{{
				Timestamp: observedAt, Engine: "postgresql", Duration: durationMS / 1000,
				IdentityHash: DatabaseIdentityHash(identity), IdentityKind: kind,
				Operation: queryOperation(matches[2]), TraceID: traceID(matches[2]),
			}}
		}
	}

	var completed []SlowQueryEvent
	if strings.HasPrefix(trimmed, "# Time:") {
		if event, ok := p.finishMySQL(observedAt); ok {
			completed = append(completed, event)
		}
		p.mysql = slowMySQLState{timestamp: parseSlowQueryTime(strings.TrimSpace(strings.TrimPrefix(trimmed, "# Time:")))}
		return completed
	}
	if matches := mysqlUserPattern.FindStringSubmatch(trimmed); len(matches) == 2 {
		p.mysql.user = matches[1]
		return completed
	}
	if matches := mysqlQueryTimePattern.FindStringSubmatch(trimmed); len(matches) == 5 {
		p.mysql.duration, _ = strconv.ParseFloat(matches[1], 64)
		p.mysql.lockSeconds, _ = strconv.ParseFloat(matches[2], 64)
		p.mysql.rowsSent, _ = strconv.ParseInt(matches[3], 10, 64)
		p.mysql.rowsExamined, _ = strconv.ParseInt(matches[4], 10, 64)
		p.mysql.hasTiming = true
		return completed
	}
	if matches := mysqlUsePattern.FindStringSubmatch(trimmed); len(matches) == 2 {
		p.mysql.database = matches[1]
		return completed
	}
	if matches := mysqlSetTimePattern.FindStringSubmatch(trimmed); len(matches) == 2 {
		if unixSeconds, err := strconv.ParseInt(matches[1], 10, 64); err == nil {
			p.mysql.timestamp = time.Unix(unixSeconds, 0).UTC()
		}
		return completed
	}
	if strings.HasPrefix(trimmed, "#") || !p.mysql.hasTiming {
		return completed
	}

	remaining := maxSlowQueryStatementBytes - p.mysql.statement.Len()
	if remaining > 0 {
		if p.mysql.statement.Len() > 0 {
			p.mysql.statement.WriteByte('\n')
			remaining--
		}
		if remaining > len(line) {
			remaining = len(line)
		}
		p.mysql.statement.WriteString(line[:max(remaining, 0)])
	}
	if strings.HasSuffix(trimmed, ";") {
		if event, ok := p.finishMySQL(observedAt); ok {
			completed = append(completed, event)
		}
		p.mysql = slowMySQLState{}
	}
	return completed
}

func (p *SlowQueryParser) finishMySQL(observedAt time.Time) (SlowQueryEvent, bool) {
	if !p.mysql.hasTiming || p.mysql.statement.Len() == 0 {
		return SlowQueryEvent{}, false
	}
	identity, kind := p.mysql.database, "database"
	if identity == "" {
		identity, kind = p.mysql.user, "user"
	}
	if identity == "" {
		kind = "unknown"
	}
	at := p.mysql.timestamp
	if at.IsZero() {
		at = observedAt
	}
	statement := p.mysql.statement.String()
	return SlowQueryEvent{
		Timestamp: at, Engine: "mysql", Duration: p.mysql.duration,
		LockSeconds: p.mysql.lockSeconds, RowsSent: p.mysql.rowsSent,
		RowsExamined: p.mysql.rowsExamined, IdentityHash: DatabaseIdentityHash(identity),
		IdentityKind: kind, Operation: queryOperation(statement), TraceID: traceID(statement),
	}, true
}

func postgresIdentity(line string) (string, string) {
	if matches := postgresDBPattern.FindStringSubmatch(line); len(matches) == 2 {
		return strings.Trim(matches[1], `"'[]`), "database"
	}
	if matches := postgresUserPattern.FindStringSubmatch(line); len(matches) == 2 {
		return strings.Trim(matches[1], `"'[]`), "user"
	}
	return "", "unknown"
}

func queryOperation(statement string) string {
	statement = strings.TrimSpace(statement)
	for strings.HasPrefix(statement, "/*") {
		end := strings.Index(statement, "*/")
		if end < 0 {
			return "other"
		}
		statement = strings.TrimSpace(statement[end+2:])
	}
	fields := strings.Fields(statement)
	if len(fields) == 0 {
		return "other"
	}
	operation := strings.ToLower(strings.Trim(fields[0], "();"))
	switch operation {
	case "select", "insert", "update", "delete", "replace", "call", "commit", "alter", "create", "drop", "truncate":
		return operation
	default:
		return "other"
	}
}

func traceID(statement string) string {
	if matches := traceIDPattern.FindStringSubmatch(statement); len(matches) == 2 {
		return matches[1]
	}
	return ""
}

func parseSlowQueryTime(value string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, "060102 15:04:05", "2006-01-02T15:04:05.999999Z"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed.UTC()
		}
	}
	return time.Time{}
}

// LineProtocol emits event samples without query/trace text or raw identities.
func (e SlowQueryEvent) LineProtocol(serverLabel string, at time.Time) string {
	if !e.Timestamp.IsZero() {
		at = e.Timestamp
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	identityHash := e.IdentityHash
	if identityHash == "" {
		identityHash = "unknown"
	}
	identityKind := e.IdentityKind
	if identityKind == "" {
		identityKind = "unknown"
	}
	tags := "server=" + escapeTag(cleanMetricTag(serverLabel, 255)) +
		",engine=" + escapeTag(e.Engine) +
		",identity_hash=" + escapeTag(identityHash) +
		",identity_kind=" + escapeTag(identityKind) +
		",operation=" + escapeTag(e.Operation)
	ts := strconv.FormatInt(at.UnixNano(), 10)
	var b strings.Builder
	emit := func(name string, value string) {
		b.WriteString(name)
		b.WriteByte(',')
		b.WriteString(tags)
		b.WriteString(" value=")
		b.WriteString(value)
		b.WriteByte(' ')
		b.WriteString(ts)
		b.WriteByte('\n')
	}
	emit("frappe_database_slow_query_duration_seconds", strconv.FormatFloat(e.Duration, 'f', -1, 64))
	emit("frappe_database_slow_query_lock_seconds", strconv.FormatFloat(e.LockSeconds, 'f', -1, 64))
	emit("frappe_database_slow_query_rows_sent", strconv.FormatInt(e.RowsSent, 10))
	emit("frappe_database_slow_query_rows_examined", strconv.FormatInt(e.RowsExamined, 10))
	return b.String()
}
