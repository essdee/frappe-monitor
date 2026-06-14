package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/control"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

func newControlAPIServer(t *testing.T, store storage.Store, runner ControlRunner) *httptest.Server {
	t.Helper()
	r := NewRouter(Deps{Store: store, Executor: sshpkg.NewFakeExecutor(), ControlRunner: runner})
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

func seedControlServer(t *testing.T, store storage.Store) int {
	t.Helper()
	srv, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: "s", Hostname: "s.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)
	return srv.ID
}

func TestControl_CatalogListsAllowlist(t *testing.T) {
	ts := newControlAPIServer(t, openMemStore(t), nil)
	resp, err := http.Get(ts.URL + "/api/v1/control/actions")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var defs []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&defs))
	require.NotEmpty(t, defs)
	keys := map[string]bool{}
	for _, d := range defs {
		keys[d["key"].(string)] = true
	}
	require.True(t, keys["bench.migrate"], "catalog must expose bench.migrate")
	require.True(t, keys["supervisor.restart"], "catalog must expose supervisor.restart")
}

func TestControl_RunWithoutRunnerReturns503(t *testing.T) {
	ts := newControlAPIServer(t, openMemStore(t), nil)
	resp, err := http.Post(ts.URL+"/api/v1/control/run", "application/json",
		bytes.NewBufferString(`{"server_id":1,"action":"supervisor.status"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
}

func TestControl_RunUnknownActionRejected(t *testing.T) {
	store := openMemStore(t)
	id := seedControlServer(t, store)
	ts := newControlAPIServer(t, store, control.New(store, sshpkg.NewFakeExecutor(), nil, nil, control.Config{}))

	resp, err := http.Post(ts.URL+"/api/v1/control/run", "application/json",
		bytes.NewBufferString(`{"server_id":`+strconv.Itoa(id)+`,"action":"bogus.cmd"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestControl_RunAcceptedAndRecorded(t *testing.T) {
	store := openMemStore(t)
	id := seedControlServer(t, store)
	ts := newControlAPIServer(t, store, control.New(store, sshpkg.NewFakeExecutor(), nil, nil, control.Config{}))

	resp, err := http.Post(ts.URL+"/api/v1/control/run", "application/json",
		bytes.NewBufferString(`{"server_id":`+strconv.Itoa(id)+`,"action":"supervisor.status"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusAccepted, resp.StatusCode)

	var act map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&act))
	require.Equal(t, "supervisor.status", act["action"])

	// The run finishes asynchronously; history must reflect it shortly.
	require.Eventually(t, func() bool {
		r, e := http.Get(ts.URL + "/api/v1/control/history?server_id=" + strconv.Itoa(id))
		if e != nil {
			return false
		}
		defer r.Body.Close()
		var rows []map[string]any
		if json.NewDecoder(r.Body).Decode(&rows) != nil {
			return false
		}
		return len(rows) >= 1
	}, 3*time.Second, 25*time.Millisecond)
}
