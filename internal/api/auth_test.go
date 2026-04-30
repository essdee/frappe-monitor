package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBasicAuth_NoPasswordIsNoOp(t *testing.T) {
	mw := basicAuth("", "")
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.True(t, called)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestBasicAuth_RejectsMissingCredentials(t *testing.T) {
	mw := basicAuth("hunter2", "")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("inner handler should not run")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Header().Get("WWW-Authenticate"), `realm="frappe-monitor"`)
}

func TestBasicAuth_RejectsWrongPassword(t *testing.T) {
	mw := basicAuth("hunter2", "")
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("inner handler should not run")
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetBasicAuth("admin", "guess")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestBasicAuth_AcceptsCorrectPassword(t *testing.T) {
	mw := basicAuth("hunter2", "myrealm")
	called := false
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest("GET", "/x", nil)
	req.SetBasicAuth("admin", "hunter2")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.True(t, called)
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestRouter_HealthzBypassesAuth(t *testing.T) {
	r := NewRouter(Deps{AuthPassword: "secret"})
	ts := httptest.NewServer(r)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/healthz")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode,
		"healthz must work without auth so external probes succeed")
}

func TestRouter_APIRequiresAuthWhenPasswordSet(t *testing.T) {
	r := NewRouter(Deps{AuthPassword: "secret"})
	ts := httptest.NewServer(r)
	defer ts.Close()

	// Without auth.
	resp, err := http.Get(ts.URL + "/api/v1/servers")
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	// With wrong auth.
	req, _ := http.NewRequest("GET", ts.URL+"/api/v1/servers", nil)
	req.SetBasicAuth("admin", "wrong")
	resp, err = http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
