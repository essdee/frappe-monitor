package api

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// proxyHandlers holds the upstream URLs for the metrics and logs query
// proxies. Both are pure read-side: the API client can send PromQL /
// LogQL through the same origin as the SPA, with no CORS gymnastics
// and (in Phase 7) auth gating.
type proxyHandlers struct {
	metricsBaseURL string
	logsBaseURL    string
	metricsClient  *http.Client
	logsClient     *http.Client
	logger         *slog.Logger
}

func newProxyHandlers(metricsURL, logsURL string,
	metricsTimeout, logsTimeout time.Duration,
	logger *slog.Logger) *proxyHandlers {
	return &proxyHandlers{
		metricsBaseURL: strings.TrimRight(metricsURL, "/"),
		logsBaseURL:    strings.TrimRight(logsURL, "/"),
		metricsClient:  &http.Client{Timeout: metricsTimeout},
		logsClient:     &http.Client{Timeout: logsTimeout},
		logger:         logger,
	}
}

// metricsQuery proxies GET /api/v1/metrics/query?query=&start=&end=&step=
// to <metrics_base>/api/v1/query_range with the same query string.
//
// Pass-through: param names are kept identical to VictoriaMetrics's
// (which is also Prometheus-compatible) so frontend code can use VM/
// Prom docs verbatim.
func (h *proxyHandlers) metricsQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if strings.TrimSpace(q.Get("query")) == "" {
		writeErr(w, http.StatusBadRequest, "missing 'query' parameter")
		return
	}
	upstream := h.metricsBaseURL + "/api/v1/query_range?" + r.URL.RawQuery
	h.proxy(w, r, h.metricsClient, upstream, "metrics")
}

// logsQuery proxies GET /api/v1/logs/query?query=&start=&end=&limit=
// to <logs_base>/loki/api/v1/query_range with the same query string.
func (h *proxyHandlers) logsQuery(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if strings.TrimSpace(q.Get("query")) == "" {
		writeErr(w, http.StatusBadRequest, "missing 'query' parameter")
		return
	}
	upstream := h.logsBaseURL + "/loki/api/v1/query_range?" + r.URL.RawQuery
	h.proxy(w, r, h.logsClient, upstream, "logs")
}

// proxy is the shared forwarder: builds an upstream GET request,
// streams the response body back, copies content-type. Errors map to
// 502 (bad gateway) so the caller can distinguish backend faults from
// our own faults (which would be 5xx via the recoverer).
func (h *proxyHandlers) proxy(
	w http.ResponseWriter, r *http.Request,
	client *http.Client, upstream, kind string,
) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, upstream, nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Sprintf("build %s request: %v", kind, err))
		return
	}

	resp, err := client.Do(req)
	if err != nil {
		h.logger.Warn("proxy: upstream call failed",
			"kind", kind, "url", upstream, "err", err)
		writeErr(w, http.StatusBadGateway,
			fmt.Sprintf("%s upstream unreachable: %v", kind, err))
		return
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	// Mirror upstream content-type and status.
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
