package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTelegram_PostsToSendMessage(t *testing.T) {
	var got struct {
		path string
		body telegramSendReq
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&got.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	tc := &TelegramClient{
		BotToken: "TOKEN", ChatID: "12345",
		HTTP: &http.Client{Timeout: 2 * time.Second}, APIBase: upstream.URL,
	}
	require.NoError(t, tc.Notify(context.Background(), Notification{
		Severity: "critical",
		RuleName: "rule1",
		Body:     "uh oh",
		Time:     time.Date(2026, 5, 7, 21, 14, 0, 0, time.UTC),
	}))

	require.Equal(t, "/botTOKEN/sendMessage", got.path)
	require.Equal(t, "12345", got.body.ChatID)
	require.Equal(t, "HTML", got.body.ParseMode)
	// Severity headline + rule + body must all appear.
	require.Contains(t, got.body.Text, "CRITICAL")
	require.Contains(t, got.body.Text, "🚨")
	require.Contains(t, got.body.Text, "rule1")
	require.Contains(t, got.body.Text, "uh oh")
	// Body should be bolded so it's the visual focal point.
	require.Contains(t, got.body.Text, "<b>uh oh</b>")
	// Footer carries the verb + timestamp.
	require.Contains(t, got.body.Text, "fired")
	require.Contains(t, got.body.Text, "21:14")
}

func TestTelegram_ResolvedLooksDifferent(t *testing.T) {
	var got telegramSendReq
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	tc := &TelegramClient{
		BotToken: "T", ChatID: "C",
		HTTP: &http.Client{Timeout: time.Second}, APIBase: upstream.URL,
	}
	require.NoError(t, tc.Notify(context.Background(), Notification{
		Severity: "critical",
		RuleName: "rule1",
		Body:     "uh oh",
		Resolved: true,
		Time:     time.Date(2026, 5, 7, 21, 18, 0, 0, time.UTC),
	}))

	require.Contains(t, got.Text, "RESOLVED")
	require.Contains(t, got.Text, "✅")
	require.Contains(t, got.Text, "cleared")
	require.NotContains(t, got.Text, "CRITICAL", "resolved headline must not still say CRITICAL")
	require.NotContains(t, got.Text, "🚨", "resolved card must not show the firing icon")
}

func TestTelegram_BreadcrumbDerivedFromLabels(t *testing.T) {
	var got telegramSendReq
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()

	tc := &TelegramClient{
		BotToken: "T", ChatID: "C",
		HTTP: &http.Client{Timeout: time.Second}, APIBase: upstream.URL,
	}
	require.NoError(t, tc.Notify(context.Background(), Notification{
		Severity: "warning",
		RuleName: "redis_queue_high",
		Body:     "queue piling up",
		Labels: map[string]string{
			"server": "prod-1",
			"bench":  "production-bench",
			"site":   "client-a.com",
			"queue":  "long",
		},
	}))
	require.Contains(t, got.Text, "prod-1 → production-bench → client-a.com",
		"breadcrumb should join server/bench/site in that order")
	require.Contains(t, got.Text, "⚠️")
}

func TestTelegram_BreadcrumbOmittedWhenNoHierarchyLabels(t *testing.T) {
	var got telegramSendReq
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstream.Close()
	tc := &TelegramClient{
		BotToken: "T", ChatID: "C",
		HTTP: &http.Client{Timeout: time.Second}, APIBase: upstream.URL,
	}
	require.NoError(t, tc.Notify(context.Background(), Notification{
		Severity: "info",
		RuleName: "global_rule",
		Body:     "something happened",
		Labels:   map[string]string{"only": "noise"},
	}))
	require.NotContains(t, got.Text, "→", "no hierarchy labels = no breadcrumb arrow")
	require.Contains(t, got.Text, "ℹ️")
}

func TestTelegram_FailsOnNon2xx(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer upstream.Close()
	tc := &TelegramClient{
		BotToken: "T", ChatID: "C",
		HTTP: &http.Client{Timeout: time.Second}, APIBase: upstream.URL,
	}
	err := tc.Notify(context.Background(), Notification{Severity: "info", RuleName: "x", Body: "y"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "429")
}

func TestTelegram_FailsOnAPIError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":false,"description":"chat not found"}`)
	}))
	defer upstream.Close()
	tc := &TelegramClient{
		BotToken: "T", ChatID: "C",
		HTTP: &http.Client{Timeout: time.Second}, APIBase: upstream.URL,
	}
	err := tc.Notify(context.Background(), Notification{Severity: "info", RuleName: "x", Body: "y"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "chat not found")
}

func TestMultiNotifier_FansOutAndJoinsErrors(t *testing.T) {
	n1ok := false
	n2ok := false
	mn := MultiNotifier{
		NotifierFunc(func(_ context.Context, _ Notification) error { n1ok = true; return nil }),
		NotifierFunc(func(_ context.Context, _ Notification) error { n2ok = true; return errors.New("boom") }),
		NotifierFunc(func(_ context.Context, _ Notification) error { return errors.New("kaboom") }),
	}
	err := mn.Notify(context.Background(), Notification{Severity: "info", RuleName: "rule", Body: "msg"})
	require.Error(t, err)
	require.True(t, n1ok)
	require.True(t, n2ok)
	require.Contains(t, err.Error(), "boom")
	require.Contains(t, err.Error(), "kaboom")
}

func TestEscapeHTML(t *testing.T) {
	require.Equal(t, "a&amp;b", escapeHTML("a&b"))
	require.Equal(t, "&lt;script&gt;", escapeHTML("<script>"))
}

func TestFormatMessage_EscapesUntrustedFields(t *testing.T) {
	out := formatMessage(Notification{
		Severity: "critical",
		RuleName: "evil<rule>",
		Body:     "body with <script>alert(1)</script>",
		Labels:   map[string]string{"server": "<weird>"},
	})
	require.NotContains(t, out, "<script>", "raw <script> must be escaped")
	require.Contains(t, out, "&lt;script&gt;")
	require.Contains(t, out, "&lt;weird&gt;")
	// HTML tags we control must remain unescaped.
	require.True(t, strings.Contains(out, "<b>") && strings.Contains(out, "</b>"),
		"our own <b> tags must survive escaping")
}

func TestFormatMessage_FallbackBodyWhenEmpty(t *testing.T) {
	out := formatMessage(Notification{Severity: "critical", RuleName: "noisy_rule"})
	require.Contains(t, out, "noisy_rule triggered.")
}
