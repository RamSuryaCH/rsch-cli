package provider

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

func TestCopilotRefreshCancellationReachesTokenExchange(t *testing.T) {
	t.Setenv("RSCH_COPILOT_TOKEN", "test-github-token")
	for _, mode := range []string{"send", "stream"} {
		t.Run(mode, func(t *testing.T) {
			started, stopped, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					close(started)
					select {
					case <-r.Context().Done():
						close(stopped)
					case <-release:
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error":{"message":"expired token","type":"authentication_error"}}`))
			}))
			defer server.Close()
			defer close(release)
			client := &copilotClient{
				providerOptions:  providerClientOptions{model: models.Model{APIModel: "test-model"}, maxTokens: 32},
				httpClient:       server.Client(),
				apiBaseURL:       server.URL,
				tokenExchangeURL: server.URL + "/token",
			}
			client.replaceBearerToken("expired-test-token")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				if mode == "send" {
					_, err := client.send(ctx, nil, nil)
					done <- err
					return
				}
				for range client.stream(ctx, nil, nil) {
				}
				done <- nil
			}()
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("request did not start token refresh")
			}
			cancel()
			select {
			case err := <-done:
				if mode == "send" && !errors.Is(err, context.Canceled) {
					t.Fatalf("send returned %v, want context cancellation", err)
				}
			case <-time.After(time.Second):
				t.Fatal("request stayed blocked in token refresh after cancellation")
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("cancellation did not reach the token exchange HTTP request")
			}
		})
	}
}

func TestCopilotRefreshWaitIsCancelable(t *testing.T) {
	t.Setenv("RSCH_COPILOT_TOKEN", "test-github-token")
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client := &copilotClient{httpClient: server.Client(), tokenExchangeURL: server.URL}
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := client.shouldRetry(firstCtx, 1, copilotAPIError(http.StatusUnauthorized))
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first refresh did not start")
	}
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	secondDone := make(chan error, 1)
	go func() {
		_, _, err := client.shouldRetry(secondCtx, 1, copilotAPIError(http.StatusUnauthorized))
		secondDone <- err
	}()
	cancelSecond()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting refresh returned %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled caller stayed blocked behind another refresh")
	}
	if calls.Load() != 1 {
		t.Fatalf("token exchange calls = %d, want one in-flight refresh", calls.Load())
	}
	cancelFirst()
	select {
	case err := <-firstDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("active refresh returned %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first refresh did not stop")
	}
}

func TestCopilotTokenExchangeDoesNotReadOrExposeErrorBody(t *testing.T) {
	const secret = "synthetic-github-secret"
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("invalid credential: " + secret))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client := &copilotClient{httpClient: server.Client(), tokenExchangeURL: server.URL}
	done := make(chan error, 1)
	go func() {
		_, err := client.exchangeGitHubToken(context.Background(), secret)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "HTTP 403") || strings.Contains(err.Error(), secret) {
			t.Fatalf("expected status-only exchange error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("token exchange tried to consume an unbounded error body")
	}
}

func TestCopilotForbiddenErrorDoesNotExposeCredential(t *testing.T) {
	const secret = "synthetic-copilot-secret"
	apiErr := &openai.Error{}
	if err := apiErr.UnmarshalJSON([]byte(`{"message":"invalid credential: ` + secret + `"}`)); err != nil {
		t.Fatal(err)
	}
	apiErr.StatusCode = http.StatusForbidden
	retry, _, err := (&copilotClient{}).shouldRetry(context.Background(), 1, apiErr)
	if retry || err == nil || strings.Contains(err.Error(), secret) || !strings.Contains(err.Error(), "RSCH_COPILOT_TOKEN") {
		t.Fatalf("expected safe authentication guidance, retry=%t error=%v", retry, err)
	}
}
