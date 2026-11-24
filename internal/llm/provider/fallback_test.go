package provider

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type fallbackTestProvider struct {
	model   models.Model
	send    *ProviderResponse
	sendErr error
	events  []ProviderEvent
}

type callbackStreamProvider struct {
	fallbackTestProvider
	stream func(context.Context) <-chan ProviderEvent
}

func (p callbackStreamProvider) StreamResponse(ctx context.Context, _ []message.Message, _ []tools.BaseTool) <-chan ProviderEvent {
	return p.stream(ctx)
}

func (p fallbackTestProvider) Model() models.Model { return p.model }
func (p fallbackTestProvider) SendMessages(context.Context, []message.Message, []tools.BaseTool) (*ProviderResponse, error) {
	return p.send, p.sendErr
}
func (p fallbackTestProvider) StreamResponse(context.Context, []message.Message, []tools.BaseTool) <-chan ProviderEvent {
	channel := make(chan ProviderEvent, len(p.events))
	for _, event := range p.events {
		channel <- event
	}
	close(channel)
	return channel
}

func TestFallbackProviderRetriesSend(t *testing.T) {
	primary := fallbackTestProvider{model: models.Model{ID: "primary", Provider: "one"}, sendErr: errors.New("primary unavailable")}
	secondary := fallbackTestProvider{model: models.Model{ID: "secondary", Provider: "two"}, send: &ProviderResponse{Content: "fallback"}}
	service, err := NewFallbackProvider(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	response, err := service.SendMessages(context.Background(), nil, nil)
	if err != nil || response == nil || response.Content != "fallback" {
		t.Fatalf("expected fallback response, response=%+v err=%v", response, err)
	}
}

func TestFallbackProviderDoesNotEmitPartialFailedStream(t *testing.T) {
	primary := fallbackTestProvider{model: models.Model{ID: "primary", Provider: "one"}, events: []ProviderEvent{{Type: EventContentDelta, Content: "partial"}, {Type: EventError, Error: errors.New("stream failed")}}}
	secondary := fallbackTestProvider{model: models.Model{ID: "secondary", Provider: "two"}, events: []ProviderEvent{{Type: EventContentDelta, Content: "complete"}, {Type: EventComplete}}}
	service, err := NewFallbackProvider(primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	var contents string
	var selected models.ModelID
	for event := range service.StreamResponse(context.Background(), nil, nil) {
		if event.Type == EventContentDelta {
			contents += event.Content
			selected = event.Model.ID
		}
	}
	if contents != "complete" {
		t.Fatalf("failed stream leaked partial output: %q", contents)
	}
	if selected != "secondary" {
		t.Fatalf("expected fallback provenance, got %q", selected)
	}
}

func TestFallbackProviderRetriesStreamWithoutCompletion(t *testing.T) {
	for _, tc := range []struct {
		name   string
		events []ProviderEvent
	}{
		{name: "empty stream"},
		{name: "truncated stream", events: []ProviderEvent{{Type: EventContentDelta, Content: "partial"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := fallbackTestProvider{model: models.Model{ID: "primary"}, events: tc.events}
			secondary := fallbackTestProvider{model: models.Model{ID: "secondary"}, events: []ProviderEvent{
				{Type: EventContentDelta, Content: "fallback"},
				{Type: EventComplete, Response: &ProviderResponse{Content: "fallback"}},
			}}
			service, err := NewFallbackProvider(primary, secondary)
			if err != nil {
				t.Fatal(err)
			}
			events := collectProviderEvents(service.StreamResponse(context.Background(), nil, nil))
			if len(events) != 2 || events[0].Content != "fallback" || events[0].Model.ID != "secondary" || events[1].Type != EventComplete {
				t.Fatalf("expected completed fallback instead of incomplete primary: %+v", events)
			}
		})
	}
}

func TestFallbackProviderErrorsWhenNoStreamCompletes(t *testing.T) {
	service, err := NewFallbackProvider(fallbackTestProvider{model: models.Model{ID: "primary"}})
	if err != nil {
		t.Fatal(err)
	}
	events := collectProviderEvents(service.StreamResponse(context.Background(), nil, nil))
	if len(events) != 1 || events[0].Type != EventError || events[0].Error == nil {
		t.Fatalf("expected terminal error for incomplete streams: %+v", events)
	}
}

func TestFallbackProviderBoundsToolAndResponsePayloads(t *testing.T) {
	large := strings.Repeat("x", maxFallbackStreamBytes+1)
	for _, tc := range []struct {
		name  string
		event ProviderEvent
	}{
		{name: "tool input", event: ProviderEvent{Type: EventToolUseDelta, ToolCall: &message.ToolCall{Input: large}}},
		{name: "response content", event: ProviderEvent{Type: EventComplete, Response: &ProviderResponse{Content: large}}},
		{name: "response tool input", event: ProviderEvent{Type: EventComplete, Response: &ProviderResponse{ToolCalls: []message.ToolCall{{Input: large}}}}},
		{name: "empty tool overhead", event: ProviderEvent{Type: EventComplete, Response: &ProviderResponse{ToolCalls: make([]message.ToolCall, maxFallbackStreamBytes/64)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			primary := fallbackTestProvider{events: []ProviderEvent{tc.event, {Type: EventComplete}}}
			secondary := fallbackTestProvider{model: models.Model{ID: "secondary"}, events: []ProviderEvent{{Type: EventComplete, Response: &ProviderResponse{Content: "fallback"}}}}
			service, err := NewFallbackProvider(primary, secondary)
			if err != nil {
				t.Fatal(err)
			}
			events := collectProviderEvents(service.StreamResponse(context.Background(), nil, nil))
			if len(events) != 1 || events[0].Model.ID != "secondary" || events[0].Response == nil || events[0].Response.Content != "fallback" {
				t.Fatal("oversized primary stream was emitted instead of falling back")
			}
		})
	}
}

func TestFallbackProviderCancellationClosesWithoutConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		service, err := NewFallbackProvider(fallbackTestProvider{events: []ProviderEvent{{Type: EventContentDelta, Content: "complete"}, {Type: EventComplete}}})
		if err != nil {
			t.Fatal(err)
		}
		output := service.StreamResponse(ctx, nil, nil)
		synctest.Wait() // The wrapper is blocked delivering its buffered attempt.
		cancel()
		synctest.Wait()
		assertCanceledStreamClosed(t, output)
	})
}

func TestFallbackProviderCancelsFailedAttemptBeforeTryingNext(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event ProviderEvent
	}{
		{name: "provider error", event: ProviderEvent{Type: EventError, Error: errors.New("failed")}},
		{name: "buffer limit", event: ProviderEvent{Type: EventContentDelta, Content: strings.Repeat("x", maxFallbackStreamBytes+1)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				stopped := make(chan struct{})
				primary := callbackStreamProvider{stream: func(ctx context.Context) <-chan ProviderEvent {
					output := make(chan ProviderEvent, 1)
					output <- tc.event
					go func() {
						<-ctx.Done()
						close(stopped)
						close(output)
					}()
					return output
				}}
				secondary := fallbackTestProvider{model: models.Model{ID: "secondary"}, events: []ProviderEvent{{Type: EventComplete}}}
				service, err := NewFallbackProvider(primary, secondary)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				output := service.StreamResponse(ctx, nil, nil)
				synctest.Wait()
				select {
				case <-stopped:
				default:
					t.Error("failed attempt retained its producer while trying fallback")
					cancel()
				}
				events := collectProviderEvents(output)
				if len(events) != 1 || events[0].Type != EventComplete || events[0].Model.ID != "secondary" {
					t.Fatal("failed attempt prevented successful fallback")
				}
			})
		})
	}
}

func TestFallbackProviderSnapshotsReusedToolCall(t *testing.T) {
	primary := callbackStreamProvider{stream: func(context.Context) <-chan ProviderEvent {
		output := make(chan ProviderEvent)
		go func() {
			defer close(output)
			call := message.ToolCall{ID: "call", Input: "original"}
			output <- ProviderEvent{Type: EventToolUseDelta, ToolCall: &call}
			output <- ProviderEvent{Type: EventToolUseStop}
			// The second send completes only after the first event was buffered.
			call.Input = "reused"
			output <- ProviderEvent{Type: EventComplete}
		}()
		return output
	}}
	service, err := NewFallbackProvider(primary)
	if err != nil {
		t.Fatal(err)
	}
	events := collectProviderEvents(service.StreamResponse(context.Background(), nil, nil))
	if len(events) != 3 || events[0].ToolCall == nil || events[0].ToolCall.Input != "original" {
		t.Fatal("buffered tool input changed after provider reused its storage")
	}
}

func assertCanceledStreamClosed(t *testing.T, output <-chan ProviderEvent) {
	t.Helper()
	select {
	case _, ok := <-output:
		if ok {
			t.Error("canceled producer waited for a consumer instead of closing")
		}
	default:
		t.Error("canceled producer did not close its stream")
	}
	// Release any failed implementation so the synctest bubble can finish.
	for range output {
	}
}
