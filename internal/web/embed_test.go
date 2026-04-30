package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func get(t *testing.T, h http.Handler, path string) (int, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + path)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// The test runs against whatever's currently in web/dist. On a clean
// checkout, dist contains only .gitkeep → notBuiltHandler. After
// `make web-build`, it has index.html + assets.
//
// Either path returns 200 — only the body differs. We assert just the
// status here, not the body, so the test isn't tied to which mode the
// developer happens to be in.
func TestHandler_ReturnsHTML(t *testing.T) {
	h := Handler()
	code, body := get(t, h, "/")
	require.Equal(t, http.StatusOK, code)
	require.True(t, strings.HasPrefix(strings.TrimSpace(body), "<!DOCTYPE html>") ||
		strings.HasPrefix(strings.TrimSpace(body), "<!doctype html>"),
		"expected HTML response, got: %.80s", body)
}

func TestHandler_SPAFallback(t *testing.T) {
	h := Handler()
	// vue-router-style deep link. Even when the frontend isn't built,
	// this should return 200 (the fallback page) — never 404.
	code, _ := get(t, h, "/servers/123")
	require.Equal(t, http.StatusOK, code)
}
