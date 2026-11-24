package provider

import (
	"context"
	"strings"
	"testing"
)

func TestAzureProviderFailsClosedWithoutEndpointConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		apiVersion string
	}{
		{name: "both missing"},
		{name: "endpoint missing", apiVersion: "2025-04-01-preview"},
		{name: "API version missing", endpoint: "https://example.openai.azure.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AZURE_OPENAI_ENDPOINT", test.endpoint)
			t.Setenv("AZURE_OPENAI_API_VERSION", test.apiVersion)
			t.Setenv("AZURE_OPENAI_API_KEY", "azure-secret")

			client, ok := newAzureClient(providerClientOptions{apiKey: "azure-secret"}).(*azureClient)
			if !ok {
				t.Fatal("expected concrete Azure client")
			}
			if client.initErr == nil || !strings.Contains(client.initErr.Error(), "AZURE_OPENAI_ENDPOINT") {
				t.Fatalf("expected incomplete Azure configuration error, got %v", client.initErr)
			}
			if client.openaiClient != nil {
				t.Fatal("incomplete Azure configuration must not create a public OpenAI client")
			}
			if _, err := client.send(context.Background(), nil, nil); err == nil {
				t.Fatal("expected Azure send to fail closed")
			}
			event, ok := <-client.stream(context.Background(), nil, nil)
			if !ok || event.Type != EventError || event.Error == nil {
				t.Fatalf("expected Azure stream initialization error, got %+v", event)
			}
		})
	}
}

func TestAzureProviderUsesExplicitAPIKeyAuthentication(t *testing.T) {
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://example.openai.azure.com")
	t.Setenv("AZURE_OPENAI_API_VERSION", "2025-04-01-preview")
	t.Setenv("AZURE_OPENAI_API_KEY", "")

	client, ok := newAzureClient(providerClientOptions{apiKey: "configured-key"}).(*azureClient)
	if !ok {
		t.Fatal("expected concrete Azure client")
	}
	if client.initErr != nil {
		t.Fatalf("unexpected Azure initialization error: %v", client.initErr)
	}
	if client.authMode != azureAuthAPIKey {
		t.Fatalf("auth mode = %q, want %q", client.authMode, azureAuthAPIKey)
	}
	if client.openaiClient == nil {
		t.Fatal("expected initialized Azure OpenAI client")
	}
}

func TestAzureProviderUsesDefaultCredentialWithoutAPIKey(t *testing.T) {
	t.Setenv("AZURE_OPENAI_ENDPOINT", "https://example.openai.azure.com")
	t.Setenv("AZURE_OPENAI_API_VERSION", "2025-04-01-preview")
	t.Setenv("AZURE_OPENAI_API_KEY", "")

	client, ok := newAzureClient(providerClientOptions{}).(*azureClient)
	if !ok {
		t.Fatal("expected concrete Azure client")
	}
	if client.initErr != nil {
		t.Fatalf("unexpected Azure initialization error: %v", client.initErr)
	}
	if client.authMode != azureAuthDefaultCredential {
		t.Fatalf("auth mode = %q, want %q", client.authMode, azureAuthDefaultCredential)
	}
}
