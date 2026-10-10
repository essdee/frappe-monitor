package alerts

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
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

type vmQuerierFunc func(context.Context, string) ([]Sample, error)

func (f vmQuerierFunc) Query(ctx context.Context, expr string) ([]Sample, error) {
	return f(ctx, expr)
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
	mu            sync.Mutex
	rows          map[string]*storage.AlertState // key = ruleName + "|" + fingerprint
	nextID        int
	servers       []*storage.Server
	serverListErr error
}

func newMemStore() *memStore {
	return &memStore{
		rows: map[string]*storage.AlertState{}, nextID: 1,
		// Most evaluator unit tests predate registry reconciliation and use
		// synthetic labels. An unavailable registry preserves that isolated
		// behavior; dedicated tests below exercise the authoritative path.
		serverListErr: errors.New("registry not configured in test"),
	}
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
func (m *memStore) GetServer(context.Context, int) (*storage.Server, error) { panic("unused") }
func (m *memStore) ListServers(context.Context) ([]*storage.Server, error) {
	return m.servers, m.serverListErr
}
func (m *memStore) UpdateServer(context.Context, int, storage.UpdateServer) (*storage.Server, error) {
	panic("unused")
}
func (m *memStore) DeleteServer(context.Context, int) error { panic("unused") }
func (m *memStore) SetServerStatus(context.Context, int, string, string) error {
	panic("unused")
}
func (m *memStore) GetLogCursor(context.Context, int, string) (*storage.LogCursor, error) {
	panic("unused")
}
func (m *memStore) UpsertLogCursor(context.Context, storage.LogCursor) error { panic("unused") }
func (m *memStore) Close() error                                             { return nil }

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
		Name:              "x",
		Expr:              "y > 0",
		FingerprintLabels: []string{"server"},
	}
	vm := &fakeVM{resp: map[string][]Sample{
		"y > 0":                        {{Labels: map[string]string{"server": "a"}, Value: 1}},
		`frappe_server_reachable == 1`: {{Labels: map[string]string{"server": "a"}, Value: 1}},
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

func TestEvaluator_ZeroRepeatNotifiesOnceButRetriesFailure(t *testing.T) {
	rule := Rule{Name: "x", Expr: "y > 0", FingerprintLabels: []string{"server"}}
	vm := &fakeVM{resp: map[string][]Sample{
		"y > 0":                        {{Labels: map[string]string{"server": "a"}, Value: 1}},
		`frappe_server_reachable == 1`: {{Labels: map[string]string{"server": "a"}, Value: 1}},
	}}
	store := newMemStore()
	attempts := 0
	notifier := NotifierFunc(func(context.Context, Notification) error {
		attempts++
		if attempts == 1 {
			return errors.New("telegram unavailable")
		}
		return nil
	})
	ev := &Evaluator{
		Rules: []Rule{rule}, VM: vm, Store: store, Notifier: notifier,
		NotifyRepeatTime: 0, Logger: silentLogger(),
		Now: func() time.Time { return time.Unix(1000, 0) },
	}

	ev.EvaluateOnce(context.Background())
	require.Equal(t, 1, attempts)
	require.Len(t, store.rows, 1, "failed delivery must retain retry state")
	ev.EvaluateOnce(context.Background())
	require.Equal(t, 2, attempts, "a failed initial delivery must retry immediately")
	ev.EvaluateOnce(context.Background())
	require.Equal(t, 2, attempts, "zero repeat means no pages after one successful fire")
}

func TestEvaluator_ResolutionDeletesAndNotifies(t *testing.T) {
	rule := Rule{
		Name:              "x",
		Expr:              "y > 0",
		FingerprintLabels: []string{"server"},
	}
	store := newMemStore()
	notif := &recordedNotifier{}
	vm := &fakeVM{resp: map[string][]Sample{
		"y > 0":                        {{Labels: map[string]string{"server": "a"}, Value: 1}},
		`frappe_server_reachable == 1`: {{Labels: map[string]string{"server": "a"}, Value: 1}},
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

func TestEvaluator_FailedResolutionIsRetriedBeforeStateDeletion(t *testing.T) {
	rule := Rule{Name: "x", Expr: "y > 0", FingerprintLabels: []string{"server"}}
	vm := &fakeVM{resp: map[string][]Sample{
		"y > 0":                        {{Labels: map[string]string{"server": "a"}, Value: 1}},
		`frappe_server_reachable == 1`: {{Labels: map[string]string{"server": "a"}, Value: 1}},
	}}
	store := newMemStore()
	resolveAttempts := 0
	notifier := NotifierFunc(func(_ context.Context, n Notification) error {
		if !n.Resolved {
			return nil
		}
		resolveAttempts++
		if resolveAttempts == 1 {
			return errors.New("telegram unavailable")
		}
		return nil
	})
	ev := &Evaluator{
		Rules: []Rule{rule}, VM: vm, Store: store, Notifier: notifier,
		NotifyRepeatTime: time.Hour, Logger: silentLogger(),
		Now: func() time.Time { return time.Unix(1000, 0) },
	}
	ev.EvaluateOnce(context.Background())
	vm.resp["y > 0"] = nil
	ev.EvaluateOnce(context.Background())
	require.Len(t, store.rows, 1, "state must remain until recovery delivery succeeds")
	ev.EvaluateOnce(context.Background())
	require.Empty(t, store.rows)
	require.Equal(t, 2, resolveAttempts)
}

func TestEvaluator_SiteAlertIncludesProbeDetails(t *testing.T) {
	rule := DefaultRules()[2]
	labels := map[string]string{"server": "srv", "bench": "bench", "site": "site.test"}
	vm := &fakeVM{resp: map[string][]Sample{
		rule.Expr: {{Labels: labels, Value: 0}},
		"frappe_site_public_http_status_code or frappe_site_http_status_code":       {{Labels: labels, Value: 503}},
		"frappe_site_public_http_response_ms or frappe_site_http_response_ms":       {{Labels: labels, Value: 2012.3}},
		"frappe_site_public_failure_reason_code or frappe_site_failure_reason_code": {{Labels: labels, Value: 3}},
	}}
	store := newMemStore()
	notifier := &recordedNotifier{}
	ev := &Evaluator{
		Rules: []Rule{rule}, VM: vm, Store: store, Notifier: notifier,
		NotifyRepeatTime: time.Hour, Logger: silentLogger(),
		Now: func() time.Time { return time.Unix(1000, 0) },
	}
	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 1)
	require.Contains(t, notifier.calls[0].message, "HTTP 5xx")
	require.Contains(t, notifier.calls[0].message, "HTTP 503")
	require.Contains(t, notifier.calls[0].message, "2012.3 ms")
}

func TestEvaluator_SiteCompanionQueriesAreConstantForManyFailures(t *testing.T) {
	rule := DefaultRules()[2]
	var queries atomic.Int64
	vm := vmQuerierFunc(func(_ context.Context, expr string) ([]Sample, error) {
		queries.Add(1)
		labelsA := map[string]string{"server": "srv", "bench": "bench", "site": "a.test"}
		labelsB := map[string]string{"server": "srv", "bench": "bench", "site": "b.test"}
		switch expr {
		case rule.Expr:
			return []Sample{{Labels: labelsA}, {Labels: labelsB}}, nil
		case "frappe_site_public_http_status_code or frappe_site_http_status_code":
			return []Sample{{Labels: labelsA, Value: 502}, {Labels: labelsB, Value: 503}}, nil
		case "frappe_site_public_http_response_ms or frappe_site_http_response_ms",
			"frappe_site_public_failure_reason_code or frappe_site_failure_reason_code",
			"frappe_site_public_is_healthy", `(frappe_site_public_is_healthy or frappe_site_is_healthy) < 0`, `frappe_server_reachable == 0`:
			return nil, nil
		default:
			return nil, errors.New("unexpected query: " + expr)
		}
	})
	ev := &Evaluator{
		Rules: []Rule{rule}, VM: vm, Store: newMemStore(), Notifier: &recordedNotifier{},
		NotifyRepeatTime: time.Hour, Logger: silentLogger(),
	}
	ev.EvaluateOnce(context.Background())
	require.Equal(t, int64(5), queries.Load(), "query count must stay constant as failing site count grows")
}

func TestEvaluator_UnknownSiteProbeDoesNotResolveExistingAlert(t *testing.T) {
	rule := DefaultRules()[2]
	labels := map[string]string{"server": "srv", "bench": "bench", "site": "site.test"}
	vm := &fakeVM{resp: map[string][]Sample{
		rule.Expr: {{Labels: labels, Value: 0}},
		`(frappe_site_public_is_healthy or frappe_site_is_healthy) < 0`: nil,
		`frappe_server_reachable == 1`:                                  {{Labels: map[string]string{"server": "srv"}, Value: 1}},
	}}
	store := newMemStore()
	notifier := &recordedNotifier{}
	ev := &Evaluator{
		Rules: []Rule{rule}, VM: vm, Store: store, Notifier: notifier,
		NotifyRepeatTime: time.Hour, Logger: silentLogger(),
		Now: func() time.Time { return time.Unix(1000, 0) },
	}
	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 1)

	vm.resp[rule.Expr] = nil
	vm.resp[`(frappe_site_public_is_healthy or frappe_site_is_healthy) < 0`] = []Sample{{Labels: labels, Value: -1}}
	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 1, "an unexecuted probe is not a recovery")
	require.Len(t, store.rows, 1, "the firing state must remain pending")
}

func TestEvaluator_DeletedServerStateIsPrunedWithoutFalseRecovery(t *testing.T) {
	rule := DefaultRules()[0]
	labels := map[string]string{"server": "retired"}
	vm := &fakeVM{resp: map[string][]Sample{rule.Expr: {{Labels: labels, Value: 0}}}}
	store := newMemStore()
	store.serverListErr = nil
	store.servers = []*storage.Server{{Name: "retired"}}
	notifier := &recordedNotifier{}
	ev := &Evaluator{
		Rules: []Rule{rule}, VM: vm, Store: store, Notifier: notifier,
		NotifyRepeatTime: time.Hour, Logger: silentLogger(),
	}

	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 1)
	require.Len(t, store.rows, 1)

	store.servers = nil // server was deleted from the registry
	vm.resp[rule.Expr] = nil
	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 1, "server deletion must not look like recovery")
	require.Empty(t, store.rows)

	// Retained VM samples after deletion must not recreate the alert.
	vm.resp[rule.Expr] = []Sample{{Labels: labels, Value: 0}}
	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 1)
	require.Empty(t, store.rows)
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
		`frappe_server_reachable == 1`: {
			{Labels: map[string]string{"server": "a"}, Value: 1},
			{Labels: map[string]string{"server": "b"}, Value: 1},
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

func TestEvaluator_ServerOutageDoesNotResolveDependentAlert(t *testing.T) {
	rule := DefaultRules()[1] // disk_almost_full
	labels := map[string]string{"server": "srv", "mount": "/data"}
	vm := &fakeVM{resp: map[string][]Sample{
		rule.Expr:                      {{Labels: labels, Value: 95}},
		`frappe_server_reachable == 1`: {{Labels: map[string]string{"server": "srv"}, Value: 1}},
	}}
	store := newMemStore()
	store.serverListErr = nil
	store.servers = []*storage.Server{{Name: "srv", Status: "reachable"}}
	notifier := &recordedNotifier{}
	ev := &Evaluator{
		Rules: []Rule{rule}, VM: vm, Store: store, Notifier: notifier,
		NotifyRepeatTime: time.Hour, Logger: silentLogger(),
	}

	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 1)

	vm.resp[rule.Expr] = nil
	store.servers[0].Status = "unreachable"
	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 1, "missing telemetry during an outage is not recovery")
	require.Len(t, store.rows, 1)

	store.servers[0].Status = "reachable"
	ev.EvaluateOnce(context.Background())
	require.Len(t, notifier.calls, 2)
	require.True(t, notifier.calls[1].resolved)
	require.Empty(t, store.rows)
}

func TestEvaluator_MissingReachabilityEvidenceDefersRecovery(t *testing.T) {
	rule := Rule{Name: "custom", Expr: "custom_metric > 0", FingerprintLabels: []string{"server"}}
	labels := map[string]string{"server": "srv"}
	vm := &fakeVM{resp: map[string][]Sample{
		rule.Expr: {{Labels: labels, Value: 1}},
	}}
	store := newMemStore() // registry deliberately unavailable
	notifier := &recordedNotifier{}
	ev := &Evaluator{
		Rules: []Rule{rule}, VM: vm, Store: store, Notifier: notifier,
		NotifyRepeatTime: time.Hour, Logger: silentLogger(),
	}

	ev.EvaluateOnce(context.Background())
	vm.resp[rule.Expr] = nil
	ev.EvaluateOnce(context.Background())

	require.Len(t, notifier.calls, 1, "an evidence-query failure must not invent recovery")
	require.Len(t, store.rows, 1)
}

func TestRule_Validate(t *testing.T) {
	require.Error(t, Rule{Expr: "x"}.Validate(), "missing name")
	require.Error(t, Rule{Name: "x"}.Validate(), "missing expr")
	require.Error(t, Rule{Name: "x", Expr: "y", Severity: "loud"}.Validate(), "bad severity")
	require.Error(t, Rule{Name: "x", Expr: "y", Message: "{{"}.Validate(), "bad template")
	require.Error(t, Rule{Name: "x", Expr: "y", FingerprintLabels: []string{"server", "server"}}.Validate(), "duplicate fingerprint label")
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
