package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/internal/realtime"
	sshpkg "frappe-monitor/internal/ssh"
	"frappe-monitor/internal/storage"
	"frappe-monitor/scripts"
)

type serverHandlers struct {
	store  storage.Store
	exec   sshpkg.Executor
	logger *slog.Logger

	// Lifecycle hooks fire after a successful create/delete so the
	// scheduler can pick up new servers (and stop calling deleted
	// ones) without a process restart. Nil = no-op.
	onServerCreated func(serverID int)
	onServerDeleted func(serverID int)

	// broadcaster pushes server.created/updated/deleted events to the
	// dashboard so the server list updates live. Nil = no-op (tests).
	broadcaster realtime.Broadcaster
}

// emit broadcasts a realtime event when a broadcaster is wired.
func (h *serverHandlers) emit(ev realtime.Event) {
	if h.broadcaster != nil {
		h.broadcaster.Broadcast(ev)
	}
}

// emitStatus broadcasts a server.status event mirroring the collector
// pipeline's payload, so an API-driven status change (a manual test-
// connection) updates the live cards on every connected dashboard
// immediately — not just the tab that issued the probe.
func (h *serverHandlers) emitStatus(id int, status, lastErr string) {
	data := map[string]any{
		"id":             id,
		"status":         status,
		"last_error":     lastErr,
		"last_pinged_at": time.Now().UTC().Format(time.RFC3339),
	}
	h.emit(realtime.Event{Type: realtime.TypeServerStatus, Topic: realtime.TopicServers(), Data: data})
	h.emit(realtime.Event{Type: realtime.TypeServerStatus, Topic: realtime.TopicServer(id), Data: data})
}

func (h *serverHandlers) mount(r chi.Router) {
	r.Post("/servers", h.create)
	r.Get("/servers", h.list)
	r.Get("/servers/{id}", h.get)
	r.Patch("/servers/{id}", h.patch)
	r.Delete("/servers/{id}", h.delete)
	r.Post("/servers/{id}/test-connection", h.testConnection)
	r.Post("/servers/{id}/deploy-collector", h.deployCollector)
	r.Post("/servers/{id}/refresh-system", h.refreshSystem)
	r.Get("/servers/{id}/system", h.getSystem)
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
	BenchPaths []string          `json:"bench_paths,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

type serverDTO struct {
	ID           int               `json:"id"`
	Name         string            `json:"name"`
	Hostname     string            `json:"hostname"`
	SSHUser      string            `json:"ssh_user"`
	SSHPort      int               `json:"ssh_port"`
	SSHKeyPath   string            `json:"ssh_key_path"`
	BenchPaths   []string          `json:"bench_paths"`
	Labels       map[string]string `json:"labels,omitempty"`
	Status       string            `json:"status"`
	LastPingedAt *time.Time        `json:"last_pinged_at,omitempty"`
	LastError    string            `json:"last_error,omitempty"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

func toDTO(s *storage.Server) serverDTO {
	bp := s.BenchPaths
	if bp == nil {
		bp = []string{}
	}
	return serverDTO{
		ID:           s.ID,
		Name:         s.Name,
		Hostname:     s.Hostname,
		SSHUser:      s.SSHUser,
		SSHPort:      s.SSHPort,
		SSHKeyPath:   s.SSHKeyPath,
		BenchPaths:   bp,
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
		SSHPort: req.SSHPort, SSHKeyPath: req.SSHKeyPath,
		BenchPaths: cleanBenchPaths(req.BenchPaths),
		Labels:     req.Labels,
	})
	if errors.Is(err, storage.ErrDuplicateHostname) {
		writeErr(w, http.StatusConflict, "hostname already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.onServerCreated != nil {
		h.onServerCreated(s.ID)
	}
	h.emit(realtime.Event{Type: realtime.TypeServerCreated, Topic: realtime.TopicServers(), Data: toDTO(s)})
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

// patchServerReq is the body for PATCH /api/v1/servers/{id}. All fields
// are pointers so "absent" (nil) means "leave unchanged"; explicit
// values overwrite. JSON null on a field is treated identically to
// absent — it's the cheapest backward-compatible way to roll out new
// fields without breaking older clients that don't send them.
type patchServerReq struct {
	Name       *string            `json:"name,omitempty"`
	Hostname   *string            `json:"hostname,omitempty"`
	SSHUser    *string            `json:"ssh_user,omitempty"`
	SSHPort    *int               `json:"ssh_port,omitempty"`
	SSHKeyPath *string            `json:"ssh_key_path,omitempty"`
	BenchPaths *[]string          `json:"bench_paths,omitempty"`
	Labels     *map[string]string `json:"labels,omitempty"`
}

// cleanBenchPaths trims whitespace and drops empty entries. Useful
// when the SPA's textarea-as-list yields trailing newlines or stray
// blank lines from operator copy-paste.
func cleanBenchPaths(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (h *serverHandlers) patch(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var req patchServerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.SSHPort != nil && *req.SSHPort <= 0 {
		writeErr(w, http.StatusBadRequest, "ssh_port must be positive")
		return
	}
	var benchPathsClean *[]string
	if req.BenchPaths != nil {
		c := cleanBenchPaths(*req.BenchPaths)
		benchPathsClean = &c
	}
	updated, err := h.store.UpdateServer(r.Context(), id, storage.UpdateServer{
		Name:       req.Name,
		Hostname:   req.Hostname,
		SSHUser:    req.SSHUser,
		SSHPort:    req.SSHPort,
		SSHKeyPath: req.SSHKeyPath,
		BenchPaths: benchPathsClean,
		Labels:     req.Labels,
	})
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	if errors.Is(err, storage.ErrDuplicateHostname) {
		writeErr(w, http.StatusConflict, "hostname already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	dto := toDTO(updated)
	h.emit(realtime.Event{Type: realtime.TypeServerUpdated, Topic: realtime.TopicServers(), Data: dto})
	h.emit(realtime.Event{Type: realtime.TypeServerUpdated, Topic: realtime.TopicServer(id), Data: dto})
	writeJSON(w, http.StatusOK, dto)
}

// delete removes a server by id. Cascading delete on the schema's
// log_cursors edge cleans up cursor rows; alert states (Phase 6) age
// out on the next reconciliation cycle when their series disappears.
// Returns 204 on success, 404 if id unknown.
func (h *serverHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	err = h.store.DeleteServer(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.onServerDeleted != nil {
		h.onServerDeleted(id)
	}
	h.emit(realtime.Event{Type: realtime.TypeServerDeleted, Topic: realtime.TopicServers(), Data: map[string]any{"id": id}})
	h.emit(realtime.Event{Type: realtime.TypeServerDeleted, Topic: realtime.TopicServer(id), Data: map[string]any{"id": id}})
	w.WriteHeader(http.StatusNoContent)
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
		h.emitStatus(id, "unreachable", pingErr.Error())
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
	h.emitStatus(id, "reachable", "")
	writeJSON(w, http.StatusOK, testConnectionResp{
		Reachable: true,
		LatencyMs: lat.Milliseconds(),
	})
}

type deployCollectorResp struct {
	Deployed bool `json:"deployed"`
	// Version is the version of the *deployed* collector — the value
	// from the embedded scripts.CollectorVersion(). It is not probed
	// from the remote target; the SSH exec returning success is taken
	// as proof that the deployment landed.
	Version string `json:"version"`
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

	// Install into the SSH user's home dir so deploy doesn't need root.
	// mkdir -p is idempotent; chmod is run after the write completes so
	// a partial script can't be exec'd. $HOME expands on the remote
	// shell side as the SSH user.
	const cmd = `mkdir -p "$HOME/.frappe-monitor" && ` +
		`cat > "$HOME/.frappe-monitor/frappe-monitor-collect.sh" && ` +
		`chmod +x "$HOME/.frappe-monitor/frappe-monitor-collect.sh"`
	if _, err := h.exec.RunWithInput(r.Context(), tgtFromServer(srv), cmd, scripts.CollectorScript); err != nil {
		writeErr(w, statusForSSHError(err), "deploy failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, deployCollectorResp{
		Deployed: true,
		Version:  scripts.CollectorVersion(),
	})
}

// systemSnapshotResp is what GET /servers/{id}/system and
// POST /servers/{id}/refresh-system return.
type systemSnapshotResp struct {
	CapturedAt time.Time       `json:"captured_at"`
	Payload    json.RawMessage `json:"payload"`
	LastError  string          `json:"last_error,omitempty"`
}

// refreshSystem SSHes into the server, pipes the embedded system
// snapshot script over stdin (so we don't need a deploy step), reads
// the JSON it prints to stdout, and upserts it into the SystemSnapshot
// table. Returns the fresh snapshot in the response body.
//
// On SSH failure the upsert still runs with last_error set so the
// dashboard shows "captured at X — failed: ...". Operators can retry.
func (h *serverHandlers) refreshSystem(w http.ResponseWriter, r *http.Request) {
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

	// Pipe the script via stdin and run it through bash. No on-disk
	// deploy needed — the script is small (≈3KB) and doesn't change
	// between cycles, so just re-shipping it is simpler than tracking
	// a deployed version.
	const cmd = "bash -s"
	stdout, runErr := h.exec.RunWithInput(r.Context(), tgtFromServer(srv), cmd, scripts.SystemScript)
	if runErr != nil {
		// Persist the failure so the dashboard shows "tried, failed".
		_ = h.store.UpsertSystemSnapshot(r.Context(), storage.SystemSnapshot{
			ServerID:  id,
			LastError: runErr.Error(),
		})
		writeErr(w, statusForSSHError(runErr), "system refresh failed: "+runErr.Error())
		return
	}

	// Validate that stdout is parseable JSON before storing — we don't
	// want to store garbage if the bench doesn't have python3.
	payload := []byte(stdout)
	if !json.Valid(payload) {
		errMsg := "snapshot output is not valid JSON: " + truncate(stdout, 256)
		_ = h.store.UpsertSystemSnapshot(r.Context(), storage.SystemSnapshot{
			ServerID:  id,
			LastError: errMsg,
		})
		writeErr(w, http.StatusBadGateway, errMsg)
		return
	}

	if err := h.store.UpsertSystemSnapshot(r.Context(), storage.SystemSnapshot{
		ServerID: id,
		Payload:  payload,
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "store snapshot: "+err.Error())
		return
	}
	snap, err := h.store.GetSystemSnapshot(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read back snapshot: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, systemSnapshotResp{
		CapturedAt: snap.CapturedAt,
		Payload:    snap.Payload,
	})
}

// getSystem returns the most recent stored snapshot, or 404 if none
// has been captured yet (i.e. server was just registered and the
// async-on-create capture hasn't completed). Doesn't trigger a fresh
// SSH call — that's the refresh endpoint's job.
func (h *serverHandlers) getSystem(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	snap, err := h.store.GetSystemSnapshot(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no snapshot yet — POST /refresh-system")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := systemSnapshotResp{
		CapturedAt: snap.CapturedAt,
		LastError:  snap.LastError,
	}
	if json.Valid(snap.Payload) {
		resp.Payload = snap.Payload
	}
	writeJSON(w, http.StatusOK, resp)
}

// truncate returns s capped at n runes, with "…" as a marker.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
