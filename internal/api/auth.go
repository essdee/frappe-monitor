package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// basicAuth returns a middleware that requires HTTP basic auth with the
// configured password. Username is ignored — this is a single-tenant
// shared-password setup; phase 7+ may add multi-user.
//
// Empty password means "auth disabled" and the middleware is a no-op
// (the caller is expected to gate the use site so this is unreachable
// when the feature is off).
//
// Browsers handle the credential prompt natively when they see a 401
// with WWW-Authenticate, so the SPA needs no login page. The realm
// is included in WWW-Authenticate to give browsers a stable cache
// key per deployment.
func basicAuth(password, realm string) func(http.Handler) http.Handler {
	if password == "" {
		return func(next http.Handler) http.Handler { return next }
	}
	if realm == "" {
		realm = "frappe-monitor"
	}
	pwBytes := []byte(password)
	challenge := `Basic realm="` + strings.ReplaceAll(realm, `"`, `\"`) + `", charset="UTF-8"`
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, gotPw, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(gotPw), pwBytes) != 1 {
				w.Header().Set("WWW-Authenticate", challenge)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
