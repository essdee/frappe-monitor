package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

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
	r.Post("/servers", h.create)
	r.Get("/servers", h.list)
	r.Get("/servers/{id}", h.get)
	// /servers/{id}/test-connection added in Task 8
}

type createServerReq struct {
	Name       string            `json:"name"`
	Hostname   string            `json:"hostname"`
	SSHUser    string            `json:"ssh_user"`
	SSHPort    int               `json:"ssh_port"`
	SSHKeyPath string            `json:"ssh_key_path"`
	Labels     map[string]string `json:"labels,omitempty"`
}

type serverDTO struct {
	ID           int               `json:"id"`
	Name         string            `json:"name"`
	Hostname     string            `json:"hostname"`
	SSHUser      string            `json:"ssh_user"`
	SSHPort      int               `json:"ssh_port"`
	SSHKeyPath   string            `json:"ssh_key_path"`
	Labels       map[string]string `json:"labels,omitempty"`
	Status       string            `json:"status"`
	LastPingedAt *time.Time        `json:"last_pinged_at,omitempty"`
	LastError    string            `json:"last_error,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

func toDTO(s *storage.Server) serverDTO {
	return serverDTO{
		ID:           s.ID,
		Name:         s.Name,
		Hostname:     s.Hostname,
		SSHUser:      s.SSHUser,
		SSHPort:      s.SSHPort,
		SSHKeyPath:   s.SSHKeyPath,
		Labels:       s.Labels,
		Status:       s.Status,
		LastPingedAt: s.LastPingedAt,
		LastError:    s.LastError,
		CreatedAt:    s.CreatedAt,
		UpdatedAt:    s.UpdatedAt,
	}
}

func (h *serverHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createServerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name == "" || req.Hostname == "" || req.SSHKeyPath == "" {
		writeErr(w, http.StatusBadRequest, "name, hostname, ssh_key_path are required")
		return
	}
	if req.SSHUser == "" {
		req.SSHUser = "monitor"
	}
	if req.SSHPort == 0 {
		req.SSHPort = 22
	}
	s, err := h.store.CreateServer(r.Context(), storage.NewServer{
		Name: req.Name, Hostname: req.Hostname, SSHUser: req.SSHUser,
		SSHPort: req.SSHPort, SSHKeyPath: req.SSHKeyPath, Labels: req.Labels,
	})
	if errors.Is(err, storage.ErrDuplicateHostname) {
		writeErr(w, http.StatusConflict, "hostname already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(s))
}

func (h *serverHandlers) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListServers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]serverDTO, 0, len(rows))
	for _, s := range rows {
		out = append(out, toDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *serverHandlers) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	s, err := h.store.GetServer(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toDTO(s))
}
