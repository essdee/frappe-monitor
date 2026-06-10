package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEscapePromQLLabel guards the PromQL-injection fix: a path param
// that tries to break out of a label matcher must come back with its
// quotes/backslashes escaped (so it stays a literal value), and control
// characters stripped.
func TestEscapePromQLLabel(t *testing.T) {
	require.Equal(t, `x\"} or on() up{a=\"`, escapePromQLLabel(`x"} or on() up{a="`))
	require.Equal(t, `a\\b`, escapePromQLLabel(`a\b`))
	require.Equal(t, "ab", escapePromQLLabel("a\nb"), "newlines stripped")
	require.Equal(t, "ab", escapePromQLLabel("a\rb"), "carriage returns stripped")
	require.Equal(t, "plain-value", escapePromQLLabel("plain-value"))
}

// TestThrottleAllowsExactlyLimit guards the off-by-one fix: with a limit
// of 5, exactly 5 attempts are served and the 6th is blocked.
func TestThrottleAllowsExactlyLimit(t *testing.T) {
	th := newLoginThrottle(5, time.Minute)
	const ip = "1.2.3.4"
	for i := 0; i < 5; i++ {
		require.Falsef(t, th.blocked(ip), "attempt %d should not be blocked yet", i+1)
		th.recordFailure(ip)
	}
	require.True(t, th.blocked(ip), "6th attempt must be blocked")
}

// TestThrottleSweepsExpired guards the unbounded-map fix: expired buckets
// are dropped on the next record so the map can't grow without bound.
func TestThrottleSweepsExpired(t *testing.T) {
	th := newLoginThrottle(5, time.Millisecond)
	th.recordFailure("1.2.3.4")
	require.Len(t, th.buckets, 1)
	time.Sleep(3 * time.Millisecond)
	th.recordFailure("5.6.7.8") // sweep runs first, dropping the expired entry
	require.Len(t, th.buckets, 1, "expired bucket should have been swept")
}

// TestClientIPTrustModel guards the X-Forwarded-For spoofing fix.
func TestClientIPTrustModel(t *testing.T) {
	// Direct public peer: XFF is attacker-controlled and must be ignored.
	pub := &http.Request{RemoteAddr: "203.0.113.9:5555", Header: http.Header{}}
	pub.Header.Set("X-Forwarded-For", "1.2.3.4")
	require.Equal(t, "203.0.113.9", clientIP(pub),
		"public peer: ignore XFF, throttle on the real source")

	// Trusted local proxy (loopback): honor the RIGHTMOST hop — the
	// address the proxy actually observed, which a client can't forge.
	prox := &http.Request{RemoteAddr: "127.0.0.1:5555", Header: http.Header{}}
	prox.Header.Set("X-Forwarded-For", "1.2.3.4, 8.8.8.8")
	require.Equal(t, "8.8.8.8", clientIP(prox),
		"trusted peer: use rightmost XFF hop")

	// No XFF at all: fall back to RemoteAddr host.
	bare := &http.Request{RemoteAddr: "10.0.0.5:9999", Header: http.Header{}}
	require.Equal(t, "10.0.0.5", clientIP(bare))
}
