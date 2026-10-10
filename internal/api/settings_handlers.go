package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"frappe-monitor/internal/alerts"
	"frappe-monitor/internal/config"
	"frappe-monitor/internal/streamer"
)

// settingsHandlers owns the desired configuration shown in the UI. Runtime
// services intentionally keep their boot-time configuration until the monitor
// is restarted; mixing live and boot-time settings would make the actual state
// impossible for an operator to reason about.
type settingsHandlers struct {
	mu              sync.RWMutex
	path            string
	cfg             config.Config
	restartRequired bool
	logger          *slog.Logger
	httpClient      *http.Client
}

type settingsAuthResponse struct {
	Realm              string `json:"realm"`
	PasswordConfigured bool   `json:"password_configured"`
}

type settingsTelegramResponse struct {
	ChatIDs            []string `json:"chat_ids"`
	SendTimeoutSeconds int      `json:"send_timeout_seconds"`
	BotTokenConfigured bool     `json:"bot_token_configured"`
}

type settingsAlertsResponse struct {
	Enabled                   bool                     `json:"enabled"`
	EvaluationIntervalSeconds int                      `json:"evaluation_interval_seconds"`
	NotifyRepeatSeconds       int                      `json:"notify_repeat_seconds"`
	VMQueryTimeoutSeconds     int                      `json:"vm_query_timeout_seconds"`
	Telegram                  settingsTelegramResponse `json:"telegram"`
	Rules                     []config.AlertsRule      `json:"rules"`
	DisableDefaults           bool                     `json:"disable_defaults"`
	BundledRuleCount          int                      `json:"bundled_rule_count"`
}

type settingsResponse struct {
	Server          config.ServerConfig       `json:"server"`
	Display         config.DisplayConfig      `json:"display"`
	Database        config.DatabaseConfig     `json:"database"`
	SSH             config.SSHConfig          `json:"ssh"`
	Log             config.LogConfig          `json:"log"`
	Metrics         config.MetricsConfig      `json:"metrics"`
	Logs            config.LogsConfig         `json:"logs"`
	Scheduler       config.SchedulerConfig    `json:"scheduler"`
	PublicProbes    config.PublicProbesConfig `json:"public_probes"`
	Auth            settingsAuthResponse      `json:"auth"`
	Alerts          settingsAlertsResponse    `json:"alerts"`
	Streaming       config.StreamingConfig    `json:"streaming"`
	RestartRequired bool                      `json:"restart_required"`
}

type settingsSecretUpdate struct {
	Password *string `json:"password,omitempty"`
}

type settingsTelegramUpdate struct {
	ChatIDs            []string `json:"chat_ids"`
	SendTimeoutSeconds int      `json:"send_timeout_seconds"`
	BotToken           *string  `json:"bot_token,omitempty"`
}

type settingsAlertsUpdate struct {
	Enabled                   bool                   `json:"enabled"`
	EvaluationIntervalSeconds int                    `json:"evaluation_interval_seconds"`
	NotifyRepeatSeconds       int                    `json:"notify_repeat_seconds"`
	VMQueryTimeoutSeconds     int                    `json:"vm_query_timeout_seconds"`
	Telegram                  settingsTelegramUpdate `json:"telegram"`
	Rules                     []config.AlertsRule    `json:"rules"`
	DisableDefaults           bool                   `json:"disable_defaults"`
}

type settingsUpdateRequest struct {
	Server       config.ServerConfig       `json:"server"`
	Display      config.DisplayConfig      `json:"display"`
	Database     config.DatabaseConfig     `json:"database"`
	SSH          config.SSHConfig          `json:"ssh"`
	Log          config.LogConfig          `json:"log"`
	Metrics      config.MetricsConfig      `json:"metrics"`
	Logs         config.LogsConfig         `json:"logs"`
	Scheduler    config.SchedulerConfig    `json:"scheduler"`
	PublicProbes config.PublicProbesConfig `json:"public_probes"`
	Auth         struct {
		Realm    string  `json:"realm"`
		Password *string `json:"password,omitempty"`
	} `json:"auth"`
	Alerts    settingsAlertsUpdate   `json:"alerts"`
	Streaming config.StreamingConfig `json:"streaming"`
}

type integrationTestRequest struct {
	Kind     string  `json:"kind"`
	BotToken *string `json:"bot_token,omitempty"`
}

type integrationTestResponse struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Message   string `json:"message"`
}

func newSettingsHandlers(path string, cfg *config.Config, logger *slog.Logger) *settingsHandlers {
	if cfg == nil || strings.TrimSpace(path) == "" {
		return nil
	}
	return &settingsHandlers{
		path:       path,
		cfg:        cloneConfig(*cfg),
		logger:     logger,
		httpClient: &http.Client{Timeout: 8 * time.Second},
	}
}

func (h *settingsHandlers) mount(r chi.Router) {
	r.Get("/settings", h.get)
	r.Put("/settings", h.update)
	r.Post("/settings/test", h.testIntegration)
}

func cloneConfig(in config.Config) config.Config {
	out := in
	out.Server.TrustedProxyCIDRs = append([]string(nil), in.Server.TrustedProxyCIDRs...)
	out.Alerts.Telegram.ChatIDs = append([]string(nil), in.Alerts.Telegram.ChatIDs...)
	out.Alerts.Rules = append([]config.AlertsRule(nil), in.Alerts.Rules...)
	for i := range out.Alerts.Rules {
		out.Alerts.Rules[i].FingerprintLabels = append([]string(nil), in.Alerts.Rules[i].FingerprintLabels...)
	}
	out.Streaming.Files = append([]config.StreamingFileConfig(nil), in.Streaming.Files...)
	out.PublicProbes.Targets = append([]config.PublicProbeTarget(nil), in.PublicProbes.Targets...)
	return out
}

func (h *settingsHandlers) responseLocked() settingsResponse {
	serverCfg := h.cfg.Server
	serverCfg.TrustedProxyCIDRs = append([]string{}, h.cfg.Server.TrustedProxyCIDRs...)
	chatIDs := append([]string{}, h.cfg.Alerts.Telegram.ChatIDs...)
	rules := make([]config.AlertsRule, len(h.cfg.Alerts.Rules))
	copy(rules, h.cfg.Alerts.Rules)
	for i := range rules {
		rules[i].FingerprintLabels = append([]string{}, h.cfg.Alerts.Rules[i].FingerprintLabels...)
	}
	streamingCfg := h.cfg.Streaming
	streamingCfg.Files = append([]config.StreamingFileConfig{}, h.cfg.Streaming.Files...)
	publicProbesCfg := h.cfg.PublicProbes
	publicProbesCfg.Targets = append([]config.PublicProbeTarget{}, h.cfg.PublicProbes.Targets...)
	return settingsResponse{
		Server:       serverCfg,
		Display:      h.cfg.Display,
		Database:     h.cfg.Database,
		SSH:          h.cfg.SSH,
		Log:          h.cfg.Log,
		Metrics:      h.cfg.Metrics,
		Logs:         h.cfg.Logs,
		Scheduler:    h.cfg.Scheduler,
		PublicProbes: publicProbesCfg,
		Auth: settingsAuthResponse{
			Realm:              h.cfg.Auth.Realm,
			PasswordConfigured: strings.TrimSpace(h.cfg.Auth.Password) != "",
		},
		Alerts: settingsAlertsResponse{
			Enabled:                   h.cfg.Alerts.Enabled,
			EvaluationIntervalSeconds: h.cfg.Alerts.EvaluationIntervalSeconds,
			NotifyRepeatSeconds:       h.cfg.Alerts.NotifyRepeatSeconds,
			VMQueryTimeoutSeconds:     h.cfg.Alerts.VMQueryTimeoutSeconds,
			Telegram: settingsTelegramResponse{
				ChatIDs:            chatIDs,
				SendTimeoutSeconds: h.cfg.Alerts.Telegram.SendTimeoutSeconds,
				BotTokenConfigured: strings.TrimSpace(h.cfg.Alerts.Telegram.BotToken) != "",
			},
			Rules:            rules,
			DisableDefaults:  h.cfg.Alerts.DisableDefaults,
			BundledRuleCount: len(alerts.DefaultRules()),
		},
		Streaming:       streamingCfg,
		RestartRequired: h.restartRequired,
	}
}

func (h *settingsHandlers) get(w http.ResponseWriter, _ *http.Request) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	writeJSON(w, http.StatusOK, h.responseLocked())
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func validateHTTPURL(name, raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute http or https URL", name)
	}
	return nil
}

func validateDetailedConfig(c config.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := validateHTTPURL("metrics.vm_url", c.Metrics.VMURL); err != nil {
		return err
	}
	if err := validateHTTPURL("logs.loki_url", c.Logs.LokiURL); err != nil {
		return err
	}
	if strings.TrimSpace(c.Auth.Realm) == "" {
		return errors.New("auth.realm is required")
	}
	alertCfg := toAlertsConfig(c.Alerts)
	if err := alertCfg.Validate(); err != nil {
		return err
	}
	seenRules := make(map[string]struct{}, len(alertCfg.Rules))
	for i, rule := range alertCfg.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("alerts.rules[%d]: %w", i, err)
		}
		if _, exists := seenRules[rule.Name]; exists {
			return fmt.Errorf("duplicate user alert rule name %q", rule.Name)
		}
		seenRules[rule.Name] = struct{}{}
	}
	streamCfg := toStreamerConfig(c.Streaming)
	// File definitions must be valid before operators enable streaming.
	streamCfg.Enabled = true
	if err := streamCfg.Validate(); err != nil {
		return err
	}
	return nil
}

func toAlertsConfig(c config.AlertsConfig) alerts.Config {
	rules := make([]alerts.Rule, 0, len(c.Rules))
	for _, rule := range c.Rules {
		rules = append(rules, alerts.Rule{
			Name: rule.Name, Expr: rule.Expr, Severity: rule.Severity,
			Message: rule.Message, FingerprintLabels: rule.FingerprintLabels,
		})
	}
	return alerts.Config{
		Enabled:                   c.Enabled,
		EvaluationIntervalSeconds: c.EvaluationIntervalSeconds,
		NotifyRepeatSeconds:       c.NotifyRepeatSeconds,
		VMQueryTimeoutSeconds:     c.VMQueryTimeoutSeconds,
		Telegram: alerts.TelegramConfig{
			BotToken: c.Telegram.BotToken, ChatIDs: c.Telegram.ChatIDs,
			SendTimeoutSeconds: c.Telegram.SendTimeoutSeconds,
		},
		Rules: rules, DisableDefaults: c.DisableDefaults,
	}
}

func toStreamerConfig(c config.StreamingConfig) streamer.Config {
	files := make([]streamer.FileSpecConfig, 0, len(c.Files))
	for _, file := range c.Files {
		files = append(files, streamer.FileSpecConfig{
			ServerID: file.ServerID, ID: file.ID, Path: file.Path,
			LogType: file.LogType, Bench: file.Bench, Site: file.Site,
		})
	}
	return streamer.Config{
		Enabled: c.Enabled, MonitorID: c.MonitorID, ScriptPath: c.ScriptPath,
		Files: files, FlushIntervalSeconds: c.FlushIntervalSeconds,
		MaxBatchLines: c.MaxBatchLines, PushTimeoutSeconds: c.PushTimeoutSeconds,
		MaxBufferedLines: c.MaxBufferedLines, MaxBufferedBytes: c.MaxBufferedBytes,
		MinBackoffSeconds: c.MinBackoffSeconds, MaxBackoffSeconds: c.MaxBackoffSeconds,
	}
}

func (h *settingsHandlers) update(w http.ResponseWriter, r *http.Request) {
	var req settingsUpdateRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	candidate := cloneConfig(h.cfg)
	candidate.Server = req.Server
	candidate.Server.TrustedProxyCIDRs = cleanStrings(req.Server.TrustedProxyCIDRs)
	candidate.Display = req.Display
	candidate.Display.Timezone = strings.TrimSpace(candidate.Display.Timezone)
	candidate.Database = req.Database
	candidate.Database.Path = strings.TrimSpace(candidate.Database.Path)
	candidate.SSH = req.SSH
	candidate.SSH.KnownHostsPath = strings.TrimSpace(candidate.SSH.KnownHostsPath)
	candidate.Log = req.Log
	candidate.Metrics = req.Metrics
	candidate.Metrics.VMURL = strings.TrimRight(strings.TrimSpace(candidate.Metrics.VMURL), "/")
	candidate.Logs = req.Logs
	candidate.Logs.LokiURL = strings.TrimRight(strings.TrimSpace(candidate.Logs.LokiURL), "/")
	candidate.Scheduler = req.Scheduler
	candidate.PublicProbes = req.PublicProbes
	candidate.PublicProbes.DefaultScheme = strings.ToLower(strings.TrimSpace(candidate.PublicProbes.DefaultScheme))
	candidate.PublicProbes.Path = strings.TrimSpace(candidate.PublicProbes.Path)
	for i := range candidate.PublicProbes.Targets {
		candidate.PublicProbes.Targets[i].Server = strings.TrimSpace(candidate.PublicProbes.Targets[i].Server)
		candidate.PublicProbes.Targets[i].Bench = strings.TrimSpace(candidate.PublicProbes.Targets[i].Bench)
		candidate.PublicProbes.Targets[i].Site = strings.TrimSpace(candidate.PublicProbes.Targets[i].Site)
		candidate.PublicProbes.Targets[i].URL = strings.TrimSpace(candidate.PublicProbes.Targets[i].URL)
	}
	candidate.Auth.Realm = strings.TrimSpace(req.Auth.Realm)
	if req.Auth.Password != nil {
		candidate.Auth.Password = *req.Auth.Password
	}
	candidate.Alerts = config.AlertsConfig{
		Enabled:                   req.Alerts.Enabled,
		EvaluationIntervalSeconds: req.Alerts.EvaluationIntervalSeconds,
		NotifyRepeatSeconds:       req.Alerts.NotifyRepeatSeconds,
		VMQueryTimeoutSeconds:     req.Alerts.VMQueryTimeoutSeconds,
		Telegram: config.AlertsTelegram{
			BotToken:           candidate.Alerts.Telegram.BotToken,
			ChatIDs:            cleanStrings(req.Alerts.Telegram.ChatIDs),
			SendTimeoutSeconds: req.Alerts.Telegram.SendTimeoutSeconds,
		},
		Rules:           req.Alerts.Rules,
		DisableDefaults: req.Alerts.DisableDefaults,
	}
	if req.Alerts.Telegram.BotToken != nil {
		candidate.Alerts.Telegram.BotToken = strings.TrimSpace(*req.Alerts.Telegram.BotToken)
	}
	for i := range candidate.Alerts.Rules {
		candidate.Alerts.Rules[i].Name = strings.TrimSpace(candidate.Alerts.Rules[i].Name)
		candidate.Alerts.Rules[i].Expr = strings.TrimSpace(candidate.Alerts.Rules[i].Expr)
		candidate.Alerts.Rules[i].Severity = strings.TrimSpace(candidate.Alerts.Rules[i].Severity)
		candidate.Alerts.Rules[i].FingerprintLabels = cleanStrings(candidate.Alerts.Rules[i].FingerprintLabels)
	}
	candidate.Streaming = req.Streaming
	candidate.Streaming.MonitorID = strings.TrimSpace(candidate.Streaming.MonitorID)
	candidate.Streaming.ScriptPath = strings.TrimSpace(candidate.Streaming.ScriptPath)
	if err := validateDetailedConfig(candidate); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := config.SaveUI(h.path, &candidate, config.SecretUpdates{
		AuthPassword:     req.Auth.Password,
		TelegramBotToken: req.Alerts.Telegram.BotToken,
	}); err != nil {
		h.logger.Error("save settings", "err", err)
		writeErr(w, http.StatusInternalServerError, "could not save configuration: "+err.Error())
		return
	}
	h.cfg = cloneConfig(candidate)
	h.restartRequired = true
	writeJSON(w, http.StatusOK, h.responseLocked())
}

func (h *settingsHandlers) testIntegration(w http.ResponseWriter, r *http.Request) {
	var req integrationTestRequest
	if err := decodeJSONBody(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	h.mu.RLock()
	cfg := cloneConfig(h.cfg)
	h.mu.RUnlock()

	var endpoint string
	switch req.Kind {
	case "metrics":
		endpoint = strings.TrimRight(cfg.Metrics.VMURL, "/") + "/health"
	case "logs":
		endpoint = strings.TrimRight(cfg.Logs.LokiURL, "/") + "/ready"
	case "telegram":
		token := cfg.Alerts.Telegram.BotToken
		if req.BotToken != nil {
			token = strings.TrimSpace(*req.BotToken)
		}
		if token == "" {
			writeErr(w, http.StatusBadRequest, "Telegram bot token is not configured")
			return
		}
		endpoint = "https://api.telegram.org/bot" + url.PathEscape(token) + "/getMe"
	default:
		writeErr(w, http.StatusBadRequest, "kind must be metrics, logs, or telegram")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid endpoint")
		return
	}
	started := time.Now()
	resp, err := h.httpClient.Do(httpReq)
	latency := time.Since(started).Milliseconds()
	if err != nil {
		message := "Connection failed: " + err.Error()
		if req.Kind == "telegram" {
			message = "Telegram connection failed"
			var urlErr *url.Error
			if errors.As(err, &urlErr) && urlErr.Err != nil {
				message += ": " + urlErr.Err.Error()
			}
		}
		writeJSON(w, http.StatusOK, integrationTestResponse{OK: false, LatencyMS: latency, Message: message})
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		writeJSON(w, http.StatusOK, integrationTestResponse{OK: false, LatencyMS: latency, Message: fmt.Sprintf("Service returned HTTP %d", resp.StatusCode)})
		return
	}
	message := "Connection healthy"
	if req.Kind == "telegram" {
		message = "Telegram bot authenticated"
	}
	writeJSON(w, http.StatusOK, integrationTestResponse{OK: true, LatencyMS: latency, Message: message})
}
