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

// Notification is the payload the evaluator hands to a Notifier on each
// fire/resolve. The notifier is responsible for any presentation —
// emoji, bolding, breadcrumb derivation. The evaluator only knows about
// the rule, the firing sample, and whether this is a fire or a resolve.
type Notification struct {
	Severity string            // "critical" | "warning" | "info" | ""
	RuleName string            // e.g. "site_unhealthy"
	Body     string            // pre-rendered message (Rule.Message template output)
	Labels   map[string]string // sample labels — used for the breadcrumb
	Resolved bool              // true when this is a recovery message
	Time     time.Time         // when the event happened (fire or clear)
}

// Notifier is the outbound side of the alert pipeline. The real
// implementation is TelegramClient; tests use NotifierFunc.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// NotifierFunc adapts a function to the Notifier interface — handy
// for tests that just want to record what was sent.
type NotifierFunc func(ctx context.Context, n Notification) error

func (f NotifierFunc) Notify(ctx context.Context, n Notification) error {
	return f(ctx, n)
}

// MultiNotifier fans out to a list of underlying Notifiers. Used to
// fan out one alert to N admin chat IDs. Errors are joined so a
// single chat outage doesn't silently drop the others.
type MultiNotifier []Notifier

func (m MultiNotifier) Notify(ctx context.Context, n Notification) error {
	var errs []error
	for _, x := range m {
		if err := x.Notify(ctx, n); err != nil {
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
	ChatID             string `json:"chat_id"`
	Text               string `json:"text"`
	ParseMode          string `json:"parse_mode,omitempty"`
	DisableLinkPreview bool   `json:"disable_web_page_preview,omitempty"`
}

// telegramSendResp matches the Telegram Bot API's reply shape; we only
// care about ok + description for error messaging.
type telegramSendResp struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Notify formats the notification and POSTs to /bot<token>/sendMessage.
//
// The Telegram Bot API rate limit is 30 messages/sec across a bot;
// our single-bot/multi-chat fan-out plus the >=1m evaluator cadence
// is far below that, so we don't backoff client-side.
func (c *TelegramClient) Notify(ctx context.Context, n Notification) error {
	if c.BotToken == "" || c.ChatID == "" {
		return fmt.Errorf("telegram: bot token or chat id missing")
	}
	body, _ := json.Marshal(telegramSendReq{
		ChatID:             c.ChatID,
		Text:               formatMessage(n),
		ParseMode:          "HTML",
		DisableLinkPreview: true,
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

// formatMessage renders one Notification as Telegram HTML. The shape:
//
//	🚨 <b>CRITICAL</b> — <i>rule_name</i>
//
//	<b>body sentence the rule template produced</b>
//
//	server → bench → site
//	<code>fired 21:14 IST</code>
//
// Breadcrumb pieces are omitted when their labels are missing; the
// whole breadcrumb line is omitted when no hierarchy labels exist.
// Keep this function deterministic — the same Notification must
// produce the same bytes (for testability and idempotency).
func formatMessage(n Notification) string {
	icon, head, verb := severityVisuals(n)

	when := n.Time
	if when.IsZero() {
		when = time.Now()
	}
	footer := fmt.Sprintf("%s %s", verb, when.Format("15:04 MST"))

	body := strings.TrimSpace(n.Body)
	if body == "" {
		// Defensive: a rule whose Message renders empty still produces
		// a useful card instead of a blank one.
		body = fmt.Sprintf("%s triggered.", n.RuleName)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b> — <i>%s</i>\n\n",
		icon, head, escapeHTML(n.RuleName))
	fmt.Fprintf(&b, "<b>%s</b>", escapeHTML(body))
	if crumb := buildBreadcrumb(n.Labels); crumb != "" {
		fmt.Fprintf(&b, "\n\n%s", crumb)
	}
	fmt.Fprintf(&b, "\n<code>%s</code>", escapeHTML(footer))
	return b.String()
}

// severityVisuals picks the leading icon, the bolded headline, and the
// footer verb. Resolved overrides severity — a recovered critical and
// a recovered warning both look the same calm green.
func severityVisuals(n Notification) (icon, head, verb string) {
	if n.Resolved {
		return "✅", "RESOLVED", "cleared"
	}
	switch strings.ToLower(strings.TrimSpace(n.Severity)) {
	case "critical":
		return "🚨", "CRITICAL", "fired"
	case "warning":
		return "⚠️", "WARNING", "fired"
	case "info":
		return "ℹ️", "INFO", "fired"
	default:
		return "🔔", "ALERT", "fired"
	}
}

// buildBreadcrumb pulls server/bench/site out of labels in that order
// and joins them with " → ". Returns "" if none are present so the
// caller can omit the line entirely.
func buildBreadcrumb(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, k := range []string{"server", "bench", "site"} {
		if v, ok := labels[k]; ok && strings.TrimSpace(v) != "" {
			parts = append(parts, escapeHTML(v))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, " → ")
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}
