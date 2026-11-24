// Package network provides a constrained HTTP client for network-capable
// tools. Requests require an explicit host policy and bounded response size.
package network

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Policy struct {
	AllowedHosts []string
	AllowPrivate bool
	Timeout      time.Duration
	MaxBody      int64
}
type Response struct {
	StatusCode int
	URL        string
	Body       []byte
	Headers    http.Header
}

const maxAllowedBody = 64 << 20

// ValidateURL applies the same origin and private-network checks used by
// Fetch without performing a request. It is used by SSE/MCP integrations.
func (p Policy) ValidateURL(raw string) error { return p.allowed(raw) }

func (p Policy) allowed(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return errors.New("only http and https URLs are allowed")
	}
	host := parsed.Hostname()
	if host == "" {
		return errors.New("URL host is required")
	}
	if parsed.User != nil {
		return errors.New("URL user information is not allowed")
	}
	host = canonicalHost(host)
	allowedHost := false
	for _, allowed := range p.AllowedHosts {
		if canonicalHost(allowed) == host {
			allowedHost = true
			break
		}
	}
	if !allowedHost {
		return fmt.Errorf("network host %q is not allowed", host)
	}
	if !p.AllowPrivate {
		private, err := resolvesToPrivateNetwork(host)
		if err != nil {
			return err
		}
		if private {
			return errors.New("private network targets are denied")
		}
	}
	return nil
}

func canonicalHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]"))), ".")
}

func resolvesToPrivateNetwork(host string) (bool, error) {
	host = canonicalHost(host)
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return isPrivate(ip), nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return false, fmt.Errorf("network host %q could not be resolved; refusing fail-open validation: %w", host, err)
	}
	for _, ip := range ips {
		if isPrivate(ip) {
			return true, nil
		}
	}
	return false, nil
}
func (p Policy) Fetch(ctx context.Context, raw string) (Response, error) {
	return p.Request(ctx, http.MethodGet, raw, nil, nil)
}

// Request performs a policy-checked HTTP request. It is the shared boundary
// for all network-capable tools, including POST-based APIs.
func (p Policy) Request(ctx context.Context, method, raw string, body []byte, headers http.Header) (Response, error) {
	if err := p.allowed(raw); err != nil {
		return Response{}, err
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	maxBody := p.MaxBody
	if maxBody <= 0 {
		maxBody = 8 << 20
	} else if maxBody > maxAllowedBody {
		maxBody = maxAllowedBody
	}
	baseTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return Response{}, errors.New("network default transport is unsupported")
	}
	transport := baseTransport.Clone()
	transport.Proxy = nil
	if !p.AllowPrivate {
		transport.DialContext = p.dialPublic
	}
	client := &http.Client{Timeout: timeout, Transport: transport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		// A custom callback replaces net/http's default redirect limit.
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return p.allowed(req.URL.String())
	}}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, raw, reader)
	if err != nil {
		return Response{}, err
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return Response{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
	if err != nil {
		return Response{}, err
	}
	if int64(len(responseBody)) > maxBody {
		return Response{}, errors.New("network response exceeds configured body limit")
	}
	return Response{StatusCode: response.StatusCode, URL: response.Request.URL.String(), Body: responseBody, Headers: response.Header.Clone()}, nil
}

// dialPublic repeats the private-network check at connection time. The
// initial URL validation and the eventual dial can otherwise observe
// different DNS answers, which would leave a DNS-rebinding escape hatch.
func (p Policy) dialPublic(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("network host %q could not be resolved; refusing fail-open connection: %w", host, err)
	}
	dialer := net.Dialer{}
	var lastErr error
	for _, ip := range ips {
		if isPrivate(ip) {
			lastErr = fmt.Errorf("network target %q resolves to a private address", host)
			continue
		}
		conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if dialErr == nil {
			return conn, nil
		}
		lastErr = dialErr
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("network host %q has no usable addresses", host)
}

func isPrivate(ip net.IP) bool {
	if ip == nil {
		return false
	}
	private := []*net.IPNet{}
	for _, cidr := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8"} {
		_, network, _ := net.ParseCIDR(cidr)
		private = append(private, network)
	}
	for _, network := range private {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
