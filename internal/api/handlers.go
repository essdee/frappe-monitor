package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

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
	onServerUpdated func(serverID int)
	onServerDeleted func(serverID int)
}

func (h *serverHandlers) mount(r chi.Router) {
	r.Post("/servers", h.create)
	r.Get("/servers", h.list)
	r.Post("/servers/test-connection", h.testDraftConnection)
	r.Get("/servers/{id}", h.get)
	r.Patch("/servers/{id}", h.patch)
	r.Delete("/servers/{id}", h.delete)
	r.Post("/servers/{id}/test-connection", h.testConnection)
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
// codes for SSH-backed inspection endpoints. Diagnostic endpoints (test-connection)
// use a different convention (200-always).
func statusForSSHError(err error) int {
	switch {
	case errors.Is(err, sshpkg.ErrAuth), errors.Is(err, sshpkg.ErrDial), errors.Is(err, sshpkg.ErrHostKey):
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

const maxJSONBodyBytes = 1 << 20

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateTextField(name, value string, maxLen int, allowEmpty bool) error {
	if value == "" && allowEmpty {
		return nil
	}
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if len(value) > maxLen {
		return fmt.Errorf("%s must be at most %d bytes", name, maxLen)
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s contains an invalid control character", name)
	}
	return nil
}

func validateHostname(host string) error {
	if err := validateTextField("hostname", host, 253, false); err != nil {
		return err
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return nil
	}
	if strings.ContainsAny(host, " \t/:@\\") || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return errors.New("hostname must be a DNS name or IP address without a port")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return errors.New("hostname contains an invalid DNS label")
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
				(char < '0' || char > '9') && char != '-' && char != '_' {
				return errors.New("hostname contains an invalid DNS character")
			}
		}
	}
	return nil
}

func validateBenchPaths(paths []string) error {
	if len(paths) > 64 {
		return errors.New("bench_paths must contain at most 64 paths")
	}
	seen := make(map[string]struct{}, len(paths))
	for i, path := range paths {
		if !filepath.IsAbs(path) {
			return fmt.Errorf("bench_paths[%d] must be absolute", i)
		}
		if len(path) > 4096 || strings.ContainsAny(path, ":\x00\r\n") {
			return fmt.Errorf("bench_paths[%d] is invalid", i)
		}
		if _, exists := seen[path]; exists {
			return fmt.Errorf("bench_paths contains duplicate %q", path)
		}
		seen[path] = struct{}{}
	}
	return nil
}

func validateLabels(labels map[string]string) error {
	if len(labels) > 32 {
		return errors.New("labels must contain at most 32 entries")
	}
	for key, value := range labels {
		if err := validateTextField("label key", key, 64, false); err != nil {
			return err
		}
		if err := validateTextField("label value", value, 256, true); err != nil {
			return err
		}
	}
	return nil
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
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Hostname = strings.TrimSpace(req.Hostname)
	req.SSHUser = strings.TrimSpace(req.SSHUser)
	req.SSHKeyPath = strings.TrimSpace(req.SSHKeyPath)
	req.BenchPaths = cleanBenchPaths(req.BenchPaths)
	if err := validateTextField("name", req.Name, 128, false); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateHostname(req.Hostname); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.SSHUser == "" {
		req.SSHUser = "monitor"
	}
	if req.SSHPort == 0 {
		req.SSHPort = 22
	}
	if req.SSHPort < 1 || req.SSHPort > 65535 {
		writeErr(w, http.StatusBadRequest, "ssh_port must be between 1 and 65535")
		return
	}
	if err := validateTextField("ssh_user", req.SSHUser, 64, false); err != nil || strings.ContainsAny(req.SSHUser, " \t") {
		if err == nil {
			err = errors.New("ssh_user must not contain whitespace")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateTextField("ssh_key_path", req.SSHKeyPath, 4096, false); err != nil || !filepath.IsAbs(req.SSHKeyPath) {
		if err == nil {
			err = errors.New("ssh_key_path must be absolute")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateBenchPaths(req.BenchPaths); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateLabels(req.Labels); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	s, err := h.store.CreateServer(r.Context(), storage.NewServer{
		Name: req.Name, Hostname: req.Hostname, SSHUser: req.SSHUser,
		SSHPort: req.SSHPort, SSHKeyPath: req.SSHKeyPath,
		BenchPaths: req.BenchPaths,
		Labels:     req.Labels,
	})
	if errors.Is(err, storage.ErrDuplicateHostname) {
		writeErr(w, http.StatusConflict, "hostname already exists")
		return
	}
	if errors.Is(err, storage.ErrDuplicateName) {
		writeErr(w, http.StatusConflict, "server name already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if h.onServerCreated != nil {
		h.onServerCreated(s.ID)
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
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.Name != nil {
		*req.Name = strings.TrimSpace(*req.Name)
		if err := validateTextField("name", *req.Name, 128, false); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.Hostname != nil {
		*req.Hostname = strings.TrimSpace(*req.Hostname)
		if err := validateHostname(*req.Hostname); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.SSHUser != nil {
		*req.SSHUser = strings.TrimSpace(*req.SSHUser)
		if err := validateTextField("ssh_user", *req.SSHUser, 64, false); err != nil || strings.ContainsAny(*req.SSHUser, " \t") {
			if err == nil {
				err = errors.New("ssh_user must not contain whitespace")
			}
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.SSHKeyPath != nil {
		*req.SSHKeyPath = strings.TrimSpace(*req.SSHKeyPath)
		if err := validateTextField("ssh_key_path", *req.SSHKeyPath, 4096, false); err != nil || !filepath.IsAbs(*req.SSHKeyPath) {
			if err == nil {
				err = errors.New("ssh_key_path must be absolute")
			}
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if req.SSHPort != nil && (*req.SSHPort < 1 || *req.SSHPort > 65535) {
		writeErr(w, http.StatusBadRequest, "ssh_port must be between 1 and 65535")
		return
	}
	var benchPathsClean *[]string
	if req.BenchPaths != nil {
		c := cleanBenchPaths(*req.BenchPaths)
		if err := validateBenchPaths(c); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		benchPathsClean = &c
	}
	if req.Labels != nil {
		if err := validateLabels(*req.Labels); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
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
	if errors.Is(err, storage.ErrDuplicateName) {
		writeErr(w, http.StatusConflict, "server name already exists")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if h.onServerUpdated != nil {
		h.onServerUpdated(id)
	}
	writeJSON(w, http.StatusOK, toDTO(updated))
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

type testDraftConnectionReq struct {
	Hostname   string `json:"hostname"`
	SSHUser    string `json:"ssh_user"`
	SSHPort    int    `json:"ssh_port"`
	SSHKeyPath string `json:"ssh_key_path"`
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
	case errors.Is(err, sshpkg.ErrHostKey):
		return "host_key"
	case errors.Is(err, sshpkg.ErrKey):
		return "key"
	default:
		return "unknown"
	}
}

// testDraftConnection lets the onboarding form verify credentials before a
// server record exists. It executes only `echo ok`; no target state is changed.
func (h *serverHandlers) testDraftConnection(w http.ResponseWriter, r *http.Request) {
	var req testDraftConnectionReq
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Hostname = strings.TrimSpace(req.Hostname)
	req.SSHUser = strings.TrimSpace(req.SSHUser)
	req.SSHKeyPath = strings.TrimSpace(req.SSHKeyPath)
	if err := validateHostname(req.Hostname); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.SSHUser == "" {
		req.SSHUser = "monitor"
	}
	if req.SSHPort == 0 {
		req.SSHPort = 22
	}
	if req.SSHPort < 1 || req.SSHPort > 65535 {
		writeErr(w, http.StatusBadRequest, "ssh_port must be between 1 and 65535")
		return
	}
	if err := validateTextField("ssh_user", req.SSHUser, 64, false); err != nil || strings.ContainsAny(req.SSHUser, " \t") {
		if err == nil {
			err = errors.New("ssh_user must not contain whitespace")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateTextField("ssh_key_path", req.SSHKeyPath, 4096, false); err != nil || !filepath.IsAbs(req.SSHKeyPath) {
		if err == nil {
			err = errors.New("ssh_key_path must be absolute")
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	latency, err := sshpkg.Ping(r.Context(), h.exec, sshpkg.Target{
		Host: req.Hostname, Port: req.SSHPort, User: req.SSHUser, KeyPath: req.SSHKeyPath,
	})
	if err != nil {
		writeJSON(w, http.StatusOK, testConnectionResp{
			Reachable: false, Error: err.Error(), ErrorKind: classifyPingError(err),
		})
		return
	}
	writeJSON(w, http.StatusOK, testConnectionResp{Reachable: true, LatencyMs: latency.Milliseconds()})
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
