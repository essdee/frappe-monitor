package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/internal/realtime"
	"frappe-monitor/internal/storage"
)

// DBChecker runs an on-demand replication check. *dbmonitor.Service
// implements it; nil when DB monitoring isn't wired.
type DBChecker interface {
	CheckOnce(ctx context.Context, id int) (*storage.DBTarget, error)
}

type dbTargetHandlers struct {
	store       storage.Store
	logger      *slog.Logger
	broadcaster realtime.Broadcaster
	checker     DBChecker
}

func (h *dbTargetHandlers) mount(r chi.Router) {
	r.Post("/db-targets", h.create)
	r.Get("/db-targets", h.list)
	r.Get("/db-targets/{id}", h.get)
	r.Patch("/db-targets/{id}", h.patch)
	r.Delete("/db-targets/{id}", h.delete)
	r.Post("/db-targets/{id}/check", h.check)
}

func (h *dbTargetHandlers) emit(ev realtime.Event) {
	if h.broadcaster != nil {
		h.broadcaster.Broadcast(ev)
	}
}

// isWhitespaceOnly reports whether s is non-empty but only whitespace — a value
// that passes an "is it set?" check yet would run as a blank command on the
// bench. Empty is allowed (the caller falls back to a default client).
func isWhitespaceOnly(s string) bool {
	return s != "" && strings.TrimSpace(s) == ""
}

type dbTargetDTO struct {
	ID                  int        `json:"id"`
	ServerID            int        `json:"server_id"`
	Name                string     `json:"name"`
	Enabled             bool       `json:"enabled"`
	Engine              string     `json:"engine"`
	LagThresholdSeconds int        `json:"lag_threshold_seconds"`
	ClientCommand       string     `json:"client_command"`
	DefaultsFile        string     `json:"defaults_file,omitempty"`
	Socket              string     `json:"socket,omitempty"`
	HeartbeatEnabled    bool       `json:"heartbeat_enabled"`
	HeartbeatQuery      string     `json:"heartbeat_query,omitempty"`
	Status              string     `json:"status"`
	LastCheckedAt       *time.Time `json:"last_checked_at,omitempty"`
	// Volatile status fields: NOT omitempty, so a db.created/updated event
	// that carries cleared values (replica caught up / error gone) actually
	// clears them in the dashboard's merge instead of leaving stale data.
	LagSeconds          *int64    `json:"lag_seconds"`
	HeartbeatLagSeconds *float64  `json:"heartbeat_lag_seconds"`
	IORunning           bool      `json:"io_running"`
	SQLRunning          bool      `json:"sql_running"`
	LastError           string    `json:"last_error"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

func dbToDTO(t *storage.DBTarget) dbTargetDTO {
	return dbTargetDTO{
		ID: t.ID, ServerID: t.ServerID, Name: t.Name, Enabled: t.Enabled, Engine: t.Engine,
		LagThresholdSeconds: t.LagThresholdSeconds, ClientCommand: t.ClientCommand,
		DefaultsFile: t.DefaultsFile, Socket: t.Socket,
		HeartbeatEnabled: t.HeartbeatEnabled, HeartbeatQuery: t.HeartbeatQuery,
		Status: t.Status, LastCheckedAt: t.LastCheckedAt,
		LagSeconds: t.LagSeconds, HeartbeatLagSeconds: t.HeartbeatLagSeconds,
		IORunning: t.IORunning, SQLRunning: t.SQLRunning, LastError: t.LastError,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt,
	}
}

type createDBTargetReq struct {
	ServerID            int    `json:"server_id"`
	Name                string `json:"name"`
	Enabled             *bool  `json:"enabled"`
	Engine              string `json:"engine"`
	LagThresholdSeconds int    `json:"lag_threshold_seconds"`
	ClientCommand       string `json:"client_command"`
	DefaultsFile        string `json:"defaults_file"`
	Socket              string `json:"socket"`
	HeartbeatEnabled    bool   `json:"heartbeat_enabled"`
	HeartbeatQuery      string `json:"heartbeat_query"`
}

func (h *dbTargetHandlers) create(w http.ResponseWriter, r *http.Request) {
	var req createDBTargetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.Name) == "" || req.ServerID == 0 {
		writeErr(w, http.StatusBadRequest, "name and server_id are required")
		return
	}
	if req.LagThresholdSeconds < 0 {
		writeErr(w, http.StatusBadRequest, "lag_threshold_seconds must not be negative")
		return
	}
	if req.HeartbeatEnabled && strings.TrimSpace(req.HeartbeatQuery) == "" {
		writeErr(w, http.StatusBadRequest, "heartbeat_query is required when heartbeat is enabled")
		return
	}
	// A whitespace-only client_command would pass firstNonEmpty() and run as a
	// blank command on the bench. Reject it (empty is fine — defaults to psql/mysql).
	if isWhitespaceOnly(req.ClientCommand) {
		writeErr(w, http.StatusBadRequest, "client_command must not be only whitespace")
		return
	}
	if req.Engine != "" && req.Engine != "mysql" && req.Engine != "postgres" {
		writeErr(w, http.StatusBadRequest, "engine must be 'mysql' or 'postgres'")
		return
	}
	// The DB target rides a registered server's SSH; reject unknown ids.
	if _, err := h.store.GetServer(r.Context(), req.ServerID); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			writeErr(w, http.StatusBadRequest, "server_id does not exist")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	t, err := h.store.CreateDBTarget(r.Context(), storage.NewDBTarget{
		ServerID: req.ServerID, Name: req.Name, Enabled: enabled, Engine: req.Engine,
		LagThresholdSeconds: req.LagThresholdSeconds, ClientCommand: req.ClientCommand,
		DefaultsFile: req.DefaultsFile, Socket: req.Socket,
		HeartbeatEnabled: req.HeartbeatEnabled, HeartbeatQuery: req.HeartbeatQuery,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create db target")
		h.logger.Error("create db target", "err", err)
		return
	}
	h.emit(realtime.Event{Type: realtime.TypeDBCreated, Topic: realtime.TopicDatabases(), Data: dbToDTO(t)})
	writeJSON(w, http.StatusCreated, dbToDTO(t))
}

func (h *dbTargetHandlers) list(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListDBTargets(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]dbTargetDTO, 0, len(rows))
	for _, t := range rows {
		out = append(out, dbToDTO(t))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *dbTargetHandlers) get(w http.ResponseWriter, r *http.Request) {
	t, err := h.load(w, r)
	if t == nil {
		return
	}
	_ = err
	writeJSON(w, http.StatusOK, dbToDTO(t))
}

type patchDBTargetReq struct {
	ServerID            *int    `json:"server_id,omitempty"`
	Name                *string `json:"name,omitempty"`
	Enabled             *bool   `json:"enabled,omitempty"`
	Engine              *string `json:"engine,omitempty"`
	LagThresholdSeconds *int    `json:"lag_threshold_seconds,omitempty"`
	ClientCommand       *string `json:"client_command,omitempty"`
	DefaultsFile        *string `json:"defaults_file,omitempty"`
	Socket              *string `json:"socket,omitempty"`
	HeartbeatEnabled    *bool   `json:"heartbeat_enabled,omitempty"`
	HeartbeatQuery      *string `json:"heartbeat_query,omitempty"`
}

func (h *dbTargetHandlers) patch(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var req patchDBTargetReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.LagThresholdSeconds != nil && *req.LagThresholdSeconds <= 0 {
		writeErr(w, http.StatusBadRequest, "lag_threshold_seconds must be positive")
		return
	}
	if req.Name != nil && strings.TrimSpace(*req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name must not be empty")
		return
	}
	if req.HeartbeatEnabled != nil && *req.HeartbeatEnabled &&
		req.HeartbeatQuery != nil && strings.TrimSpace(*req.HeartbeatQuery) == "" {
		writeErr(w, http.StatusBadRequest, "heartbeat_query is required when heartbeat is enabled")
		return
	}
	if req.Engine != nil && *req.Engine != "mysql" && *req.Engine != "postgres" {
		writeErr(w, http.StatusBadRequest, "engine must be 'mysql' or 'postgres'")
		return
	}
	if req.ClientCommand != nil && isWhitespaceOnly(*req.ClientCommand) {
		writeErr(w, http.StatusBadRequest, "client_command must not be only whitespace")
		return
	}
	// Changing the SSH host: re-validate the new server exists (the edge
	// is a FK; a bad id would otherwise fail opaquely).
	if req.ServerID != nil {
		if _, err := h.store.GetServer(r.Context(), *req.ServerID); err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				writeErr(w, http.StatusBadRequest, "server_id does not exist")
				return
			}
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	t, err := h.store.UpdateDBTarget(r.Context(), id, storage.UpdateDBTarget{
		ServerID: req.ServerID, Engine: req.Engine,
		Name: req.Name, Enabled: req.Enabled, LagThresholdSeconds: req.LagThresholdSeconds,
		ClientCommand: req.ClientCommand, DefaultsFile: req.DefaultsFile, Socket: req.Socket,
		HeartbeatEnabled: req.HeartbeatEnabled, HeartbeatQuery: req.HeartbeatQuery,
	})
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "db target not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.emit(realtime.Event{Type: realtime.TypeDBUpdated, Topic: realtime.TopicDatabases(), Data: dbToDTO(t)})
	writeJSON(w, http.StatusOK, dbToDTO(t))
}

func (h *dbTargetHandlers) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	err := h.store.DeleteDBTarget(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "db target not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.emit(realtime.Event{Type: realtime.TypeDBDeleted, Topic: realtime.TopicDatabases(), Data: map[string]any{"id": id}})
	w.WriteHeader(http.StatusNoContent)
}

// check runs an immediate replication check and returns the fresh status.
func (h *dbTargetHandlers) check(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if h.checker == nil {
		writeErr(w, http.StatusServiceUnavailable, "db monitoring is not enabled on this server")
		return
	}
	t, err := h.checker.CheckOnce(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "db target not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, dbToDTO(t))
}

func (h *dbTargetHandlers) load(w http.ResponseWriter, r *http.Request) (*storage.DBTarget, error) {
	id, ok := idParam(w, r)
	if !ok {
		return nil, nil
	}
	t, err := h.store.GetDBTarget(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "db target not found")
		return nil, err
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return nil, err
	}
	return t, nil
}

func idParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}
