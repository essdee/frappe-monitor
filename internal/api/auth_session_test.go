package api

import (
	"bytes"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// (1) Token codec: issueSession / validateSession
// ---------------------------------------------------------------------------

// TestSessionToken_RoundTripAndRejections exercises the stateless HMAC
// session token: a freshly issued token validates under the same password,
// and every tamper / rotation / expiry / malformed case is rejected.
func TestSessionToken_RoundTripAndRejections(t *testing.T) {
	const pw = "correct horse battery staple"

	// Happy path: issue with a future expiry, validate with same password.
	good := issueSession(pw, time.Hour)
	require.True(t, validateSession(good, pw),
		"a freshly issued token must validate under the issuing password")

	// Password rotation: the signing key is derived from the password, so a
	// rotated password must reject an old token.
	require.False(t, validateSession(good, "rotated-pw"),
		"token must not validate after the password rotates")

	// Expired token: negative ttl puts the expiry in the past.
	expired := issueSession(pw, -time.Hour)
	require.False(t, validateSession(expired, pw),
		"an expired token must be rejected")

	// Tampered signature: flip a single hex byte of the signature. The token
	// is "<exp>.<hex_hmac>"; the byte right after the dot is part of the sig.
	tampered := flipSigByte(t, good)
	require.NotEqual(t, good, tampered)
	require.False(t, validateSession(tampered, pw),
		"a token with a corrupted signature must be rejected")

	// Malformed inputs: no dot, empty, dangling dot, leading dot, etc.
	for _, bad := range []string{
		"",                 // empty
		"abc",              // no dot at all
		"123.",             // dot at the very end → empty signature
		".sig",             // dot at index 0 → empty expiry
		".",                // just a dot
		"123",              // numeric but no dot
		"notanum.deadbeef", // dot present but expiry is not an integer
	} {
		require.Falsef(t, validateSession(bad, pw),
			"malformed input %q must be rejected", bad)
	}
}

// flipSigByte returns the token with one hex character of its signature
// changed to a different valid hex character, keeping the token well-formed
// (still "<exp>.<hex>") so only the signature comparison can fail.
func flipSigByte(t *testing.T, token string) string {
	t.Helper()
	dot := bytes.IndexByte([]byte(token), '.')
	require.Greater(t, dot, 0, "token must contain a dot")
	require.Less(t, dot, len(token)-1, "token must have a signature after the dot")
	b := []byte(token)
	// First signature character sits at dot+1.
	i := dot + 1
	if b[i] == '0' {
		b[i] = '1'
	} else {
		b[i] = '0'
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// (2) httptest end-to-end: login → whoami → logout, plus the brute-force
//     throttle (5 wrong → 401, 6th → 429).
// ---------------------------------------------------------------------------

func TestSession_LoginWhoamiLogoutAndThrottle(t *testing.T) {
	// The login throttle is package-global mutable state. Swap in a fresh
	// one for determinism and restore the original afterward so we don't
	// leak state into other tests in the package.
	prev := loginThrottle
	loginThrottle = newLoginThrottle(5, time.Minute)
	t.Cleanup(func() { loginThrottle = prev })

	r := NewRouter(Deps{AuthPassword: "secret", MaxBodyBytes: 1 << 20})
	ts := httptest.NewServer(r)
	defer ts.Close()

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	client := &http.Client{Jar: jar}

	// --- POST /login with the correct password -> 204 + Set-Cookie ---
	resp := postJSON(t, client, ts.URL+"/api/v1/login", `{"password":"secret"}`)
	require.Equal(t, http.StatusNoContent, resp.StatusCode,
		"correct password should log in")
	require.NotEmpty(t, sessionCookie(resp),
		"login must set the session cookie")
	resp.Body.Close()

	// The jar should now carry the session cookie for this server.
	require.NotEmpty(t, jarCookie(t, jar, ts.URL, sessionCookieName),
		"cookie jar should hold the session cookie after login")

	// --- GET /whoami with the jar -> 200 ---
	wResp, err := client.Get(ts.URL + "/api/v1/whoami")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, wResp.StatusCode,
		"whoami should succeed while the session cookie is valid")
	wResp.Body.Close()

	// --- POST /logout -> 204 and the cookie is cleared ---
	loResp, err := client.Post(ts.URL+"/api/v1/logout", "application/json", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, loResp.StatusCode)
	// The logout response sends a max-age<0 deletion cookie; the jar drops it.
	loResp.Body.Close()
	require.Empty(t, jarCookie(t, jar, ts.URL, sessionCookieName),
		"logout should clear the session cookie from the jar")

	// After logout the (now cookie-less) client must be rejected by /whoami.
	wResp2, err := client.Get(ts.URL + "/api/v1/whoami")
	require.NoError(t, err)
	require.Equal(t, http.StatusUnauthorized, wResp2.StatusCode,
		"whoami should fail after logout clears the cookie")
	wResp2.Body.Close()

	// --- Brute-force throttle: 5 wrong-password attempts -> 401 each ---
	for i := 1; i <= 5; i++ {
		bad := postJSON(t, client, ts.URL+"/api/v1/login", `{"password":"nope"}`)
		require.Equalf(t, http.StatusUnauthorized, bad.StatusCode,
			"wrong-password attempt %d should be 401", i)
		bad.Body.Close()
	}

	// --- 6th attempt -> 429 (locked out for the window) ---
	locked := postJSON(t, client, ts.URL+"/api/v1/login", `{"password":"nope"}`)
	require.Equal(t, http.StatusTooManyRequests, locked.StatusCode,
		"6th attempt within the window must be throttled")
	locked.Body.Close()

	// Even the CORRECT password is throttled once locked out.
	stillLocked := postJSON(t, client, ts.URL+"/api/v1/login", `{"password":"secret"}`)
	require.Equal(t, http.StatusTooManyRequests, stillLocked.StatusCode,
		"throttle blocks before the password is even checked")
	stillLocked.Body.Close()
}

// postJSON POSTs a JSON body and returns the response (body still open).
func postJSON(t *testing.T, c *http.Client, url, body string) *http.Response {
	t.Helper()
	resp, err := c.Post(url, "application/json", bytes.NewBufferString(body))
	require.NoError(t, err)
	return resp
}

// sessionCookie pulls the monitor_session Set-Cookie off a response, if any.
func sessionCookie(resp *http.Response) string {
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			return c.Value
		}
	}
	return ""
}

// jarCookie returns the value of the named cookie the jar holds for url.
func jarCookie(t *testing.T, jar *cookiejar.Jar, rawURL, name string) string {
	t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	require.NoError(t, err)
	for _, c := range jar.Cookies(req.URL) {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}
