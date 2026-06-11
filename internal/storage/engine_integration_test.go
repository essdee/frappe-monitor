package storage

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// These integration tests run against a REAL MariaDB / PostgreSQL when the
// corresponding DSN env var is set, otherwise they skip. They prove the ent
// auto-migration, JSON/enum fields, control-action lifecycle, and cross-table
// cascade delete all work on the production engines — not just SQLite.
//
//	MONITOR_TEST_MYSQL_DSN='monitor:monitor@tcp(127.0.0.1:3306)/frappe_monitor?parseTime=true'
//	MONITOR_TEST_PG_DSN='postgres://monitor:monitor@127.0.0.1:5432/frappe_monitor?sslmode=disable'

func TestEngine_MySQL(t *testing.T) {
	dsn := os.Getenv("MONITOR_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set MONITOR_TEST_MYSQL_DSN to run the MariaDB/MySQL integration test")
	}
	runEngineSmoke(t, "mysql", dsn)
}

func TestEngine_Postgres(t *testing.T) {
	dsn := os.Getenv("MONITOR_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("set MONITOR_TEST_PG_DSN to run the PostgreSQL integration test")
	}
	runEngineSmoke(t, "postgres", dsn)
}

func runEngineSmoke(t *testing.T, driver, dsn string) {
	t.Helper()
	ctx := context.Background()

	s, err := Open(ctx, driver, dsn)
	require.NoError(t, err, "Open + auto-migrate must succeed on %s", driver)
	t.Cleanup(func() { _ = s.Close() })

	require.NoError(t, s.RunMigrations(ctx, nil), "patches/seeds on %s", driver)
	// Re-run to prove idempotency on the real engine.
	require.NoError(t, s.RunMigrations(ctx, nil))

	hostname := "it-" + driver + ".example.com"
	// Clean any leftover from a prior failed run (hostname is unique).
	if existing, _ := s.ListServers(ctx); existing != nil {
		for _, e := range existing {
			if e.Hostname == hostname {
				_ = s.DeleteServer(ctx, e.ID)
			}
		}
	}

	// JSON (labels, bench_paths) + scalar round-trip.
	srv, err := s.CreateServer(ctx, NewServer{
		Name: "it", Hostname: hostname, SSHUser: "m", SSHPort: 22, SSHKeyPath: "/k",
		BenchPaths: []string{"/home/frappe/frappe-bench"},
		Labels:     map[string]string{"env": "prod"},
	})
	require.NoError(t, err)

	got, err := s.GetServer(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, "it", got.Name)
	require.Equal(t, "prod", got.Labels["env"])
	require.Equal(t, []string{"/home/frappe/frappe-bench"}, got.BenchPaths)
	require.Equal(t, "unknown", got.Status, "enum default")

	// Control-action lifecycle (enum + text + timestamps).
	act, err := s.CreateControlAction(ctx, NewControlAction{
		ServerID: srv.ID, Action: "bench.migrate", Command: "cd x && bench migrate",
	})
	require.NoError(t, err)
	require.Equal(t, "pending", act.Status)
	fin, err := s.FinishControlAction(ctx, act.ID, ControlActionResult{
		Status: "success", ExitOK: true, Output: "migrated", DurationMs: 42,
	})
	require.NoError(t, err)
	require.Equal(t, "success", fin.Status)
	require.NotNil(t, fin.FinishedAt)

	// Cross-table cascade delete (FK enforced on the real engine).
	require.NoError(t, s.DeleteServer(ctx, srv.ID))
	_, err = s.GetServer(ctx, srv.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetControlAction(ctx, act.ID)
	require.ErrorIs(t, err, ErrNotFound, "control action must cascade-delete with its server on %s", driver)
}
