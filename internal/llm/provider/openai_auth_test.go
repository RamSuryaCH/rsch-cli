package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/opencode-ai/opencode/internal/llm/models"
)

func TestOpenAICompatibleProviderKeepsProviderEndpoint(t *testing.T) {
	t.Setenv("LOCAL_ENDPOINT", "http://127.0.0.1:11434/v1")

	tests := []struct {
		provider models.ModelProvider
		baseURL  string
	}{
		{provider: models.ProviderOpenAI, baseURL: ""},
		{provider: models.ProviderGROQ, baseURL: "https://api.groq.com/openai/v1"},
		{provider: models.ProviderOpenRouter, baseURL: "https://openrouter.ai/api/v1"},
		{provider: models.ProviderXAI, baseURL: "https://api.x.ai/v1"},
		{provider: models.ProviderLocal, baseURL: "http://127.0.0.1:11434/v1"},
	}

	for _, test := range tests {
		t.Run(string(test.provider), func(t *testing.T) {
			created, err := NewProvider(test.provider, WithAPIKey("test-key"))
			if err != nil {
				t.Fatal(err)
			}
			base, ok := created.(*baseProvider[OpenAIClient])
			if !ok {
				t.Fatalf("provider type = %T, want OpenAI-compatible provider", created)
			}
			client, ok := base.client.(*openaiClient)
			if !ok {
				t.Fatalf("client type = %T, want *openaiClient", base.client)
			}
			if client.providerOptions.provider != test.provider {
				t.Fatalf("provider identity = %q, want %q", client.providerOptions.provider, test.provider)
			}
			if client.options.baseURL != test.baseURL {
				t.Fatalf("base URL = %q, want %q", client.options.baseURL, test.baseURL)
			}
			if test.provider == models.ProviderOpenRouter {
				if client.options.extraHeaders["HTTP-Referer"] != "https://github.com/RamSuryaCH/rsch" || client.options.extraHeaders["X-Title"] != "RSCH" {
					t.Fatalf("OpenRouter attribution headers = %v", client.options.extraHeaders)
				}
			}
		})
	}
}

func TestOpenRouterAuthenticationErrorIsActionableAndDoesNotEchoKey(t *testing.T) {
	key := "sk-or-v1-sensitive-test-key"
	requestURL, err := url.Parse("https://openrouter.ai/api/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	apiErr := &openai.Error{}
	if err := json.Unmarshal([]byte(`{"message":"Incorrect API key provided: `+key+`"}`), apiErr); err != nil {
		t.Fatal(err)
	}
	apiErr.StatusCode = http.StatusUnauthorized
	apiErr.Request = &http.Request{Method: http.MethodPost, URL: requestURL}
	apiErr.Response = &http.Response{StatusCode: http.StatusUnauthorized, Header: make(http.Header)}
	client := &openaiClient{providerOptions: providerClientOptions{provider: models.ProviderOpenRouter}}

	retry, _, got := client.shouldRetry(1, apiErr)
	if retry || got == nil {
		t.Fatalf("retry = %t, error = %v; want a terminal authentication error", retry, got)
	}
	if strings.Contains(got.Error(), key) {
		t.Fatalf("authentication error exposed credential material: %s", got)
	}
	for _, want := range []string{"openrouter authentication failed", "OPENROUTER_API_KEY", "providers.openrouter.apiKey"} {
		if !strings.Contains(got.Error(), want) {
			t.Fatalf("authentication error %q does not contain %q", got, want)
		}
	}
}

func TestOpenAICompatibleProvidersDoNotInheritOpenAICredentials(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "openai-only-test-key")
	t.Setenv("OPENAI_ORG_ID", "openai-only-org")
	t.Setenv("OPENAI_PROJECT_ID", "openai-only-project")
	requests := make(chan http.Header, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	t.Setenv("LOCAL_ENDPOINT", server.URL)
	t.Setenv("AZURE_OPENAI_ENDPOINT", server.URL)
	t.Setenv("AZURE_OPENAI_API_VERSION", "2025-04-01-preview")
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-only-test-key")

	for _, test := range []struct {
		provider models.ModelProvider
		key      string
	}{
		{provider: models.ProviderOpenAI},
		{provider: models.ProviderGROQ},
		{provider: models.ProviderOpenRouter},
		{provider: models.ProviderXAI},
		{provider: models.ProviderLocal},
		{provider: models.ProviderAzure},
		{provider: models.ProviderCopilot, key: "copilot-only-test-key"},
		{provider: models.ProviderLocal, key: "local-only-test-key"},
	} {
		t.Run(string(test.provider)+"/"+test.key, func(t *testing.T) {
			var client openai.Client
			switch test.provider {
			case models.ProviderAzure:
				azureClient := newAzureClient(providerClientOptions{provider: test.provider}).(*azureClient)
				if azureClient.initErr != nil {
					t.Fatal(azureClient.initErr)
				}
				client = azureClient.client
			case models.ProviderCopilot:
				client = newCopilotAPIClient(server.URL, test.key, nil)
			default:
				client = newOpenAIClient(providerClientOptions{provider: test.provider, apiKey: test.key}).(*openaiClient).client
			}
			_, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
				Model:    "test-model",
				Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hello")},
			}, option.WithBaseURL(server.URL), option.WithMaxRetries(0))
			if err != nil {
				t.Fatal(err)
			}
			headers := <-requests
			if test.provider == models.ProviderOpenAI {
				if headers.Get("Authorization") != "Bearer openai-only-test-key" || headers.Get("OpenAI-Organization") != "openai-only-org" || headers.Get("OpenAI-Project") != "openai-only-project" {
					t.Fatal("native OpenAI client did not preserve its environment credentials")
				}
				return
			}
			wantAuth := ""
			if test.key != "" {
				wantAuth = "Bearer " + test.key
			}
			if headers.Get("Authorization") != wantAuth {
				t.Fatal("compatible provider did not use only its own authorization")
			}
			if headers.Get("OpenAI-Organization") != "" || headers.Get("OpenAI-Project") != "" {
				t.Fatal("compatible provider inherited OpenAI account headers")
			}
			if test.provider == models.ProviderAzure && headers.Get("Api-Key") != "azure-only-test-key" {
				t.Fatal("Azure client did not retain its own API key")
			}
		})
	}
}
