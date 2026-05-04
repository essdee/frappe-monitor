package metrics

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// VMClient pushes influx-line-protocol metrics to a VictoriaMetrics
// endpoint via the /write path. Phase 2 only writes; queries proxy
// through the API layer in Phase 4.
type VMClient struct {
	baseURL string
	http    *http.Client
}

// NewVMClient wraps a *http.Client with the configured push timeout and
// caches the base URL. baseURL should be the VM root (e.g.
// "http://127.0.0.1:8428") — /write is appended.
func NewVMClient(baseURL string, pushTimeout time.Duration) *VMClient {
	return &VMClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: pushTimeout},
	}
}

// Push posts the supplied line-protocol body to <baseURL>/write.
// Returns an error on non-2xx responses; the body is read and included
// in the error message for debugging.
func (c *VMClient) Push(ctx context.Context, body string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/write", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("vmclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("vmclient: post: %w", err)
	}
	defer func() {
		// Drain so HTTP/1.1 keep-alive can reuse the conn. VM's /write
		// returns 204 with empty body on success so this is normally a
		// no-op — but if VM ever switches to a non-empty body, we want
		// the connection back in the pool.
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}()

	if resp.StatusCode/100 != 2 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("vmclient: HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return nil
}
