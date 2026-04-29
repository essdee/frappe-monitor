package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestTestConnection_Reachable(t *testing.T) {
	store := openMemStore(t)
	s, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: "p", Hostname: "host-a", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	exec := sshpkg.NewFakeExecutor()
	exec.SetResponse("host-a", "ok\n", nil)

	ts := newAPIServer(t, store, exec)
	resp, err := http.Post(ts.URL+"/api/v1/servers/"+strconv.Itoa(s.ID)+"/test-connection", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, true, body["reachable"])
	require.Contains(t, body, "latency_ms", "latency_ms always emitted on success")
	require.GreaterOrEqual(t, body["latency_ms"].(float64), float64(0))
	require.NotContains(t, body, "error_kind", "no error_kind on success")

	refreshed, err := store.GetServer(context.Background(), s.ID)
	require.NoError(t, err)
	require.Equal(t, "reachable", refreshed.Status)
	require.NotNil(t, refreshed.LastPingedAt)
}

func TestTestConnection_Unreachable(t *testing.T) {
	store := openMemStore(t)
	s, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: "p", Hostname: "host-b", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	exec := sshpkg.NewFakeExecutor()
	exec.SetResponse("host-b", "", errors.New("auth failed"))

	ts := newAPIServer(t, store, exec)
	resp, err := http.Post(ts.URL+"/api/v1/servers/"+strconv.Itoa(s.ID)+"/test-connection", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, false, body["reachable"])
	require.Contains(t, body["error"], "auth failed")
	// Plain error not wrapping a sentinel → "unknown".
	require.Equal(t, "unknown", body["error_kind"])

	refreshed, err := store.GetServer(context.Background(), s.ID)
	require.NoError(t, err)
	require.Equal(t, "unreachable", refreshed.Status)
}

func TestTestConnection_ServerNotFound(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	resp, err := http.Post(ts.URL+"/api/v1/servers/9999/test-connection", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestTestConnection_ErrorKindFromSentinels(t *testing.T) {
	cases := []struct {
		name       string
		sentinel   error
		wantKind   string
	}{
		{"auth", sshpkg.ErrAuth, "auth"},
		{"dial", sshpkg.ErrDial, "dial"},
		{"timeout", sshpkg.ErrTimeout, "timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openMemStore(t)
			host := "host-" + tc.name
			s, err := store.CreateServer(context.Background(), storage.NewServer{
				Name: tc.name, Hostname: host, SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
			})
			require.NoError(t, err)

			exec := sshpkg.NewFakeExecutor()
			exec.SetResponse(host, "", fmt.Errorf("simulated %s: %w", tc.name, tc.sentinel))

			ts := newAPIServer(t, store, exec)
			resp, err := http.Post(ts.URL+"/api/v1/servers/"+strconv.Itoa(s.ID)+"/test-connection", "application/json", nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var body map[string]any
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			require.Equal(t, false, body["reachable"])
			require.Equal(t, tc.wantKind, body["error_kind"])
		})
	}
}

func TestDeployCollector_HappyPath(t *testing.T) {
	store := openMemStore(t)
	s, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: "p", Hostname: "deploy-host", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
	})
	require.NoError(t, err)

	exec := sshpkg.NewFakeExecutor()
	exec.SetResponse("deploy-host", "", nil)

	ts := newAPIServer(t, store, exec)
	resp, err := http.Post(ts.URL+"/api/v1/servers/"+strconv.Itoa(s.ID)+"/deploy-collector", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, true, body["deployed"])
	require.Regexp(t, `^\d+\.\d+\.\d+$`, body["version"], "version should be semver")

	// FakeExecutor captured the embedded collector script as stdin.
	stdin := exec.LastStdin("deploy-host")
	require.Contains(t, stdin, "###META", "deployed script should be the embedded collector")
	require.Contains(t, stdin, "###SERVER")
	require.Contains(t, stdin, "###END")
	cmd := exec.LastCmd("deploy-host")
	require.Contains(t, cmd, "/usr/local/bin/frappe-monitor-collect.sh")
	require.Contains(t, cmd, "chmod +x")
}

func TestDeployCollector_ServerNotFound(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	resp, err := http.Post(ts.URL+"/api/v1/servers/9999/deploy-collector", "application/json", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDeployCollector_SSHFailureMaps(t *testing.T) {
	cases := []struct {
		name       string
		sentinel   error
		wantStatus int
	}{
		{"auth_502", sshpkg.ErrAuth, http.StatusBadGateway},
		{"dial_502", sshpkg.ErrDial, http.StatusBadGateway},
		{"timeout_504", sshpkg.ErrTimeout, http.StatusGatewayTimeout},
		{"other_500", errors.New("filesystem full"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := openMemStore(t)
			host := "deploy-fail-" + tc.name
			s, err := store.CreateServer(context.Background(), storage.NewServer{
				Name: tc.name, Hostname: host, SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/k",
			})
			require.NoError(t, err)

			exec := sshpkg.NewFakeExecutor()
			exec.SetResponse(host, "", fmt.Errorf("simulated: %w", tc.sentinel))

			ts := newAPIServer(t, store, exec)
			resp, err := http.Post(ts.URL+"/api/v1/servers/"+strconv.Itoa(s.ID)+"/deploy-collector", "application/json", nil)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.wantStatus, resp.StatusCode)

			var body map[string]string
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
			require.Contains(t, body["error"], "deploy failed")
		})
	}
}
