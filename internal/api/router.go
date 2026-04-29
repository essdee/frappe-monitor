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
	r.Use(requestLogger(d.Logger))
	r.Use(recoverer(d.Logger))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	// Mount /api/v1/... — populated in Task 7 & 8.
	r.Route("/api/v1", func(api chi.Router) {
		h := &serverHandlers{store: d.Store, exec: d.Executor, logger: d.Logger}
		h.mount(api)
	})

	return r
}
