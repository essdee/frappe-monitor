package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	sshpkg "frappe-monitor/internal/ssh"
)

// helper: create and return id.
func createForCRUDTest(t *testing.T, baseURL, hostname string) int {
	t.Helper()
	body := `{
		"name": "n-` + hostname + `",
		"hostname": "` + hostname + `",
		"ssh_user": "monitor",
		"ssh_port": 22,
		"ssh_key_path": "/tmp/k"
	}`
	resp, err := http.Post(baseURL+"/api/v1/servers", "application/json", bytes.NewBufferString(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	return int(got["id"].(float64))
}

func TestPatchServer_RenameAndChangeSSHKey(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	id := createForCRUDTest(t, ts.URL, "old.example.com")

	body := `{"name": "renamed", "ssh_key_path": "/tmp/k2"}`
	req, _ := http.NewRequest(http.MethodPatch,
		ts.URL+"/api/v1/servers/"+strconv.Itoa(id),
		bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, "renamed", got["name"])
	require.Equal(t, "/tmp/k2", got["ssh_key_path"])
	// Untouched field stays the same.
	require.Equal(t, "old.example.com", got["hostname"])
}

func TestPatchServer_RejectsBadPort(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	id := createForCRUDTest(t, ts.URL, "x.example.com")
	req, _ := http.NewRequest(http.MethodPatch,
		ts.URL+"/api/v1/servers/"+strconv.Itoa(id),
		bytes.NewBufferString(`{"ssh_port": 0}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestPatchServer_404OnUnknownID(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	req, _ := http.NewRequest(http.MethodPatch,
		ts.URL+"/api/v1/servers/9999",
		bytes.NewBufferString(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestPatchServer_ConflictOnDuplicateHostname(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	createForCRUDTest(t, ts.URL, "a.example.com")
	id := createForCRUDTest(t, ts.URL, "b.example.com")

	req, _ := http.NewRequest(http.MethodPatch,
		ts.URL+"/api/v1/servers/"+strconv.Itoa(id),
		bytes.NewBufferString(`{"hostname": "a.example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestDeleteServer_HappyPath(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	id := createForCRUDTest(t, ts.URL, "z.example.com")

	req, _ := http.NewRequest(http.MethodDelete,
		ts.URL+"/api/v1/servers/"+strconv.Itoa(id), nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// Subsequent GET returns 404.
	resp, err = http.Get(ts.URL + "/api/v1/servers/" + strconv.Itoa(id))
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestDeleteServer_404OnUnknownID(t *testing.T) {
	ts := newAPIServer(t, openMemStore(t), sshpkg.NewFakeExecutor())
	req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/v1/servers/9999", nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "body: %s", body)
}
