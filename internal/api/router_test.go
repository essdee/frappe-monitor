package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHealthz(t *testing.T) {
	r := NewRouter(Deps{}) // zero deps: healthz shouldn't need them
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	var body map[string]string
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.Equal(t, "ok", body["status"])
}

func TestRuntimeStatus(t *testing.T) {
	started := time.Now().Add(-2 * time.Minute).UTC()
	r := NewRouter(Deps{
		StartedAt: started,
		StreamStatus: func() StreamRuntimeStatus {
			return StreamRuntimeStatus{
				Enabled: true, Sessions: 3, Connected: 2,
				BufferedLines: 12, BufferedBytes: 4096,
				MaxBufferedLines: 5000, MaxBufferedBytes: 32 << 20,
			}
		},
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/status")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body runtimeStatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.GreaterOrEqual(t, body.UptimeSeconds, int64(119))
	require.Greater(t, body.Goroutines, 0)
	require.Greater(t, body.HeapSysBytes, uint64(0))
	require.True(t, body.Streaming.Enabled)
	require.Equal(t, 2, body.Streaming.Connected)
	require.Equal(t, 12, body.Streaming.BufferedLines)
}

func TestWhoamiIncludesDisplayTimezone(t *testing.T) {
	r := NewRouter(Deps{DisplayTimezone: "Asia/Kolkata"})
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/whoami")
	require.NoError(t, err)
	defer resp.Body.Close()
	var body struct {
		Authenticated bool   `json:"authenticated"`
		AuthEnabled   bool   `json:"auth_enabled"`
		Timezone      string `json:"timezone"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	require.True(t, body.Authenticated)
	require.False(t, body.AuthEnabled)
	require.Equal(t, "Asia/Kolkata", body.Timezone)
}
