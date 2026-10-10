// Package probes performs monitor-side, read-only availability checks.
package probes

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"frappe-monitor/internal/config"
)

const (
	ReasonOK int64 = iota
	ReasonDNS
	ReasonBlockedTarget
	ReasonConnection
	ReasonTLS
	ReasonHTTP4xx
	ReasonHTTP5xx
	ReasonUnexpectedStatus
	ReasonUnexpectedBody
	ReasonInvalidTarget
)

type Site struct {
	Server string
	Bench  string
	Site   string
}

type Result struct {
	Server                 string
	Bench                  string
	Site                   string
	URL                    string
	Timestamp              time.Time
	HTTPStatusCode         int64
	HTTPResponseMs         float64
	IsHealthy              int64
	FailureReasonCode      int64
	TLSCertExpiryTimestamp int64
}

type resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

// Prober owns an HTTP transport that resolves and dials destinations itself.
// That guarantees the address checked by the SSRF policy is the address used
// for the connection, including after redirects.
type Prober struct {
	cfg      config.PublicProbesConfig
	resolver resolver
	client   *http.Client
}

var errBlockedTarget = errors.New("target resolves to a non-public address")

func NewPublicProber(cfg config.PublicProbesConfig) *Prober {
	p := &Prober{cfg: cfg, resolver: net.DefaultResolver}
	dialer := &net.Dialer{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          max(16, cfg.MaxParallel*2),
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       60 * time.Second,
		TLSHandshakeTimeout:   time.Duration(cfg.TimeoutSeconds) * time.Second,
		ResponseHeaderTimeout: time.Duration(cfg.TimeoutSeconds) * time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("split target: %w", err)
		}
		ips, err := p.resolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
		}
		for _, ip := range ips {
			if !p.addressAllowed(ip) {
				return nil, fmt.Errorf("%w: %s", errBlockedTarget, ip)
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
	p.client = &http.Client{
		Transport: transport,
		Timeout:   time.Duration(cfg.TimeoutSeconds) * time.Second,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		},
	}
	return p
}

func (p *Prober) addressAllowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if p.cfg.AllowPrivateTargets {
		return ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
	}
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

func targetKey(site Site) string {
	return site.Server + "\x00" + site.Bench + "\x00" + site.Site
}

func (p *Prober) targetFor(site Site) (string, bool) {
	for _, target := range p.cfg.Targets {
		if targetKey(site) != targetKey(Site{Server: target.Server, Bench: target.Bench, Site: target.Site}) {
			continue
		}
		if target.Disabled {
			return "", false
		}
		return strings.TrimSpace(target.URL), true
	}
	if !p.cfg.AutoDiscover {
		return "", false
	}
	u := url.URL{Scheme: p.cfg.DefaultScheme, Host: site.Site, Path: p.cfg.Path}
	return u.String(), true
}

// ProbeSites checks only sites selected by auto-discovery or an exact target
// override. Results preserve the discovered input order.
func (p *Prober) ProbeSites(ctx context.Context, sites []Site) []Result {
	if p == nil || !p.cfg.Enabled || len(sites) == 0 {
		return nil
	}
	type job struct {
		index int
		site  Site
		url   string
	}
	jobs := make([]job, 0, len(sites))
	for _, site := range sites {
		target, selected := p.targetFor(site)
		if selected {
			jobs = append(jobs, job{index: len(jobs), site: site, url: target})
		}
	}
	results := make([]Result, len(jobs))
	queue := make(chan job)
	workers := min(max(1, p.cfg.MaxParallel), len(jobs))
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for work := range queue {
				results[work.index] = p.probe(ctx, work.site, work.url)
			}
		}()
	}
	for _, work := range jobs {
		queue <- work
	}
	close(queue)
	wg.Wait()
	return results
}

func (p *Prober) probe(ctx context.Context, site Site, rawURL string) Result {
	result := Result{Server: site.Server, Bench: site.Bench, Site: site.Site, URL: rawURL, Timestamp: time.Now().UTC(), IsHealthy: 0}
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		result.FailureReasonCode = ReasonInvalidTarget
		return result
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		result.FailureReasonCode = ReasonInvalidTarget
		return result
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "frappe-monitor/public-probe")
	started := time.Now()
	resp, err := p.client.Do(request)
	result.HTTPResponseMs = float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		result.FailureReasonCode = classifyError(err)
		return result
	}
	defer resp.Body.Close()
	result.HTTPStatusCode = int64(resp.StatusCode)
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		result.TLSCertExpiryTimestamp = resp.TLS.PeerCertificates[0].NotAfter.Unix()
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if readErr != nil {
		result.FailureReasonCode = ReasonConnection
		return result
	}
	switch {
	case resp.StatusCode >= 400 && resp.StatusCode < 500:
		result.FailureReasonCode = ReasonHTTP4xx
		return result
	case resp.StatusCode >= 500:
		result.FailureReasonCode = ReasonHTTP5xx
		return result
	case resp.StatusCode < 200 || resp.StatusCode >= 300:
		result.FailureReasonCode = ReasonUnexpectedStatus
		return result
	}
	if p.cfg.RequirePong {
		var payload struct {
			Message string `json:"message"`
		}
		if len(body) > 4096 || json.Unmarshal(body, &payload) != nil || payload.Message != "pong" {
			result.FailureReasonCode = ReasonUnexpectedBody
			return result
		}
	}
	result.IsHealthy = 1
	result.FailureReasonCode = ReasonOK
	return result
}

func classifyError(err error) int64 {
	if errors.Is(err, errBlockedTarget) {
		return ReasonBlockedTarget
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ReasonDNS
	}
	var certErr x509.CertificateInvalidError
	var unknownAuthority x509.UnknownAuthorityError
	var hostErr x509.HostnameError
	if errors.As(err, &certErr) || errors.As(err, &unknownAuthority) || errors.As(err, &hostErr) ||
		strings.Contains(strings.ToLower(err.Error()), "tls") || strings.Contains(strings.ToLower(err.Error()), "certificate") {
		return ReasonTLS
	}
	return ReasonConnection
}

func (r Result) String() string {
	return r.Server + "/" + r.Bench + "/" + r.Site + "=" + strconv.FormatInt(r.IsHealthy, 10)
}
