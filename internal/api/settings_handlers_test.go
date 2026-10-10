package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"frappe-monitor/internal/config"
)

func settingsFixture(t *testing.T) (string, *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
auth:
  password: do-not-expose
alerts:
  telegram:
    bot_token: do-not-expose-token
custom_section:
  retained: yes
`), 0o600))
	cfg, err := config.Load(path)
	require.NoError(t, err)
	return path, cfg
}

func updateRequestFromConfig(c *config.Config) settingsUpdateRequest {
	var req settingsUpdateRequest
	req.Server = c.Server
	req.Display = c.Display
	req.Database = c.Database
	req.SSH = c.SSH
	req.Log = c.Log
	req.Metrics = c.Metrics
	req.Logs = c.Logs
	req.Scheduler = c.Scheduler
	req.PublicProbes = c.PublicProbes
	req.Auth.Realm = c.Auth.Realm
	req.Alerts = settingsAlertsUpdate{
		Enabled:                   c.Alerts.Enabled,
		EvaluationIntervalSeconds: c.Alerts.EvaluationIntervalSeconds,
		NotifyRepeatSeconds:       c.Alerts.NotifyRepeatSeconds,
		VMQueryTimeoutSeconds:     c.Alerts.VMQueryTimeoutSeconds,
		Telegram: settingsTelegramUpdate{
			ChatIDs:            c.Alerts.Telegram.ChatIDs,
			SendTimeoutSeconds: c.Alerts.Telegram.SendTimeoutSeconds,
		},
		Rules:           c.Alerts.Rules,
		DisableDefaults: c.Alerts.DisableDefaults,
	}
	req.Streaming = c.Streaming
	return req
}

func TestSettings_GetRedactsSecrets(t *testing.T) {
	path, cfg := settingsFixture(t)
	ts := httptest.NewServer(NewRouter(Deps{ConfigPath: path, Config: cfg}))
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/api/v1/settings")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), "do-not-expose")
	var got settingsResponse
	require.NoError(t, json.Unmarshal(body, &got))
	require.True(t, got.Auth.PasswordConfigured)
	require.True(t, got.Alerts.Telegram.BotTokenConfigured)
	require.False(t, got.RestartRequired)
	require.NotNil(t, got.Alerts.Telegram.ChatIDs)
	require.NotNil(t, got.Alerts.Rules)
	require.NotNil(t, got.Streaming.Files)
	require.NotNil(t, got.PublicProbes.Targets)
}

func TestSettings_UpdatePersistsAndReportsRestart(t *testing.T) {
	path, cfg := settingsFixture(t)
	ts := httptest.NewServer(NewRouter(Deps{ConfigPath: path, Config: cfg}))
	t.Cleanup(ts.Close)
	req := updateRequestFromConfig(cfg)
	req.Scheduler.DefaultIntervalSeconds = 75
	req.Log.Level = "debug"
	req.Display.Timezone = "Asia/Kolkata"
	req.Database.Path = "./data/changed.db"
	req.PublicProbes.Enabled = true
	req.PublicProbes.Targets = []config.PublicProbeTarget{{
		Server: "Production", Bench: "frappe-bench", Site: "erp.example.com",
		URL: "https://erp.example.com/api/method/ping",
	}}
	body, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq, err := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/settings", bytes.NewReader(body))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got settingsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.True(t, got.RestartRequired)
	require.Equal(t, 75, got.Scheduler.DefaultIntervalSeconds)

	reloaded, err := config.Load(path)
	require.NoError(t, err)
	require.Equal(t, 75, reloaded.Scheduler.DefaultIntervalSeconds)
	require.Equal(t, "debug", reloaded.Log.Level)
	require.Equal(t, "Asia/Kolkata", reloaded.Display.Timezone)
	require.Equal(t, "./data/changed.db", reloaded.Database.Path)
	require.True(t, reloaded.PublicProbes.Enabled)
	require.Equal(t, "erp.example.com", reloaded.PublicProbes.Targets[0].Site)
	require.Equal(t, "do-not-expose", reloaded.Auth.Password)
	require.Equal(t, "do-not-expose-token", reloaded.Alerts.Telegram.BotToken)
	saved, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(saved), "custom_section:")
	_, err = os.Stat(path + ".bak")
	require.NoError(t, err)
}

func TestSettings_UpdateRejectsInvalidConfigWithoutWriting(t *testing.T) {
	path, cfg := settingsFixture(t)
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	ts := httptest.NewServer(NewRouter(Deps{ConfigPath: path, Config: cfg}))
	t.Cleanup(ts.Close)
	req := updateRequestFromConfig(cfg)
	req.Metrics.VMURL = "not a URL"
	body, err := json.Marshal(req)
	require.NoError(t, err)
	httpReq, err := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/settings", bytes.NewReader(body))
	require.NoError(t, err)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, original, after)
}

func TestSettings_TestMetricsIntegration(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/health", r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	path, cfg := settingsFixture(t)
	cfg.Metrics.VMURL = backend.URL
	ts := httptest.NewServer(NewRouter(Deps{ConfigPath: path, Config: cfg}))
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/api/v1/settings/test", "application/json", bytes.NewBufferString(`{"kind":"metrics"}`))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var got integrationTestResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	require.True(t, got.OK)
	require.Equal(t, "Connection healthy", got.Message)
}
