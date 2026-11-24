package provider

import (
	"context"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

// WithTimeout applies a hard per-request deadline to a provider while
// preserving the provider interface and model provenance. It is intentionally
// a wrapper so routing, fallback, and direct providers share the same limit.
func WithTimeout(inner Provider, timeout time.Duration) Provider {
	if inner == nil || timeout <= 0 {
		return inner
	}
	return timeoutProvider{inner: inner, timeout: timeout}
}

type timeoutProvider struct {
	inner   Provider
	timeout time.Duration
}

func (p timeoutProvider) Model() models.Model { return p.inner.Model() }

func (p timeoutProvider) SendMessages(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) (*ProviderResponse, error) {
	limited, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	return p.inner.SendMessages(limited, messages, toolset)
}

func (p timeoutProvider) StreamResponse(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) <-chan ProviderEvent {
	limited, cancel := context.WithTimeout(ctx, p.timeout)
	source := p.inner.StreamResponse(limited, messages, toolset)
	// A terminal timeout must remain readable without keeping the producer
	// alive if its consumer pauses or stops reading.
	output := make(chan ProviderEvent, 1)
	go func() {
		defer close(output)
		defer cancel()
		discardPending := func() {
			select {
			case <-output:
			default:
			}
		}
		finishDeadline := func() {
			discardPending()
			if ctx.Err() == nil {
				// This goroutine is the only sender, so the slot remains free
				// after discarding an undelivered content event.
				output <- ProviderEvent{Type: EventError, Error: limited.Err()}
			}
		}
		for {
			if ctx.Err() != nil {
				discardPending()
				return
			}
			if limited.Err() != nil {
				finishDeadline()
				return
			}
			select {
			case event, ok := <-source:
				if limited.Err() != nil {
					finishDeadline()
					return
				}
				if !ok {
					return
				}
				select {
				case output <- event:
					if event.Type == EventComplete || event.Type == EventError {
						return
					}
				case <-ctx.Done():
					discardPending()
					return
				case <-limited.Done():
					finishDeadline()
					return
				}
			case <-limited.Done():
				finishDeadline()
				return
			}
		}
	}()
	return output
}
