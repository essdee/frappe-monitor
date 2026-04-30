package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// vmStub is a tiny fake VictoriaMetrics that returns canned responses
// keyed by exact PromQL string. Anything unmatched returns an error JSON
// so tests fail loudly if the handler issues an unexpected query.
type vmStub struct {
	responses map[string]string
	calls     []string
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
	const labels = `{server="srv-a",bench="bench-1"}`
	vm.responses[`frappe_bench_info`+labels] = vmInstantBody(
		[]map[string]string{{"server": "srv-a", "bench": "bench-1", "frappe_version": "v15.42.1"}},
		[]string{"1"},
	)
	vm.responses[`frappe_bench_apps_count`+labels] = vmInstantBody(
		[]map[string]string{{"server": "srv-a", "bench": "bench-1"}},
		[]string{"7"},
	)
	vm.responses[`frappe_bench_supervisor_running`+labels] = vmInstantBody(
		[]map[string]string{{"server": "srv-a", "bench": "bench-1"}},
		[]string{"5"},
	)
	vm.responses[`frappe_bench_supervisor_total`+labels] = vmInstantBody(
		[]map[string]string{{"server": "srv-a", "bench": "bench-1"}},
		[]string{"6"},
	)
	vm.responses[`frappe_bench_redis_queue_depth`+labels] = vmInstantBody(
		[]map[string]string{
			{"server": "srv-a", "bench": "bench-1", "queue": "default"},
			{"server": "srv-a", "bench": "bench-1", "queue": "long"},
		},
		[]string{"3", "0"},
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
	require.Equal(t, int64(5), got.SupervisorRun)
	require.Equal(t, int64(6), got.SupervisorTotal)
	require.Equal(t, int64(3), got.RedisQueues["default"])
	require.Equal(t, int64(0), got.RedisQueues["long"])
}

func TestGetBench_PartialDataStillReturnsZeros(t *testing.T) {
	// VM stub returns an empty result for everything — handler should
	// still respond 200 with zero values rather than 5xx.
	vm := newVMStub()
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
	vm.responses[`count by (server, bench, site) (frappe_site_is_healthy)`] = vmInstantBody(
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
		{Server: "srv-a", Bench: "bench-1", Site: "alpha.test"},
		{Server: "srv-a", Bench: "bench-1", Site: "beta.test"},
	}, got)
}

func TestGetSite_ReturnsAggregateDetail(t *testing.T) {
	vm := newVMStub()
	const labels = `{server="srv-a",bench="bench-1",site="alpha.test"}`
	vm.responses[`frappe_site_http_status_code`+labels] = vmInstantBody(
		[]map[string]string{{"server": "srv-a", "bench": "bench-1", "site": "alpha.test"}},
		[]string{"200"},
	)
	vm.responses[`frappe_site_is_healthy`+labels] = vmInstantBody(
		[]map[string]string{{"server": "srv-a", "bench": "bench-1", "site": "alpha.test"}},
		[]string{"1"},
	)
	vm.responses[`frappe_site_http_response_ms`+labels] = vmInstantBody(
		[]map[string]string{{"server": "srv-a", "bench": "bench-1", "site": "alpha.test"}},
		[]string{"42.5"},
	)
	vm.responses[`avg_over_time(frappe_site_http_response_ms`+labels+`[1h])`] = vmInstantBody(
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
	require.InDelta(t, 42.5, got.HTTPResponseMs, 0.001)
	require.InDelta(t, 55.25, got.AvgResponseMs1h, 0.001)
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
