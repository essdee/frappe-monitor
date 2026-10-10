package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Phase 7 v1.5 — cookie-based session auth.
//
// Replaces the browser-native HTTP basic prompt with a proper in-view
// login page. Wire format:
//
//   POST /api/v1/login   {password} → 204 + Set-Cookie monitor_session
//   POST /api/v1/logout                 → 204 + Set-Cookie max-age=0
//   GET  /api/v1/whoami                  → 200 + authentication mode
//
// Session is a stateless HMAC-signed token: "<exp_unix>.<hex_hmac>".
// Signing key is derived from the auth password — changing the password
// invalidates every active session. No server-side store; restart-safe.
//
// The auth middleware accepts EITHER a valid cookie OR HTTP basic auth
// (for curl/CI). It NO LONGER sets WWW-Authenticate, so browsers won't
// pop the native credential prompt; the SPA catches 401 and routes
// to /login instead.

const sessionCookieName = "monitor_session"
const sessionTTL = 7 * 24 * time.Hour

const maxLoginThrottleBuckets = 10_000

type ipBucket struct {
	count   int
	resetAt time.Time
}

type throttle struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	buckets    map[string]*ipBucket
	maxBuckets int
}

func newLoginThrottle(limit int, window time.Duration, maxBuckets int) *throttle {
	return &throttle{
		limit:      limit,
		window:     window,
		buckets:    map[string]*ipBucket{},
		maxBuckets: maxBuckets,
	}
}

// allow returns true if the IP is below its limit for the current
// window. Caller invokes after a FAILED login to count the attempt;
// successful logins should NOT call allow (we don't want to penalize
// legitimate users for typing fast).
func (t *throttle) recordFailure(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.cleanupLocked(now)
	ip = t.bucketKeyLocked(ip)
	b, ok := t.buckets[ip]
	if !ok || now.After(b.resetAt) {
		b = &ipBucket{count: 0, resetAt: now.Add(t.window)}
		t.buckets[ip] = b
	}
	b.count++
	return b.count <= t.limit
}

func (t *throttle) reset(ip string) {
	t.mu.Lock()
	delete(t.buckets, t.bucketKeyLocked(ip))
	t.mu.Unlock()
}

// blocked returns true if this IP is currently over the limit.
func (t *throttle) blocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	t.cleanupLocked(now)
	b, ok := t.buckets[t.bucketKeyLocked(ip)]
	if !ok {
		return false
	}
	return b.count > t.limit
}

func (t *throttle) cleanupLocked(now time.Time) {
	for key, bucket := range t.buckets {
		if now.After(bucket.resetAt) {
			delete(t.buckets, key)
		}
	}
}

func (t *throttle) bucketKeyLocked(ip string) string {
	if _, exists := t.buckets[ip]; exists {
		return ip
	}
	const overflow = "__overflow__"
	if _, exists := t.buckets[overflow]; exists {
		return overflow
	}
	if t.maxBuckets <= 1 || len(t.buckets) >= t.maxBuckets-1 {
		return overflow
	}
	return ip
}

// deriveSessionKey returns the HMAC key used to sign session cookies.
// Bound to the auth password so a password change invalidates all
// active sessions automatically.
func deriveSessionKey(password string) []byte {
	mac := hmac.New(sha256.New, []byte("frappe-monitor.session.v1"))
	mac.Write([]byte(password))
	return mac.Sum(nil)
}

// issueSession builds a signed token "<exp>.<hex_hmac>".
func issueSession(password string, ttl time.Duration) string {
	exp := strconv.FormatInt(time.Now().Add(ttl).Unix(), 10)
	mac := hmac.New(sha256.New, deriveSessionKey(password))
	mac.Write([]byte(exp))
	return exp + "." + hex.EncodeToString(mac.Sum(nil))
}

// validateSession returns true if cookie is a non-expired token signed
// with the current password's session key. Constant-time compare so
// timing attacks can't recover bytes of the signature.
func validateSession(cookie, password string) bool {
	dot := strings.IndexByte(cookie, '.')
	if dot < 1 || dot == len(cookie)-1 {
		return false
	}
	expStr := cookie[:dot]
	sig := cookie[dot+1:]
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	mac := hmac.New(sha256.New, deriveSessionKey(password))
	mac.Write([]byte(expStr))
	want := hex.EncodeToString(mac.Sum(nil))
	return subtle.ConstantTimeCompare([]byte(want), []byte(sig)) == 1
}

type proxyTrust struct {
	prefixes []netip.Prefix
}

func newProxyTrust(cidrs []string) proxyTrust {
	trust := proxyTrust{}
	for _, cidr := range cidrs {
		if prefix, err := netip.ParsePrefix(cidr); err == nil {
			trust.prefixes = append(trust.prefixes, prefix)
		}
	}
	return trust
}

func remoteIP(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = strings.Trim(r.RemoteAddr, "[]")
	}
	ip, err := netip.ParseAddr(host)
	return ip.Unmap(), err == nil
}

func (p proxyTrust) contains(ip netip.Addr) bool {
	for _, prefix := range p.prefixes {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

// clientIP trusts forwarding headers only when the direct peer is in an
// explicitly configured proxy CIDR. It walks the chain from right to left so
// a client cannot smuggle a fake leftmost address through a trusted proxy.
func clientIP(r *http.Request, trust proxyTrust) string {
	peer, ok := remoteIP(r)
	if !ok {
		return r.RemoteAddr
	}
	if !trust.contains(peer) {
		return peer.String()
	}
	chain := make([]netip.Addr, 0, 4)
	for _, raw := range strings.Split(r.Header.Get("X-Forwarded-For"), ",") {
		if ip, err := netip.ParseAddr(strings.TrimSpace(raw)); err == nil {
			chain = append(chain, ip.Unmap())
		}
	}
	chain = append(chain, peer)
	for i := len(chain) - 1; i >= 0; i-- {
		if !trust.contains(chain[i]) {
			return chain[i].String()
		}
	}
	return chain[0].String()
}

func requestIsHTTPS(r *http.Request, trust proxyTrust) bool {
	if r.TLS != nil {
		return true
	}
	peer, ok := remoteIP(r)
	return ok && trust.contains(peer) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// loginReq matches what Login.vue POSTs.
type loginReq struct {
	Password string `json:"password"`
}

// newLoginHandler returns a handler that validates the password,
// throttles brute-force, and on success sets the session cookie.
func newLoginHandler(password string, trust proxyTrust, loginThrottle *throttle) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r, trust)
		if loginThrottle.blocked(ip) {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(loginThrottle.window.Seconds()), 10))
			writeErr(w, http.StatusTooManyRequests,
				"too many failed attempts; wait a minute")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		var req loginReq
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if password == "" {
			// Server has no password configured — any call is a misconfig.
			writeErr(w, http.StatusServiceUnavailable,
				"auth not configured on this server")
			return
		}
		if !securePasswordEqual(req.Password, password) {
			loginThrottle.recordFailure(ip)
			writeErr(w, http.StatusUnauthorized, "wrong password")
			return
		}
		loginThrottle.reset(ip)
		// Issue cookie. HttpOnly so JS can't read it; SameSite=Strict so
		// a malicious cross-site form can't trigger an authed POST.
		// Secure attribute set when the connection looks TLS-terminated
		// (request itself is TLS, OR a reverse proxy says so).
		secure := requestIsHTTPS(r, trust)
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    issueSession(password, sessionTTL),
			Path:     "/",
			MaxAge:   int(sessionTTL.Seconds()),
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			Secure:   secure,
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// logoutHandler clears the cookie regardless of state. Matches the
// Secure attribute used at login time — without that, browsers behind
// HTTPS may keep the cookie because the deletion cookie's attributes
// don't match the original.
func logoutHandler(trust proxyTrust) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
			Secure:   requestIsHTTPS(r, trust),
		})
		w.WriteHeader(http.StatusNoContent)
	}
}

// whoamiHandler is intentionally public and always returns 200. That lets the
// router decide which view to mount without first producing a noisy 401 in the
// browser console. It reports state only and never exposes credentials.
func whoamiHandler(password, timezone string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if timezone == "" {
			timezone = "UTC"
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"authenticated": requestAuthenticated(r, password),
			"auth_enabled":  password != "",
			"timezone":      timezone,
		})
	}
}
