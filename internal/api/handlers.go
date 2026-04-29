package api

import (
	"log/slog"

	"github.com/go-chi/chi/v5"

	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
)

type serverHandlers struct {
	store  storage.Store
	exec   sshpkg.Executor
	logger *slog.Logger
}

func (h *serverHandlers) mount(r chi.Router) {
	// routes added in Tasks 7 & 8
	_ = r
}
