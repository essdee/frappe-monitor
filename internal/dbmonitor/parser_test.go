package dbmonitor

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const mariadbSlaveStatus = `*************************** 1. row ***************************
                Slave_IO_State: Waiting for master to send event
                   Master_Host: 10.0.0.1
              Slave_IO_Running: Yes
             Slave_SQL_Running: Yes
        Seconds_Behind_Master: 12
                Last_IO_Error:
               Last_SQL_Error:
`

const mysql8ReplicaStatus = `*************************** 1. row ***************************
             Replica_IO_State: Waiting for source to send event
                  Source_Host: 10.0.0.1
            Replica_IO_Running: Yes
           Replica_SQL_Running: Yes
        Seconds_Behind_Source: 0
                Last_IO_Error:
               Last_SQL_Error:
`

const stoppedSQLThread = `*************************** 1. row ***************************
              Slave_IO_Running: Yes
             Slave_SQL_Running: No
        Seconds_Behind_Master: NULL
                Last_IO_Error:
               Last_SQL_Error: Error 'Duplicate entry' on query. Default database: 'erp'
`

func TestParseReplStatus_MariaDB(t *testing.T) {
	st := parseReplStatus(mariadbSlaveStatus)
	require.True(t, st.IsReplica)
	require.True(t, st.IORunning)
	require.True(t, st.SQLRunning)
	require.NotNil(t, st.LagSeconds)
	require.EqualValues(t, 12, *st.LagSeconds)
	require.Empty(t, st.LastError)
}

func TestParseReplStatus_MySQL8Replica(t *testing.T) {
	st := parseReplStatus(mysql8ReplicaStatus)
	require.True(t, st.IsReplica)
	require.True(t, st.IORunning)
	require.True(t, st.SQLRunning)
	require.NotNil(t, st.LagSeconds)
	require.EqualValues(t, 0, *st.LagSeconds)
}

func TestParseReplStatus_StoppedThread(t *testing.T) {
	st := parseReplStatus(stoppedSQLThread)
	require.True(t, st.IsReplica)
	require.True(t, st.IORunning)
	require.False(t, st.SQLRunning)
	require.Nil(t, st.LagSeconds, "NULL lag → nil")
	require.Contains(t, st.LastError, "Duplicate entry", "value with a colon survives first-colon split")
}

func TestParseReplStatus_NotAReplica(t *testing.T) {
	st := parseReplStatus("") // SHOW SLAVE STATUS on a master returns no rows
	require.False(t, st.IsReplica)
}

func TestDeriveStatus(t *testing.T) {
	lag := func(n int64) *int64 { return &n }

	cases := []struct {
		name      string
		res       Result
		threshold int
		want      string
	}{
		{"unreachable", Result{Reachable: false, LastError: "ssh dial failed"}, 30, "unreachable"},
		{"not a replica", Result{Reachable: true, IsReplica: false}, 30, "broken"},
		{"sql stopped", Result{Reachable: true, IsReplica: true, IORunning: true, SQLRunning: false}, 30, "broken"},
		{"healthy", Result{Reachable: true, IsReplica: true, IORunning: true, SQLRunning: true, LagSeconds: lag(5)}, 30, "healthy"},
		{"lagging", Result{Reachable: true, IsReplica: true, IORunning: true, SQLRunning: true, LagSeconds: lag(120)}, 30, "lagging"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, _ := deriveStatus(c.res, c.threshold)
			require.Equal(t, c.want, got)
		})
	}
}

func TestParseSingleFloat(t *testing.T) {
	v, ok := parseSingleFloat("  3.5\n")
	require.True(t, ok)
	require.Equal(t, 3.5, v)
	_, ok = parseSingleFloat("NULL")
	require.False(t, ok)
}
