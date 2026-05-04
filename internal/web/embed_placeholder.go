//go:build !embed_dist

// Package web's no-frontend fallback. Used when the binary is built
// without the `embed_dist` tag — typically a fresh `go build` before
// the frontend has been compiled. `make build` enables the real
// embed via `-tags=embed_dist`.
package web

import "net/http"

// Handler returns a small handler that explains the SPA hasn't been
// built. Same signature as the real Handler() in embed.go so the
// rest of the codebase doesn't care which mode it's in.
func Handler() http.Handler {
	return notBuiltHandler()
}

func notBuiltHandler() http.Handler {
	body := []byte(`<!DOCTYPE html>
<html lang="en">
  <head><meta charset="utf-8"><title>frappe-monitor</title></head>
  <body>
    <h1>frontend not built</h1>
    <p>The SPA wasn't bundled into this binary.
       Run <code>make web-build &amp;&amp; make build</code> to enable the dashboard.
       The API is still available at
       <a href="/api/v1/servers">/api/v1/servers</a>.</p>
  </body>
</html>`)
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	})
}
