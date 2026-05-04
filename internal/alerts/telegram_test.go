package alerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
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
	require.NoError(t, tc.Notify(context.Background(), "critical", "rule1", "uh oh"))

	require.Equal(t, "/botTOKEN/sendMessage", got.path)
	require.Equal(t, "12345", got.body.ChatID)
	require.Contains(t, got.body.Text, "[CRITICAL]")
	require.Contains(t, got.body.Text, "rule1")
	require.Contains(t, got.body.Text, "uh oh")
	require.Equal(t, "HTML", got.body.ParseMode)
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
	err := tc.Notify(context.Background(), "info", "x", "y")
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
	err := tc.Notify(context.Background(), "info", "x", "y")
	require.Error(t, err)
	require.Contains(t, err.Error(), "chat not found")
}

func TestMultiNotifier_FansOutAndJoinsErrors(t *testing.T) {
	n1ok := false
	n2ok := false
	mn := MultiNotifier{
		NotifierFunc(func(_ context.Context, _, _, _ string) error { n1ok = true; return nil }),
		NotifierFunc(func(_ context.Context, _, _, _ string) error { n2ok = true; return errors.New("boom") }),
		NotifierFunc(func(_ context.Context, _, _, _ string) error { return errors.New("kaboom") }),
	}
	err := mn.Notify(context.Background(), "info", "rule", "msg")
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
