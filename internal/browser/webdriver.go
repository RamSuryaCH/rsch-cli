// Package browser provides a provider-neutral WebDriver client. Browser
// binaries remain an external capability; absence is reported explicitly.
package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var ErrBackendUnavailable = errors.New("browser WebDriver backend is unavailable")

type Policy struct {
	AllowedHosts []string
	AllowPrivate bool
}
type Client struct {
	BaseURL   string
	SessionID string
	HTTP      *http.Client
	Policy    Policy
}
type Screenshot struct {
	PNG    []byte
	Width  int
	Height int
}

// Health describes the externally hosted browser capability without creating
// a session. Ready is intentionally separate from Available: a reachable
// WebDriver service can still be starting up or unable to launch a browser.
type Health struct {
	Available bool      `json:"available"`
	Ready     bool      `json:"ready"`
	Message   string    `json:"message,omitempty"`
	CheckedAt time.Time `json:"checkedAt"`
}

type Element struct {
	ID string `json:"id"`
}

func New(baseURL string, policy Policy) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("WebDriver URL is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("invalid WebDriver URL")
	}
	policy.AllowedHosts = append([]string(nil), policy.AllowedHosts...)
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 30 * time.Second}, Policy: policy}, nil
}

// Health checks the WebDriver status endpoint. It does not create a browser
// session, so callers can distinguish a missing/unready backend from a
// browser launch failure.
func (c *Client) Health(ctx context.Context) Health {
	status := Health{CheckedAt: time.Now().UTC()}
	if c == nil {
		status.Message = ErrBackendUnavailable.Error()
		return status
	}
	var response struct {
		Value struct {
			Ready   bool   `json:"ready"`
			Message string `json:"message"`
		} `json:"value"`
	}
	if err := c.request(ctx, http.MethodGet, "/status", nil, &response); err != nil {
		status.Message = err.Error()
		return status
	}
	status.Available = true
	status.Ready = response.Value.Ready
	status.Message = response.Value.Message
	if status.Message == "" && !status.Ready {
		status.Message = "WebDriver is reachable but not ready"
	}
	return status
}

func (c *Client) Start(ctx context.Context, browserName string) error {
	if c == nil {
		return ErrBackendUnavailable
	}
	browserName = strings.TrimSpace(browserName)
	if browserName == "" {
		browserName = "chrome"
	}
	var response struct {
		Value struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
		SessionID string `json:"sessionId"`
	}
	if err := c.request(ctx, http.MethodPost, "/session", map[string]any{"capabilities": map[string]any{"alwaysMatch": map[string]any{"browserName": browserName}}}, &response); err != nil {
		return fmt.Errorf("start browser session: %w", err)
	}
	c.SessionID = response.Value.SessionID
	if c.SessionID == "" {
		c.SessionID = response.SessionID
	}
	if c.SessionID == "" {
		return errors.New("WebDriver did not return a session id")
	}
	return nil
}
func (c *Client) Navigate(ctx context.Context, rawURL string) error {
	if err := c.requireSession(); err != nil {
		return err
	}
	if err := c.allowed(rawURL); err != nil {
		return err
	}
	path, err := c.sessionPath("/url")
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, path, map[string]any{"url": rawURL}, nil)
}
func (c *Client) DOM(ctx context.Context) (string, error) {
	if err := c.requireSession(); err != nil {
		return "", err
	}
	var response struct {
		Value string `json:"value"`
	}
	path, err := c.sessionPath("/source")
	if err != nil {
		return "", err
	}
	if err := c.request(ctx, http.MethodGet, path, nil, &response); err != nil {
		return "", err
	}
	return response.Value, nil
}

// FindElement resolves a CSS selector through the WebDriver element API.
// Selectors stay inside the browser session; callers never need to construct
// protocol URLs or bypass the client lifecycle and policy checks.
func (c *Client) FindElement(ctx context.Context, selector string) (Element, error) {
	if err := c.requireSession(); err != nil {
		return Element{}, err
	}
	if strings.TrimSpace(selector) == "" {
		return Element{}, errors.New("browser selector is required")
	}
	var response struct {
		Value map[string]string `json:"value"`
	}
	path, err := c.sessionPath("/element")
	if err != nil {
		return Element{}, err
	}
	if err := c.request(ctx, http.MethodPost, path, map[string]any{"using": "css selector", "value": selector}, &response); err != nil {
		return Element{}, err
	}
	id := response.Value["element-6066-11e4-a52e-4f735466cecf"]
	if id == "" {
		id = response.Value["ELEMENT"]
	}
	if id == "" {
		return Element{}, errors.New("WebDriver did not return an element id")
	}
	return Element{ID: id}, nil
}

func (c *Client) Click(ctx context.Context, element Element) error {
	if err := c.requireSession(); err != nil {
		return err
	}
	if element.ID == "" {
		return errors.New("browser element id is required")
	}
	path, err := c.sessionPath("/element/" + url.PathEscape(element.ID) + "/click")
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, path, map[string]any{}, nil)
}

func (c *Client) Type(ctx context.Context, element Element, text string) error {
	if err := c.requireSession(); err != nil {
		return err
	}
	if element.ID == "" {
		return errors.New("browser element id is required")
	}
	path, err := c.sessionPath("/element/" + url.PathEscape(element.ID) + "/value")
	if err != nil {
		return err
	}
	return c.request(ctx, http.MethodPost, path, map[string]any{"text": text, "value": []string{text}}, nil)
}

func (c *Client) Evaluate(ctx context.Context, script string, args []any) (any, error) {
	if err := c.requireSession(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(script) == "" {
		return nil, errors.New("browser script is required")
	}
	if args == nil {
		args = []any{}
	}
	var response struct {
		Value any `json:"value"`
	}
	path, err := c.sessionPath("/execute/sync")
	if err != nil {
		return nil, err
	}
	if err := c.request(ctx, http.MethodPost, path, map[string]any{"script": script, "args": args}, &response); err != nil {
		return nil, err
	}
	return response.Value, nil
}
func (c *Client) Screenshot(ctx context.Context) (Screenshot, error) {
	if err := c.requireSession(); err != nil {
		return Screenshot{}, err
	}
	var response struct {
		Value string `json:"value"`
	}
	path, err := c.sessionPath("/screenshot")
	if err != nil {
		return Screenshot{}, err
	}
	if err := c.request(ctx, http.MethodGet, path, nil, &response); err != nil {
		return Screenshot{}, err
	}
	png, err := base64.StdEncoding.DecodeString(response.Value)
	if err != nil {
		return Screenshot{}, fmt.Errorf("decode browser screenshot: %w", err)
	}
	return Screenshot{PNG: png}, nil
}
func (c *Client) Console(ctx context.Context) ([]any, error) {
	if err := c.requireSession(); err != nil {
		return nil, err
	}
	var response struct {
		Value []any `json:"value"`
	}
	path, err := c.sessionPath("/log")
	if err != nil {
		return nil, err
	}
	if err := c.request(ctx, http.MethodPost, path, map[string]any{"type": "browser"}, &response); err != nil {
		return nil, err
	}
	return response.Value, nil
}
func (c *Client) Close(ctx context.Context) error {
	if c == nil || c.SessionID == "" {
		return nil
	}
	path, pathErr := c.sessionPath("")
	if pathErr != nil {
		c.SessionID = ""
		return pathErr
	}
	if err := c.request(ctx, http.MethodDelete, path, nil, nil); err != nil {
		return err
	}
	c.SessionID = ""
	return nil
}

func (c *Client) requireSession() error {
	if c == nil || c.SessionID == "" {
		return errors.New("browser session is not started")
	}
	if strings.ContainsAny(c.SessionID, "/\\") {
		return errors.New("browser session id is invalid")
	}
	return nil
}

func (c *Client) sessionPath(suffix string) (string, error) {
	if err := c.requireSession(); err != nil {
		return "", err
	}
	return "/session/" + url.PathEscape(c.SessionID) + suffix, nil
}
func (c *Client) allowed(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("browser navigation requires http or https")
	}
	if parsed.Hostname() == "" {
		return errors.New("browser navigation requires a host")
	}
	if parsed.User != nil {
		return errors.New("browser navigation does not allow URL user information")
	}
	hostname := canonicalHost(parsed.Hostname())
	allowed := false
	for _, host := range c.Policy.AllowedHosts {
		if canonicalHost(host) == hostname {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("browser host %q is not allowed", hostname)
	}
	if !c.Policy.AllowPrivate {
		private, err := browserHostPrivate(hostname)
		if err != nil {
			return err
		}
		if private {
			return errors.New("private browser target is denied")
		}
	}
	return nil
}

func canonicalHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(strings.Trim(host, "[]"))), ".")
}

func browserHostPrivate(host string) (bool, error) {
	host = strings.ToLower(host)
	host = canonicalHost(host)
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return true, nil
	}
	if ip := net.ParseIP(host); ip != nil {
		return privateIP(ip), nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return false, fmt.Errorf("browser host %q could not be resolved; refusing fail-open validation: %w", host, err)
	}
	for _, ip := range ips {
		if privateIP(ip) {
			return true, nil
		}
	}
	return false, nil
}

func privateIP(ip net.IP) bool {
	for _, cidr := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/4", "240.0.0.0/4", "::/128", "::1/128", "2001:db8::/32", "fc00::/7", "fe80::/10", "ff00::/8"} {
		_, network, _ := net.ParseCIDR(cidr)
		if network.Contains(ip) {
			return true
		}
	}
	return false
}
func (c *Client) request(ctx context.Context, method, path string, body any, output any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrBackendUnavailable
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<20)+1))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("WebDriver returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
	}
	if len(data) > 16<<20 {
		return errors.New("WebDriver response exceeds configured body limit")
	}
	if output != nil && len(data) > 0 {
		if err := json.Unmarshal(data, output); err != nil {
			return err
		}
	}
	return nil
}
