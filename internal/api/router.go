package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/internal/alerts"
	"frappe-monitor/internal/realtime"
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

	// SSHResponseDeadline bounds the SSH-backed handlers (test-connection,
	// deploy-collector, refresh-system) whose response blocks on an SSH
	// round-trip longer than the server's global WriteTimeout. main derives
	// it from the SSH dial + command timeouts plus headroom. Zero = leave the
	// global write timeout in force.
	SSHResponseDeadline time.Duration

	// MaxBodyBytes caps every request body (including the pre-auth /login).
	// Zero = no limit (tests). main sets it from server.max_body_bytes.
	MaxBodyBytes int64

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

	// Phase 8 real-time: the WebSocket hub. When non-nil, GET
	// /api/v1/ws is mounted (behind the auth gate) so the dashboard can
	// receive pushes instead of polling. Nil = endpoint not mounted.
	Hub *realtime.Hub

	// Phase 9 DB monitor: the on-demand replication checker
	// (*dbmonitor.Service). Nil = /db-targets/{id}/check returns 503,
	// but the CRUD endpoints still work.
	DBChecker DBChecker

	// Phase 10 control panel: runs allowlisted bench/service commands and
	// site-config edits (*control.Service). Nil = run/site-config return
	// 503, but the catalog + history endpoints still work.
	ControlRunner ControlRunner
}

// limitBody wraps each request body in http.MaxBytesReader so an oversized
// body is rejected (the reader errors past the cap, surfacing as a 400 in the
// JSON decoders, or a 413 if a handler maps *http.MaxBytesError).
func limitBody(maxBytes int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
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
	// Cap request bodies globally so it also covers /login + /logout (which
	// sit outside the auth group). A pathological/huge body is rejected before
	// any handler decodes it.
	if d.MaxBodyBytes > 0 {
		r.Use(limitBody(d.MaxBodyBytes))
	}

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	auth := authGate(d.AuthPassword)

	// Login + logout live OUTSIDE the auth middleware so the SPA can
	// reach them when unauthenticated. Both are no-ops when no password
	// is configured (the SPA detects this via /api/v1/whoami's response).
	if d.AuthPassword != "" {
		r.Post("/api/v1/login", newLoginHandler(d.AuthPassword))
	}
	r.Post("/api/v1/logout", logoutHandler)

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(auth)
		api.Get("/whoami", whoamiHandler)
		h := &serverHandlers{
			store:           d.Store,
			exec:            d.Executor,
			logger:          d.Logger,
			sshDeadline:     d.SSHResponseDeadline,
			onServerCreated: d.OnServerCreated,
			onServerDeleted: d.OnServerDeleted,
			broadcaster:     d.Hub,
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

		// Phase 9 DB monitor: admin-managed replication targets (CRUD +
		// on-demand check). Always mounted; check returns 503 when the
		// monitor service isn't wired.
		dh := &dbTargetHandlers{
			store:       d.Store,
			logger:      d.Logger,
			broadcaster: d.Hub,
			checker:     d.DBChecker,
		}
		dh.mount(api)

		// Phase 10 control panel: allowlisted bench/service commands +
		// site-config edits with a full audit trail. Always mounted; the
		// mutating endpoints return 503 when the runner isn't wired.
		ch := &controlHandlers{
			store:  d.Store,
			runner: d.ControlRunner,
			logger: d.Logger,
		}
		ch.mount(api)

		// Phase 8 real-time push. Behind the auth gate: the upgrade
		// request carries the session cookie, validated by authGate
		// before we hijack the connection.
		if d.Hub != nil {
			api.Get("/ws", d.Hub.ServeWS)
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
