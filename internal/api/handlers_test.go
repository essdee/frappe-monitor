package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

func openMemStore(t *testing.T) storage.Store {
	t.Helper()
	s, err := storage.OpenEntStore(context.Background(), "file:apitest?mode=memory&cache=shared&_pragma=foreign_keys(1)")
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newAPIServer(t *testing.T, store storage.Store, exec sshpkg.Executor) *httptest.Server {
	t.Helper()
	r := NewRouter(Deps{Store: store, Executor: exec})
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

func TestCreateServer_HappyPath(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	body := `{
		"name": "prod-1",
		"hostname": "prod1.example.com",
		"ssh_user": "monitor",
		"ssh_port": 22,
		"ssh_key_path": "/tmp/k"
	}`
	resp, err := http.Post(ts.URL+"/api/v1/servers", "application/json", bytes.NewBufferString(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)

	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, "prod-1", got["name"])
	require.NotZero(t, got["id"])
	require.Equal(t, "unknown", got["status"])
}

func TestCreateServer_BadJSON(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	resp, err := http.Post(ts.URL+"/api/v1/servers", "application/json", bytes.NewBufferString(`{`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestCreateServer_DuplicateHostname(t *testing.T) {
	store := openMemStore(t)
	ts := newAPIServer(t, store, sshpkg.NewFakeExecutor())
	body := `{
		"name": "first",
		"hostname": "dup.example.com",
		"ssh_user": "monitor",
		"ssh_port": 22,
		"ssh_key_path": "/tmp/k"
	}`
	resp1, err := http.Post(ts.URL+"/api/v1/servers", "application/json", bytes.NewBufferString(body))
	require.NoError(t, err)
	resp1.Body.Close()
	require.Equal(t, http.StatusCreated, resp1.StatusCode)

	// Same hostname, different name — should 409.
	bodyDup := `{
		"name": "second",
		"hostname": "dup.example.com",
		"ssh_user": "monitor",
		"ssh_port": 22,
		"ssh_key_path": "/tmp/k"
	}`
	resp2, err := http.Post(ts.URL+"/api/v1/servers", "application/json", bytes.NewBufferString(bodyDup))
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, http.StatusConflict, resp2.StatusCode)

	var body409 map[string]string
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&body409))
	require.Contains(t, body409["error"], "hostname")
}

func TestListServers(t *testing.T) {
	store := openMemStore(t)
	_, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: "a", Hostname: "a.example.com", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	ts := newAPIServer(t, store, sshpkg.NewFakeExecutor())
	resp, err := http.Get(ts.URL + "/api/v1/servers")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var arr []map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&arr))
	require.Len(t, arr, 1)
}

func TestGetServer_NotFound(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	resp, err := http.Get(ts.URL + "/api/v1/servers/9999")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}
