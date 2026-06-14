package alerts

import (
	"strings"
	"testing"
	"time"
)

// An over-long alert body must still render to a card within Telegram's 4096
// limit AND with every HTML tag balanced — otherwise Telegram rejects it with a
// 400 ("Unclosed tag") every cycle, the infinite re-send loop the budgeting
// fixes. (Regression test for the rune-blind truncation bug.)
func TestFormatMessage_OverLongBodyStaysValidHTML(t *testing.T) {
	n := Notification{
		Severity: "critical",
		RuleName: "some_rule",
		Body:     strings.Repeat("A", 10000), // way over the limit
		Time:     time.Unix(0, 0).UTC(),
	}
	out := formatMessage(n)

	if rc := len([]rune(out)); rc > maxTelegramText {
		t.Errorf("rendered card is %d runes, want <= %d", rc, maxTelegramText)
	}
	for _, tag := range []string{"b", "i", "code"} {
		open := strings.Count(out, "<"+tag+">")
		closed := strings.Count(out, "</"+tag+">")
		if open != closed {
			t.Errorf("tag <%s> unbalanced: %d open vs %d close", tag, open, closed)
		}
	}
	if !strings.HasSuffix(out, "</code>") {
		t.Errorf("card should end with the closing </code> footer; tail = %q", out[len(out)-12:])
	}
}

// plainMessage (the fallback for cards that can't be sent as HTML) must be
// tag-free and within the limit, so it always delivers and breaks any re-send
// loop a formatting issue could otherwise cause.
func TestPlainMessage_TagFreeAndBounded(t *testing.T) {
	n := Notification{
		Severity: "warning",
		RuleName: "r",
		Body:     strings.Repeat("<&>", 5000), // escapes ~5x — would blow the HTML card
		Time:     time.Unix(0, 0).UTC(),
	}
	p := plainMessage(n)
	if rc := len([]rune(p)); rc > maxTelegramText {
		t.Errorf("plain message is %d runes, want <= %d", rc, maxTelegramText)
	}
	for _, tag := range []string{"<b>", "</b>", "<i>", "<code>", "</code>"} {
		if strings.Contains(p, tag) {
			t.Errorf("plain message must not contain the HTML tag %q", tag)
		}
	}
}

func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("hello", 10); got != "hello" {
		t.Errorf("under-limit should be unchanged, got %q", got)
	}
	if got := truncateRunes("hello", 0); got != "" {
		t.Errorf("n=0 should be empty, got %q", got)
	}
	got := truncateRunes("hello world", 6)
	if len([]rune(got)) != 6 {
		t.Errorf("truncated to %d runes, want 6: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a cut result should end with the ellipsis, got %q", got)
	}
	// Multi-byte runes must not be split (valid UTF-8 out).
	if got := truncateRunes(strings.Repeat("€", 10), 5); len([]rune(got)) != 5 {
		t.Errorf("multibyte truncation wrong: %q", got)
	}
}
