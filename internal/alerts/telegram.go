package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Notifier is the outbound side of the alert pipeline. The real
// implementation is TelegramClient; tests use NotifierFunc.
type Notifier interface {
	Notify(ctx context.Context, severity, ruleName, message string) error
}

// NotifierFunc adapts a function to the Notifier interface — handy
// for tests that just want to record what was sent.
type NotifierFunc func(ctx context.Context, severity, ruleName, message string) error

func (f NotifierFunc) Notify(ctx context.Context, severity, ruleName, message string) error {
	return f(ctx, severity, ruleName, message)
}

// MultiNotifier fans out to a list of underlying Notifiers. Used to
// fan out one alert to N admin chat IDs. Errors are joined so a
// single chat outage doesn't silently drop the others.
type MultiNotifier []Notifier

func (m MultiNotifier) Notify(ctx context.Context, severity, ruleName, message string) error {
	var errs []error
	for _, n := range m {
		if err := n.Notify(ctx, severity, ruleName, message); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// TelegramClient sends one chat's worth of messages.
type TelegramClient struct {
	BotToken string
	ChatID   string
	HTTP     *http.Client
	APIBase  string // override for tests; default https://api.telegram.org
}

// NewTelegramClient builds a client targeting a single chat.
func NewTelegramClient(botToken, chatID string, timeout time.Duration) *TelegramClient {
	return &TelegramClient{
		BotToken: botToken,
		ChatID:   chatID,
		HTTP:     &http.Client{Timeout: timeout},
		APIBase:  "https://api.telegram.org",
	}
}

// telegramSendReq is the JSON body for sendMessage.
type telegramSendReq struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// telegramSendResp matches the Telegram Bot API's reply shape; we only
// care about ok + description for error messaging.
type telegramSendResp struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Notify formats the message and POSTs to /bot<token>/sendMessage.
// Severity becomes a leading emoji-ish prefix; ruleName is bolded; the
// caller-supplied message body is appended.
//
// The Telegram Bot API rate limit is 30 messages/sec across a bot;
// our single-bot/multi-chat fan-out plus the >=1m evaluator cadence
// is far below that, so we don't backoff client-side.
func (c *TelegramClient) Notify(ctx context.Context, severity, ruleName, message string) error {
	if c.BotToken == "" || c.ChatID == "" {
		return fmt.Errorf("telegram: bot token or chat id missing")
	}
	body, _ := json.Marshal(telegramSendReq{
		ChatID:    c.ChatID,
		Text:      formatMessage(severity, ruleName, message),
		ParseMode: "HTML",
	})
	api := strings.TrimRight(c.APIBase, "/")
	url := fmt.Sprintf("%s/bot%s/sendMessage", api, c.BotToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("telegram: send: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("telegram: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out telegramSendResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("telegram: decode: %w", err)
	}
	if !out.OK {
		return fmt.Errorf("telegram: api error: %s", out.Description)
	}
	return nil
}

// formatMessage builds the text body. HTML parse mode for bolding;
// the SeverityPrefix is plain text so it survives clients that strip
// emoji.
func formatMessage(severity, ruleName, message string) string {
	prefix := "[ALERT]"
	switch severity {
	case "critical":
		prefix = "[CRITICAL]"
	case "warning":
		prefix = "[WARN]"
	case "info":
		prefix = "[INFO]"
	}
	// HTML escape the rule name + message body since users-controlled
	// label values flow through here; bold the rule name only.
	return fmt.Sprintf("%s <b>%s</b>\n%s",
		prefix, escapeHTML(ruleName), escapeHTML(message))
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
