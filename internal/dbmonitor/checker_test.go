package dbmonitor

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/realtime"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// captureHub is a fake realtime.Broadcaster that records the events it is
// asked to broadcast so checkOne/checkAll wiring can be asserted.
type captureHub struct {
	events []realtime.Event
}

func (h *captureHub) Broadcast(ev realtime.Event)      { h.events = append(h.events, ev) }
func (h *captureHub) HasSubscribers(topic string) bool { return true }

// capturePusher is a fake MetricsPusher recording every pushed body.
type capturePusher struct {
	bodies []string
	err    error
}

func (p *capturePusher) Push(_ context.Context, body string) error {
	p.bodies = append(p.bodies, body)
	return p.err
}

// host used for the synthetic server in all command-construction cases.
const checkerHost = "db.example.com"

// runMySQLCheck wires a Checker around a FakeExecutor that returns the given
// canned status output for the (single) host, runs checkMySQL, and returns
// the executor so the caller can inspect the recorded command + call count.
func runMySQLCheck(t *testing.T, tgt *storage.DBTarget, statusOut string) (*sshpkg.FakeExecutor, Result) {
	t.Helper()
	fx := sshpkg.NewFakeExecutor()
	fx.SetResponse(checkerHost, statusOut, nil)
	c := &Checker{Exec: fx}
	res := c.checkMySQL(context.Background(), sshpkg.Target{Host: checkerHost}, tgt)
	return fx, res
}

func runPostgresCheck(t *testing.T, tgt *storage.DBTarget, statusOut string) (*sshpkg.FakeExecutor, Result) {
	t.Helper()
	fx := sshpkg.NewFakeExecutor()
	fx.SetResponse(checkerHost, statusOut, nil)
	c := &Checker{Exec: fx}
	res := c.checkPostgres(context.Background(), sshpkg.Target{Host: checkerHost}, tgt)
	return fx, res
}

// ---------------------------------------------------------------------------
// (1) command construction
// ---------------------------------------------------------------------------

func TestCheckMySQL_CommandConstruction(t *testing.T) {
	// A status output that parses as an *up* replica so the heartbeat branch
	// is reachable (heartbeat only fires on an actual replica).
	const replicaOut = mariadbSlaveStatus

	t.Run("plain target", func(t *testing.T) {
		fx, res := runMySQLCheck(t, &storage.DBTarget{Engine: "mysql"}, replicaOut)
		require.True(t, res.Reachable)
		require.Equal(t, 1, fx.CallCount(checkerHost), "no heartbeat → exactly one command")
		want := `'mysql' -e 'SHOW REPLICA STATUS\G' 2>/dev/null || 'mysql' -e 'SHOW SLAVE STATUS\G'`
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("custom client command", func(t *testing.T) {
		fx, _ := runMySQLCheck(t, &storage.DBTarget{ClientCommand: "/usr/bin/mariadb"}, replicaOut)
		want := `'/usr/bin/mariadb' -e 'SHOW REPLICA STATUS\G' 2>/dev/null || '/usr/bin/mariadb' -e 'SHOW SLAVE STATUS\G'`
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("with DefaultsFile", func(t *testing.T) {
		fx, _ := runMySQLCheck(t, &storage.DBTarget{DefaultsFile: "/etc/my.cnf"}, replicaOut)
		base := `'mysql' --defaults-file='/etc/my.cnf'`
		want := fmt.Sprintf(`%s -e 'SHOW REPLICA STATUS\G' 2>/dev/null || %s -e 'SHOW SLAVE STATUS\G'`, base, base)
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("with Socket", func(t *testing.T) {
		fx, _ := runMySQLCheck(t, &storage.DBTarget{Socket: "/var/run/mysqld/mysqld.sock"}, replicaOut)
		base := `'mysql' --socket='/var/run/mysqld/mysqld.sock'`
		want := fmt.Sprintf(`%s -e 'SHOW REPLICA STATUS\G' 2>/dev/null || %s -e 'SHOW SLAVE STATUS\G'`, base, base)
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("with DefaultsFile and Socket", func(t *testing.T) {
		fx, _ := runMySQLCheck(t, &storage.DBTarget{DefaultsFile: "/etc/my.cnf", Socket: "/tmp/m.sock"}, replicaOut)
		base := `'mysql' --defaults-file='/etc/my.cnf' --socket='/tmp/m.sock'`
		want := fmt.Sprintf(`%s -e 'SHOW REPLICA STATUS\G' 2>/dev/null || %s -e 'SHOW SLAVE STATUS\G'`, base, base)
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("heartbeat off on a replica → no heartbeat command", func(t *testing.T) {
		fx, _ := runMySQLCheck(t, &storage.DBTarget{
			HeartbeatEnabled: false,
			HeartbeatQuery:   "SELECT lag FROM heartbeat",
		}, replicaOut)
		require.Equal(t, 1, fx.CallCount(checkerHost))
		require.Contains(t, fx.LastCmd(checkerHost), "SHOW REPLICA STATUS")
	})

	t.Run("heartbeat enabled but empty query → no heartbeat command", func(t *testing.T) {
		fx, _ := runMySQLCheck(t, &storage.DBTarget{
			HeartbeatEnabled: true,
			HeartbeatQuery:   "   ",
		}, replicaOut)
		require.Equal(t, 1, fx.CallCount(checkerHost))
	})

	t.Run("heartbeat on a non-replica → not issued", func(t *testing.T) {
		// Empty status output ⇒ not a replica ⇒ heartbeat branch is skipped.
		fx, res := runMySQLCheck(t, &storage.DBTarget{
			HeartbeatEnabled: true,
			HeartbeatQuery:   "SELECT 1",
		}, "")
		require.False(t, res.IsReplica)
		require.Equal(t, 1, fx.CallCount(checkerHost))
	})

	t.Run("heartbeat on a replica → second command is the heartbeat", func(t *testing.T) {
		const hbQuery = "SELECT UNIX_TIMESTAMP() - ts FROM heartbeat"
		fx := sshpkg.NewFakeExecutor()
		fx.SetResponse(checkerHost, replicaOut, nil)
		c := &Checker{Exec: fx}
		res := c.checkMySQL(context.Background(), sshpkg.Target{Host: checkerHost}, &storage.DBTarget{
			HeartbeatEnabled: true,
			HeartbeatQuery:   hbQuery,
		})
		// Fake returns the SAME canned output for every call to the host,
		// which parses to a single float; parseSingleFloat picks the last
		// numeric field, so a heartbeat value is set.
		require.True(t, res.IsReplica)
		require.Equal(t, 2, fx.CallCount(checkerHost), "status + heartbeat")
		wantHB := `'mysql' -N -B -e '` + hbQuery + `' 2>/dev/null`
		require.Equal(t, wantHB, fx.LastCmd(checkerHost), "last command is the heartbeat query")
	})

	t.Run("unreachable: exec error becomes an unreachable Result", func(t *testing.T) {
		fx := sshpkg.NewFakeExecutor()
		fx.SetResponse(checkerHost, "", fmt.Errorf("ssh: connect failed"))
		c := &Checker{Exec: fx}
		res := c.checkMySQL(context.Background(), sshpkg.Target{Host: checkerHost}, &storage.DBTarget{})
		require.False(t, res.Reachable)
		require.Contains(t, res.LastError, "connect failed")
	})
}

// TestCheckMySQL_ShellEscapesSingleQuote locks the injection boundary: a
// single quote anywhere in DefaultsFile / Socket / HeartbeatQuery must be
// rendered as the '\” escape so it can't break out of the quoting.
func TestCheckMySQL_ShellEscapesSingleQuote(t *testing.T) {
	const replicaOut = mariadbSlaveStatus

	t.Run("DefaultsFile with single quote", func(t *testing.T) {
		fx, _ := runMySQLCheck(t, &storage.DBTarget{DefaultsFile: `/etc/o'brien.cnf`}, replicaOut)
		cmd := fx.LastCmd(checkerHost)
		require.Contains(t, cmd, `--defaults-file='/etc/o'\''brien.cnf'`)
		// The raw, un-escaped quote sequence must NOT appear.
		require.NotContains(t, cmd, `o'brien`)
	})

	t.Run("Socket with single quote", func(t *testing.T) {
		fx, _ := runMySQLCheck(t, &storage.DBTarget{Socket: `/tmp/a'b.sock`}, replicaOut)
		require.Contains(t, fx.LastCmd(checkerHost), `--socket='/tmp/a'\''b.sock'`)
	})

	t.Run("HeartbeatQuery with single quote", func(t *testing.T) {
		const q = `SELECT 'x'`
		fx := sshpkg.NewFakeExecutor()
		fx.SetResponse(checkerHost, replicaOut, nil)
		c := &Checker{Exec: fx}
		c.checkMySQL(context.Background(), sshpkg.Target{Host: checkerHost}, &storage.DBTarget{
			HeartbeatEnabled: true,
			HeartbeatQuery:   q,
		})
		// 'SELECT 'x'' → 'SELECT '\''x'\'''
		require.Contains(t, fx.LastCmd(checkerHost), `-e 'SELECT '\''x'\''' 2>/dev/null`)
	})
}

func TestCheckPostgres_CommandConstruction(t *testing.T) {
	// Output that parses as a streaming standby so the heartbeat branch is
	// reachable.
	const standbyOut = "1|streaming|3\n"

	const pgStatusQuery = `SELECT pg_is_in_recovery()::int, ` +
		`COALESCE((SELECT status FROM pg_stat_wal_receiver LIMIT 1),'none'), ` +
		`CASE WHEN pg_last_wal_receive_lsn() IS DISTINCT FROM pg_last_wal_replay_lsn() ` +
		`THEN COALESCE(EXTRACT(EPOCH FROM (now()-pg_last_xact_replay_timestamp()))::bigint,-1) ELSE 0 END`

	// shellQuote of the status SQL, which itself contains single quotes
	// ('none') → exercises the escape inside the constructed command.
	quotedQuery := shellQuote(pgStatusQuery)

	t.Run("plain target", func(t *testing.T) {
		fx, res := runPostgresCheck(t, &storage.DBTarget{Engine: "postgres"}, standbyOut)
		require.True(t, res.Reachable)
		require.True(t, res.IsReplica)
		require.Equal(t, 1, fx.CallCount(checkerHost))
		want := `'psql' -tA -F'|' -c ` + quotedQuery + ` 2>/dev/null`
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("custom client command", func(t *testing.T) {
		fx, _ := runPostgresCheck(t, &storage.DBTarget{ClientCommand: "/usr/pgsql/bin/psql"}, standbyOut)
		want := `'/usr/pgsql/bin/psql' -tA -F'|' -c ` + quotedQuery + ` 2>/dev/null`
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("with DefaultsFile (PGPASSFILE)", func(t *testing.T) {
		fx, _ := runPostgresCheck(t, &storage.DBTarget{DefaultsFile: "/home/pg/.pgpass"}, standbyOut)
		want := `PGPASSFILE='/home/pg/.pgpass' 'psql' -tA -F'|' -c ` + quotedQuery + ` 2>/dev/null`
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("with Socket (-h)", func(t *testing.T) {
		fx, _ := runPostgresCheck(t, &storage.DBTarget{Socket: "/var/run/postgresql"}, standbyOut)
		want := `'psql' -h '/var/run/postgresql' -tA -F'|' -c ` + quotedQuery + ` 2>/dev/null`
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("with DefaultsFile and Socket", func(t *testing.T) {
		fx, _ := runPostgresCheck(t, &storage.DBTarget{
			DefaultsFile: "/home/pg/.pgpass",
			Socket:       "/var/run/postgresql",
		}, standbyOut)
		want := `PGPASSFILE='/home/pg/.pgpass' 'psql' -h '/var/run/postgresql' -tA -F'|' -c ` + quotedQuery + ` 2>/dev/null`
		require.Equal(t, want, fx.LastCmd(checkerHost))
	})

	t.Run("heartbeat on a standby → second command is the heartbeat", func(t *testing.T) {
		const hbQuery = "SELECT extract(epoch FROM now()-ts) FROM hb"
		fx := sshpkg.NewFakeExecutor()
		fx.SetResponse(checkerHost, standbyOut, nil)
		c := &Checker{Exec: fx}
		res := c.checkPostgres(context.Background(), sshpkg.Target{Host: checkerHost}, &storage.DBTarget{
			HeartbeatEnabled: true,
			HeartbeatQuery:   hbQuery,
		})
		require.True(t, res.IsReplica)
		require.Equal(t, 2, fx.CallCount(checkerHost))
		wantHB := `'psql' -tA -c ` + shellQuote(hbQuery) + ` 2>/dev/null`
		require.Equal(t, wantHB, fx.LastCmd(checkerHost))
	})

	t.Run("heartbeat on a primary → not issued", func(t *testing.T) {
		fx, res := runPostgresCheck(t, &storage.DBTarget{
			HeartbeatEnabled: true,
			HeartbeatQuery:   "SELECT 1",
		}, "0|none|-1")
		require.False(t, res.IsReplica)
		require.Equal(t, 1, fx.CallCount(checkerHost))
	})

	t.Run("DefaultsFile with single quote is escaped", func(t *testing.T) {
		fx, _ := runPostgresCheck(t, &storage.DBTarget{DefaultsFile: `/home/o'brien/.pgpass`}, standbyOut)
		require.Contains(t, fx.LastCmd(checkerHost), `PGPASSFILE='/home/o'\''brien/.pgpass'`)
	})
}

// ---------------------------------------------------------------------------
// (2) buildMetrics golden + escapeTag
// ---------------------------------------------------------------------------

func TestBuildMetrics_Golden(t *testing.T) {
	ts := time.Unix(0, 1_700_000_000_000_000_000).UTC() // fixed nanosecond instant
	tsNanos := ts.UnixNano()
	lag := int64(7)
	hb := 1.5

	res := Result{
		Reachable:           true,
		IsReplica:           true,
		IORunning:           true,
		SQLRunning:          true,
		LagSeconds:          &lag,
		HeartbeatLagSeconds: &hb,
	}
	got := buildMetrics("erp_db", "prod-replica-1", res, "healthy", ts)

	want := strings.Join([]string{
		fmt.Sprintf("frappe_db_up,db=erp_db,server=prod-replica-1 value=1 %d", tsNanos),
		fmt.Sprintf("frappe_db_replication_io_running,db=erp_db,server=prod-replica-1 value=1 %d", tsNanos),
		fmt.Sprintf("frappe_db_replication_sql_running,db=erp_db,server=prod-replica-1 value=1 %d", tsNanos),
		fmt.Sprintf("frappe_db_replication_healthy,db=erp_db,server=prod-replica-1 value=1 %d", tsNanos),
		fmt.Sprintf("frappe_db_replication_lagging,db=erp_db,server=prod-replica-1 value=0 %d", tsNanos),
		fmt.Sprintf("frappe_db_replication_lag_seconds,db=erp_db,server=prod-replica-1 value=7 %d", tsNanos),
		fmt.Sprintf("frappe_db_heartbeat_lag_seconds,db=erp_db,server=prod-replica-1 value=1.5 %d", tsNanos),
		"", // trailing newline after the final line
	}, "\n")

	require.Equal(t, want, got)
}

func TestBuildMetrics_NoLagOmitsLagLines(t *testing.T) {
	ts := time.Unix(0, 42).UTC()
	res := Result{Reachable: false} // unreachable, no lag values
	got := buildMetrics("db", "srv", res, "unreachable", ts)

	require.Contains(t, got, "frappe_db_up,db=db,server=srv value=0 42")
	require.Contains(t, got, "frappe_db_replication_healthy,db=db,server=srv value=0 42")
	require.NotContains(t, got, "frappe_db_replication_lag_seconds")
	require.NotContains(t, got, "frappe_db_heartbeat_lag_seconds")
}

func TestEscapeTag(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"plain", "plain"},
		{"a,b", `a\,b`},
		{"k=v", `k\=v`},
		{"a b", `a\ b`},
		{"prod, db=main name", `prod\,\ db\=main\ name`},
		{`back\slash`, `back\\slash`},
	}
	for _, c := range cases {
		require.Equal(t, c.want, escapeTag(c.in), "escapeTag(%q)", c.in)
	}

	// Used inside a real line: a server name with comma/equals/space must not
	// break the tag set.
	ts := time.Unix(0, 100).UTC()
	got := buildMetrics("my,db", "srv x=1", Result{Reachable: true}, "healthy", ts)
	require.Contains(t, got, `db=my\,db,server=srv\ x\=1 value=1 100`)
}

// ---------------------------------------------------------------------------
// (3) Service.checkAll end-to-end wiring
// ---------------------------------------------------------------------------

func newMemStore(t *testing.T) storage.Store {
	t.Helper()
	// Unique DSN name so concurrent test binaries / packages don't share the
	// cache=shared in-memory database.
	dsn := fmt.Sprintf("file:dbmon_%d?mode=memory&cache=shared&_pragma=foreign_keys(1)", time.Now().UnixNano())
	s, err := storage.OpenEntStore(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestServiceCheckAll_PersistsAndPushes(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)

	srv, err := store.CreateServer(ctx, storage.NewServer{
		Name:       "prod-replica-1",
		Hostname:   checkerHost,
		SSHUser:    "monitor",
		SSHPort:    22,
		SSHKeyPath: "/keys/id",
	})
	require.NoError(t, err)

	tgt, err := store.CreateDBTarget(ctx, storage.NewDBTarget{
		ServerID:            srv.ID,
		Name:                "erp_db",
		Enabled:             true,
		Engine:              "mysql",
		LagThresholdSeconds: 30,
	})
	require.NoError(t, err)

	// A disabled target must be skipped entirely.
	_, err = store.CreateDBTarget(ctx, storage.NewDBTarget{
		ServerID: srv.ID,
		Name:     "disabled_db",
		Enabled:  false,
		Engine:   "mysql",
	})
	require.NoError(t, err)

	fx := sshpkg.NewFakeExecutor()
	fx.SetResponse(checkerHost, mariadbSlaveStatus, nil) // healthy replica, lag=12

	hub := &captureHub{}
	pusher := &capturePusher{}

	svc := New(store, fx, pusher, hub, nil, Config{})
	svc.checkAll(ctx)

	// --- persisted status ---
	got, err := store.GetDBTarget(ctx, tgt.ID)
	require.NoError(t, err)
	require.Equal(t, "healthy", got.Status, "IO+SQL Yes, lag 12 < threshold 30")
	require.True(t, got.IORunning)
	require.True(t, got.SQLRunning)
	require.NotNil(t, got.LagSeconds)
	require.EqualValues(t, 12, *got.LagSeconds)
	require.NotNil(t, got.LastCheckedAt)

	// Only the enabled target was checked over SSH.
	require.Equal(t, 1, fx.CallCount(checkerHost))
	require.Equal(t,
		`'mysql' -e 'SHOW REPLICA STATUS\G' 2>/dev/null || 'mysql' -e 'SHOW SLAVE STATUS\G'`,
		fx.LastCmd(checkerHost))

	// --- pushed line-protocol body ---
	require.Len(t, pusher.bodies, 1, "exactly one VM push for the sweep")
	body := pusher.bodies[0]
	require.Contains(t, body, "frappe_db_up,db=erp_db,server=prod-replica-1 value=1 ")
	require.Contains(t, body, "frappe_db_replication_healthy,db=erp_db,server=prod-replica-1 value=1 ")
	require.Contains(t, body, "frappe_db_replication_lagging,db=erp_db,server=prod-replica-1 value=0 ")
	require.Contains(t, body, "frappe_db_replication_lag_seconds,db=erp_db,server=prod-replica-1 value=12 ")
	// The disabled target must NOT appear in the pushed metrics.
	require.NotContains(t, body, "disabled_db")

	// --- broadcast ---
	require.NotEmpty(t, hub.events)
	var dbEvent *realtime.Event
	for i := range hub.events {
		if hub.events[i].Type == realtime.TypeDBStatus {
			dbEvent = &hub.events[i]
			break
		}
	}
	require.NotNil(t, dbEvent, "a db.status event was broadcast")
	require.Equal(t, realtime.TopicDatabases(), dbEvent.Topic)
	data, ok := dbEvent.Data.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "healthy", data["status"])
	require.Equal(t, "erp_db", data["name"])
}

func TestServiceCheckAll_LaggingPersistsAndFlags(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)

	srv, err := store.CreateServer(ctx, storage.NewServer{
		Name: "rep", Hostname: checkerHost, SSHUser: "u", SSHPort: 22, SSHKeyPath: "/k",
	})
	require.NoError(t, err)
	tgt, err := store.CreateDBTarget(ctx, storage.NewDBTarget{
		ServerID: srv.ID, Name: "erp", Enabled: true, Engine: "mysql",
		LagThresholdSeconds: 5, // 12s lag from mariadbSlaveStatus exceeds this
	})
	require.NoError(t, err)

	fx := sshpkg.NewFakeExecutor()
	fx.SetResponse(checkerHost, mariadbSlaveStatus, nil)
	pusher := &capturePusher{}

	svc := New(store, fx, pusher, nil, nil, Config{})
	svc.checkAll(ctx)

	got, err := store.GetDBTarget(ctx, tgt.ID)
	require.NoError(t, err)
	require.Equal(t, "lagging", got.Status)

	require.Len(t, pusher.bodies, 1)
	require.Contains(t, pusher.bodies[0], "frappe_db_replication_lagging,db=erp,server=rep value=1 ")
	require.Contains(t, pusher.bodies[0], "frappe_db_replication_healthy,db=erp,server=rep value=0 ")
}

func TestServiceCheckAll_UnreachablePersists(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)

	srv, err := store.CreateServer(ctx, storage.NewServer{
		Name: "rep", Hostname: checkerHost, SSHUser: "u", SSHPort: 22, SSHKeyPath: "/k",
	})
	require.NoError(t, err)
	tgt, err := store.CreateDBTarget(ctx, storage.NewDBTarget{
		ServerID: srv.ID, Name: "erp", Enabled: true, Engine: "mysql", LagThresholdSeconds: 30,
	})
	require.NoError(t, err)

	fx := sshpkg.NewFakeExecutor()
	fx.SetResponse(checkerHost, "", fmt.Errorf("ssh: handshake failed"))
	pusher := &capturePusher{}

	svc := New(store, fx, pusher, nil, nil, Config{})
	svc.checkAll(ctx)

	got, err := store.GetDBTarget(ctx, tgt.ID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", got.Status)
	require.Contains(t, got.LastError, "handshake failed")

	require.Len(t, pusher.bodies, 1)
	require.Contains(t, pusher.bodies[0], "frappe_db_up,db=erp,server=rep value=0 ")
}

func TestServiceCheckOnce_ReturnsPersistedTarget(t *testing.T) {
	ctx := context.Background()
	store := newMemStore(t)

	srv, err := store.CreateServer(ctx, storage.NewServer{
		Name: "rep", Hostname: checkerHost, SSHUser: "u", SSHPort: 22, SSHKeyPath: "/k",
	})
	require.NoError(t, err)
	tgt, err := store.CreateDBTarget(ctx, storage.NewDBTarget{
		ServerID: srv.ID, Name: "erp", Enabled: true, Engine: "mysql", LagThresholdSeconds: 30,
	})
	require.NoError(t, err)

	fx := sshpkg.NewFakeExecutor()
	fx.SetResponse(checkerHost, mariadbSlaveStatus, nil)

	// vm=nil and hub=nil must be tolerated by CheckOnce (no push, no broadcast).
	svc := New(store, fx, nil, nil, nil, Config{})
	out, err := svc.CheckOnce(ctx, tgt.ID)
	require.NoError(t, err)
	require.Equal(t, "healthy", out.Status)
	require.Equal(t, 1, fx.CallCount(checkerHost))
}
