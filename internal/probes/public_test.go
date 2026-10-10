package probes

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"frappe-monitor/internal/config"
)

func testConfig() config.PublicProbesConfig {
	return config.PublicProbesConfig{
		Enabled: true, AutoDiscover: false, DefaultScheme: "https",
		Path: "/api/method/ping", TimeoutSeconds: 2, MaxParallel: 2,
		AllowPrivateTargets: true, RequirePong: true,
	}
}

func TestPublicProbeHealthyAndBodyValidation(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"pong"}`))
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.Targets = []config.PublicProbeTarget{{Server: "s", Bench: "b", Site: "site.test", URL: server.URL}}
	p := NewPublicProber(cfg)
	transport := p.client.Transport.(*http.Transport)
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // test certificate only

	results := p.ProbeSites(context.Background(), []Site{{Server: "s", Bench: "b", Site: "site.test"}})
	if len(results) != 1 || results[0].IsHealthy != 1 || results[0].FailureReasonCode != ReasonOK {
		t.Fatalf("unexpected result: %#v", results)
	}
	if results[0].HTTPStatusCode != 200 || results[0].TLSCertExpiryTimestamp <= time.Now().Unix() {
		t.Fatalf("missing HTTP/TLS data: %#v", results[0])
	}
}

func TestPublicProbeRejectsUnexpectedBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"message":"not-pong"}`))
	}))
	defer server.Close()
	cfg := testConfig()
	cfg.Targets = []config.PublicProbeTarget{{Server: "s", Bench: "b", Site: "x", URL: server.URL}}
	result := NewPublicProber(cfg).ProbeSites(context.Background(), []Site{{Server: "s", Bench: "b", Site: "x"}})[0]
	if result.IsHealthy != 0 || result.FailureReasonCode != ReasonUnexpectedBody {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestPublicProbeBlocksPrivateTargetsByDefault(t *testing.T) {
	cfg := testConfig()
	cfg.AllowPrivateTargets = false
	cfg.Targets = []config.PublicProbeTarget{{Server: "s", Bench: "b", Site: "x", URL: "http://127.0.0.1:9/ping"}}
	result := NewPublicProber(cfg).ProbeSites(context.Background(), []Site{{Server: "s", Bench: "b", Site: "x"}})[0]
	if result.FailureReasonCode != ReasonBlockedTarget {
		t.Fatalf("reason=%d, want %d", result.FailureReasonCode, ReasonBlockedTarget)
	}
}

func TestPublicProbeSelectionAndOverrides(t *testing.T) {
	cfg := testConfig()
	cfg.AutoDiscover = true
	cfg.Targets = []config.PublicProbeTarget{{Server: "s", Bench: "b", Site: "disabled.test", Disabled: true}}
	p := NewPublicProber(cfg)
	if target, ok := p.targetFor(Site{Server: "s", Bench: "b", Site: "normal.test"}); !ok || target != "https://normal.test/api/method/ping" {
		t.Fatalf("target=%q selected=%v", target, ok)
	}
	if _, ok := p.targetFor(Site{Server: "s", Bench: "b", Site: "disabled.test"}); ok {
		t.Fatal("disabled override was selected")
	}
}
