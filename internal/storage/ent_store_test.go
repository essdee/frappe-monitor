package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAlertIncidentHistoryRetainsResolvedEpisodes(t *testing.T) {
	store := newTestStore(t)
	incidents, ok := store.(AlertIncidentStore)
	require.True(t, ok)
	ctx := context.Background()
	started := time.Date(2026, 8, 13, 8, 0, 0, 0, time.UTC)

	first, err := incidents.UpsertOpenAlertIncident(ctx, AlertIncident{
		RuleName: "rq_workers_saturated", Fingerprint: "abc", Severity: "critical",
		Labels: map[string]string{"server": "prod", "bench": "bench-1"},
		Value: 17, StartedAt: started,
	})
	require.NoError(t, err)
	require.Equal(t, "firing", first.Status)

	resolved := started.Add(12 * time.Minute)
	require.NoError(t, incidents.ResolveOpenAlertIncident(ctx, first.RuleName, first.Fingerprint, first.Value, resolved))
	second, err := incidents.UpsertOpenAlertIncident(ctx, AlertIncident{
		RuleName: first.RuleName, Fingerprint: first.Fingerprint, Severity: "critical",
		Labels: first.Labels, Value: 17, StartedAt: resolved.Add(time.Hour),
	})
	require.NoError(t, err)
	require.NotEqual(t, first.ID, second.ID, "a recurrence is a new incident episode")

	history, err := incidents.ListAlertIncidents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, second.ID, history[0].ID)
	require.Equal(t, "resolved", history[1].Status)
	require.Equal(t, &resolved, history[1].ResolvedAt)
}

func TestOpenEntStore_RepairsLegacyDuplicateServerNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", "file:"+path)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE servers (
        id integer PRIMARY KEY AUTOINCREMENT,
        name text NOT NULL,
        hostname text NOT NULL UNIQUE,
        ssh_user text NOT NULL DEFAULT 'monitor',
        ssh_port integer NOT NULL DEFAULT 22,
        ssh_key_path text NOT NULL,
        bench_paths json NULL,
        labels json NULL,
        status text NOT NULL DEFAULT 'unknown',
        last_pinged_at datetime NULL,
        last_error text NULL,
        created_at datetime NOT NULL,
        updated_at datetime NOT NULL
    )`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO servers
        (name,hostname,ssh_user,ssh_port,ssh_key_path,status,created_at,updated_at)
        VALUES ('prod','one.example.com','monitor',22,'/tmp/k','unknown',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP),
               ('prod','two.example.com','monitor',22,'/tmp/k','unknown',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	store, err := OpenEntStore(context.Background(), "file:"+path+"?_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	servers, err := store.ListServers(context.Background())
	require.NoError(t, err)
	require.Len(t, servers, 2)
	require.Equal(t, "prod", servers[0].Name)
	require.Equal(t, "prod-server-2", servers[1].Name)
}

func newTestStore(t *testing.T) Store {
	t.Helper()
	// in-memory sqlite, shared across connections in this process
	s, err := OpenEntStore(context.Background(), "file:ent?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCreateAndGetServer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := NewServer{
		Name:       "prod-1",
		Hostname:   "prod1.example.com",
		SSHUser:    "monitor",
		SSHPort:    22,
		SSHKeyPath: "/etc/monitor/ssh-keys/prod.key",
		Labels:     map[string]string{"env": "prod"},
	}
	created, err := s.CreateServer(ctx, in)
	require.NoError(t, err)
	require.NotZero(t, created.ID)
	require.Equal(t, "prod-1", created.Name)
	require.Equal(t, "unknown", created.Status)

	got, err := s.GetServer(ctx, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.Hostname, got.Hostname)
	require.Equal(t, "prod", got.Labels["env"])
}

func TestCreateServer_DuplicateHostname(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	in := NewServer{
		Name: "a", Hostname: "dup.example.com", SSHUser: "monitor",
		SSHPort: 22, SSHKeyPath: "/tmp/k",
	}
	_, err := s.CreateServer(ctx, in)
	require.NoError(t, err)
	_, err = s.CreateServer(ctx, in)
	require.ErrorIs(t, err, ErrDuplicateHostname)
}

func TestCreateAndUpdateServer_DuplicateCanonicalName(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first, err := s.CreateServer(ctx, NewServer{
		Name: "canonical", Hostname: "one.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	second, err := s.CreateServer(ctx, NewServer{
		Name: "other", Hostname: "two.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	_, err = s.CreateServer(ctx, NewServer{
		Name: first.Name, Hostname: "three.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.ErrorIs(t, err, ErrDuplicateName)

	name := first.Name
	_, err = s.UpdateServer(ctx, second.ID, UpdateServer{Name: &name})
	require.ErrorIs(t, err, ErrDuplicateName)
}

func TestCreateServer_LabelsNormalizedToEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	created, err := s.CreateServer(ctx, NewServer{
		Name: "x", Hostname: "x.example.com", SSHUser: "monitor",
		SSHPort: 22, SSHKeyPath: "/tmp/k",
		// no Labels set
	})
	require.NoError(t, err)
	require.NotNil(t, created.Labels, "Labels must be non-nil so callers can read/write without checks")
	require.Empty(t, created.Labels)
}

func TestListServers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	_, err := s.CreateServer(ctx, NewServer{
		Name: "a", Hostname: "a.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	_, err = s.CreateServer(ctx, NewServer{
		Name: "b", Hostname: "b.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	list, err := s.ListServers(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
}

func TestGetServer_NotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetServer(context.Background(), 99999)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestDeleteServer_CascadesOwnedState(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	srv, err := s.CreateServer(ctx, NewServer{
		Name: "delete-with-state", Hostname: "delete-with-state.example.com",
		SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	require.NoError(t, s.UpsertLogCursor(ctx, LogCursor{
		ServerID: srv.ID, LogPath: "/logs/web.log",
		FileIdentity: "2049-1", ByteOffset: 42,
	}))
	require.NoError(t, s.UpsertSystemSnapshot(ctx, SystemSnapshot{
		ServerID: srv.ID, Payload: []byte(`{"cpu":"ok"}`),
	}))

	require.NoError(t, s.DeleteServer(ctx, srv.ID))
	_, err = s.GetServer(ctx, srv.ID)
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetLogCursor(ctx, srv.ID, "/logs/web.log")
	require.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetSystemSnapshot(ctx, srv.ID)
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLogCursor_GetReturnsNotFoundForMissing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	srv, err := s.CreateServer(ctx, NewServer{
		Name: "lc-host", Hostname: "lc-host.example.com", SSHUser: "monitor",
		SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	_, err = s.GetLogCursor(ctx, srv.ID, "/var/log/frappe.log")
	require.ErrorIs(t, err, ErrNotFound)
}

func TestLogCursor_UpsertAndGet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	srv, err := s.CreateServer(ctx, NewServer{
		Name: "lc-host-2", Hostname: "lc-host-2.example.com", SSHUser: "monitor",
		SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	require.NoError(t, s.UpsertLogCursor(ctx, LogCursor{
		ServerID:     srv.ID,
		LogPath:      "/var/log/frappe/web.error.log",
		FileIdentity: "2049-100",
		ByteOffset:   1024,
	}))

	got, err := s.GetLogCursor(ctx, srv.ID, "/var/log/frappe/web.error.log")
	require.NoError(t, err)
	require.Equal(t, srv.ID, got.ServerID)
	require.Equal(t, "/var/log/frappe/web.error.log", got.LogPath)
	require.Equal(t, "2049-100", got.FileIdentity)
	require.Equal(t, int64(1024), got.ByteOffset)
	require.False(t, got.LastSeenAt.IsZero())

	// Upsert update — same key, new offset.
	require.NoError(t, s.UpsertLogCursor(ctx, LogCursor{
		ServerID:     srv.ID,
		LogPath:      "/var/log/frappe/web.error.log",
		FileIdentity: "2049-200",
		ByteOffset:   2048,
	}))
	got2, err := s.GetLogCursor(ctx, srv.ID, "/var/log/frappe/web.error.log")
	require.NoError(t, err)
	require.Equal(t, "2049-200", got2.FileIdentity)
	require.Equal(t, int64(2048), got2.ByteOffset)
}

func TestLogCursor_PerServerSeparation(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.CreateServer(ctx, NewServer{
		Name: "a", Hostname: "a.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	b, err := s.CreateServer(ctx, NewServer{
		Name: "b", Hostname: "b.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	require.NoError(t, s.UpsertLogCursor(ctx, LogCursor{ServerID: a.ID, LogPath: "/log", ByteOffset: 1}))
	require.NoError(t, s.UpsertLogCursor(ctx, LogCursor{ServerID: b.ID, LogPath: "/log", ByteOffset: 2}))

	gotA, err := s.GetLogCursor(ctx, a.ID, "/log")
	require.NoError(t, err)
	require.Equal(t, int64(1), gotA.ByteOffset)

	gotB, err := s.GetLogCursor(ctx, b.ID, "/log")
	require.NoError(t, err)
	require.Equal(t, int64(2), gotB.ByteOffset)
}

func TestSetServerStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	srv, err := s.CreateServer(ctx, NewServer{
		Name: "x", Hostname: "x.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	err = s.SetServerStatus(ctx, srv.ID, "reachable", "")
	require.NoError(t, err)

	got, err := s.GetServer(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, "reachable", got.Status)
	require.NotNil(t, got.LastPingedAt)
}

func TestUpsertSystemSnapshot_ErrorOnlyPreservesPayload(t *testing.T) {
	// Failure path of refreshSystem passes only LastError. Storage must
	// keep the previously-captured payload — otherwise a transient SSH
	// blip would wipe the dashboard's "System details" card and leave
	// the operator looking at "Last capture failed" with nothing else
	// useful, even though they had a fresh inventory minutes ago.
	s := newTestStore(t)
	ctx := context.Background()
	srv, err := s.CreateServer(ctx, NewServer{
		Name: "snap", Hostname: "snap.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	good := []byte(`{"hello":"world"}`)
	require.NoError(t, s.UpsertSystemSnapshot(ctx, SystemSnapshot{
		ServerID: srv.ID,
		Payload:  good,
	}))

	// Simulate a refresh failure — empty payload, only LastError.
	require.NoError(t, s.UpsertSystemSnapshot(ctx, SystemSnapshot{
		ServerID:  srv.ID,
		LastError: "ssh: command timeout",
	}))

	got, err := s.GetSystemSnapshot(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, "ssh: command timeout", got.LastError)
	require.Equal(t, string(good), string(got.Payload),
		"transient failure must not erase last good payload")

	// Subsequent successful refresh replaces payload AND clears the error.
	better := []byte(`{"hello":"again"}`)
	require.NoError(t, s.UpsertSystemSnapshot(ctx, SystemSnapshot{
		ServerID: srv.ID,
		Payload:  better,
	}))
	got, err = s.GetSystemSnapshot(ctx, srv.ID)
	require.NoError(t, err)
	require.Empty(t, got.LastError)
	require.Equal(t, string(better), string(got.Payload))
}

func TestUpsertSystemSnapshot_FirstCaptureFailureIsPersisted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	srv, err := s.CreateServer(ctx, NewServer{
		Name: "snap-failed", Hostname: "snap.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	require.NoError(t, s.UpsertSystemSnapshot(ctx, SystemSnapshot{
		ServerID:  srv.ID,
		LastError: "ssh: host key mismatch",
	}))

	got, err := s.GetSystemSnapshot(ctx, srv.ID)
	require.NoError(t, err)
	require.Equal(t, "ssh: host key mismatch", got.LastError)
	require.Empty(t, got.Payload)
}
