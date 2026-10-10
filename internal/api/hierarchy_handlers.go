package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/internal/storage"
)

// hierarchyHandlers serves the bench / site list + detail endpoints.
// All four are derived from VictoriaMetrics — no new storage tables in
// Phase 5; the collector + VM is the source of truth for what benches
// and sites exist on which servers.
type hierarchyHandlers struct {
	metricsBaseURL string
	httpClient     *http.Client
	lookback       time.Duration
	logger         *slog.Logger
	store          storage.Store
}

func newHierarchyHandlers(metricsURL string, queryTimeout, lookback time.Duration, logger *slog.Logger, stores ...storage.Store) *hierarchyHandlers {
	if lookback <= 0 {
		lookback = 5 * time.Minute
	}
	h := &hierarchyHandlers{
		metricsBaseURL: strings.TrimRight(metricsURL, "/"),
		httpClient:     &http.Client{Timeout: queryTimeout},
		lookback:       lookback,
		logger:         logger,
	}
	if len(stores) > 0 {
		h.store = stores[0]
	}
	return h
}

// registeredServerNames prevents retained VictoriaMetrics series from deleted
// or renamed targets from reappearing in the live fleet. A nil store keeps the
// standalone handler useful in focused tests and API-only embeddings.
func (h *hierarchyHandlers) registeredServerNames(ctx context.Context) (map[string]struct{}, bool, error) {
	if h.store == nil {
		return nil, false, nil
	}
	servers, err := h.store.ListServers(ctx)
	if err != nil {
		return nil, true, fmt.Errorf("list registered servers: %w", err)
	}
	names := make(map[string]struct{}, len(servers))
	for _, server := range servers {
		if server != nil && server.Name != "" {
			names[server.Name] = struct{}{}
		}
	}
	return names, true, nil
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
	v.Set("step", strconv.FormatInt(max(1, int64(h.lookback.Seconds())), 10))
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
	registered, filter, err := h.registeredServerNames(r.Context())
	if err != nil {
		h.logger.Warn("listBenches: registry query failed", "err", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if filter && len(registered) == 0 {
		writeJSON(w, http.StatusOK, []benchPair{})
		return
	}
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
		if _, ok := registered[server]; filter && !ok {
			continue
		}
		out = append(out, benchPair{Server: server, Bench: bench})
	}
	writeJSON(w, http.StatusOK, out)
}

type benchDetail struct {
	Server                      string           `json:"server"`
	Bench                       string           `json:"bench"`
	FrappeVersion               string           `json:"frappe_version,omitempty"`
	AppsCount                   int64            `json:"apps_count"`
	WebWorkersConfigured        int64            `json:"web_workers_configured"`
	BackgroundWorkersConfigured int64            `json:"background_workers_configured"`
	SupervisorRun               int64            `json:"supervisor_running"`
	SupervisorTotal             int64            `json:"supervisor_total"`
	RedisQueues                 map[string]int64 `json:"redis_queues"`
	WorkersRegistered           int64            `json:"workers_registered"`
	WorkersBusy                 int64            `json:"workers_busy"`
	WorkersIdle                 int64            `json:"workers_idle"`
	Services                    []serviceDetail  `json:"services"`
}

type serviceDetail struct {
	Name          string  `json:"name"`
	Kind          string  `json:"kind"`
	Queue         string  `json:"queue,omitempty"`
	State         int64   `json:"state"`
	PID           int64   `json:"pid"`
	CPUPercent    float64 `json:"cpu_percent"`
	MemoryPercent float64 `json:"memory_percent"`
	RSSBytes      int64   `json:"rss_bytes"`
	UptimeSeconds int64   `json:"uptime_seconds"`
	// ObservedAtSeconds is an internal snapshot-membership marker. It is not
	// serialized because consumers only need the already-filtered topology.
	ObservedAtSeconds int64 `json:"-"`
}

func (h *hierarchyHandlers) getBench(w http.ResponseWriter, r *http.Request) {
	server := chi.URLParam(r, "server")
	bench := chi.URLParam(r, "bench")
	if server == "" || bench == "" {
		writeErr(w, http.StatusBadRequest, "server and bench are required")
		return
	}
	registered, filter, err := h.registeredServerNames(r.Context())
	if err != nil {
		h.logger.Warn("getBench: registry query failed", "err", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, ok := registered[server]; filter && !ok {
		writeErr(w, http.StatusNotFound, "server is not registered")
		return
	}

	out := benchDetail{Server: server, Bench: bench, RedisQueues: map[string]int64{}, Services: []serviceDetail{}}
	selector := fmt.Sprintf(
		`{__name__=~"frappe_bench_(info|apps_count|web_workers_configured|background_workers_configured|supervisor_running|supervisor_total|redis_queue_depth|workers_registered|workers_busy|workers_idle|snapshot_timestamp_seconds|service_observed_at_seconds|service_state|service_pid|service_cpu_percent|service_memory_percent|service_rss_bytes|service_uptime_seconds)",server=%s,bench=%s}`,
		strconv.Quote(server), strconv.Quote(bench),
	)
	resp, err := h.queryInstant(r.Context(), selector)
	if err != nil {
		h.logger.Warn("getBench: vm query failed", "server", server, "bench", bench, "err", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	intFields := map[string]*int64{
		"frappe_bench_apps_count":                    &out.AppsCount,
		"frappe_bench_web_workers_configured":        &out.WebWorkersConfigured,
		"frappe_bench_background_workers_configured": &out.BackgroundWorkersConfigured,
		"frappe_bench_supervisor_running":            &out.SupervisorRun,
		"frappe_bench_supervisor_total":              &out.SupervisorTotal,
		"frappe_bench_workers_registered":            &out.WorkersRegistered,
		"frappe_bench_workers_busy":                  &out.WorkersBusy,
		"frappe_bench_workers_idle":                  &out.WorkersIdle,
	}
	services := map[string]*serviceDetail{}
	var snapshotTimestamp int64
	for _, s := range resp.Data.Result {
		metric := s.Metric["__name__"]
		switch metric {
		case "frappe_bench_info":
			if out.FrappeVersion == "" {
				out.FrappeVersion = s.Metric["frappe_version"]
			}
		case "frappe_bench_redis_queue_depth":
			q := s.Metric["queue"]
			if q == "" {
				continue
			}
			v, err := scalarValue(s.Value)
			if err != nil {
				continue
			}
			out.RedisQueues[q] = int64(v)
		case "frappe_bench_snapshot_timestamp_seconds":
			value, err := scalarValue(s.Value)
			if err == nil {
				snapshotTimestamp = int64(value)
			}
		case "frappe_bench_service_observed_at_seconds", "frappe_bench_service_state", "frappe_bench_service_pid", "frappe_bench_service_cpu_percent", "frappe_bench_service_memory_percent", "frappe_bench_service_rss_bytes", "frappe_bench_service_uptime_seconds":
			name := s.Metric["service"]
			if name == "" {
				continue
			}
			service := services[name]
			if service == nil {
				service = &serviceDetail{Name: name, Kind: s.Metric["kind"], Queue: s.Metric["queue"]}
				services[name] = service
			}
			value, err := scalarValue(s.Value)
			if err != nil {
				continue
			}
			switch metric {
			case "frappe_bench_service_observed_at_seconds":
				service.ObservedAtSeconds = int64(value)
			case "frappe_bench_service_state":
				service.State = int64(value)
			case "frappe_bench_service_pid":
				service.PID = int64(value)
			case "frappe_bench_service_cpu_percent":
				service.CPUPercent = value
			case "frappe_bench_service_memory_percent":
				service.MemoryPercent = value
			case "frappe_bench_service_rss_bytes":
				service.RSSBytes = int64(value)
			case "frappe_bench_service_uptime_seconds":
				service.UptimeSeconds = int64(value)
			}
		default:
			dst, ok := intFields[metric]
			if !ok {
				continue
			}
			v, err := scalarValue(s.Value)
			if err == nil {
				*dst = int64(v)
			}
		}
	}
	for _, service := range services {
		// Dynamic service metrics are snapshot data. Only include a process
		// observed by the latest completed collection; older series can remain
		// queryable during VictoriaMetrics' lookback window.
		if snapshotTimestamp > 0 && service.ObservedAtSeconds != snapshotTimestamp {
			continue
		}
		out.Services = append(out.Services, *service)
	}
	slices.SortFunc(out.Services, func(a, b serviceDetail) int { return strings.Compare(a.Name, b.Name) })

	writeJSON(w, http.StatusOK, out)
}

// --- sites -----------------------------------------------------------------

type sitePair struct {
	Server    string `json:"server"`
	Bench     string `json:"bench"`
	Site      string `json:"site"`
	IsHealthy int64  `json:"is_healthy"`
}

func (h *hierarchyHandlers) listSites(w http.ResponseWriter, r *http.Request) {
	registered, filter, err := h.registeredServerNames(r.Context())
	if err != nil {
		h.logger.Warn("listSites: registry query failed", "err", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if filter && len(registered) == 0 {
		writeJSON(w, http.StatusOK, []sitePair{})
		return
	}
	resp, err := h.queryInstant(r.Context(),
		`max by (server, bench, site) (frappe_site_public_is_healthy) or max by (server, bench, site) (frappe_site_is_healthy)`)
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
		if _, ok := registered[server]; filter && !ok {
			continue
		}
		value, err := scalarValue(r.Value)
		if err != nil {
			continue
		}
		out = append(out, sitePair{Server: server, Bench: bench, Site: site, IsHealthy: int64(value)})
	}
	writeJSON(w, http.StatusOK, out)
}

type siteDetail struct {
	Server                    string                 `json:"server"`
	Bench                     string                 `json:"bench"`
	Site                      string                 `json:"site"`
	HTTPStatusCode            int64                  `json:"http_status_code"`
	HTTPResponseMs            float64                `json:"http_response_ms"`
	IsHealthy                 int64                  `json:"is_healthy"`
	FailureReasonCode         int64                  `json:"failure_reason_code"`
	InternalHTTPStatusCode    int64                  `json:"internal_http_status_code"`
	InternalHTTPResponseMs    float64                `json:"internal_http_response_ms"`
	InternalIsHealthy         int64                  `json:"internal_is_healthy"`
	InternalFailureReasonCode int64                  `json:"internal_failure_reason_code"`
	PublicProbeAvailable      bool                   `json:"public_probe_available"`
	PublicHTTPStatusCode      int64                  `json:"public_http_status_code"`
	PublicHTTPResponseMs      float64                `json:"public_http_response_ms"`
	PublicIsHealthy           int64                  `json:"public_is_healthy"`
	PublicFailureReasonCode   int64                  `json:"public_failure_reason_code"`
	PublicTLSCertExpiry       int64                  `json:"public_tls_cert_expiry_timestamp_seconds"`
	NativeMonitorEnabled      int64                  `json:"native_monitor_enabled"`
	DatabaseIdentities        []siteDatabaseIdentity `json:"database_identities"`
	// Last hour's average response time. quantile_over_time would be
	// preferred for p95 but isn't always cheap; avg_over_time(1h) is
	// the Phase 5 v1 approximation.
	AvgResponseMs1h float64 `json:"avg_response_ms_1h"`
}

type siteDatabaseIdentity struct {
	Hash string `json:"hash"`
	Kind string `json:"kind"`
}

func (h *hierarchyHandlers) getSite(w http.ResponseWriter, r *http.Request) {
	server := chi.URLParam(r, "server")
	bench := chi.URLParam(r, "bench")
	site := chi.URLParam(r, "site")
	if server == "" || bench == "" || site == "" {
		writeErr(w, http.StatusBadRequest, "server, bench, and site are required")
		return
	}
	registered, filter, err := h.registeredServerNames(r.Context())
	if err != nil {
		h.logger.Warn("getSite: registry query failed", "err", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, ok := registered[server]; filter && !ok {
		writeErr(w, http.StatusNotFound, "server is not registered")
		return
	}

	out := siteDetail{
		Server: server, Bench: bench, Site: site,
		IsHealthy: -1, FailureReasonCode: 4,
		InternalIsHealthy: -1, InternalFailureReasonCode: 4,
		PublicIsHealthy: -1, PublicFailureReasonCode: -1,
		DatabaseIdentities: make([]siteDatabaseIdentity, 0, 2),
	}
	labels := fmt.Sprintf(`server=%s,bench=%s,site=%s`,
		strconv.Quote(server), strconv.Quote(bench), strconv.Quote(site))
	selector := fmt.Sprintf(
		`{__name__=~"frappe_site_(public_)?(http_status_code|is_healthy|failure_reason_code|http_response_ms|tls_cert_expiry_timestamp_seconds|native_monitor_enabled|database_identity)",%s}`,
		labels,
	)
	resp, err := h.queryInstant(r.Context(), selector)
	if err != nil {
		h.logger.Warn("getSite: vm query failed", "server", server, "bench", bench, "site", site, "err", err)
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	intFields := map[string]*int64{
		"frappe_site_http_status_code":                         &out.InternalHTTPStatusCode,
		"frappe_site_is_healthy":                               &out.InternalIsHealthy,
		"frappe_site_failure_reason_code":                      &out.InternalFailureReasonCode,
		"frappe_site_public_http_status_code":                  &out.PublicHTTPStatusCode,
		"frappe_site_public_is_healthy":                        &out.PublicIsHealthy,
		"frappe_site_public_failure_reason_code":               &out.PublicFailureReasonCode,
		"frappe_site_public_tls_cert_expiry_timestamp_seconds": &out.PublicTLSCertExpiry,
		"frappe_site_native_monitor_enabled":                   &out.NativeMonitorEnabled,
	}
	for _, sample := range resp.Data.Result {
		metric := sample.Metric["__name__"]
		if metric == "frappe_site_database_identity" {
			hash, kind := sample.Metric["identity_hash"], sample.Metric["identity_kind"]
			if len(hash) == 24 && strings.Trim(hash, "0123456789abcdef") == "" && (kind == "database" || kind == "user") {
				identity := siteDatabaseIdentity{Hash: hash, Kind: kind}
				if !slices.Contains(out.DatabaseIdentities, identity) {
					out.DatabaseIdentities = append(out.DatabaseIdentities, identity)
				}
			}
			continue
		}
		value, err := scalarValue(sample.Value)
		if err != nil {
			continue
		}
		if metric == "frappe_site_http_response_ms" {
			out.InternalHTTPResponseMs = value
			continue
		}
		if metric == "frappe_site_public_http_response_ms" {
			out.PublicHTTPResponseMs = value
			out.PublicProbeAvailable = true
			continue
		}
		if dst, ok := intFields[metric]; ok {
			*dst = int64(value)
			if strings.HasPrefix(metric, "frappe_site_public_") {
				out.PublicProbeAvailable = true
			}
		}
	}
	slices.SortFunc(out.DatabaseIdentities, func(a, b siteDatabaseIdentity) int {
		if byKind := strings.Compare(a.Kind, b.Kind); byKind != 0 {
			return byKind
		}
		return strings.Compare(a.Hash, b.Hash)
	})
	out.HTTPStatusCode = out.InternalHTTPStatusCode
	out.HTTPResponseMs = out.InternalHTTPResponseMs
	out.IsHealthy = out.InternalIsHealthy
	out.FailureReasonCode = out.InternalFailureReasonCode
	if out.PublicProbeAvailable {
		out.HTTPStatusCode = out.PublicHTTPStatusCode
		out.HTTPResponseMs = out.PublicHTTPResponseMs
		out.IsHealthy = out.PublicIsHealthy
		out.FailureReasonCode = out.PublicFailureReasonCode
	}

	// 1h average.
	resp, err = h.queryInstant(r.Context(),
		fmt.Sprintf(`avg_over_time(frappe_site_public_http_response_ms{%s}[1h]) or avg_over_time(frappe_site_http_response_ms{%s}[1h])`, labels, labels))
	if err == nil && len(resp.Data.Result) > 0 {
		v, err := scalarValue(resp.Data.Result[0].Value)
		if err == nil {
			out.AvgResponseMs1h = v
		}
	}

	writeJSON(w, http.StatusOK, out)
}
