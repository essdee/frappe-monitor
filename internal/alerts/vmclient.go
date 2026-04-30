package alerts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// VMQuerier abstracts the upstream VM client so the evaluator can be
// tested with a static-data fake.
type VMQuerier interface {
	Query(ctx context.Context, expr string) ([]Sample, error)
}

// Sample is one (labels, value) returned by VM's instant query.
type Sample struct {
	Labels map[string]string
	Value  float64
}

// HTTPVMClient hits VM's /api/v1/query. Same shape as
// internal/api/hierarchy_handlers.go's queryInstant; lifted into its
// own package so the alerts evaluator doesn't depend on the api one.
type HTTPVMClient struct {
	BaseURL string
	HTTP    *http.Client
}

// NewHTTPVMClient builds an HTTPVMClient with a sensible timeout.
// baseURL is the VM root (e.g. http://127.0.0.1:8428); /api/v1/query
// is appended at request time.
func NewHTTPVMClient(baseURL string, timeout time.Duration) *HTTPVMClient {
	return &HTTPVMClient{
		BaseURL: strings.TrimRight(baseURL, "/"),
		HTTP:    &http.Client{Timeout: timeout},
	}
}

// vmInstantResponse mirrors VM's response. Inline here for the same
// reason the hierarchy_handlers.go file has its own.
type vmInstantResponse struct {
	Status string `json:"status"`
	Data   struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]json.RawMessage `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error,omitempty"`
}

func (c *HTTPVMClient) Query(ctx context.Context, expr string) ([]Sample, error) {
	v := url.Values{}
	v.Set("query", expr)
	full := c.BaseURL + "/api/v1/query?" + v.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, full, nil)
	if err != nil {
		return nil, fmt.Errorf("build vm request: %w", err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vm unreachable: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("vm HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out vmInstantResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("vm response decode: %w", err)
	}
	if out.Status != "success" {
		return nil, fmt.Errorf("vm error: %s", out.Error)
	}
	samples := make([]Sample, 0, len(out.Data.Result))
	for _, r := range out.Data.Result {
		var s string
		if err := json.Unmarshal(r.Value[1], &s); err != nil {
			continue
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			continue
		}
		labels := r.Metric
		if labels == nil {
			labels = map[string]string{}
		}
		samples = append(samples, Sample{Labels: labels, Value: f})
	}
	return samples, nil
}
