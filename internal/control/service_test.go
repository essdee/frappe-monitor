package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/realtime"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

// fakeExec records the commands it is asked to run and returns canned output.
type fakeExec struct {
	mu   sync.Mutex
	cmds []string
	out  string
	err  error
}

func (f *fakeExec) Run(_ context.Context, _ sshpkg.Target, cmd string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	return f.out, f.err
}

func (f *fakeExec) RunWithInput(_ context.Context, _ sshpkg.Target, cmd, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, cmd)
	return f.out, f.err
}

func (f *fakeExec) Stream(context.Context, sshpkg.Target, string) (sshpkg.StreamHandle, error) {
	return nil, nil
}

func (f *fakeExec) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

type fakeHub struct {
	mu     sync.Mutex
	events []realtime.Event
}

func (h *fakeHub) Broadcast(ev realtime.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
}

func (h *fakeHub) HasSubscribers(string) bool { return true }

func (h *fakeHub) types() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.events))
	for i, e := range h.events {
		out[i] = e.Type
	}
	return out
}

func newStore(t *testing.T, name string) storage.Store {
	t.Helper()
	s, err := storage.OpenEntStore(context.Background(),
		"file:"+name+"?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func seedServer(t *testing.T, store storage.Store) *storage.Server {
	t.Helper()
	srv, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: "s1", Hostname: "s1.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	return srv
}

func waitFor(t *testing.T, store storage.Store, id int, status string) *storage.ControlAction {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a, err := store.GetControlAction(context.Background(), id)
		require.NoError(t, err)
		if a.Status == status {
			return a
		}
		if a.Status == "failed" && status != "failed" {
			t.Fatalf("action failed unexpectedly: %s", a.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for status %q, last=%q", status, a.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func count(ss []string, v string) int {
	n := 0
	for _, s := range ss {
		if s == v {
			n++
		}
	}
	return n
}

func TestService_RunRecordsAuditAndExecutesSafeCommand(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlrun")
	srv := seedServer(t, store)

	ex := &fakeExec{out: "migrated ok"}
	hub := &fakeHub{}
	svc := New(store, ex, hub, nil)

	act, err := svc.Run(ctx, RunRequest{
		ServerID: srv.ID, ActionKey: "bench.migrate",
		BenchPath: "/home/frappe/frappe-bench", Site: "site1.local",
	})
	require.NoError(t, err)
	require.Equal(t, "pending", act.Status)

	final := waitFor(t, store, act.ID, "success")
	require.True(t, final.ExitOK)
	require.Contains(t, final.Output, "migrated ok")

	cmds := ex.commands()
	require.Len(t, cmds, 1)
	require.Equal(t, "cd '/home/frappe/frappe-bench' && bench --site 'site1.local' migrate", cmds[0])

	types := hub.types()
	require.Contains(t, types, realtime.TypeControlStarted)
	require.GreaterOrEqual(t, count(types, realtime.TypeControlUpdated), 2, "running + terminal updates")
}

func TestService_RunFailurePropagatesToAudit(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlfail")
	srv := seedServer(t, store)

	svc := New(store, &fakeExec{err: context.DeadlineExceeded}, &fakeHub{}, nil)
	act, err := svc.Run(ctx, RunRequest{ServerID: srv.ID, ActionKey: "supervisor.status"})
	require.NoError(t, err)

	final := waitFor(t, store, act.ID, "failed")
	require.False(t, final.ExitOK)
	require.NotEmpty(t, final.Error)
}

func TestService_RunRejectsUnknownInjectionAndMissingServer(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlrej")
	srv := seedServer(t, store)
	svc := New(store, &fakeExec{}, &fakeHub{}, nil)

	_, err := svc.Run(ctx, RunRequest{ServerID: srv.ID, ActionKey: "totally.bogus"})
	require.ErrorIs(t, err, ErrUnknownAction)

	_, err = svc.Run(ctx, RunRequest{
		ServerID: srv.ID, ActionKey: "bench.migrate",
		BenchPath: "/home/frappe/frappe-bench", Site: "evil; rm -rf /",
	})
	require.ErrorIs(t, err, ErrInvalidParams)

	_, err = svc.Run(ctx, RunRequest{ServerID: 99999, ActionKey: "supervisor.status"})
	require.ErrorIs(t, err, storage.ErrNotFound)
}

func TestService_RunRejectsUnregisteredBench(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlbench")
	srv, err := store.CreateServer(ctx, storage.NewServer{
		Name: "s1", Hostname: "s1.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
		BenchPaths: []string{"/home/frappe/frappe-bench"},
	})
	require.NoError(t, err)
	svc := New(store, &fakeExec{out: "ok"}, &fakeHub{}, nil)

	// A validation-clean path that is NOT one of the server's registered
	// bench paths must be refused (defense-in-depth containment).
	_, err = svc.Run(ctx, RunRequest{
		ServerID: srv.ID, ActionKey: "bench.restart", BenchPath: "/home/frappe/other-bench",
	})
	require.ErrorIs(t, err, ErrInvalidParams)

	// The registered path is accepted and runs.
	act, err := svc.Run(ctx, RunRequest{
		ServerID: srv.ID, ActionKey: "bench.restart", BenchPath: "/home/frappe/frappe-bench",
	})
	require.NoError(t, err)
	final := waitFor(t, store, act.ID, "success")
	require.True(t, final.ExitOK)
}

func TestService_WriteSiteConfigValidatesAndAudits(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlcfg")
	srv := seedServer(t, store)
	svc := New(store, &fakeExec{out: "{}"}, &fakeHub{}, nil)

	_, err := svc.WriteSiteConfig(ctx, WriteConfigRequest{
		ServerID: srv.ID, BenchPath: "/home/frappe/frappe-bench", Site: "s.local", Content: "{not json",
	})
	require.ErrorIs(t, err, ErrInvalidParams, "invalid JSON must be rejected before any SSH")

	act, err := svc.WriteSiteConfig(ctx, WriteConfigRequest{
		ServerID: srv.ID, BenchPath: "/home/frappe/frappe-bench", Site: "s.local",
		Content: `{"maintenance_mode":1}`,
	})
	require.NoError(t, err)
	final := waitFor(t, store, act.ID, "success")
	require.True(t, final.ExitOK)
	require.Equal(t, "site.config-edit", final.Action)
}
