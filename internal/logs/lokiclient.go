// Package logs holds the typed value model for log streams plus the
// Loki push client. Phase 3 wires log tailing on the bench server through
// here; future log-querying (Phase 4 dashboard) goes through the API
// proxy and not directly through this client.
package logs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Entry is one log line with its timestamp.
type Entry struct {
	Time time.Time
	Line string
}

// Stream is a set of log entries sharing a label set. Loki's pushdown
// model: one stream per (label-set), many entries.
//
// Label values follow master plan §4 — server, bench, log_type, etc.
// Keep cardinality low; user IDs / request IDs go in the line, not the
// labels.
type Stream struct {
	Labels  map[string]string
	Entries []Entry
}

// LokiClient pushes structured log streams to Loki. Phase 3 only writes;
// reads will be proxied through the API layer in Phase 4.
type LokiClient struct {
	baseURL string
	http    *http.Client
}

// NewLokiClient wraps an *http.Client with the configured push timeout
// and caches the base URL. baseURL should be the Loki root (e.g.
// "http://127.0.0.1:3100") — `/loki/api/v1/push` is appended.
func NewLokiClient(baseURL string, pushTimeout time.Duration) *LokiClient {
	return &LokiClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: pushTimeout},
	}
}

// Push posts the supplied streams to <baseURL>/loki/api/v1/push.
// Returns an error on non-2xx responses; the body is read (capped at
// 4 KiB) and included in the error message for debugging.
//
// Empty input is a no-op (no HTTP call) — callers that fan in tail
// results can pass nil/empty streams without a special-case branch.
func (c *LokiClient) Push(ctx context.Context, streams []Stream) error {
	if len(streams) == 0 {
		return nil
	}

	body, err := encodeStreams(streams)
	if err != nil {
		return fmt.Errorf("lokiclient: encode: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/loki/api/v1/push", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("lokiclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("lokiclient: post: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("lokiclient: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}

// encodeStreams marshals the supplied streams in Loki's push body shape:
//
//	{"streams":[{"stream":{...labels},"values":[["<ts_ns>","<line>"],...]},...]}
func encodeStreams(streams []Stream) ([]byte, error) {
	type valuePair = [2]string

	type wireStream struct {
		Stream map[string]string `json:"stream"`
		Values []valuePair       `json:"values"`
	}

	type wireBody struct {
		Streams []wireStream `json:"streams"`
	}

	wire := wireBody{Streams: make([]wireStream, 0, len(streams))}
	for _, s := range streams {
		ws := wireStream{
			Stream: s.Labels,
			Values: make([]valuePair, 0, len(s.Entries)),
		}
		for _, e := range s.Entries {
			ws.Values = append(ws.Values, valuePair{
				strconv.FormatInt(e.Time.UnixNano(), 10),
				e.Line,
			})
		}
		wire.Streams = append(wire.Streams, ws)
	}
	return json.Marshal(wire)
}
