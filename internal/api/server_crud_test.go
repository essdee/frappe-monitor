package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"

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

func TestDeleteServer_RemovesRemoteCollector(t *testing.T) {
	fe := sshpkg.NewFakeExecutor()
	const host = "del.example.com"
	fe.SetResponse(host, "", nil) // remote cleanup succeeds
	ts := newAPIServer(t, openMemStore(t), fe)
	id := createForCRUDTest(t, ts.URL, host)

	req, _ := http.NewRequest(http.MethodDelete,
		ts.URL+"/api/v1/servers/"+strconv.Itoa(id), nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	// The collector cleanup runs in the background, so poll for it. It must SSH
	// exactly one cleanup command that removes the collector script and the
	// directory — and must NEVER use rm -rf (it can't be able to nuke unrelated
	// files).
	require.Eventually(t, func() bool { return fe.CallCount(host) == 1 }, 3*time.Second, 10*time.Millisecond,
		"delete should run one background SSH cleanup command")
	last := fe.LastCmd(host)
	require.Contains(t, last, `rm -f "$HOME/.frappe-monitor/frappe-monitor-collect.sh"`)
	require.Contains(t, last, `rmdir "$HOME/.frappe-monitor"`)
	require.NotContains(t, last, "rm -rf")
}

func TestDeleteServer_SucceedsWhenHostUnreachable(t *testing.T) {
	// The remote cleanup is best-effort: a host whose SSH fails (decommissioned
	// box) must still be deletable.
	fe := sshpkg.NewFakeExecutor()
	const host = "gone.example.com"
	fe.SetResponse(host, "", errors.New("ssh dial gone.example.com: connection refused"))
	ts := newAPIServer(t, openMemStore(t), fe)
	id := createForCRUDTest(t, ts.URL, host)

	req, _ := http.NewRequest(http.MethodDelete,
		ts.URL+"/api/v1/servers/"+strconv.Itoa(id), nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusNoContent, resp.StatusCode, "delete must succeed even when cleanup SSH fails")

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
