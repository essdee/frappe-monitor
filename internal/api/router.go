package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/internal/alerts"
	"frappe-monitor/internal/config"
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
	// MetricsLookback is passed as the instant-query step so samples remain
	// visible between collection cycles even when operators choose an
	// interval longer than VictoriaMetrics' default lookback.
	MetricsLookback  time.Duration
	LogsQueryTimeout time.Duration

	// Auth. Empty means no auth. Non-empty protects /api/v1/* with a
	// signed session cookie (SPA) or HTTP basic credentials (automation).
	// /healthz and the SPA shell stay open so the login view can load.
	AuthPassword      string
	AuthRealm         string
	DisplayTimezone   string
	TrustedProxyCIDRs []string

	// OnServerCreated and OnServerDeleted are optional lifecycle hooks
	// the API calls after a successful server CRUD operation. Used by
	// main to register/deregister a scheduler entry so newly-added
	// servers start collecting on the next tick without a process
	// restart. Nil hooks are a silent no-op (tests don't need them).
	OnServerCreated func(serverID int)
	OnServerUpdated func(serverID int)
	OnServerDeleted func(serverID int)

	// AlertsRules is the effective policy, also passed while evaluation is
	// disabled so the Incidents page can review what would be evaluated.
	AlertsRules   []alerts.Rule
	AlertsEnabled bool

	// Runtime status is a local, read-only and credential-free view used by
	// the Settings health card. It never probes or mutates monitored targets.
	StartedAt    time.Time
	StreamStatus func() StreamRuntimeStatus

	// ConfigPath and Config enable the local administration API. Monitored
	// targets remain read-only; this only persists the monitor's own YAML.
	// Both are required so test routers and API-only embeddings stay unchanged.
	ConfigPath string
	Config     *config.Config
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

	auth := authGate(d.AuthPassword)
	proxyTrust := newProxyTrust(d.TrustedProxyCIDRs)
	loginThrottle := newLoginThrottle(5, time.Minute, maxLoginThrottleBuckets)

	// Login + logout live OUTSIDE the auth middleware so the SPA can reach
	// them when unauthenticated. Login is omitted when no password is set;
	// /whoami tells the SPA that authentication is disabled in that mode.
	if d.AuthPassword != "" {
		r.Post("/api/v1/login", newLoginHandler(d.AuthPassword, proxyTrust, loginThrottle))
	}
	r.Post("/api/v1/logout", logoutHandler(proxyTrust))
	r.Get("/api/v1/whoami", whoamiHandler(d.AuthPassword, d.DisplayTimezone))

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(auth)
		api.Get("/status", runtimeStatusHandler(d.StartedAt, d.StreamStatus))
		h := &serverHandlers{
			store:           d.Store,
			exec:            d.Executor,
			logger:          d.Logger,
			onServerCreated: d.OnServerCreated,
			onServerUpdated: d.OnServerUpdated,
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
			hh := newHierarchyHandlers(d.MetricsBaseURL, d.MetricsQueryTimeout, d.MetricsLookback, d.Logger, d.Store)
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

		if sh := newSettingsHandlers(d.ConfigPath, d.Config, d.Logger); sh != nil {
			sh.mount(api)
		}
	})

	// SPA mount: every non-/api, non-/healthz path is delegated to the
	// embedded dashboard. NOT auth-wrapped — the SPA itself decides
	// when to show the login page based on whether /api/v1/whoami
	// returns 200 or 401. (Wrapping the SPA in auth would trigger
	// the browser's native credential prompt before the SPA even
	// loads, defeating the in-view-login UX.)
	// Registered last so /api/v1/* and /healthz take precedence.
	r.Handle("/*", webpkg.Handler())

	return r
}
