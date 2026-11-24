package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestWebDriverLifecycleAndHostPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/session":
			json.NewEncoder(w).Encode(map[string]any{"value": map[string]string{"sessionId": "s1"}})
		case "/session/s1/url":
			json.NewEncoder(w).Encode(map[string]any{"value": nil})
		case "/session/s1/source":
			json.NewEncoder(w).Encode(map[string]string{"value": "<html></html>"})
		case "/session/s1/element":
			json.NewEncoder(w).Encode(map[string]any{"value": map[string]string{"element-6066-11e4-a52e-4f735466cecf": "e1"}})
		case "/session/s1/element/e1/click", "/session/s1/element/e1/value":
			json.NewEncoder(w).Encode(map[string]any{"value": nil})
		case "/session/s1/execute/sync":
			json.NewEncoder(w).Encode(map[string]any{"value": map[string]any{"ok": true}})
		case "/session/s1/screenshot":
			json.NewEncoder(w).Encode(map[string]string{"value": base64.StdEncoding.EncodeToString([]byte("png"))})
		case "/session/s1":
			json.NewEncoder(w).Encode(map[string]any{"value": nil})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := New(server.URL, Policy{AllowedHosts: []string{"example.test"}, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(context.Background(), "chrome"); err != nil {
		t.Fatal(err)
	}
	if err := client.Navigate(context.Background(), "https://denied.test/"); err == nil {
		t.Fatal("expected host policy denial")
	}
	if err := client.Navigate(context.Background(), "https://example.test/"); err != nil {
		t.Fatal(err)
	}
	dom, err := client.DOM(context.Background())
	if err != nil || dom == "" {
		t.Fatalf("unexpected DOM: %q %v", dom, err)
	}
	element, err := client.FindElement(context.Background(), "#submit")
	if err != nil || element.ID != "e1" {
		t.Fatalf("unexpected element: %+v %v", element, err)
	}
	if err := client.Click(context.Background(), element); err != nil {
		t.Fatal(err)
	}
	if err := client.Type(context.Background(), element, "hello"); err != nil {
		t.Fatal(err)
	}
	evaluated, err := client.Evaluate(context.Background(), "return arguments[0]", []any{"value"})
	if err != nil || evaluated == nil {
		t.Fatalf("unexpected evaluation: %v %v", evaluated, err)
	}
	screenshot, err := client.Screenshot(context.Background())
	if err != nil || string(screenshot.PNG) != "png" {
		t.Fatalf("unexpected screenshot: %q %v", screenshot.PNG, err)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBrowserNavigationRequiresExplicitAllowlist(t *testing.T) {
	client, err := New("https://webdriver.example", Policy{})
	if err != nil {
		t.Fatal(err)
	}
	client.SessionID = "session"
	if err := client.Navigate(context.Background(), "https://example.test/"); err == nil {
		t.Fatal("expected empty allowlist to deny navigation")
	}
}

func TestWebDriverHealthReportsReadiness(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"value":{"ready":true,"message":"ready"}}`))
	}))
	defer server.Close()
	client, err := New(server.URL, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	status := client.Health(context.Background())
	if !status.Available || !status.Ready || status.Message != "ready" {
		t.Fatalf("unexpected WebDriver health: %+v", status)
	}
}

func TestBrowserBoundaryRejectsUserInfoAndSessionPathInjection(t *testing.T) {
	client, err := New("https://webdriver.example", Policy{AllowedHosts: []string{"example.test"}, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	client.SessionID = "session/escape"
	if _, err := client.DOM(context.Background()); err == nil || !strings.Contains(err.Error(), "session id is invalid") {
		t.Fatalf("expected invalid session id rejection, got %v", err)
	}
	client.SessionID = "session"
	if err := client.Navigate(context.Background(), "https://user:password@example.test/"); err == nil || !strings.Contains(err.Error(), "user information") {
		t.Fatalf("expected navigation credentials rejection, got %v", err)
	}
}

func TestTypeSendsW3CTextString(t *testing.T) {
	const text = "Hello, 世界 👋"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/session/s1/element/e1/value" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct {
			Text  string   `json:"text"`
			Value []string `json:"value"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "text must be a string", http.StatusBadRequest)
			return
		}
		if body.Text != text || strings.Join(body.Value, "") != text {
			t.Errorf("unexpected typing payload: %+v", body)
		}
		_, _ = w.Write([]byte(`{"value":null}`))
	}))
	defer server.Close()
	client, err := New(server.URL, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	client.SessionID = "s1"
	if err := client.Type(context.Background(), Element{ID: "e1"}, text); err != nil {
		t.Fatalf("W3C WebDriver rejected typing: %v", err)
	}
}

func TestEvaluateSendsEmptyArrayForAbsentArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Args json.RawMessage `json:"args"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || string(body.Args) != "[]" {
			http.Error(w, "args must be an array", http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(`{"value":2}`))
	}))
	defer server.Close()
	client, err := New(server.URL, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	client.SessionID = "s1"
	value, err := client.Evaluate(context.Background(), "return 1 + 1", nil)
	if err != nil || value != float64(2) {
		t.Fatalf("evaluate without arguments: value=%v error=%v", value, err)
	}
}

func TestBrowserRequestsPreserveCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("canceled request reached the backend")
	}))
	defer server.Close()
	client, err := New(server.URL, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	client.SessionID = "s1"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.DOM(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
}

func TestClosePreservesSessionForRetryAfterBackendFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/session/s1" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if attempts.Add(1) == 1 {
			http.Error(w, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"value":null}`))
	}))
	defer server.Close()
	client, err := New(server.URL, Policy{})
	if err != nil {
		t.Fatal(err)
	}
	client.SessionID = "s1"
	if err := client.Close(context.Background()); err == nil {
		t.Fatal("expected backend failure")
	}
	if client.SessionID != "s1" {
		t.Fatalf("failed deletion lost session for retry: %q", client.SessionID)
	}
	if err := client.Close(context.Background()); err != nil {
		t.Fatalf("retry deletion: %v", err)
	}
	if client.SessionID != "" || attempts.Load() != 2 {
		t.Fatalf("successful deletion did not clear session: id=%q attempts=%d", client.SessionID, attempts.Load())
	}
}
