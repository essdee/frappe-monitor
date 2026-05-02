package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/internal/alerts"
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

	// Phase 7 auth. Empty means no auth (dev / behind-internal-network
	// deploys). Non-empty applies HTTP basic auth to every /api/v1/*
	// route and the SPA — browsers handle the credential prompt
	// natively. /healthz stays unauthenticated so external probes work.
	AuthPassword string
	AuthRealm    string

	// OnServerCreated and OnServerDeleted are optional lifecycle hooks
	// the API calls after a successful server CRUD operation. Used by
	// main to register/deregister a scheduler entry so newly-added
	// servers start collecting on the next tick without a process
	// restart. Nil hooks are a silent no-op (tests don't need them).
	OnServerCreated func(serverID int)
	OnServerDeleted func(serverID int)

	// Phase 6 alerts: passed when alerts.Service is enabled so the
	// Alerts page in the dashboard can render configured rules and
	// firing state. AlertsRules nil/empty is fine (the page just
	// shows "no rules configured").
	AlertsRules   []alerts.Rule
	AlertsEnabled bool
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

	auth := basicAuth(d.AuthPassword, d.AuthRealm)

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(auth)
		h := &serverHandlers{
			store:           d.Store,
			exec:            d.Executor,
			logger:          d.Logger,
			onServerCreated: d.OnServerCreated,
			onServerDeleted: d.OnServerDeleted,
		}
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

		// Phase 5 hierarchy endpoints derive bench + site lists from
		// VM directly, so they only mount when MetricsBaseURL is set.
		if d.MetricsBaseURL != "" {
			hh := newHierarchyHandlers(d.MetricsBaseURL, d.MetricsQueryTimeout, d.Logger)
			hh.mount(api)
		}

		// Phase 6 alerts read endpoint. Always mounted — when alerts
		// are disabled it returns enabled=false with whatever rules
		// were configured (typically none).
		ah := &alertHandlers{
			store:   d.Store,
			rules:   d.AlertsRules,
			enabled: d.AlertsEnabled,
			logger:  d.Logger,
		}
		api.Get("/alerts", ah.list)
	})

	// SPA mount: every non-/api, non-/healthz path is delegated to the
	// embedded dashboard. Auth wraps the SPA too so the dashboard
	// itself isn't accessible without credentials. The web handler
	// serves real assets when the path matches and falls back to
	// index.html for client-side routes. Registered last so /api/v1/*
	// and /healthz take precedence (chi resolves more specific routes
	// first).
	r.Handle("/*", auth(webpkg.Handler()))

	return r
}
