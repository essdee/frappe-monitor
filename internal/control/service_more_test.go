package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/realtime"
	"frappe-monitor/internal/storage"
)

// terminalControlViews returns the ActionView payloads of every TypeControlUpdated
// event whose status is terminal (success/failed).
func terminalControlViews(t *testing.T, hub *fakeHub) []ActionView {
	t.Helper()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	var out []ActionView
	for _, ev := range hub.events {
		if ev.Type != realtime.TypeControlUpdated {
			continue
		}
		view, ok := ev.Data.(ActionView)
		require.Truef(t, ok, "event Data is %T, want ActionView", ev.Data)
		if view.Status == "success" || view.Status == "failed" {
			out = append(out, view)
		}
	}
	return out
}

// ---- (1) ReadSiteConfig ----------------------------------------------------

func TestService_ReadSiteConfig_HappyPath(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlread")
	srv := seedServer(t, store)

	canned := `{"db_name":"abc","maintenance_mode":0}`
	ex := &fakeExec{out: canned}
	svc := New(store, ex, &fakeHub{}, nil, Config{})

	out, err := svc.ReadSiteConfig(ctx, srv.ID, "/home/frappe/frappe-bench", "site1.local")
	require.NoError(t, err)
	require.Equal(t, canned, out)

	cmds := ex.commands()
	require.Len(t, cmds, 1)
	require.Equal(t, "cat '/home/frappe/frappe-bench/sites/site1.local/site_config.json'", cmds[0])
}

func TestService_ReadSiteConfig_InvalidBench(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlreadbb")
	srv := seedServer(t, store)
	ex := &fakeExec{out: "{}"}
	svc := New(store, ex, &fakeHub{}, nil, Config{})

	_, err := svc.ReadSiteConfig(ctx, srv.ID, "relative/bench", "site1.local")
	require.ErrorIs(t, err, ErrInvalidParams)
	require.Empty(t, ex.commands(), "no SSH must run when the bench path is invalid")
}

func TestService_ReadSiteConfig_InvalidSite(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlreadbs")
	srv := seedServer(t, store)
	ex := &fakeExec{out: "{}"}
	svc := New(store, ex, &fakeHub{}, nil, Config{})

	_, err := svc.ReadSiteConfig(ctx, srv.ID, "/home/frappe/frappe-bench", "evil; rm -rf /")
	require.ErrorIs(t, err, ErrInvalidParams)
	require.Empty(t, ex.commands(), "no SSH must run when the site is invalid")
}

func TestService_ReadSiteConfig_UnregisteredBench(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlreadub")
	srv, err := store.CreateServer(ctx, storage.NewServer{
		Name: "s1", Hostname: "s1.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
		BenchPaths: []string{"/home/frappe/frappe-bench"},
	})
	require.NoError(t, err)
	ex := &fakeExec{out: "{}"}
	svc := New(store, ex, &fakeHub{}, nil, Config{})

	// Validation-clean but not a registered bench path -> refused.
	_, err = svc.ReadSiteConfig(ctx, srv.ID, "/home/frappe/other-bench", "site1.local")
	require.ErrorIs(t, err, ErrInvalidParams)
	require.Empty(t, ex.commands(), "no SSH must run for an unregistered bench")
}

// ---- (2) truncate ----------------------------------------------------------

func TestTruncate_ShorterThanMaxUnchanged(t *testing.T) {
	in := "hello"
	require.Equal(t, in, truncate(in, 100))
}

func TestTruncate_ExactlyMaxUnchanged(t *testing.T) {
	in := "abcdef"
	require.Equal(t, in, truncate(in, len(in)))
}

func TestTruncate_OverMaxAppendsMarker(t *testing.T) {
	in := strings.Repeat("a", 50)
	got := truncate(in, 10)
	require.NotEqual(t, in, got)
	require.Contains(t, got, "(truncated)")
	require.True(t, strings.HasPrefix(got, strings.Repeat("a", 10)))
}

func TestTruncate_MultiByteRuneStraddleStaysValidUTF8(t *testing.T) {
	// "€" is a 3-byte rune; cutting at any byte offset that lands mid-rune
	// must back off to a rune boundary so the result is valid UTF-8.
	in := strings.Repeat("€", 20) // 60 bytes
	for max := 1; max < len(in); max++ {
		got := truncate(in, max)
		require.Truef(t, utf8.ValidString(got), "truncate(in, %d) produced invalid UTF-8", max)
	}
}

// ---- (3) cleanErr ----------------------------------------------------------

func TestCleanErr_CapsAndTrims(t *testing.T) {
	in := "   " + strings.Repeat("x", 3000) + "   "
	got := cleanErr(in)
	// Trimmed: no leading/trailing whitespace.
	require.Equal(t, got, strings.TrimSpace(got))
	// Capped: 2000-char limit + the single appended ellipsis rune.
	require.LessOrEqual(t, len(got), 2000+len("…"))
	require.Less(t, len(got), 3000)
	require.True(t, strings.HasSuffix(got, "…"))
	require.True(t, utf8.ValidString(got))
}

func TestCleanErr_ShortUnchanged(t *testing.T) {
	require.Equal(t, "boom", cleanErr("  boom  "))
}

// ---- (4) maxOutputBytes cap ------------------------------------------------

func TestService_MaxOutputBytesCapsPersistedOutput(t *testing.T) {
	ctx := context.Background()
	store := newStore(t, "ctlcap")
	srv := seedServer(t, store)

	const limit = 16
	long := strings.Repeat("z", 500)
	ex := &fakeExec{out: long}
	svc := New(store, ex, &fakeHub{}, nil, Config{MaxOutputBytes: limit})

	act, err := svc.Run(ctx, RunRequest{ServerID: srv.ID, ActionKey: "supervisor.status"})
	require.NoError(t, err)

	final := waitFor(t, store, act.ID, "success")
	require.Less(t, len(final.Output), len(long), "output must be capped below the raw length")
	require.True(t, strings.HasPrefix(final.Output, strings.Repeat("z", limit)))
	require.Contains(t, final.Output, "(truncated)")
}

// ---- (5) FinishControlAction-failure fallback ------------------------------

// finishErrStore wraps a Store and forces FinishControlAction to fail, so we can
// exercise execAudited's in-memory terminal broadcast fallback. The wrapped
// action is still marked running/created via the real store so GetControlAction
// keeps working for other assertions.
type finishErrStore struct {
	storage.Store
}

func (f finishErrStore) FinishControlAction(context.Context, int, storage.ControlActionResult) (*storage.ControlAction, error) {
	return nil, errors.New("forced finish failure")
}

func TestService_FinishFailureStillBroadcastsTerminalSuccess(t *testing.T) {
	ctx := context.Background()
	base := newStore(t, "ctlfinerr")
	srv := seedServer(t, base)
	store := finishErrStore{Store: base}

	hub := &fakeHub{}
	ex := &fakeExec{out: "ok"}
	svc := New(store, ex, hub, nil, Config{})

	act, err := svc.Run(ctx, RunRequest{ServerID: srv.ID, ActionKey: "supervisor.status"})
	require.NoError(t, err)

	// FinishControlAction always errors, so the row never reaches "success" in
	// the DB. Wait for a terminal broadcast from the in-memory fallback instead.
	var views []ActionView
	require.Eventually(t, func() bool {
		views = terminalControlViews(t, hub)
		return len(views) > 0
	}, 3*time.Second, 10*time.Millisecond, "expected a terminal control.updated broadcast")

	last := views[len(views)-1]
	require.Equal(t, realtime.TypeControlUpdated, lastControlEventType(t, hub))
	require.Equal(t, "success", last.Status)
	require.True(t, last.ExitOK)
	require.Equal(t, act.ID, last.ID)
}

func TestService_FinishFailureStillBroadcastsTerminalFailed(t *testing.T) {
	ctx := context.Background()
	base := newStore(t, "ctlfinerrf")
	srv := seedServer(t, base)
	store := finishErrStore{Store: base}

	hub := &fakeHub{}
	ex := &fakeExec{err: context.DeadlineExceeded}
	svc := New(store, ex, hub, nil, Config{})

	_, err := svc.Run(ctx, RunRequest{ServerID: srv.ID, ActionKey: "supervisor.status"})
	require.NoError(t, err)

	var views []ActionView
	require.Eventually(t, func() bool {
		views = terminalControlViews(t, hub)
		return len(views) > 0
	}, 3*time.Second, 10*time.Millisecond, "expected a terminal control.updated broadcast")

	last := views[len(views)-1]
	require.Equal(t, "failed", last.Status)
	require.False(t, last.ExitOK)
	require.NotEmpty(t, last.Error)
}

func lastControlEventType(t *testing.T, hub *fakeHub) string {
	t.Helper()
	hub.mu.Lock()
	defer hub.mu.Unlock()
	require.NotEmpty(t, hub.events)
	return hub.events[len(hub.events)-1].Type
}
