package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func newAPIServerWithProxies(
	t *testing.T,
	metricsURL, logsURL string,
) *httptest.Server {
	t.Helper()
	r := NewRouter(Deps{
		// Phase 1/2/3 deps are nil — proxy endpoints don't touch them.
		MetricsBaseURL:      metricsURL,
		LogsBaseURL:         logsURL,
		MetricsQueryTimeout: 5 * time.Second,
		LogsQueryTimeout:    5 * time.Second,
	})
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

func TestMetricsQuery_ForwardsToVMWithSamePath(t *testing.T) {
	var got struct {
		path  string
		query string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[]}}`)
	}))
	defer upstream.Close()

	ts := newAPIServerWithProxies(t, upstream.URL, "")

	resp, err := http.Get(ts.URL + "/api/v1/metrics/query?query=up&start=1&end=2&step=1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	require.Equal(t, "/api/v1/query_range", got.path)
	require.Contains(t, got.query, "query=up")
	require.Contains(t, got.query, "start=1")
	require.Contains(t, got.query, "end=2")
	require.Contains(t, got.query, "step=1")
}

func TestLogsQuery_ForwardsToLokiWithSamePath(t *testing.T) {
	var got struct {
		path  string
		query string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"streams","result":[]}}`)
	}))
	defer upstream.Close()

	ts := newAPIServerWithProxies(t, "", upstream.URL)

	resp, err := http.Get(ts.URL + `/api/v1/logs/query?query=%7Bserver%3D%22x%22%7D&start=1&end=2&limit=10`)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "/loki/api/v1/query_range", got.path)
	require.Contains(t, got.query, "query=")
	require.Contains(t, got.query, "limit=10")
}

func TestMetricsQuery_RejectsMissingQuery(t *testing.T) {
	ts := newAPIServerWithProxies(t, "http://upstream-not-called", "")
	resp, err := http.Get(ts.URL + "/api/v1/metrics/query?start=1&end=2")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestLogsQuery_RejectsMissingQuery(t *testing.T) {
	ts := newAPIServerWithProxies(t, "", "http://upstream-not-called")
	resp, err := http.Get(ts.URL + "/api/v1/logs/query?start=1&end=2")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestMetricsQuery_UpstreamUnreachable_502(t *testing.T) {
	// Point at a port nothing listens on. The Get to that URL will fail
	// → proxy returns 502.
	ts := newAPIServerWithProxies(t, "http://127.0.0.1:1", "")
	resp, err := http.Get(ts.URL + "/api/v1/metrics/query?query=up")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestProxy_NotMountedWhenURLEmpty(t *testing.T) {
	// Both URLs empty → endpoints not registered → 404 from chi.
	ts := newAPIServerWithProxies(t, "", "")
	for _, p := range []string{"/api/v1/metrics/query?query=up", "/api/v1/logs/query?query=x"} {
		resp, err := http.Get(ts.URL + p)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode, "path %q", p)
	}
}

func TestMetricsQuery_MirrorsUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "vm rejected", http.StatusBadRequest)
	}))
	defer upstream.Close()

	ts := newAPIServerWithProxies(t, upstream.URL, "")
	resp, err := http.Get(ts.URL + "/api/v1/metrics/query?query=invalid")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	require.Contains(t, string(body), "vm rejected")
}
