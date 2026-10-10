package metrics

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSlowQueryParserMySQLCorrelatesHashedDatabaseAndTrace(t *testing.T) {
	parser := &SlowQueryParser{}
	lines := []string{
		"# Time: 2026-08-13T18:15:31.123456Z",
		"# User@Host: _site_user[_site_user] @ localhost []  Id: 42",
		"# Query_time: 4.250000  Lock_time: 0.125000 Rows_sent: 3 Rows_examined: 9123",
		"use `_site_db`;",
		"SET timestamp=1786644931;",
		"SELECT name FROM `tabSales Invoice` /* FRAPPE_TRACE_ID: 7ff1b6f4-abc */;",
	}
	var events []SlowQueryEvent
	for _, line := range lines {
		events = append(events, parser.Consume(line, time.Unix(200, 0).UTC())...)
	}
	require.Len(t, events, 1)
	event := events[0]
	require.Equal(t, "mysql", event.Engine)
	require.Equal(t, DatabaseIdentityHash("_site_db"), event.IdentityHash)
	require.Equal(t, "database", event.IdentityKind)
	require.Equal(t, "select", event.Operation)
	require.Equal(t, "7ff1b6f4-abc", event.TraceID)
	require.InDelta(t, 4.25, event.Duration, 0.0001)
	require.Equal(t, int64(9123), event.RowsExamined)

	body := event.LineProtocol("local prod", time.Time{})
	require.Contains(t, body, "frappe_database_slow_query_duration_seconds,server=local\\ prod,engine=mysql,identity_hash="+DatabaseIdentityHash("_site_db")+",identity_kind=database,operation=select value=4.25")
	require.NotContains(t, body, "_site_db")
	require.NotContains(t, body, "7ff1b6f4")
	summary := event.LogSummary()
	require.JSONEq(t, `{"engine":"mysql","operation":"select","duration_seconds":4.25,"lock_seconds":0.125,"rows_sent":3,"rows_examined":9123,"trace_id":"7ff1b6f4-abc"}`, summary)
	require.NotContains(t, summary, "_site_db")
	require.NotContains(t, summary, "SELECT name")
}

func TestSlowQueryParserPostgreSQLDurationLine(t *testing.T) {
	parser := &SlowQueryParser{}
	events := parser.Consume(`2026-08-13 18:00:00 UTC [55] user=frappe,db=site_db LOG: duration: 812.50 ms statement: UPDATE "tabTask" SET status='Open'`, time.Unix(300, 0).UTC())
	require.Len(t, events, 1)
	require.Equal(t, "postgresql", events[0].Engine)
	require.Equal(t, DatabaseIdentityHash("site_db"), events[0].IdentityHash)
	require.Equal(t, "database", events[0].IdentityKind)
	require.Equal(t, "update", events[0].Operation)
	require.InDelta(t, 0.8125, events[0].Duration, 0.0001)
}

func TestSlowQueryParserBoundsUnterminatedStatement(t *testing.T) {
	parser := &SlowQueryParser{}
	parser.Consume("# Query_time: 1.0 Lock_time: 0 Rows_sent: 0 Rows_examined: 1", time.Now())
	parser.Consume(string(make([]byte, maxSlowQueryStatementBytes*2)), time.Now())
	require.LessOrEqual(t, parser.mysql.statement.Len(), maxSlowQueryStatementBytes)
}

func TestDatabaseIdentityHashIsStableAndDoesNotRevealIdentity(t *testing.T) {
	hash := DatabaseIdentityHash("secret_database")
	require.Len(t, hash, 24)
	require.Equal(t, hash, DatabaseIdentityHash("secret_database"))
	require.NotContains(t, hash, "secret")
	require.Empty(t, DatabaseIdentityHash(""))
}
