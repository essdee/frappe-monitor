package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControlAction_LifecycleListAndCascade(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	srv, err := s.CreateServer(ctx, NewServer{
		Name: "ctl", Hostname: "ctl.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	act, err := s.CreateControlAction(ctx, NewControlAction{
		ServerID: srv.ID, Action: "bench.migrate",
		BenchPath: "/home/frappe/frappe-bench", Site: "site1.local",
		Command: "cd '/home/frappe/frappe-bench' && bench --site 'site1.local' migrate",
	})
	require.NoError(t, err)
	require.Equal(t, "pending", act.Status)
	require.Equal(t, "operator", act.RequestedBy, "requested_by defaults to operator")
	require.False(t, act.ExitOK)
	require.Nil(t, act.FinishedAt)

	require.NoError(t, s.MarkControlActionRunning(ctx, act.ID))
	got, err := s.GetControlAction(ctx, act.ID)
	require.NoError(t, err)
	require.Equal(t, "running", got.Status)

	final, err := s.FinishControlAction(ctx, act.ID, ControlActionResult{
		Status: "success", ExitOK: true, Output: "migrated", DurationMs: 1234,
	})
	require.NoError(t, err)
	require.Equal(t, "success", final.Status)
	require.True(t, final.ExitOK)
	require.Equal(t, "migrated", final.Output)
	require.Equal(t, 1234, final.DurationMs)
	require.NotNil(t, final.FinishedAt)

	// A failed run with an explicit requester.
	act2, err := s.CreateControlAction(ctx, NewControlAction{
		ServerID: srv.ID, Action: "supervisor.restart", RequestedBy: "alice",
	})
	require.NoError(t, err)
	require.Equal(t, "alice", act2.RequestedBy)
	fin2, err := s.FinishControlAction(ctx, act2.ID, ControlActionResult{Status: "failed", Error: "boom"})
	require.NoError(t, err)
	require.Equal(t, "failed", fin2.Status)
	require.False(t, fin2.ExitOK)
	require.Equal(t, "boom", fin2.Error)

	// History lists newest-first and honours the server filter.
	list, err := s.ListControlActions(ctx, ListControlActions{ServerID: srv.ID})
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, act2.ID, list[0].ID, "newest first (desc by id)")

	// Cascade: deleting the server removes its audit rows too.
	require.NoError(t, s.DeleteServer(ctx, srv.ID))
	_, err = s.GetControlAction(ctx, act.ID)
	require.ErrorIs(t, err, ErrNotFound, "control actions should cascade-delete with the server")
}

func TestControlAction_GetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.GetControlAction(context.Background(), 4242)
	require.ErrorIs(t, err, ErrNotFound)
}
