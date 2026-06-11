package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/internal/control"
	"frappe-monitor/internal/storage"
)

// ControlRunner is the control-panel surface the API depends on. *control.Service
// implements it; nil when the control panel isn't wired.
type ControlRunner interface {
	Run(ctx context.Context, req control.RunRequest) (*storage.ControlAction, error)
	ReadSiteConfig(ctx context.Context, serverID int, bench, site string) (string, error)
	WriteSiteConfig(ctx context.Context, req control.WriteConfigRequest) (*storage.ControlAction, error)
}

type controlHandlers struct {
	store  storage.Store
	runner ControlRunner
	logger *slog.Logger
}

func (h *controlHandlers) mount(r chi.Router) {
	r.Get("/control/actions", h.catalog)
	r.Post("/control/run", h.run)
	r.Get("/control/history", h.history)
	r.Get("/control/history/{id}", h.historyItem)
	r.Get("/control/site-config", h.readConfig)
	r.Put("/control/site-config", h.writeConfig)
}

// catalog returns the fixed allowlist of runnable actions.
func (h *controlHandlers) catalog(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, control.Catalog())
}

type runControlReq struct {
	ServerID  int    `json:"server_id"`
	Action    string `json:"action"`
	BenchPath string `json:"bench_path"`
	Site      string `json:"site"`
}

func (h *controlHandlers) run(w http.ResponseWriter, r *http.Request) {
	if h.runner == nil {
		writeErr(w, http.StatusServiceUnavailable, "control panel is not enabled on this server")
		return
	}
	var req runControlReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ServerID == 0 {
		writeErr(w, http.StatusBadRequest, "server_id is required")
		return
	}
	act, err := h.runner.Run(r.Context(), control.RunRequest{
		ServerID: req.ServerID, ActionKey: req.Action,
		BenchPath: req.BenchPath, Site: req.Site,
	})
	switch {
	case errors.Is(err, control.ErrUnknownAction), errors.Is(err, control.ErrInvalidParams):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, storage.ErrNotFound):
		writeErr(w, http.StatusNotFound, "server not found")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not start action")
		h.logger.Error("control run", "err", err)
		return
	}
	// 202: accepted; the run continues asynchronously and streams over WS.
	writeJSON(w, http.StatusAccepted, control.ViewOf(act, h.serverName(r.Context(), act.ServerID)))
}

func (h *controlHandlers) history(w http.ResponseWriter, r *http.Request) {
	f := storage.ListControlActions{}
	if v := r.URL.Query().Get("server_id"); v != "" {
		f.ServerID, _ = strconv.Atoi(v)
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	rows, err := h.store.ListControlActions(r.Context(), f)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	names := h.serverNames(r.Context())
	out := make([]control.ActionView, 0, len(rows))
	for _, a := range rows {
		out = append(out, control.ViewOf(a, names[a.ServerID]))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *controlHandlers) historyItem(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	a, err := h.store.GetControlAction(r.Context(), id)
	if errors.Is(err, storage.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "action not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, control.ViewOf(a, h.serverName(r.Context(), a.ServerID)))
}

func (h *controlHandlers) readConfig(w http.ResponseWriter, r *http.Request) {
	if h.runner == nil {
		writeErr(w, http.StatusServiceUnavailable, "control panel is not enabled on this server")
		return
	}
	serverID, err := strconv.Atoi(r.URL.Query().Get("server_id"))
	if err != nil || serverID == 0 {
		writeErr(w, http.StatusBadRequest, "server_id is required")
		return
	}
	content, err := h.runner.ReadSiteConfig(r.Context(), serverID,
		r.URL.Query().Get("bench"), r.URL.Query().Get("site"))
	switch {
	case errors.Is(err, control.ErrInvalidParams):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, storage.ErrNotFound):
		writeErr(w, http.StatusNotFound, "server not found")
		return
	case err != nil:
		writeErr(w, http.StatusBadGateway, "could not read site config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": content})
}

type writeConfigReq struct {
	ServerID  int    `json:"server_id"`
	BenchPath string `json:"bench_path"`
	Site      string `json:"site"`
	Content   string `json:"content"`
	Restart   bool   `json:"restart"`
}

func (h *controlHandlers) writeConfig(w http.ResponseWriter, r *http.Request) {
	if h.runner == nil {
		writeErr(w, http.StatusServiceUnavailable, "control panel is not enabled on this server")
		return
	}
	var req writeConfigReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.ServerID == 0 {
		writeErr(w, http.StatusBadRequest, "server_id is required")
		return
	}
	act, err := h.runner.WriteSiteConfig(r.Context(), control.WriteConfigRequest{
		ServerID: req.ServerID, BenchPath: req.BenchPath, Site: req.Site,
		Content: req.Content, Restart: req.Restart,
	})
	switch {
	case errors.Is(err, control.ErrInvalidParams):
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, storage.ErrNotFound):
		writeErr(w, http.StatusNotFound, "server not found")
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "could not start site-config edit")
		h.logger.Error("control write-config", "err", err)
		return
	}
	writeJSON(w, http.StatusAccepted, control.ViewOf(act, h.serverName(r.Context(), act.ServerID)))
}

// serverNames returns an id→name map (best effort; empty on error).
func (h *controlHandlers) serverNames(ctx context.Context) map[int]string {
	m := map[int]string{}
	srvs, err := h.store.ListServers(ctx)
	if err != nil {
		return m
	}
	for _, s := range srvs {
		m[s.ID] = s.Name
	}
	return m
}

func (h *controlHandlers) serverName(ctx context.Context, id int) string {
	s, err := h.store.GetServer(ctx, id)
	if err != nil {
		return ""
	}
	return s.Name
}
