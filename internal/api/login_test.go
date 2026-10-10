package api

import (
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClientIP_IgnoresForwardingHeaderFromUntrustedPeer(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/login", nil)
	req.RemoteAddr = "198.51.100.20:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	trust := newProxyTrust([]string{"127.0.0.1/32"})
	require.Equal(t, "198.51.100.20", clientIP(req, trust))
	require.False(t, requestIsHTTPS(req, trust))
}

func TestClientIP_UsesRightmostUntrustedAddressBehindTrustedProxy(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/v1/login", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "192.0.2.44, 127.0.0.2")
	req.Header.Set("X-Forwarded-Proto", "https")

	trust := newProxyTrust([]string{"127.0.0.0/8"})
	require.Equal(t, "192.0.2.44", clientIP(req, trust))
	require.True(t, requestIsHTTPS(req, trust))
}

func TestLoginThrottle_BoundsAttackerControlledBuckets(t *testing.T) {
	throttle := newLoginThrottle(1, time.Hour, 3)
	for _, ip := range []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "192.0.2.4"} {
		throttle.recordFailure(ip)
	}
	require.Len(t, throttle.buckets, 3)
	require.Contains(t, throttle.buckets, "__overflow__")

	throttle.recordFailure("192.0.2.99")
	require.True(t, throttle.blocked("192.0.2.100"), "new attacker IPs share the bounded overflow bucket")
}
