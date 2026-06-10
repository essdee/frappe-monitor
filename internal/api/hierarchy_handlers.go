package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// hierarchyHandlers serves the bench / site list + detail endpoints.
// All four are derived from VictoriaMetrics — no new storage tables in
// Phase 5; the collector + VM is the source of truth for what benches
// and sites exist on which servers.
type hierarchyHandlers struct {
	metricsBaseURL string
	httpClient     *http.Client
	logger         *slog.Logger
}

func newHierarchyHandlers(metricsURL string, queryTimeout time.Duration, logger *slog.Logger) *hierarchyHandlers {
	return &hierarchyHandlers{
		metricsBaseURL: strings.TrimRight(metricsURL, "/"),
		httpClient:     &http.Client{Timeout: queryTimeout},
		logger:         logger,
	}
}

// vmInstantResponse mirrors VictoriaMetrics's /api/v1/query response shape.
type vmInstantResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			// Value is [unix_seconds_float, "value_string"].
			Value [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

// queryInstant runs a PromQL query against VM's /api/v1/query.
func (h *hierarchyHandlers) queryInstant(ctx context.Context, promql string) (*vmInstantResponse, error) {
	v := url.Values{}
	v.Set("query", promql)
	full := h.metricsBaseURL + "/api/v1/query?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("build vm request: %w", err)
	}
	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vm unreachable: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("vm HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out vmInstantResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("vm response decode: %w", err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("vm error: %s", out.Error)
	}
	return &out, nil
}

// escapePromQLLabel escapes a string for safe interpolation inside a
// PromQL double-quoted label matcher. Backslash and double-quote are
// escaped per the PromQL string grammar, and newlines/CRs are stripped,
// so a path param can't break out of the matcher and inject arbitrary
// PromQL (e.g. `x"} or on() frappe_bench_info{bench="`).
func escapePromQLLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", "")
	v = strings.ReplaceAll(v, "\r", "")
	return v
}

// scalarValue extracts a float64 from a vmInstantResponse result's Value.
// VM emits values as ["<ts_seconds>","<sample_string>"]; the second
// element is what we parse.
func scalarValue(raw [2]json.RawMessage) (float64, error) {
	var s string
	if err := json.Unmarshal(raw[1], &s); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(s, 64)
}

func (h *hierarchyHandlers) mount(r chi.Router) {
	r.Get("/benches", h.listBenches)
	r.Get("/benches/{server}/{bench}", h.getBench)
	r.Get("/sites", h.listSites)
	r.Get("/sites/{server}/{bench}/{site}", h.getSite)
}

// --- benches ---------------------------------------------------------------

type benchPair struct {
	Server string `json:"server"`
	Bench  string `json:"bench"`
}

func (h *hierarchyHandlers) listBenches(w http.ResponseWriter, r *http.Request) {
	resp, err := h.queryInstant(r.Context(),
		`count by (server, bench) (frappe_bench_apps_count)`)
	if err != nil {
		h.logger.Warn("listBenches: vm query failed", "err", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := make([]benchPair, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		server := r.Metric["server"]
		bench := r.Metric["bench"]
		if server == "" || bench == "" {
			continue
		}
		out = append(out, benchPair{Server: server, Bench: bench})
	}
	writeJSON(w, http.StatusOK, out)
}

type benchDetail struct {
	Server          string             `json:"server"`
	Bench           string             `json:"bench"`
	FrappeVersion   string             `json:"frappe_version,omitempty"`
	AppsCount       int64              `json:"apps_count"`
	SupervisorRun   int64              `json:"supervisor_running"`
	SupervisorTotal int64              `json:"supervisor_total"`
	RedisQueues     map[string]int64   `json:"redis_queues"`
}

func (h *hierarchyHandlers) getBench(w http.ResponseWriter, r *http.Request) {
	server := chi.URLParam(r, "server")
	bench := chi.URLParam(r, "bench")
	if server == "" || bench == "" {
		writeErr(w, http.StatusBadRequest, "server and bench are required")
		return
	}

	out := benchDetail{Server: server, Bench: bench, RedisQueues: map[string]int64{}}

	// Escape path params before interpolating into PromQL label matchers
	// so a value containing a quote/brace can't break out and inject.
	qs, qb := escapePromQLLabel(server), escapePromQLLabel(bench)

	// info{frappe_version} — pick any series matching the labels.
	if resp, err := h.queryInstant(r.Context(),
		fmt.Sprintf(`frappe_bench_info{server="%s",bench="%s"}`, qs, qb)); err == nil {
		for _, s := range resp.Data.Result {
			if v, ok := s.Metric["frappe_version"]; ok {
				out.FrappeVersion = v
				break
			}
		}
	}

	// Single-value fields.
	intFields := map[string]*int64{
		"frappe_bench_apps_count":          &out.AppsCount,
		"frappe_bench_supervisor_running":  &out.SupervisorRun,
		"frappe_bench_supervisor_total":    &out.SupervisorTotal,
	}
	for metric, dst := range intFields {
		resp, err := h.queryInstant(r.Context(),
			fmt.Sprintf(`%s{server="%s",bench="%s"}`, metric, qs, qb))
		if err != nil || len(resp.Data.Result) == 0 {
			continue
		}
		v, err := scalarValue(resp.Data.Result[0].Value)
		if err == nil {
			*dst = int64(v)
		}
	}

	// Per-queue depths.
	if resp, err := h.queryInstant(r.Context(),
		fmt.Sprintf(`frappe_bench_redis_queue_depth{server="%s",bench="%s"}`, qs, qb)); err == nil {
		for _, s := range resp.Data.Result {
			q := s.Metric["queue"]
			if q == "" {
				continue
			}
			v, err := scalarValue(s.Value)
			if err != nil {
				continue
			}
			out.RedisQueues[q] = int64(v)
		}
	}

	writeJSON(w, http.StatusOK, out)
}

// --- sites -----------------------------------------------------------------

type sitePair struct {
	Server string `json:"server"`
	Bench  string `json:"bench"`
	Site   string `json:"site"`
}

func (h *hierarchyHandlers) listSites(w http.ResponseWriter, r *http.Request) {
	resp, err := h.queryInstant(r.Context(),
		`count by (server, bench, site) (frappe_site_is_healthy)`)
	if err != nil {
		h.logger.Warn("listSites: vm query failed", "err", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	out := make([]sitePair, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		server := r.Metric["server"]
		bench := r.Metric["bench"]
		site := r.Metric["site"]
		if server == "" || bench == "" || site == "" {
			continue
		}
		out = append(out, sitePair{Server: server, Bench: bench, Site: site})
	}
	writeJSON(w, http.StatusOK, out)
}

type siteDetail struct {
	Server          string  `json:"server"`
	Bench           string  `json:"bench"`
	Site            string  `json:"site"`
	HTTPStatusCode  int64   `json:"http_status_code"`
	HTTPResponseMs  float64 `json:"http_response_ms"`
	IsHealthy       int64   `json:"is_healthy"`
	// Last hour's average response time. quantile_over_time would be
	// preferred for p95 but isn't always cheap; avg_over_time(1h) is
	// the Phase 5 v1 approximation.
	AvgResponseMs1h float64 `json:"avg_response_ms_1h"`
}

func (h *hierarchyHandlers) getSite(w http.ResponseWriter, r *http.Request) {
	server := chi.URLParam(r, "server")
	bench := chi.URLParam(r, "bench")
	site := chi.URLParam(r, "site")
	if server == "" || bench == "" || site == "" {
		writeErr(w, http.StatusBadRequest, "server, bench, and site are required")
		return
	}

	out := siteDetail{Server: server, Bench: bench, Site: site}
	// Escape path params before interpolating into the PromQL matcher.
	labels := fmt.Sprintf(`server="%s",bench="%s",site="%s"`,
		escapePromQLLabel(server), escapePromQLLabel(bench), escapePromQLLabel(site))

	intFields := map[string]*int64{
		"frappe_site_http_status_code": &out.HTTPStatusCode,
		"frappe_site_is_healthy":       &out.IsHealthy,
	}
	for metric, dst := range intFields {
		resp, err := h.queryInstant(r.Context(),
			fmt.Sprintf(`%s{%s}`, metric, labels))
		if err != nil || len(resp.Data.Result) == 0 {
			continue
		}
		v, err := scalarValue(resp.Data.Result[0].Value)
		if err == nil {
			*dst = int64(v)
		}
	}

	// Float fields.
	floatFields := map[string]*float64{
		"frappe_site_http_response_ms": &out.HTTPResponseMs,
	}
	for metric, dst := range floatFields {
		resp, err := h.queryInstant(r.Context(),
			fmt.Sprintf(`%s{%s}`, metric, labels))
		if err != nil || len(resp.Data.Result) == 0 {
			continue
		}
		v, err := scalarValue(resp.Data.Result[0].Value)
		if err == nil {
			*dst = v
		}
	}

	// 1h average.
	resp, err := h.queryInstant(r.Context(),
		fmt.Sprintf(`avg_over_time(frappe_site_http_response_ms{%s}[1h])`, labels))
	if err == nil && len(resp.Data.Result) > 0 {
		v, err := scalarValue(resp.Data.Result[0].Value)
		if err == nil {
			out.AvgResponseMs1h = v
		}
	}

	writeJSON(w, http.StatusOK, out)
}
