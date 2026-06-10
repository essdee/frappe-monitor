package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDBTarget_CRUDStatusAndServerMove(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	srv, err := s.CreateServer(ctx, NewServer{
		Name: "db-host", Hostname: "db.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	tgt, err := s.CreateDBTarget(ctx, NewDBTarget{
		ServerID: srv.ID, Name: "replica-1", Enabled: true, LagThresholdSeconds: 45,
	})
	require.NoError(t, err)
	require.Equal(t, srv.ID, tgt.ServerID)
	require.Equal(t, 45, tgt.LagThresholdSeconds)
	require.Equal(t, "mysql", tgt.MySQLCommand, "default applied")
	require.Equal(t, "unknown", tgt.Status)

	list, err := s.ListDBTargets(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)

	// Move it to a second server (guards the patch-server fix).
	srv2, err := s.CreateServer(ctx, NewServer{
		Name: "db-host-2", Hostname: "db2.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	newName := "replica-renamed"
	upd, err := s.UpdateDBTarget(ctx, tgt.ID, UpdateDBTarget{ServerID: &srv2.ID, Name: &newName})
	require.NoError(t, err)
	require.Equal(t, srv2.ID, upd.ServerID, "server change must persist")
	require.Equal(t, "replica-renamed", upd.Name)

	// Status write.
	lag := int64(7)
	require.NoError(t, s.SetDBTargetStatus(ctx, tgt.ID, DBTargetStatus{
		Status: "healthy", IORunning: true, SQLRunning: true, LagSeconds: &lag,
	}))
	got, err := s.GetDBTarget(ctx, tgt.ID)
	require.NoError(t, err)
	require.Equal(t, "healthy", got.Status)
	require.NotNil(t, got.LagSeconds)
	require.EqualValues(t, 7, *got.LagSeconds)
	require.NotNil(t, got.LastCheckedAt)

	// Cascade: deleting the (current) server removes its DB target too.
	require.NoError(t, s.DeleteServer(ctx, srv2.ID))
	_, err = s.GetDBTarget(ctx, tgt.ID)
	require.ErrorIs(t, err, ErrNotFound, "db target should cascade-delete with its server")
}

func TestDBTarget_DeleteNotFound(t *testing.T) {
	s := newTestStore(t)
	require.ErrorIs(t, s.DeleteDBTarget(context.Background(), 9999), ErrNotFound)
}
