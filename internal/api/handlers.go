package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/scripts"
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
	r.Post("/servers/{id}/test-connection", h.testConnection)
	r.Post("/servers/{id}/deploy-collector", h.deployCollector)
}

// tgtFromServer builds an SSH Target from a stored Server record.
func tgtFromServer(s *storage.Server) sshpkg.Target {
	return sshpkg.Target{
		Host: s.Hostname, Port: s.SSHPort, User: s.SSHUser, KeyPath: s.SSHKeyPath,
	}
}

// statusForSSHError maps the ssh package's typed sentinels to HTTP status
// codes for write-style endpoints (deploy-collector, future scheduler-
// triggered ops surfaced via API). Diagnostic endpoints (test-connection)
// use a different convention (200-always).
func statusForSSHError(err error) int {
	switch {
	case errors.Is(err, sshpkg.ErrAuth), errors.Is(err, sshpkg.ErrDial):
		return http.StatusBadGateway // 502 — couldn't reach upstream
	case errors.Is(err, sshpkg.ErrTimeout):
		return http.StatusGatewayTimeout // 504
	default:
		return http.StatusInternalServerError
	}
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

// testConnectionResp is the body shape returned by POST /servers/{id}/test-connection.
//
// HTTP status is always 200 for a completed probe (success OR failure) and 404
// only when the server id does not exist. This is intentional: this is a
// diagnostic endpoint, and the diagnosis lives in the body. Callers that want
// a structured error type (auth/dial/timeout/unknown) read `error_kind`.
type testConnectionResp struct {
	Reachable bool   `json:"reachable"`
	LatencyMs int64  `json:"latency_ms"` // always present; only meaningful when Reachable=true
	Error     string `json:"error,omitempty"`
	ErrorKind string `json:"error_kind,omitempty"` // "auth" | "dial" | "timeout" | "unknown"
}

// classifyPingError maps the ssh package's typed sentinels into the
// stable string set returned to clients. Unknown errors land in "unknown"
// rather than panicking, so the field is always meaningful when present.
func classifyPingError(err error) string {
	switch {
	case errors.Is(err, sshpkg.ErrAuth):
		return "auth"
	case errors.Is(err, sshpkg.ErrDial):
		return "dial"
	case errors.Is(err, sshpkg.ErrTimeout):
		return "timeout"
	default:
		return "unknown"
	}
}

func (h *serverHandlers) testConnection(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	srv, err := h.store.GetServer(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	tgt := sshpkg.Target{
		Host: srv.Hostname, Port: srv.SSHPort, User: srv.SSHUser, KeyPath: srv.SSHKeyPath,
	}
	lat, pingErr := sshpkg.Ping(r.Context(), h.exec, tgt)

	if pingErr != nil {
		if upd := h.store.SetServerStatus(r.Context(), id, "unreachable", pingErr.Error()); upd != nil {
			h.logger.Error("set status", "err", upd)
		}
		writeJSON(w, http.StatusOK, testConnectionResp{
			Reachable: false,
			Error:     pingErr.Error(),
			ErrorKind: classifyPingError(pingErr),
		})
		return
	}

	if upd := h.store.SetServerStatus(r.Context(), id, "reachable", ""); upd != nil {
		h.logger.Error("set status", "err", upd)
	}
	writeJSON(w, http.StatusOK, testConnectionResp{
		Reachable: true,
		LatencyMs: lat.Milliseconds(),
	})
}

type deployCollectorResp struct {
	Deployed bool   `json:"deployed"`
	Version  string `json:"version"`
}

// deployCollector pipes the embedded collector script to the target via
// SSH and chmods it executable. Returns 200 on success with the deployed
// version. SSH failures map to 502 / 504 / 500 per statusForSSHError.
func (h *serverHandlers) deployCollector(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	srv, err := h.store.GetServer(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	const cmd = `cat > /usr/local/bin/frappe-monitor-collect.sh && ` +
		`chmod +x /usr/local/bin/frappe-monitor-collect.sh`
	if _, err := h.exec.RunWithInput(r.Context(), tgtFromServer(srv), cmd, scripts.CollectorScript); err != nil {
		writeErr(w, statusForSSHError(err), "deploy failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, deployCollectorResp{
		Deployed: true,
		Version:  scripts.CollectorVersion(),
	})
}
