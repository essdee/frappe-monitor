package api

import (
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
	pwBytes := []byte(password)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// 1. Cookie session (preferred — set by /login).
			if c, err := r.Cookie(sessionCookieName); err == nil {
				if validateSession(c.Value, password) {
					next.ServeHTTP(w, r)
					return
				}
			}
			// 2. HTTP basic auth (for curl + automation).
			if _, got, ok := r.BasicAuth(); ok {
				if subtle.ConstantTimeCompare([]byte(got), pwBytes) == 1 {
					next.ServeHTTP(w, r)
					return
				}
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

// basicAuth is kept for backwards compatibility with existing tests.
// New callers should use authGate; this just delegates.
func basicAuth(password, _ string) func(http.Handler) http.Handler {
	return authGate(password)
}
