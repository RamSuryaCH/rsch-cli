package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/session"
)

func TestRunCancellationDoesNotWaitForProviderStream(t *testing.T) {
	for _, terminalSent := range []bool{false, true} {
		name := "before completion"
		if terminalSent {
			name = "after completion before stream close"
		}
		t.Run(name, func(t *testing.T) {
			stream := make(chan provider.ProviderEvent, 1)
			defer close(stream)
			if terminalSent {
				stream <- provider.ProviderEvent{Type: provider.EventComplete, Response: &provider.ProviderResponse{FinishReason: message.FinishReasonEndTurn}}
			}
			started := make(chan struct{})
			p := &testProvider{
				model: models.Model{ID: "stalled-stream"},
				stream: func(context.Context, []message.Message, []tools.BaseTool) <-chan provider.ProviderEvent {
					close(started)
					return stream
				},
			}
			a := newTestAgent(p)
			defer a.Shutdown()
			results, err := a.Run(context.Background(), "session-1", "hello")
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("provider never started")
			}
			a.Cancel("session-1")
			result := awaitAgentEvent(t, results)
			if !errors.Is(result.Error, ErrRequestCancelled) {
				t.Fatalf("expected cancellation, got %+v", result)
			}
			if a.IsBusy() || len(a.modelSlots) != 0 {
				t.Fatal("canceled stream retained session or model capacity")
			}
			stored, err := a.messages.List(context.Background(), "session-1")
			if err != nil {
				t.Fatal(err)
			}
			if len(stored) != 2 || stored[1].FinishReason() != message.FinishReasonCanceled {
				t.Fatalf("interrupted assistant was not marked canceled: %+v", stored)
			}
		})
	}
}

func TestRunContainsParallelToolPanicAndPersistsResults(t *testing.T) {
	p := &testProvider{
		model: models.Model{ID: "tool-panic"},
		stream: func(context.Context, []message.Message, []tools.BaseTool) <-chan provider.ProviderEvent {
			return completeStream(provider.ProviderResponse{
				FinishReason: message.FinishReasonToolUse,
				ToolCalls: []message.ToolCall{
					{ID: "panic-call", Name: "panic-tool", Input: `{}`},
					{ID: "read-call", Name: "read-tool", Input: `{}`},
				},
			})
		},
	}
	a := newTestAgent(p,
		testTool{
			info: tools.ToolInfo{Name: "panic-tool", Risk: "low"},
			run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
				panic("private implementation details")
			},
		},
		testTool{
			info: tools.ToolInfo{Name: "read-tool", Risk: "low"},
			run: func(context.Context, tools.ToolCall) (tools.ToolResponse, error) {
				return tools.NewTextResponse("read result"), nil
			},
		},
	)
	defer a.Shutdown()
	results, err := a.Run(context.Background(), "session-1", "use tools")
	if err != nil {
		t.Fatal(err)
	}
	result := awaitAgentEvent(t, results)
	if result.Error == nil || !strings.Contains(result.Error.Error(), "panicked") {
		t.Fatalf("expected terminal tool panic error, got %+v", result)
	}
	if strings.Contains(result.Error.Error(), "private implementation details") {
		t.Fatal("tool panic payload leaked into the result")
	}
	if a.IsBusy() || len(a.modelSlots) != 0 {
		t.Fatal("tool panic retained session or model capacity")
	}
	stored, err := a.messages.List(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 3 || stored[1].FinishReason() != message.FinishReasonError {
		t.Fatalf("tool panic did not persist an interrupted assistant: %+v", stored)
	}
	toolResults := stored[2].ToolResults()
	if len(toolResults) != 2 || toolResults[0].ToolCallID != "panic-call" || !toolResults[0].IsError || toolResults[1].ToolCallID != "read-call" {
		t.Fatalf("tool panic lost tool result identities: %+v", toolResults)
	}
}

func TestSummarizePanicReleasesModelCapacity(t *testing.T) {
	a := newTestAgent(&testProvider{model: models.Model{ID: "unused"}})
	defer a.Shutdown()
	a.modelSlots = make(chan struct{}, 1)
	a.summarizeProvider = &testProvider{
		model: models.Model{ID: "panic-summary"},
		send: func(context.Context, []message.Message, []tools.BaseTool) (*provider.ProviderResponse, error) {
			panic("summarizer failed")
		},
	}
	_, err := a.messages.Create(context.Background(), "session-1", message.CreateMessageParams{
		Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events := a.Subscribe(ctx)
	if err := a.Summarize(ctx, "session-1"); err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("no terminal summary event")
			}
			if !event.Payload.Done {
				continue
			}
			if event.Payload.Error == nil || !strings.Contains(event.Payload.Error.Error(), "panic while summarizing") {
				t.Fatalf("unexpected summary result: %+v", event.Payload)
			}
			release, err := a.acquireModel(ctx)
			if err != nil {
				t.Fatalf("summary panic leaked the model slot: %v", err)
			}
			release()
			return
		case <-ctx.Done():
			t.Fatal("timed out waiting for summary failure")
		}
	}
}

type saveFailingSessions struct {
	session.Service
	err      error
	contexts chan context.Context
}

func (s saveFailingSessions) Save(ctx context.Context, _ session.Session) (session.Session, error) {
	s.contexts <- ctx
	return session.Session{}, s.err
}

func TestSummarizeSaveFailureDoesNotPublishSuccess(t *testing.T) {
	a := newTestAgent(&testProvider{model: models.Model{ID: "unused"}})
	defer a.Shutdown()
	saveErr := errors.New("session storage unavailable")
	savedContexts := make(chan context.Context, 1)
	a.sessions = saveFailingSessions{Service: a.sessions, err: saveErr, contexts: savedContexts}
	a.summarizeProvider = &testProvider{
		model: models.Model{ID: "summary-model"},
		send: func(context.Context, []message.Message, []tools.BaseTool) (*provider.ProviderResponse, error) {
			return &provider.ProviderResponse{Content: "Conversation summary"}, nil
		},
	}
	_, err := a.messages.Create(context.Background(), "session-1", message.CreateMessageParams{
		Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	events := a.Subscribe(ctx)
	if err := a.Summarize(ctx, "session-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case savedCtx := <-savedContexts:
		// Summarize cancels its context after publishing all of its events.
		<-savedCtx.Done()
	case <-ctx.Done():
		t.Fatal("summary never reached session persistence")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("summary did not finish before its deadline: %v", err)
	}
	terminalErrors := 0
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("summary event subscription closed unexpectedly")
			}
			if event.Payload.Type == AgentEventTypeSummarize && event.Payload.Done {
				t.Fatalf("failed save published summary success: %+v", event.Payload)
			}
			if event.Payload.Done {
				if event.Payload.Type != AgentEventTypeError || !errors.Is(event.Payload.Error, saveErr) {
					t.Fatalf("unexpected terminal summary event: %+v", event.Payload)
				}
				terminalErrors++
			}
		default:
			if terminalErrors != 1 {
				t.Fatalf("terminal summary errors = %d, want 1", terminalErrors)
			}
			return
		}
	}
}
