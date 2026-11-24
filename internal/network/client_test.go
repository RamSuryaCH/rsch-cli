package network

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchRequiresAllowedHost(t *testing.T) {
	server := httptest.NewServer(nil)
	defer server.Close()
	policy := Policy{}
	if _, err := policy.Fetch(context.Background(), server.URL); err == nil {
		t.Fatal("expected host policy denial")
	}
}

func TestRequestBoundsRedirectChains(t *testing.T) {
	for _, redirects := range []int32{2, 12} {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if requests.Add(1) <= redirects {
				http.Redirect(w, r, "/next", http.StatusFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
		policy := Policy{AllowedHosts: []string{"127.0.0.1"}, AllowPrivate: true}
		response, err := policy.Fetch(context.Background(), server.URL)
		server.Close()
		if redirects < 10 {
			if err != nil || response.StatusCode != http.StatusOK {
				t.Fatalf("short redirect chain failed: response=%+v err=%v", response, err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "10 redirects") || requests.Load() != 10 {
			t.Fatalf("redirect limit not enforced: requests=%d err=%v", requests.Load(), err)
		}
	}
}

func TestRequestAllowsConfiguredHostAndPreservesHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	policy := Policy{AllowedHosts: []string{"127.0.0.1"}, AllowPrivate: true}
	response, err := policy.Request(context.Background(), http.MethodPost, server.URL, []byte(`{"x":1}`), http.Header{"X-Test": []string{"yes"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusCreated || string(response.Body) != "ok" || response.Headers.Get("Content-Type") != "text/plain" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestRequestRedirectCannotEscapeAllowlist(t *testing.T) {
	server := httptest.NewServer(http.RedirectHandler("https://denied.example/", http.StatusFound))
	defer server.Close()
	policy := Policy{AllowedHosts: []string{"127.0.0.1"}, AllowPrivate: true}
	if _, err := policy.Fetch(context.Background(), server.URL); err == nil {
		t.Fatal("expected redirect allowlist denial")
	}
}

func TestPolicyRejectsURLCredentialsAndNonRoutableTargets(t *testing.T) {
	policy := Policy{AllowedHosts: []string{"127.0.0.1"}}
	if err := policy.ValidateURL("http://user:password@127.0.0.1/"); err == nil || !strings.Contains(err.Error(), "user information") {
		t.Fatalf("expected URL credentials to be rejected, got %v", err)
	}
	if err := (Policy{AllowedHosts: []string{"0.0.0.0"}}).ValidateURL("http://0.0.0.0/"); err == nil || !strings.Contains(err.Error(), "private network") {
		t.Fatalf("expected non-routable target to be rejected, got %v", err)
	}
}

func TestRequestEnforcesResponseBodyLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer server.Close()
	policy := Policy{AllowedHosts: []string{"127.0.0.1"}, AllowPrivate: true, MaxBody: 1}
	_, err := policy.Fetch(context.Background(), server.URL)
	if err == nil || !strings.Contains(err.Error(), "exceeds configured body limit") {
		t.Fatalf("expected bounded response failure, got %v", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("response limit must not be reported as cancellation")
	}
}
