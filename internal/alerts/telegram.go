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

// redact masks the bot token in an arbitrary string so it never reaches a
// log line. The token appears in *url.Error messages (which embed the full
// /bot<token>/ request path) and could appear in other transport errors.
func (c *TelegramClient) redact(s string) string {
	if c.BotToken == "" {
		return s
	}
	return strings.ReplaceAll(s, c.BotToken, "***")
}

// maxTelegramText is the Telegram Bot API per-message text ceiling (4096
// chars). A longer message is rejected with HTTP 400 every evaluation
// cycle — the alert never marks notified, so it re-sends forever. Truncate
// defensively on a rune boundary so an over-long rendered card still delivers.
const maxTelegramText = 4096

// truncateRunes caps s at n runes on a rune boundary, appending an ellipsis
// when it cuts. n <= 0 yields "". Operating on a rune boundary keeps the result
// valid UTF-8; callers that need valid HTML must truncate the PLAIN text before
// escaping/wrapping (see formatMessage), never the rendered HTML.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	const ell = "…"
	if n <= len([]rune(ell)) {
		return string(r[:n])
	}
	return string(r[:n-len([]rune(ell))]) + ell
}

// telegramHTTPError is a non-2xx response from the Bot API, typed so the caller
// can distinguish a permanent client error (4xx — malformed message, retrying
// won't help) from a transient 5xx.
type telegramHTTPError struct {
	status int
	body   string
}

func (e *telegramHTTPError) Error() string {
	return fmt.Sprintf("telegram: HTTP %d: %s", e.status, e.body)
}

func isClientError(err error) bool {
	var he *telegramHTTPError
	return errors.As(err, &he) && he.status >= 400 && he.status < 500
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
	text := formatMessage(n)
	// If escaping expanded the (body-budgeted) card past the limit — e.g. a body
	// full of <, > or & — the HTML form can't be sent safely; send plain text.
	if len([]rune(text)) > maxTelegramText {
		return c.send(ctx, plainMessage(n), "")
	}
	err := c.send(ctx, text, "HTML")
	// A 4xx (e.g. an "Unclosed tag"/"can't parse entities" 400) means the HTML
	// is malformed — retrying it would re-fail every cycle forever. Fall back to
	// plain text ONCE so a formatting bug can never become an infinite re-send
	// loop. Transient (network/5xx) errors propagate so the caller can retry.
	if isClientError(err) {
		return c.send(ctx, plainMessage(n), "")
	}
	return err
}

// send POSTs one message to the Bot API. parseMode "" sends plain text.
func (c *TelegramClient) send(ctx context.Context, text, parseMode string) error {
	body, _ := json.Marshal(telegramSendReq{
		ChatID:             c.ChatID,
		Text:               text,
		ParseMode:          parseMode,
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
		// http.Client.Do returns a *url.Error whose message embeds the full
		// request URL — which contains /bot<token>/. Stringify and redact rather
		// than %w-wrapping so the bot token never reaches the evaluator logs.
		return fmt.Errorf("telegram: send: %s", c.redact(err.Error()))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &telegramHTTPError{status: resp.StatusCode, body: strings.TrimSpace(string(b))}
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

	rule := truncateRunes(strings.TrimSpace(n.RuleName), 256) // operator input — cap it
	body := strings.TrimSpace(n.Body)
	if body == "" {
		// Defensive: a rule whose Message renders empty still produces
		// a useful card instead of a blank one.
		body = fmt.Sprintf("%s triggered.", rule)
	}
	crumb := buildBreadcrumb(n.Labels)

	// Budget the PLAIN body so the final rendered card stays within Telegram's
	// 4096-char limit with every tag balanced. Truncating the plain text here
	// (then escaping + wrapping) guarantees the <b>…</b>/<code>…</code> always
	// close — unlike slicing the rendered HTML, which can strand an open tag and
	// get the card permanently rejected (400 → re-send-forever). For ordinary
	// ASCII bodies escaping is ~1:1; a body that escapes much larger trips the
	// length re-check in Notify and is sent as plain text instead.
	// 64 generously covers the fixed tag/separator chars (<b></b><i></i><code>
	// </code>, the " — " divider, newlines) plus the truncation ellipsis. The
	// rendered card is re-checked against the hard limit in Notify regardless.
	overhead := len([]rune(icon)) + len([]rune(head)) + len([]rune(escapeHTML(rule))) +
		len([]rune(escapeHTML(footer))) + len([]rune(crumb)) + 64
	body = truncateRunes(body, maxTelegramText-overhead)

	var b strings.Builder
	fmt.Fprintf(&b, "%s <b>%s</b> — <i>%s</i>\n\n", icon, head, escapeHTML(rule))
	fmt.Fprintf(&b, "<b>%s</b>", escapeHTML(body))
	if crumb != "" {
		fmt.Fprintf(&b, "\n\n%s", crumb)
	}
	fmt.Fprintf(&b, "\n<code>%s</code>", escapeHTML(footer))
	return b.String()
}

// plainMessage renders the notification as tag-free text, truncated safely. It
// is the fallback when the HTML card can't be sent (over-long after escaping, or
// rejected by the API) — plain text has no tags to strand, so it always
// delivers, breaking any re-send loop a formatting issue might otherwise cause.
func plainMessage(n Notification) string {
	_, head, verb := severityVisuals(n)
	when := n.Time
	if when.IsZero() {
		when = time.Now()
	}
	rule := truncateRunes(strings.TrimSpace(n.RuleName), 256)
	body := strings.TrimSpace(n.Body)
	if body == "" {
		body = rule + " triggered."
	}
	msg := fmt.Sprintf("%s — %s\n\n%s\n%s %s", head, rule, body, verb, when.Format("15:04 MST"))
	return truncateRunes(msg, maxTelegramText)
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
