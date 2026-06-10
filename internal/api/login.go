package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
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
//   GET  /api/v1/whoami                  → 200 (proves session is valid)
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

// Brute-force throttle: 5 failed login attempts per IP per minute.
// Successes don't count. Single in-memory map; lost on restart.
var loginThrottle = newLoginThrottle(5, time.Minute)

type ipBucket struct {
	count   int
	resetAt time.Time
}

// maxThrottleBuckets bounds the throttle map so a flood of distinct
// keys can't exhaust memory. Expired buckets are swept on every record,
// so under normal load the map stays far below this.
const maxThrottleBuckets = 8192

type throttle struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*ipBucket
}

func newLoginThrottle(limit int, window time.Duration) *throttle {
	return &throttle{
		limit:   limit,
		window:  window,
		buckets: map[string]*ipBucket{},
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
	t.sweepLocked(now)
	b, ok := t.buckets[ip]
	if !ok || now.After(b.resetAt) {
		// Cap total tracked IPs so a key-flood can't grow the map
		// without bound. Once saturated we stop tracking new IPs; live
		// buckets keep working and the map drains as windows expire.
		if !ok && len(t.buckets) >= maxThrottleBuckets {
			return false
		}
		b = &ipBucket{count: 0, resetAt: now.Add(t.window)}
		t.buckets[ip] = b
	}
	b.count++
	return b.count <= t.limit
}

// sweepLocked drops buckets whose window has elapsed. Called under the
// lock on every record so the map can't accumulate stale entries.
func (t *throttle) sweepLocked(now time.Time) {
	for ip, b := range t.buckets {
		if now.After(b.resetAt) {
			delete(t.buckets, ip)
		}
	}
}

// blocked returns true if this IP is currently at/over the limit. Uses
// >= so exactly `limit` attempts are allowed before lockout (the check
// runs before the attempt is counted).
func (t *throttle) blocked(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	b, ok := t.buckets[ip]
	if !ok || time.Now().After(b.resetAt) {
		return false
	}
	return b.count >= t.limit
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

// clientIP extracts a best-effort client IP for throttling.
//
// RemoteAddr (the immediate peer) is the only address we can trust. We
// consult X-Forwarded-For ONLY when that peer is a local/private hop —
// i.e. a reverse proxy on the same host or network (e.g. Caddy). For a
// direct client, honoring XFF would let an attacker send a fresh header
// per request to dodge the brute-force throttle and mint unbounded
// throttle buckets. When trusted, we take the RIGHTMOST hop — the address
// the proxy actually observed — which a client can't forge by prepending
// its own XFF value.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if cand := strings.TrimSpace(parts[len(parts)-1]); cand != "" {
				return cand
			}
		}
	}
	return host
}

// loginReq matches what Login.vue POSTs.
type loginReq struct {
	Password string `json:"password"`
}

// newLoginHandler returns a handler that validates the password,
// throttles brute-force, and on success sets the session cookie.
func newLoginHandler(password string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
		if loginThrottle.blocked(ip) {
			writeErr(w, http.StatusTooManyRequests,
				"too many failed attempts; wait a minute")
			return
		}
		var req loginReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if password == "" {
			// Server has no password configured — any call is a misconfig.
			writeErr(w, http.StatusServiceUnavailable,
				"auth not configured on this server")
			return
		}
		if subtle.ConstantTimeCompare([]byte(req.Password), []byte(password)) != 1 {
			loginThrottle.recordFailure(ip)
			writeErr(w, http.StatusUnauthorized, "wrong password")
			return
		}
		// Issue cookie. HttpOnly so JS can't read it; SameSite=Strict so
		// a malicious cross-site form can't trigger an authed POST.
		// Secure attribute set when the connection looks TLS-terminated
		// (request itself is TLS, OR a reverse proxy says so).
		secure := r.TLS != nil ||
			strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
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
func logoutHandler(w http.ResponseWriter, r *http.Request) {
	secure := r.TLS != nil ||
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   secure,
	})
	w.WriteHeader(http.StatusNoContent)
}

// whoamiHandler is the SPA's "is my session still valid?" probe.
// Returns 200 if authed (cookie OR basic auth passed the middleware
// to get here); the middleware itself does the actual check.
func whoamiHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
