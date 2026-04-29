package api

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

type Deps struct {
	Store    storage.Store
	Executor sshpkg.Executor
	Logger   *slog.Logger
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
	})

	return r
}
