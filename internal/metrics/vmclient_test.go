package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestVMClient_PostsLineProtocol(t *testing.T) {
	var got struct {
		path        string
		method      string
		contentType string
		body        []byte
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		got.method = r.Method
		got.contentType = r.Header.Get("Content-Type")
		got.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := NewVMClient(ts.URL, 5*time.Second)
	err := c.Push(context.Background(),
		"frappe_server_load_1m,server=x value=1 0\n")
	require.NoError(t, err)

	require.Equal(t, "/write", got.path)
	require.Equal(t, http.MethodPost, got.method)
	require.Equal(t, "text/plain", got.contentType)
	require.Contains(t, string(got.body), "frappe_server_load_1m")
}

func TestVMClient_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var hit string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := NewVMClient(ts.URL+"/", 5*time.Second)
	err := c.Push(context.Background(), "x value=1 0\n")
	require.NoError(t, err)
	require.Equal(t, "/write", hit, "trailing slash on baseURL must not cause /write/")
}

func TestVMClient_HTTPErrorBubblesUp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "vm exploded", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := NewVMClient(ts.URL, 5*time.Second)
	err := c.Push(context.Background(), "x value=1 0\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
	require.Contains(t, err.Error(), "vm exploded")
}

func TestVMClient_ContextCancel(t *testing.T) {
	// Server hangs forever. Client should bail when ctx cancels.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	c := NewVMClient(ts.URL, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Push(ctx, "x value=1 0\n")
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "canceled")
}
