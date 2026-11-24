package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/routing"
)

// RoutedProvider chooses a task-class-appropriate candidate for each request,
// then applies the same buffered fallback behavior as FallbackProvider. Model
// selection is based only on observable request content and declared model
// capabilities; governance and credential validation happen before creation.
type RoutedProvider struct {
	providers []Provider
}

func NewRoutedProvider(primary Provider, alternatives ...Provider) (Provider, error) {
	if primary == nil {
		return nil, errors.New("primary routed provider is required")
	}
	providers := make([]Provider, 0, 1+len(alternatives))
	providers = append(providers, primary)
	for _, candidate := range alternatives {
		if candidate != nil {
			providers = append(providers, candidate)
		}
	}
	return &RoutedProvider{providers: providers}, nil
}

func (p *RoutedProvider) Model() models.Model {
	if p == nil || len(p.providers) == 0 {
		return models.Model{}
	}
	return p.providers[0].Model()
}

func (p *RoutedProvider) SendMessages(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) (*ProviderResponse, error) {
	fallback, err := p.selectedFallback(messages)
	if err != nil {
		return nil, err
	}
	return fallback.SendMessages(ctx, messages, toolset)
}

func (p *RoutedProvider) StreamResponse(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) <-chan ProviderEvent {
	output := make(chan ProviderEvent)
	go func() {
		defer close(output)
		if ctx.Err() != nil {
			return
		}
		fallback, err := p.selectedFallback(messages)
		if err != nil {
			sendProviderEvent(ctx, output, ProviderEvent{Type: EventError, Error: err})
			return
		}
		stream := fallback.StreamResponse(ctx, messages, toolset)
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-stream:
				if !ok || !sendProviderEvent(ctx, output, event) {
					return
				}
			}
		}
	}()
	return output
}

func (p *RoutedProvider) selectedFallback(messages []message.Message) (Provider, error) {
	if p == nil || len(p.providers) == 0 {
		return nil, errors.New("no routed providers configured")
	}
	prompt, attachments := routingInput(messages)
	decision := routing.Classify(routing.Signals{Prompt: prompt, HasAttachments: attachments})
	candidates := make([]models.Model, 0, len(p.providers))
	for _, candidate := range p.providers {
		model := candidate.Model()
		candidates = append(candidates, model)
	}
	ordered := routing.OrderModels(decision.Class, candidates, routing.Signals{Prompt: prompt, HasAttachments: attachments})
	orderedProviders := make([]Provider, 0, len(ordered))
	used := make([]bool, len(candidates))
	for _, model := range ordered {
		// Preserve the exact candidate whose metadata passed the routing gate.
		// IDs can be shared across providers or differently configured clients.
		for i, candidate := range candidates {
			if !used[i] && candidate == model {
				orderedProviders = append(orderedProviders, p.providers[i])
				used[i] = true
				break
			}
		}
	}
	if len(orderedProviders) == 0 {
		return nil, fmt.Errorf("routing class %s has no eligible providers", decision.Class)
	}
	if len(orderedProviders) == 1 {
		return orderedProviders[0], nil
	}
	return NewFallbackProvider(orderedProviders[0], orderedProviders[1:]...)
}

func routingInput(messages []message.Message) (string, bool) {
	var text strings.Builder
	attachments := false
	for i := range messages {
		text.WriteString(messages[i].Content().Text)
		text.WriteByte('\n')
		if len(messages[i].ImageURLContent()) > 0 || len(messages[i].BinaryContent()) > 0 {
			attachments = true
		}
		if !attachments {
			for _, result := range messages[i].ToolResults() {
				if len(result.Media) > 0 {
					attachments = true
					break
				}
			}
		}
	}
	return text.String(), attachments
}
