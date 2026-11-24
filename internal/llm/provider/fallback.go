package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unsafe"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

const maxFallbackStreamBytes = 32 << 20

// FallbackProvider retries a complete model request with the configured
// providers when the preceding provider returns an error. Streaming attempts
// are buffered until completion so a partial failed response is never mixed
// with a fallback response.
type FallbackProvider struct {
	providers []Provider
}

func NewFallbackProvider(primary Provider, fallbacks ...Provider) (Provider, error) {
	if primary == nil {
		return nil, errors.New("primary provider is required")
	}
	providers := make([]Provider, 0, 1+len(fallbacks))
	providers = append(providers, primary)
	for _, fallback := range fallbacks {
		if fallback != nil {
			providers = append(providers, fallback)
		}
	}
	return &FallbackProvider{providers: providers}, nil
}

func (p *FallbackProvider) Model() models.Model { return p.providers[0].Model() }

func (p *FallbackProvider) SendMessages(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) (*ProviderResponse, error) {
	var failures []string
	for _, candidate := range p.providers {
		response, err := candidate.SendMessages(ctx, messages, toolset)
		if err == nil && response != nil {
			return response, nil
		}
		if err == nil {
			err = errors.New("provider returned an empty response")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		failures = append(failures, fmt.Sprintf("%s/%s: %v", candidate.Model().Provider, candidate.Model().ID, err))
	}
	return nil, fmt.Errorf("all configured model providers failed: %s", strings.Join(failures, "; "))
}

func (p *FallbackProvider) StreamResponse(ctx context.Context, messages []message.Message, toolset []tools.BaseTool) <-chan ProviderEvent {
	output := make(chan ProviderEvent)
	go func() {
		defer close(output)
		var failures []string
		for _, candidate := range p.providers {
			if ctx.Err() != nil {
				return
			}
			buffer, failure := bufferFallbackAttempt(ctx, candidate, messages, toolset)
			if failure == nil {
				for _, event := range buffer {
					if !sendProviderEvent(ctx, output, event) {
						return
					}
				}
				return
			}
			if ctx.Err() != nil {
				return
			}
			failures = append(failures, fmt.Sprintf("%s/%s: %v", candidate.Model().Provider, candidate.Model().ID, failure))
		}
		sendProviderEvent(ctx, output, ProviderEvent{Type: EventError, Error: fmt.Errorf("all configured model providers failed: %s", strings.Join(failures, "; "))})
	}()
	return output
}

func bufferFallbackAttempt(ctx context.Context, candidate Provider, messages []message.Message, toolset []tools.BaseTool) ([]ProviderEvent, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream := candidate.StreamResponse(attemptCtx, messages, toolset)
	buffer := make([]ProviderEvent, 0, 8)
	// Reserve initial storage as well as the growth allowance charged per event.
	remaining := maxFallbackStreamBytes - 2*cap(buffer)*int(unsafe.Sizeof(ProviderEvent{}))
	completed := false
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case event, ok := <-stream:
			if !ok {
				if !completed {
					return nil, errors.New("provider stream ended without completion")
				}
				return buffer, nil
			}
			if event.Type == EventError {
				if event.Error == nil {
					event.Error = errors.New("provider returned an unspecified streaming error")
				}
				return nil, event.Error
			}
			event.Model = candidate.Model()
			snapshot, size, fits := snapshotFallbackEvent(event, remaining)
			if !fits {
				return nil, errors.New("provider stream exceeded fallback buffer limit")
			}
			remaining -= size
			buffer = append(buffer, snapshot)
			completed = completed || event.Type == EventComplete
		}
	}
}

// snapshotFallbackEvent accounts for retained payloads before allocating a
// snapshot. Copies prevent provider-owned pointers, oversized slice backing
// arrays, or substrings of larger buffers from escaping the limit. Charging
// twice the payload size plus allocation overhead conservatively allows for
// allocator rounding and event-slice growth, including streams of empty events.
func snapshotFallbackEvent(event ProviderEvent, limit int) (ProviderEvent, int, bool) {
	remaining := limit
	charge := func(size int) bool {
		if size < 0 || size > remaining {
			return false
		}
		remaining -= size
		return true
	}
	allocation := func(size int) bool {
		return charge(size) && charge(size) && charge(16)
	}
	text := func(value string) bool {
		return value == "" || allocation(len(value))
	}
	tool := func(call message.ToolCall) bool {
		return text(call.ID) && text(call.Name) && text(call.Input) && text(call.Type)
	}
	if !allocation(int(unsafe.Sizeof(event))) || !text(string(event.Type)) || !text(event.Content) || !text(event.Thinking) ||
		!text(string(event.Model.ID)) || !text(event.Model.Name) || !text(string(event.Model.Provider)) || !text(event.Model.APIModel) {
		return ProviderEvent{}, 0, false
	}
	if event.ToolCall != nil && (!allocation(int(unsafe.Sizeof(*event.ToolCall))) || !tool(*event.ToolCall)) {
		return ProviderEvent{}, 0, false
	}
	if event.Response != nil {
		if !allocation(int(unsafe.Sizeof(*event.Response))) || !text(event.Response.Content) || !text(string(event.Response.FinishReason)) {
			return ProviderEvent{}, 0, false
		}
		for _, call := range event.Response.ToolCalls {
			if !allocation(int(unsafe.Sizeof(call))) || !tool(call) {
				return ProviderEvent{}, 0, false
			}
		}
	}
	var errorText string
	if event.Error != nil {
		errorText = event.Error.Error()
		if !allocation(int(unsafe.Sizeof(errorText))) || !text(errorText) {
			return ProviderEvent{}, 0, false
		}
	}

	event.Type = EventType(strings.Clone(string(event.Type)))
	event.Content = strings.Clone(event.Content)
	event.Thinking = strings.Clone(event.Thinking)
	event.Model.ID = models.ModelID(strings.Clone(string(event.Model.ID)))
	event.Model.Name = strings.Clone(event.Model.Name)
	event.Model.Provider = models.ModelProvider(strings.Clone(string(event.Model.Provider)))
	event.Model.APIModel = strings.Clone(event.Model.APIModel)
	if event.ToolCall != nil {
		call := cloneFallbackToolCall(*event.ToolCall)
		event.ToolCall = &call
	}
	if event.Response != nil {
		response := *event.Response
		response.Content = strings.Clone(response.Content)
		response.FinishReason = message.FinishReason(strings.Clone(string(response.FinishReason)))
		if event.Response.ToolCalls != nil {
			response.ToolCalls = make([]message.ToolCall, len(event.Response.ToolCalls))
			for i, call := range event.Response.ToolCalls {
				response.ToolCalls[i] = cloneFallbackToolCall(call)
			}
		}
		event.Response = &response
	}
	if event.Error != nil {
		event.Error = errors.New(strings.Clone(errorText))
	}
	return event, limit - remaining, true
}

func cloneFallbackToolCall(call message.ToolCall) message.ToolCall {
	call.ID = strings.Clone(call.ID)
	call.Name = strings.Clone(call.Name)
	call.Input = strings.Clone(call.Input)
	call.Type = strings.Clone(call.Type)
	return call
}
