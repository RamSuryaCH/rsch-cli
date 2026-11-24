package provider

import (
	"context"
	"os"

	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// mockClient is deterministic and local. It is intended for tests, offline
// demos, and provider-routing verification; it never executes tools.
type mockClient struct{ options providerClientOptions }

type MockClient ProviderClient

func newMockClient(options providerClientOptions) MockClient { return &mockClient{options: options} }

func (m *mockClient) response(ctx context.Context) (*ProviderResponse, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	content := os.Getenv("RSCH_MOCK_RESPONSE")
	if content == "" {
		content = "mock response"
	}
	return &ProviderResponse{Content: content, FinishReason: message.FinishReasonEndTurn}, nil
}

func (m *mockClient) send(ctx context.Context, _ []message.Message, _ []tools.BaseTool) (*ProviderResponse, error) {
	return m.response(ctx)
}

func (m *mockClient) stream(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) <-chan ProviderEvent {
	ch := make(chan ProviderEvent, 5)
	go func() {
		defer close(ch)
		response, err := m.response(ctx)
		if err != nil {
			ch <- ProviderEvent{Type: EventError, Error: err}
			return
		}
		select {
		case ch <- ProviderEvent{Type: EventContentStart}:
		case <-ctx.Done():
			ch <- ProviderEvent{Type: EventError, Error: ctx.Err()}
			return
		}
		select {
		case ch <- ProviderEvent{Type: EventContentDelta, Content: response.Content}:
		case <-ctx.Done():
			ch <- ProviderEvent{Type: EventError, Error: ctx.Err()}
			return
		}
		select {
		case ch <- ProviderEvent{Type: EventContentStop}:
		case <-ctx.Done():
			return
		}
		select {
		case ch <- ProviderEvent{Type: EventComplete, Response: response}:
		case <-ctx.Done():
		}
	}()
	return ch
}
