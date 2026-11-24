package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go"
)

func copilotAPIError(status int) *openai.Error {
	request, _ := http.NewRequest(http.MethodPost, "https://api.githubcopilot.com/chat/completions", nil)
	return &openai.Error{
		StatusCode: status,
		Request:    request,
		Response: &http.Response{
			StatusCode: status,
			Header:     make(http.Header),
		},
	}
}

func TestCopilotRefreshRebuildsClientWithNewBearerToken(t *testing.T) {
	var tokenCalls atomic.Int32
	var apiAuthorization atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			tokenCalls.Add(1)
			if got := r.Header.Get("Authorization"); got != "Token github-token" {
				t.Errorf("token exchange authorization = %q", got)
			}
			_ = json.NewEncoder(w).Encode(CopilotTokenResponse{Token: "fresh-bearer", ExpiresAt: 1})
		case "/chat/completions":
			apiAuthorization.Store(r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":1,"model":"gpt-4o","choices":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := &copilotClient{
		providerOptions:  providerClientOptions{apiKey: "github-token"},
		options:          copilotOptions{bearerToken: "expired-bearer"},
		httpClient:       server.Client(),
		apiBaseURL:       server.URL,
		tokenExchangeURL: server.URL + "/token",
	}
	client.replaceBearerToken("expired-bearer")

	retry, after, err := client.shouldRetry(context.Background(), 1, copilotAPIError(http.StatusUnauthorized))
	if err != nil || !retry || after != 0 {
		t.Fatalf("refresh result retry=%t after=%d err=%v", retry, after, err)
	}
	params := openai.ChatCompletionNewParams{
		Model:    "gpt-4o",
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")},
	}
	apiClient := client.currentClient()
	if _, err := apiClient.Chat.Completions.New(context.Background(), params); err != nil {
		t.Fatalf("request with refreshed client failed: %v", err)
	}
	if tokenCalls.Load() != 1 {
		t.Fatalf("token exchange calls = %d, want 1", tokenCalls.Load())
	}
	if got, _ := apiAuthorization.Load().(string); got != "Bearer fresh-bearer" {
		t.Fatalf("API authorization = %q, want refreshed bearer token", got)
	}
}

func TestCopilotAuthenticationRetryIsBounded(t *testing.T) {
	var tokenCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenCalls.Add(1)
		_ = json.NewEncoder(w).Encode(CopilotTokenResponse{Token: "fresh-bearer"})
	}))
	defer server.Close()

	client := &copilotClient{
		providerOptions:  providerClientOptions{apiKey: "github-token"},
		options:          copilotOptions{bearerToken: "expired-bearer"},
		httpClient:       server.Client(),
		tokenExchangeURL: server.URL,
	}
	retry, _, err := client.shouldRetry(context.Background(), maxRetries+1, copilotAPIError(http.StatusUnauthorized))
	if retry || err == nil || !strings.Contains(err.Error(), "maximum retry attempts") {
		t.Fatalf("bounded retry result retry=%t err=%v", retry, err)
	}
	if tokenCalls.Load() != 0 {
		t.Fatalf("token exchange called after retry bound: %d", tokenCalls.Load())
	}
}
