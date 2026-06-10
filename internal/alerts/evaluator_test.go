package alerts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/storage"
)

// fakeVM is a static map keyed by expr → samples. Anything not in
// the map errors so a typo in a test produces a loud failure.
type fakeVM struct {
	resp map[string][]Sample
}

func (f *fakeVM) Query(_ context.Context, expr string) ([]Sample, error) {
	s, ok := f.resp[expr]
	if !ok {
		return nil, errors.New("no fake response for: " + expr)
	}
	return s, nil
}

// memStore implements the subset of storage.Store the evaluator needs.
type memStore struct {
	mu     sync.Mutex
	rows   map[string]*storage.AlertState // key = ruleName + "|" + fingerprint
	nextID int
}

func newMemStore() *memStore {
	return &memStore{rows: map[string]*storage.AlertState{}, nextID: 1}
}

func (m *memStore) ListAlertStatesByRule(_ context.Context, ruleName string) ([]*storage.AlertState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*storage.AlertState, 0)
	for _, r := range m.rows {
		if r.RuleName == ruleName {
			out = append(out, r)
		}
	}
	return out, nil
}

func (m *memStore) UpsertAlertState(_ context.Context, in storage.AlertState) (*storage.AlertState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := in.RuleName + "|" + in.Fingerprint
	if existing, ok := m.rows[key]; ok {
		existing.Value = in.Value
		if in.Status != "" {
			existing.Status = in.Status
		}
		if in.Labels != nil {
			existing.Labels = in.Labels
		}
		if !in.LastNotifiedAt.IsZero() {
			existing.LastNotifiedAt = in.LastNotifiedAt
		}
		return existing, nil
	}
	in.ID = m.nextID
	m.nextID++
	if in.Status == "" {
		in.Status = "firing"
	}
	if in.FirstFiredAt.IsZero() {
		in.FirstFiredAt = time.Now()
	}
	m.rows[key] = &in
	return &in, nil
}

func (m *memStore) DeleteAlertState(_ context.Context, id int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, r := range m.rows {
		if r.ID == id {
			delete(m.rows, k)
			return nil
		}
	}
	return storage.ErrNotFound
}

// GetSystemSnapshot / UpsertSystemSnapshot are required by the
// storage.Store interface but irrelevant to the alerts evaluator.
func (m *memStore) GetSystemSnapshot(context.Context, int) (*storage.SystemSnapshot, error) {
	return nil, storage.ErrNotFound
}
func (m *memStore) UpsertSystemSnapshot(context.Context, storage.SystemSnapshot) error {
	return nil
}

// ListAlertStates is a thin pass-through used by the dashboard's
// /api/v1/alerts endpoint. The evaluator itself doesn't call it but
// the storage.Store interface requires it.
func (m *memStore) ListAlertStates(_ context.Context) ([]*storage.AlertState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*storage.AlertState, 0, len(m.rows))
	for _, r := range m.rows {
		out = append(out, r)
	}
	return out, nil
}

// Stub the unused store methods so memStore satisfies storage.Store.
func (m *memStore) CreateServer(context.Context, storage.NewServer) (*storage.Server, error) {
	panic("unused")
}
func (m *memStore) GetServer(context.Context, int) (*storage.Server, error)  { panic("unused") }
func (m *memStore) ListServers(context.Context) ([]*storage.Server, error)   { panic("unused") }
func (m *memStore) UpdateServer(context.Context, int, storage.UpdateServer) (*storage.Server, error) {
	panic("unused")
}
func (m *memStore) DeleteServer(context.Context, int) error              { panic("unused") }
func (m *memStore) SetServerStatus(context.Context, int, string, string) error {
	panic("unused")
}
func (m *memStore) GetLogCursor(context.Context, int, string) (*storage.LogCursor, error) {
	panic("unused")
}
func (m *memStore) UpsertLogCursor(context.Context, storage.LogCursor) error { panic("unused") }
func (m *memStore) CreateDBTarget(context.Context, storage.NewDBTarget) (*storage.DBTarget, error) {
	panic("unused")
}
func (m *memStore) GetDBTarget(context.Context, int) (*storage.DBTarget, error) { panic("unused") }
func (m *memStore) ListDBTargets(context.Context) ([]*storage.DBTarget, error)  { panic("unused") }
func (m *memStore) UpdateDBTarget(context.Context, int, storage.UpdateDBTarget) (*storage.DBTarget, error) {
	panic("unused")
}
func (m *memStore) DeleteDBTarget(context.Context, int) error                  { panic("unused") }
func (m *memStore) SetDBTargetStatus(context.Context, int, storage.DBTargetStatus) error {
	panic("unused")
}
func (m *memStore) Close() error { return nil }

// recordedNotifier captures every Notify call.
type recordedNotifier struct {
	mu    sync.Mutex
	calls []notifyCall
}

// notifyCall mirrors the legacy positional signature so existing
// assertions (.severity / .rule / .message / .resolved) still read
// naturally even though the interface now takes a Notification struct.
type notifyCall struct {
	severity string
	rule     string
	message  string
	resolved bool
}

func (r *recordedNotifier) Notify(_ context.Context, n Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, notifyCall{
		severity: n.Severity,
		rule:     n.RuleName,
		message:  n.Body,
		resolved: n.Resolved,
	})
	return nil
}

// flakyNotifier fails the Notify calls whose 1-based attempt number is
// in failOn, succeeding otherwise. Used to assert the evaluator retries
// rather than silently dropping a page when the notifier errors.
type flakyNotifier struct {
	mu       sync.Mutex
	attempts int
	failOn   map[int]bool
}

func (f *flakyNotifier) Notify(_ context.Context, _ Notification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts++
	if f.failOn[f.attempts] {
		return errors.New("notify boom")
	}
	return nil
}

// silentLogger discards everything; tests assert via their own state.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestEvaluator_NewFiringAlertNotifies(t *testing.T) {
	rule := Rule{
		Name:              "site_down",
		Expr:              `site_is_healthy == 0`,
		Severity:          "critical",
		FingerprintLabels: []string{"site"},
		Message:           `Site {{.Labels.site}} down (value={{.Value}})`,
	}
	vm := &fakeVM{resp: map[string][]Sample{
		`site_is_healthy == 0`: {
			{Labels: map[string]string{"site": "alpha"}, Value: 0},
		},
	}}
	store := newMemStore()
	notif := &recordedNotifier{}

	ev := &Evaluator{
		Rules:            []Rule{rule},
		VM:               vm,
		Store:            store,
		Notifier:         notif,
		NotifyRepeatTime: time.Hour,
		Logger:           silentLogger(),
		Now:              func() time.Time { return time.Unix(1000, 0) },
	}
	ev.EvaluateOnce(context.Background())

	require.Len(t, notif.calls, 1, "first firing should notify exactly once")
	require.Equal(t, "site_down", notif.calls[0].rule)
	require.Equal(t, "critical", notif.calls[0].severity)
	require.Contains(t, notif.calls[0].message, "Site alpha down")
	require.Len(t, store.rows, 1)
}

func TestEvaluator_RepeatedFiringWithinCooldownDoesNotNotify(t *testing.T) {
	rule := Rule{
		Name: "x",
		Expr: "y > 0",
		FingerprintLabels: []string{"server"},
	}
	vm := &fakeVM{resp: map[string][]Sample{
		"y > 0": {{Labels: map[string]string{"server": "a"}, Value: 1}},
	}}
	store := newMemStore()
	notif := &recordedNotifier{}

	now := time.Unix(1000, 0)
	ev := &Evaluator{
		Rules:            []Rule{rule},
		VM:               vm,
		Store:            store,
		Notifier:         notif,
		NotifyRepeatTime: time.Hour,
		Logger:           silentLogger(),
		Now:              func() time.Time { return now },
	}
	ev.EvaluateOnce(context.Background()) // initial fire
	require.Len(t, notif.calls, 1)

	// Second tick a minute later — within the 1h cooldown, no notify.
	now = now.Add(60 * time.Second)
	ev.EvaluateOnce(context.Background())
	require.Len(t, notif.calls, 1, "still 1 — cooldown suppressed repeat")

	// Past the cooldown — notify again.
	now = now.Add(2 * time.Hour)
	ev.EvaluateOnce(context.Background())
	require.Len(t, notif.calls, 2, "cooldown elapsed — re-paged")
}

func TestEvaluator_ResolutionDeletesAndNotifies(t *testing.T) {
	rule := Rule{
		Name: "x",
		Expr: "y > 0",
		FingerprintLabels: []string{"server"},
	}
	store := newMemStore()
	notif := &recordedNotifier{}
	vm := &fakeVM{resp: map[string][]Sample{
		"y > 0": {{Labels: map[string]string{"server": "a"}, Value: 1}},
	}}

	ev := &Evaluator{
		Rules:            []Rule{rule},
		VM:               vm,
		Store:            store,
		Notifier:         notif,
		NotifyRepeatTime: time.Hour,
		Logger:           silentLogger(),
		Now:              func() time.Time { return time.Unix(1000, 0) },
	}
	ev.EvaluateOnce(context.Background())
	require.Len(t, notif.calls, 1)
	require.Len(t, store.rows, 1)

	// VM now returns empty — series is gone, alert should resolve.
	vm.resp["y > 0"] = nil
	ev.EvaluateOnce(context.Background())

	require.Len(t, notif.calls, 2, "resolution should be the second notification")
	require.True(t, notif.calls[1].resolved, "second call must be flagged as a resolution")
	require.Equal(t, "x", notif.calls[1].rule, "rule name should not be mutated for resolutions")
	require.False(t, notif.calls[0].resolved, "first call is the firing event, not a resolution")
	require.Empty(t, store.rows, "resolved state should be deleted")
}

func TestEvaluator_DistinctFingerprintsTrackedSeparately(t *testing.T) {
	rule := Rule{
		Name:              "x",
		Expr:              "y > 0",
		FingerprintLabels: []string{"server"},
	}
	store := newMemStore()
	notif := &recordedNotifier{}
	vm := &fakeVM{resp: map[string][]Sample{
		"y > 0": {
			{Labels: map[string]string{"server": "a"}, Value: 1},
			{Labels: map[string]string{"server": "b"}, Value: 2},
		},
	}}
	ev := &Evaluator{
		Rules:            []Rule{rule},
		VM:               vm,
		Store:            store,
		Notifier:         notif,
		NotifyRepeatTime: time.Hour,
		Logger:           silentLogger(),
		Now:              func() time.Time { return time.Unix(1000, 0) },
	}
	ev.EvaluateOnce(context.Background())
	require.Len(t, notif.calls, 2, "one notify per (rule,fingerprint)")
	require.Len(t, store.rows, 2)

	// Resolve only "a"; "b" still firing.
	vm.resp["y > 0"] = []Sample{{Labels: map[string]string{"server": "b"}, Value: 2}}
	ev.EvaluateOnce(context.Background())
	require.Len(t, notif.calls, 3, "one resolution call for a; b stays firing within cooldown")
	require.Len(t, store.rows, 1)
	for _, row := range store.rows {
		require.Equal(t, "firing", row.Status)
	}
}

func TestRule_Validate(t *testing.T) {
	require.Error(t, Rule{Expr: "x"}.Validate(),         "missing name")
	require.Error(t, Rule{Name: "x"}.Validate(),         "missing expr")
	require.Error(t, Rule{Name: "x", Expr: "y", Severity: "loud"}.Validate(), "bad severity")
	require.NoError(t, Rule{Name: "x", Expr: "y"}.Validate())
	require.NoError(t, Rule{Name: "x", Expr: "y", Severity: "info"}.Validate())
}

func TestRule_FingerprintIsStable(t *testing.T) {
	r := Rule{Name: "n", Expr: "e", FingerprintLabels: []string{"a", "b"}}
	a := r.Fingerprint(map[string]string{"a": "1", "b": "2", "c": "ignored"})
	b := r.Fingerprint(map[string]string{"b": "2", "a": "1", "c": "different"})
	require.Equal(t, a, b, "fingerprint must ignore non-listed labels and key ordering")

	c := r.Fingerprint(map[string]string{"a": "1", "b": "DIFFERENT"})
	require.NotEqual(t, a, c, "different listed-label values must differ")
}

func TestConfig_ValidateRejectsMissingTelegramWhenEnabled(t *testing.T) {
	require.Error(t, Config{
		Enabled:                   true,
		EvaluationIntervalSeconds: 60,
		VMQueryTimeoutSeconds:     10,
	}.Validate(), "missing bot token")

	require.NoError(t, Config{Enabled: false}.Validate(),
		"disabled config should validate without telegram fields")
}

// TestEvaluator_FirstFireNotifyFailureRetriesNextTick guards the fix for
// the dropped first-fire page: when the notifier fails, LastNotifiedAt
// must NOT be advanced, so the alert is retried on the next tick instead
// of being suppressed for a full cooldown window.
func TestEvaluator_FirstFireNotifyFailureRetriesNextTick(t *testing.T) {
	rule := Rule{Name: "x", Expr: "y > 0", FingerprintLabels: []string{"server"}}
	vm := &fakeVM{resp: map[string][]Sample{
		"y > 0": {{Labels: map[string]string{"server": "a"}, Value: 1}},
	}}
	store := newMemStore()
	fn := &flakyNotifier{failOn: map[int]bool{1: true}} // first page fails

	// Realistic wall-clock: the "never paged" sentinel is the epoch, so
	// `now` must be a real modern time for the cooldown math to treat a
	// failed-and-never-paged alert as overdue (as it always is in prod).
	now := time.Unix(1_700_000_000, 0)
	ev := &Evaluator{
		Rules:            []Rule{rule},
		VM:               vm,
		Store:            store,
		Notifier:         fn,
		NotifyRepeatTime: time.Hour,
		Logger:           silentLogger(),
		Now:              func() time.Time { return now },
	}

	ev.EvaluateOnce(context.Background()) // first fire — notify FAILS
	require.Equal(t, 1, fn.attempts)
	require.Len(t, store.rows, 1, "firing state persisted despite notify failure")

	// Next tick, still well within the 1h cooldown. The failed page must
	// be retried (not suppressed), and this time it succeeds.
	now = now.Add(30 * time.Second)
	ev.EvaluateOnce(context.Background())
	require.Equal(t, 2, fn.attempts, "failed first page retried next tick, not suppressed")
}

// TestEvaluator_ResolveNotifyFailureKeepsRow guards the fix for the lost
// resolve page: if the recovery notification fails, the state row must be
// kept so the resolve is retried, not deleted-and-forgotten.
func TestEvaluator_ResolveNotifyFailureKeepsRow(t *testing.T) {
	rule := Rule{Name: "x", Expr: "y > 0", FingerprintLabels: []string{"server"}}
	firing := &fakeVM{resp: map[string][]Sample{
		"y > 0": {{Labels: map[string]string{"server": "a"}, Value: 1}},
	}}
	empty := &fakeVM{resp: map[string][]Sample{"y > 0": {}}}
	store := newMemStore()
	fn := &flakyNotifier{failOn: map[int]bool{2: true}} // fire ok, resolve #2 fails

	now := time.Unix(1_700_000_000, 0)
	ev := &Evaluator{
		Rules:            []Rule{rule},
		VM:               firing,
		Store:            store,
		Notifier:         fn,
		NotifyRepeatTime: time.Hour,
		Logger:           silentLogger(),
		Now:              func() time.Time { return now },
	}

	ev.EvaluateOnce(context.Background()) // fire (notify #1 ok)
	require.Len(t, store.rows, 1)

	// Series gone; resolve page (#2) fails → row must be retained.
	ev.VM = empty
	now = now.Add(time.Minute)
	ev.EvaluateOnce(context.Background())
	require.Len(t, store.rows, 1, "row kept after failed resolve so it retries")

	// Next tick, resolve (#3) succeeds → row finally deleted.
	now = now.Add(time.Minute)
	ev.EvaluateOnce(context.Background())
	require.Len(t, store.rows, 0, "row deleted after resolve finally succeeds")
}
