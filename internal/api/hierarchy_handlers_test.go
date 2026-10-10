package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/storage"
)

// vmStub is a tiny fake VictoriaMetrics that returns canned responses
// keyed by exact PromQL string. Anything unmatched returns an error JSON
// so tests fail loudly if the handler issues an unexpected query.
type vmStub struct {
	responses map[string]string
	calls     []string
	steps     []string
}

func newVMStub() *vmStub { return &vmStub{responses: map[string]string{}} }

func (v *vmStub) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/query" {
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
			return
		}
		q := r.URL.Query().Get("query")
		v.calls = append(v.calls, q)
		v.steps = append(v.steps, r.URL.Query().Get("step"))
		body, ok := v.responses[q]
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"status":"error","error":"no stub for query: `+q+`"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	})
}

func newAPIServerWithVM(t *testing.T, vmURL string) *httptest.Server {
	t.Helper()
	r := NewRouter(Deps{
		MetricsBaseURL:      vmURL,
		MetricsQueryTimeout: 5 * time.Second,
	})
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

func newAPIServerWithVMAndStore(t *testing.T, vmURL string, store storage.Store) *httptest.Server {
	t.Helper()
	r := NewRouter(Deps{
		Store: store, MetricsBaseURL: vmURL, MetricsQueryTimeout: 5 * time.Second,
	})
	ts := httptest.NewServer(r)
	t.Cleanup(ts.Close)
	return ts
}

// vmInstantBody builds a tiny VM-shaped instant-query JSON response with
// the given series. labels is per-series; values is parallel.
func vmInstantBody(series []map[string]string, values []string) string {
	type sample struct {
		Metric map[string]string `json:"metric"`
		Value  [2]any            `json:"value"`
	}
	out := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []sample `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	out.Data.ResultType = "vector"
	for i, m := range series {
		out.Data.Result = append(out.Data.Result, sample{
			Metric: m,
			Value:  [2]any{1.0, values[i]},
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func TestHierarchyListsExcludeUnregisteredRetainedSeries(t *testing.T) {
	vm := newVMStub()
	vm.responses[`count by (server, bench) (frappe_bench_apps_count)`] = vmInstantBody(
		[]map[string]string{{"server": "current", "bench": "bench"}, {"server": "deleted", "bench": "old"}},
		[]string{"1", "1"},
	)
	vm.responses[`max by (server, bench, site) (frappe_site_public_is_healthy) or max by (server, bench, site) (frappe_site_is_healthy)`] = vmInstantBody(
		[]map[string]string{{"server": "current", "bench": "bench", "site": "site.test"}, {"server": "deleted", "bench": "old", "site": "stale.test"}},
		[]string{"1", "0"},
	)
	upstream := httptest.NewServer(vm.handler())
	t.Cleanup(upstream.Close)
	store := openMemStore(t)
	_, err := store.CreateServer(context.Background(), storage.NewServer{
		Name: "current", Hostname: "current.test", SSHUser: "monitor", SSHPort: 22, SSHKeyPath: "/tmp/key",
	})
	require.NoError(t, err)
	ts := newAPIServerWithVMAndStore(t, upstream.URL, store)

	resp, err := http.Get(ts.URL + "/api/v1/benches")
	require.NoError(t, err)
	defer resp.Body.Close()
	var benches []benchPair
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&benches))
	require.Equal(t, []benchPair{{Server: "current", Bench: "bench"}}, benches)

	resp, err = http.Get(ts.URL + "/api/v1/sites")
	require.NoError(t, err)
	defer resp.Body.Close()
	var sites []sitePair
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sites))
	require.Equal(t, []sitePair{{Server: "current", Bench: "bench", Site: "site.test", IsHealthy: 1}}, sites)

	resp, err = http.Get(ts.URL + "/api/v1/benches/deleted/old")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestListBenches_AggregatesByServerBench(t *testing.T) {
	vm := newVMStub()
	vm.responses[`count by (server, bench) (frappe_bench_apps_count)`] = vmInstantBody(
		[]map[string]string{
			{"server": "srv-a", "bench": "bench-1"},
			{"server": "srv-a", "bench": "bench-2"},
			{"server": "srv-b", "bench": "bench-1"},
		},
		[]string{"1", "1", "1"},
	)
	upstream := httptest.NewServer(vm.handler())
	defer upstream.Close()

	ts := newAPIServerWithVM(t, upstream.URL)
	resp, err := http.Get(ts.URL + "/api/v1/benches")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []benchPair
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.ElementsMatch(t, []benchPair{
		{Server: "srv-a", Bench: "bench-1"},
		{Server: "srv-a", Bench: "bench-2"},
		{Server: "srv-b", Bench: "bench-1"},
	}, got)
	require.Equal(t, "300", vm.steps[0], "instant queries need an explicit freshness window")
}

func TestListBenches_VMUnreachable_502(t *testing.T) {
	ts := newAPIServerWithVM(t, "http://127.0.0.1:1")
	resp, err := http.Get(ts.URL + "/api/v1/benches")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)
}

func TestGetBench_ReturnsAggregateDetail(t *testing.T) {
	vm := newVMStub()
	const selector = `{__name__=~"frappe_bench_(info|apps_count|web_workers_configured|background_workers_configured|supervisor_running|supervisor_total|redis_queue_depth|workers_registered|workers_busy|workers_idle|snapshot_timestamp_seconds|service_observed_at_seconds|service_state|service_pid|service_cpu_percent|service_memory_percent|service_rss_bytes|service_uptime_seconds)",server="srv-a",bench="bench-1"}`
	vm.responses[selector] = vmInstantBody(
		[]map[string]string{
			{"__name__": "frappe_bench_info", "frappe_version": "v15.42.1"},
			{"__name__": "frappe_bench_apps_count"},
			{"__name__": "frappe_bench_web_workers_configured"},
			{"__name__": "frappe_bench_background_workers_configured"},
			{"__name__": "frappe_bench_supervisor_running"},
			{"__name__": "frappe_bench_supervisor_total"},
			{"__name__": "frappe_bench_redis_queue_depth", "queue": "default"},
			{"__name__": "frappe_bench_redis_queue_depth", "queue": "long"},
			{"__name__": "frappe_bench_workers_registered"},
			{"__name__": "frappe_bench_workers_busy"},
			{"__name__": "frappe_bench_workers_idle"},
			{"__name__": "frappe_bench_snapshot_timestamp_seconds"},
			{"__name__": "frappe_bench_service_observed_at_seconds", "service": "bench-1-worker-default", "kind": "worker", "queue": "default"},
			{"__name__": "frappe_bench_service_state", "service": "bench-1-worker-default", "kind": "worker", "queue": "default"},
			{"__name__": "frappe_bench_service_pid", "service": "bench-1-worker-default", "kind": "worker", "queue": "default"},
			{"__name__": "frappe_bench_service_cpu_percent", "service": "bench-1-worker-default", "kind": "worker", "queue": "default"},
			{"__name__": "frappe_bench_service_memory_percent", "service": "bench-1-worker-default", "kind": "worker", "queue": "default"},
			{"__name__": "frappe_bench_service_rss_bytes", "service": "bench-1-worker-default", "kind": "worker", "queue": "default"},
			{"__name__": "frappe_bench_service_uptime_seconds", "service": "bench-1-worker-default", "kind": "worker", "queue": "default"},
			{"__name__": "frappe_bench_service_observed_at_seconds", "service": "stale-worker", "kind": "worker", "queue": "long"},
			{"__name__": "frappe_bench_service_state", "service": "stale-worker", "kind": "worker", "queue": "long"},
		},
		[]string{"1", "7", "17", "6", "5", "6", "3", "0", "4", "2", "2", "1714363200", "1714363200", "1", "4321", "81.25", "1.75", "268435456", "7200", "1714363199", "1"},
	)
	upstream := httptest.NewServer(vm.handler())
	defer upstream.Close()

	ts := newAPIServerWithVM(t, upstream.URL)
	resp, err := http.Get(ts.URL + "/api/v1/benches/srv-a/bench-1")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got benchDetail
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, "srv-a", got.Server)
	require.Equal(t, "bench-1", got.Bench)
	require.Equal(t, "v15.42.1", got.FrappeVersion)
	require.Equal(t, int64(7), got.AppsCount)
	require.Equal(t, int64(17), got.WebWorkersConfigured)
	require.Equal(t, int64(6), got.BackgroundWorkersConfigured)
	require.Equal(t, int64(5), got.SupervisorRun)
	require.Equal(t, int64(6), got.SupervisorTotal)
	require.Equal(t, int64(3), got.RedisQueues["default"])
	require.Equal(t, int64(0), got.RedisQueues["long"])
	require.Equal(t, int64(4), got.WorkersRegistered)
	require.Equal(t, int64(2), got.WorkersBusy)
	require.Equal(t, int64(2), got.WorkersIdle)
	require.Len(t, got.Services, 1, "services omitted from the latest snapshot must be filtered")
	require.Equal(t, "bench-1-worker-default", got.Services[0].Name)
	require.Equal(t, "worker", got.Services[0].Kind)
	require.Equal(t, "default", got.Services[0].Queue)
	require.Equal(t, int64(4321), got.Services[0].PID)
	require.Equal(t, 81.25, got.Services[0].CPUPercent)
	require.Equal(t, int64(268435456), got.Services[0].RSSBytes)
	require.Equal(t, []string{selector}, vm.calls, "bench detail should use one VM read")
}

func TestGetBench_PartialDataStillReturnsZeros(t *testing.T) {
	// VM stub returns an empty result for everything — handler should
	// still respond 200 with zero values rather than 5xx.
	vm := newVMStub()
	const selector = `{__name__=~"frappe_bench_(info|apps_count|web_workers_configured|background_workers_configured|supervisor_running|supervisor_total|redis_queue_depth|workers_registered|workers_busy|workers_idle|snapshot_timestamp_seconds|service_observed_at_seconds|service_state|service_pid|service_cpu_percent|service_memory_percent|service_rss_bytes|service_uptime_seconds)",server="srv-a",bench="bench-x"}`
	vm.responses[selector] = vmInstantBody(nil, nil)
	upstream := httptest.NewServer(vm.handler())
	defer upstream.Close()

	ts := newAPIServerWithVM(t, upstream.URL)
	resp, err := http.Get(ts.URL + "/api/v1/benches/srv-a/bench-x")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got benchDetail
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, "srv-a", got.Server)
	require.Equal(t, "bench-x", got.Bench)
	require.Equal(t, int64(0), got.AppsCount)
	require.NotNil(t, got.RedisQueues) // never nil; JSON serializes as {}
}

func TestListSites_AggregatesByServerBenchSite(t *testing.T) {
	vm := newVMStub()
	vm.responses[`max by (server, bench, site) (frappe_site_public_is_healthy) or max by (server, bench, site) (frappe_site_is_healthy)`] = vmInstantBody(
		[]map[string]string{
			{"server": "srv-a", "bench": "bench-1", "site": "alpha.test"},
			{"server": "srv-a", "bench": "bench-1", "site": "beta.test"},
		},
		[]string{"1", "1"},
	)
	upstream := httptest.NewServer(vm.handler())
	defer upstream.Close()

	ts := newAPIServerWithVM(t, upstream.URL)
	resp, err := http.Get(ts.URL + "/api/v1/sites")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got []sitePair
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.ElementsMatch(t, []sitePair{
		{Server: "srv-a", Bench: "bench-1", Site: "alpha.test", IsHealthy: 1},
		{Server: "srv-a", Bench: "bench-1", Site: "beta.test", IsHealthy: 1},
	}, got)
}

func TestGetSite_ReturnsAggregateDetail(t *testing.T) {
	vm := newVMStub()
	const labels = `{server="srv-a",bench="bench-1",site="alpha.test"}`
	const selector = `{__name__=~"frappe_site_(public_)?(http_status_code|is_healthy|failure_reason_code|http_response_ms|tls_cert_expiry_timestamp_seconds|native_monitor_enabled|database_identity)",server="srv-a",bench="bench-1",site="alpha.test"}`
	vm.responses[selector] = vmInstantBody(
		[]map[string]string{
			{"__name__": "frappe_site_http_status_code"},
			{"__name__": "frappe_site_is_healthy"},
			{"__name__": "frappe_site_failure_reason_code"},
			{"__name__": "frappe_site_http_response_ms"},
			{"__name__": "frappe_site_native_monitor_enabled"},
			{"__name__": "frappe_site_database_identity", "identity_hash": "0123456789abcdef01234567", "identity_kind": "database"},
			{"__name__": "frappe_site_database_identity", "identity_hash": "89abcdef0123456789abcdef", "identity_kind": "user"},
		},
		[]string{"200", "1", "0", "42.5", "1", "1", "1"},
	)
	vm.responses[`avg_over_time(frappe_site_public_http_response_ms`+labels+`[1h]) or avg_over_time(frappe_site_http_response_ms`+labels+`[1h])`] = vmInstantBody(
		[]map[string]string{{"server": "srv-a", "bench": "bench-1", "site": "alpha.test"}},
		[]string{"55.25"},
	)
	upstream := httptest.NewServer(vm.handler())
	defer upstream.Close()

	ts := newAPIServerWithVM(t, upstream.URL)
	resp, err := http.Get(ts.URL + "/api/v1/sites/srv-a/bench-1/alpha.test")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got siteDetail
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, "srv-a", got.Server)
	require.Equal(t, "bench-1", got.Bench)
	require.Equal(t, "alpha.test", got.Site)
	require.Equal(t, int64(200), got.HTTPStatusCode)
	require.Equal(t, int64(1), got.IsHealthy)
	require.Equal(t, int64(0), got.FailureReasonCode)
	require.InDelta(t, 42.5, got.HTTPResponseMs, 0.001)
	require.InDelta(t, 55.25, got.AvgResponseMs1h, 0.001)
	require.Equal(t, int64(1), got.NativeMonitorEnabled)
	require.Equal(t, []siteDatabaseIdentity{
		{Hash: "0123456789abcdef01234567", Kind: "database"},
		{Hash: "89abcdef0123456789abcdef", Kind: "user"},
	}, got.DatabaseIdentities)
	require.Equal(t, []string{selector, `avg_over_time(frappe_site_public_http_response_ms` + labels + `[1h]) or avg_over_time(frappe_site_http_response_ms` + labels + `[1h])`}, vm.calls)
}

func TestGetSite_PublicProbeOverridesOverallButKeepsInternalDetail(t *testing.T) {
	vm := newVMStub()
	const labels = `{server="srv-a",bench="bench-1",site="alpha.test"}`
	const selector = `{__name__=~"frappe_site_(public_)?(http_status_code|is_healthy|failure_reason_code|http_response_ms|tls_cert_expiry_timestamp_seconds|native_monitor_enabled|database_identity)",server="srv-a",bench="bench-1",site="alpha.test"}`
	vm.responses[selector] = vmInstantBody(
		[]map[string]string{
			{"__name__": "frappe_site_http_status_code"}, {"__name__": "frappe_site_is_healthy"},
			{"__name__": "frappe_site_http_response_ms"}, {"__name__": "frappe_site_failure_reason_code"},
			{"__name__": "frappe_site_public_http_status_code"}, {"__name__": "frappe_site_public_is_healthy"},
			{"__name__": "frappe_site_public_http_response_ms"}, {"__name__": "frappe_site_public_failure_reason_code"},
			{"__name__": "frappe_site_public_tls_cert_expiry_timestamp_seconds"},
		},
		[]string{"200", "1", "18", "0", "503", "0", "95", "6", "1735689600"},
	)
	vm.responses[`avg_over_time(frappe_site_public_http_response_ms`+labels+`[1h]) or avg_over_time(frappe_site_http_response_ms`+labels+`[1h])`] = vmInstantBody(nil, nil)
	upstream := httptest.NewServer(vm.handler())
	defer upstream.Close()
	ts := newAPIServerWithVM(t, upstream.URL)

	resp, err := http.Get(ts.URL + "/api/v1/sites/srv-a/bench-1/alpha.test")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got siteDetail
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.True(t, got.PublicProbeAvailable)
	require.Equal(t, int64(503), got.HTTPStatusCode)
	require.Equal(t, int64(0), got.IsHealthy)
	require.Equal(t, int64(200), got.InternalHTTPStatusCode)
	require.Equal(t, int64(1), got.InternalIsHealthy)
	require.Equal(t, int64(1735689600), got.PublicTLSCertExpiry)
}

func TestHierarchy_NotMountedWhenVMUnset(t *testing.T) {
	// MetricsBaseURL empty — none of the four endpoints should be
	// reachable; chi returns 404.
	r := NewRouter(Deps{})
	ts := httptest.NewServer(r)
	defer ts.Close()
	for _, p := range []string{
		"/api/v1/benches",
		"/api/v1/benches/srv/bench",
		"/api/v1/sites",
		"/api/v1/sites/srv/bench/site",
	} {
		resp, err := http.Get(ts.URL + p)
		require.NoError(t, err)
		resp.Body.Close()
		require.Equal(t, http.StatusNotFound, resp.StatusCode, "path %q", p)
	}
}

// Sanity check that URL-escaped site names with dots round-trip through
// the chi route.
func TestGetSite_SiteWithDotsInName(t *testing.T) {
	vm := newVMStub()
	const selector = `{__name__=~"frappe_site_(public_)?(http_status_code|is_healthy|failure_reason_code|http_response_ms|tls_cert_expiry_timestamp_seconds|native_monitor_enabled|database_identity)",server="srv-a",bench="bench-1",site="alpha.example.com"}`
	vm.responses[selector] = vmInstantBody(nil, nil)
	upstream := httptest.NewServer(vm.handler())
	defer upstream.Close()
	ts := newAPIServerWithVM(t, upstream.URL)

	site := url.PathEscape("alpha.example.com")
	resp, err := http.Get(ts.URL + "/api/v1/sites/srv-a/bench-1/" + site)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got siteDetail
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.Equal(t, "alpha.example.com", got.Site)
	// Verify the handler issued queries with the unescaped site label.
	saw := false
	for _, q := range vm.calls {
		if strings.Contains(q, `site="alpha.example.com"`) {
			saw = true
			break
		}
	}
	require.True(t, saw, "expected a VM query with site=\"alpha.example.com\"; got %v", vm.calls)
}
