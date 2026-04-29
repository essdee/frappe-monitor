package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

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
	require.Error(t, err)
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
