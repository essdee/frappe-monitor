package logs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEncodeStreams_Shape(t *testing.T) {
	streams := []Stream{
		{
			Labels: map[string]string{"server": "p1", "log_type": "error"},
			Entries: []Entry{
				{Time: time.Unix(1714363200, 0).UTC(), Line: "first"},
				{Time: time.Unix(1714363201, 500_000_000).UTC(), Line: "second"},
			},
		},
	}
	body, err := encodeStreams(streams)
	require.NoError(t, err)

	// Decode as a generic map so the test isn't coupled to wire types.
	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got))

	streamsAny, ok := got["streams"].([]any)
	require.True(t, ok)
	require.Len(t, streamsAny, 1)

	s := streamsAny[0].(map[string]any)
	labels := s["stream"].(map[string]any)
	require.Equal(t, "p1", labels["server"])
	require.Equal(t, "error", labels["log_type"])

	values := s["values"].([]any)
	require.Len(t, values, 2)
	first := values[0].([]any)
	require.Equal(t, "1714363200000000000", first[0])
	require.Equal(t, "first", first[1])
	second := values[1].([]any)
	require.Equal(t, "1714363201500000000", second[0])
	require.Equal(t, "second", second[1])
}

func TestLokiClient_PostsToPushPath(t *testing.T) {
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

	c := NewLokiClient(ts.URL, 5*time.Second)
	err := c.Push(context.Background(), []Stream{
		{
			Labels:  map[string]string{"server": "x"},
			Entries: []Entry{{Time: time.Unix(0, 0), Line: "hello"}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "/loki/api/v1/push", got.path)
	require.Equal(t, http.MethodPost, got.method)
	require.Equal(t, "application/json", got.contentType)
	require.Contains(t, string(got.body), `"server":"x"`)
	require.Contains(t, string(got.body), `"hello"`)
}

func TestLokiClient_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	var hit string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := NewLokiClient(ts.URL+"/", 5*time.Second)
	err := c.Push(context.Background(), []Stream{
		{Labels: map[string]string{"a": "b"}, Entries: []Entry{{Time: time.Unix(0, 0), Line: "x"}}},
	})
	require.NoError(t, err)
	require.Equal(t, "/loki/api/v1/push", hit)
}

func TestLokiClient_HTTPErrorBubblesUp(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "loki exploded", http.StatusInternalServerError)
	}))
	defer ts.Close()

	c := NewLokiClient(ts.URL, 5*time.Second)
	err := c.Push(context.Background(), []Stream{
		{Labels: map[string]string{"a": "b"}, Entries: []Entry{{Time: time.Unix(0, 0), Line: "x"}}},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "500")
	require.Contains(t, err.Error(), "loki exploded")
}

func TestLokiClient_EmptyInputIsNoOp(t *testing.T) {
	called := false
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()

	c := NewLokiClient(ts.URL, 5*time.Second)
	err := c.Push(context.Background(), nil)
	require.NoError(t, err)
	require.False(t, called, "no HTTP call expected for empty input")

	err = c.Push(context.Background(), []Stream{})
	require.NoError(t, err)
	require.False(t, called)
}

func TestLokiClient_ContextCancel(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer ts.Close()

	c := NewLokiClient(ts.URL, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := c.Push(ctx, []Stream{
		{Labels: map[string]string{"a": "b"}, Entries: []Entry{{Time: time.Unix(0, 0), Line: "x"}}},
	})
	require.Error(t, err)
	require.Contains(t, strings.ToLower(err.Error()), "canceled")
}
