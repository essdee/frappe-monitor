package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
	webpkg "frappe-monitor/internal/web"
)

type Deps struct {
	Store    storage.Store
	Executor sshpkg.Executor
	Logger   *slog.Logger

	// Phase 4 query proxies. If MetricsBaseURL or LogsBaseURL is "",
	// the corresponding endpoint is not mounted (a fresh dev binary
	// without a configured backend just 404s on /api/v1/metrics/query
	// and /api/v1/logs/query).
	MetricsBaseURL      string
	LogsBaseURL         string
	MetricsQueryTimeout time.Duration
	LogsQueryTimeout    time.Duration
}

func NewRouter(d Deps) http.Handler {
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	r := chi.NewRouter()
	// Order matters: recoverer must be outermost so a panic in any inner
	// middleware (including the logger) is caught and turned into a 500.
	r.Use(recoverer(d.Logger))
	r.Use(requestLogger(d.Logger))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/api/v1", func(api chi.Router) {
		h := &serverHandlers{store: d.Store, exec: d.Executor, logger: d.Logger}
		h.mount(api)

		if d.MetricsBaseURL != "" || d.LogsBaseURL != "" {
			ph := newProxyHandlers(
				d.MetricsBaseURL, d.LogsBaseURL,
				d.MetricsQueryTimeout, d.LogsQueryTimeout,
				d.Logger,
			)
			if d.MetricsBaseURL != "" {
				api.Get("/metrics/query", ph.metricsQuery)
			}
			if d.LogsBaseURL != "" {
				api.Get("/logs/query", ph.logsQuery)
			}
		}
	})

	// SPA mount: every non-/api, non-/healthz path is delegated to the
	// embedded dashboard. The web handler serves real assets when the
	// path matches and falls back to index.html for client-side routes.
	// Registered last so /api/v1/* and /healthz take precedence (chi
	// resolves more specific routes first).
	r.Handle("/*", webpkg.Handler())

	return r
}
