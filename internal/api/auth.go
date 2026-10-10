package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
)

// authGate accepts EITHER a valid session cookie OR HTTP basic auth.
// Returns 401 (no WWW-Authenticate header) so browsers don't pop the
// native credential prompt — the SPA catches 401 and routes to /login.
// curl/CI users keep working via -u user:password (basic auth).
//
// Empty password means "auth disabled" and the middleware is a no-op
// (the caller is expected to gate the use site so this is unreachable
// when the feature is off).
func authGate(password string) func(http.Handler) http.Handler {
	if password == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requestAuthenticated(r, password) {
				next.ServeHTTP(w, r)
				return
			}
			// 3. Reject — JSON response, NO WWW-Authenticate so the
			//    browser does not pop a dialog. SPA's fetch wrapper
			//    catches 401 and redirects to /login.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
		})
	}
}

func requestAuthenticated(r *http.Request, password string) bool {
	if password == "" {
		return true
	}
	// Cookie session is preferred; Basic auth remains available for CLI/API
	// automation and for the public authentication-state endpoint.
	if c, err := r.Cookie(sessionCookieName); err == nil && validateSession(c.Value, password) {
		return true
	}
	if _, got, ok := r.BasicAuth(); ok && securePasswordEqual(got, password) {
		return true
	}
	return false
}

func securePasswordEqual(got, want string) bool {
	gotHash := sha256.Sum256([]byte(got))
	wantHash := sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) == 1
}

// basicAuth is kept for backwards compatibility with existing tests.
// New callers should use authGate; this just delegates.
func basicAuth(password, _ string) func(http.Handler) http.Handler {
	return authGate(password)
}
