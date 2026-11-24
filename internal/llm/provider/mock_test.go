package provider

import (
	"context"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
)

func TestMockProviderIsDeterministicAndCancellable(t *testing.T) {
	client := newMockClient(providerClientOptions{})
	response, err := client.send(context.Background(), nil, nil)
	if err != nil || response.Content == "" {
		t.Fatalf("unexpected mock response: %#v, %v", response, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.send(ctx, nil, nil); err == nil {
		t.Fatal("expected cancellation")
	}
	provider, err := NewProvider(models.ProviderMock, WithModel(models.Model{Provider: models.ProviderMock}))
	if err != nil || provider == nil {
		t.Fatalf("failed to construct mock provider: %v", err)
	}
}
