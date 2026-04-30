//go:build embed_dist

// Package web embeds the Vite build output and exposes an HTTP handler
// that serves the SPA, including the client-side-router fallback.
//
// This file is built only with the `embed_dist` tag (set by `make
// build` after `make web-build` has produced internal/web/dist/).
// Without the tag, the placeholder handler in embed_placeholder.go
// is used instead — so a fresh `go build` works without running the
// frontend build first.
package web

import (
	"embed"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// Handler returns the SPA HTTP handler. It serves files from the embedded
// dist/ directory; for any path that doesn't match a real file (e.g.
// /servers/123 — a client-side route), it falls back to index.html so
// vue-router takes over.
func Handler() http.Handler {
	root, err := fs.Sub(distFS, "dist")
	if err != nil {
		return notBuiltHandler()
	}
	if !indexExists(root) {
		return notBuiltHandler()
	}
	indexBytes, _ := fs.ReadFile(root, "index.html")
	fileServer := http.FileServer(http.FS(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip leading slash for fs.FS lookups.
		clean := path.Clean(r.URL.Path)
		if clean == "/" {
			clean = "index.html"
		} else {
			clean = strings.TrimPrefix(clean, "/")
		}

		// If the requested path is a real file in the bundle, serve it.
		if f, err := root.Open(clean); err == nil {
			_ = f.Close()
			fileServer.ServeHTTP(w, r)
			return
		}

		// Otherwise it's a client-side route — return index.html.
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(indexBytes)
	})
}

// indexExists reports whether dist/index.html is present in the embed.
func indexExists(root fs.FS) bool {
	_, err := fs.Stat(root, "index.html")
	return err == nil || !errors.Is(err, fs.ErrNotExist)
}

func notBuiltHandler() http.Handler {
	body := []byte(`<!DOCTYPE html>
<html lang="en">
  <head><meta charset="utf-8"><title>frappe-monitor</title></head>
  <body>
    <h1>frontend not built</h1>
    <p>Run <code>make web-build</code> to populate internal/web/dist.
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
