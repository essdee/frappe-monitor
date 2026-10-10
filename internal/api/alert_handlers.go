package api

import (
	"log/slog"
	"net/http"
	"time"

	"frappe-monitor/internal/alerts"
	"frappe-monitor/internal/storage"
)

// alertHandlers serves the dashboard's "Alerts" page. Read-only — rules
// are configured via /etc/frappe-monitor/monitor.yaml; firing state
// is owned by the alerts.Service evaluator. This package is a thin
// projection: GET /api/v1/alerts returns the current snapshot.
type alertHandlers struct {
	store   storage.Store
	rules   []alerts.Rule
	enabled bool
	logger  *slog.Logger
}

// alertRuleDTO is the JSON shape for one configured rule.
type alertRuleDTO struct {
	Name              string   `json:"name"`
	Expr              string   `json:"expr"`
	Severity          string   `json:"severity"`
	Message           string   `json:"message,omitempty"`
	FingerprintLabels []string `json:"fingerprint_labels,omitempty"`
}

// firingAlertDTO is one currently-firing-or-recently-resolved alert.
type firingAlertDTO struct {
	ID             int               `json:"id"`
	RuleName       string            `json:"rule_name"`
	Fingerprint    string            `json:"fingerprint"`
	Status         string            `json:"status"`
	Value          float64           `json:"value"`
	Labels         map[string]string `json:"labels"`
	FirstFiredAt   time.Time         `json:"first_fired_at"`
	LastNotifiedAt time.Time         `json:"last_notified_at"`
}

// alertsResp is the body for GET /api/v1/alerts.
type alertsResp struct {
	Enabled   bool             `json:"enabled"`
	Rules     []alertRuleDTO   `json:"rules"`
	Firing    []firingAlertDTO `json:"firing"`
	Incidents []incidentDTO    `json:"incidents"`
}

type incidentDTO struct {
	ID             int               `json:"id"`
	RuleName       string            `json:"rule_name"`
	Fingerprint    string            `json:"fingerprint"`
	Severity       string            `json:"severity"`
	Status         string            `json:"status"`
	Value          float64           `json:"value"`
	Labels         map[string]string `json:"labels"`
	StartedAt      time.Time         `json:"started_at"`
	ResolvedAt     *time.Time        `json:"resolved_at,omitempty"`
	LastNotifiedAt *time.Time        `json:"last_notified_at,omitempty"`
}

func (h *alertHandlers) list(w http.ResponseWriter, r *http.Request) {
	rules := make([]alertRuleDTO, 0, len(h.rules))
	for _, ru := range h.rules {
		rules = append(rules, alertRuleDTO{
			Name:              ru.Name,
			Expr:              ru.Expr,
			Severity:          ru.Severity,
			Message:           ru.Message,
			FingerprintLabels: ru.FingerprintLabels,
		})
	}
	firing := []firingAlertDTO{}
	incidents := []incidentDTO{}
	if h.store != nil {
		rows, err := h.store.ListAlertStates(r.Context())
		if err != nil {
			h.logger.Warn("alerts: list state failed", "err", err)
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, ro := range rows {
			firing = append(firing, firingAlertDTO{
				ID:             ro.ID,
				RuleName:       ro.RuleName,
				Fingerprint:    ro.Fingerprint,
				Status:         ro.Status,
				Value:          ro.Value,
				Labels:         ro.Labels,
				FirstFiredAt:   ro.FirstFiredAt,
				LastNotifiedAt: ro.LastNotifiedAt,
			})
		}
		if incidentStore, ok := h.store.(storage.AlertIncidentStore); ok {
			rows, err := incidentStore.ListAlertIncidents(r.Context(), 250)
			if err != nil {
				h.logger.Warn("alerts: list incidents failed", "err", err)
				writeErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			for _, row := range rows {
				incidents = append(incidents, incidentDTO{
					ID: row.ID, RuleName: row.RuleName, Fingerprint: row.Fingerprint,
					Severity: row.Severity, Status: row.Status, Value: row.Value,
					Labels: row.Labels, StartedAt: row.StartedAt, ResolvedAt: row.ResolvedAt,
					LastNotifiedAt: row.LastNotifiedAt,
				})
			}
		}
	}
	writeJSON(w, http.StatusOK, alertsResp{
		Enabled: h.enabled, Rules: rules, Firing: firing, Incidents: incidents,
	})
}
