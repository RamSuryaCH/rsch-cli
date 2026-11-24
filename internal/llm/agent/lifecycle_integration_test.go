package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

type integrationStream func(context.Context, []message.Message, []tools.BaseTool) <-chan provider.ProviderEvent

type integrationProvider struct {
	model   models.Model
	streams []integrationStream

	mu        sync.Mutex
	calls     int
	histories [][]message.Message
}

func (p *integrationProvider) Model() models.Model { return p.model }

func (p *integrationProvider) SendMessages(context.Context, []message.Message, []tools.BaseTool) (*provider.ProviderResponse, error) {
	return nil, errors.New("integration provider does not support non-streaming requests")
}

func (p *integrationProvider) StreamResponse(ctx context.Context, history []message.Message, available []tools.BaseTool) <-chan provider.ProviderEvent {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.histories = append(p.histories, cloneIntegrationMessages(history))
	p.mu.Unlock()
	if call >= len(p.streams) {
		return integrationEventStream(provider.ProviderEvent{Type: provider.EventError, Error: fmt.Errorf("unexpected provider call %d", call+1)})
	}
	return p.streams[call](ctx, history, available)
}

func (p *integrationProvider) snapshot() (int, [][]message.Message) {
	p.mu.Lock()
	defer p.mu.Unlock()
	histories := make([][]message.Message, len(p.histories))
	for i := range p.histories {
		histories[i] = cloneIntegrationMessages(p.histories[i])
	}
	return p.calls, histories
}

func cloneIntegrationMessages(input []message.Message) []message.Message {
	result := make([]message.Message, len(input))
	for i, item := range input {
		result[i] = item
		result[i].Parts = append([]message.ContentPart(nil), item.Parts...)
	}
	return result
}

func integrationEventStream(events ...provider.ProviderEvent) <-chan provider.ProviderEvent {
	stream := make(chan provider.ProviderEvent, len(events))
	for _, event := range events {
		stream <- event
	}
	close(stream)
	return stream
}

type integrationTool struct {
	info tools.ToolInfo
	run  func(context.Context, tools.ToolCall) (tools.ToolResponse, error)
}

func (t integrationTool) Info() tools.ToolInfo { return t.info }

func (t integrationTool) Run(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
	return t.run(ctx, call)
}

func newPersistentIntegrationAgent(t *testing.T, p provider.Provider, available ...tools.BaseTool) (*agent, session.Service, message.Service, session.Session) {
	t.Helper()
	database, err := db.ConnectAt(filepath.Join(t.TempDir(), "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	queries := db.New(database)
	sessions := session.NewServiceWithDB(queries, database)
	messages := message.NewService(queries)
	created, err := sessions.Create(context.Background(), "integration session")
	if err != nil {
		t.Fatal(err)
	}
	a := &agent{
		Broker:         pubsub.NewBroker[AgentEvent](),
		sessions:       sessions,
		messages:       messages,
		tools:          available,
		provider:       p,
		activeRequests: sync.Map{},
		modelSlots:     make(chan struct{}, 2),
	}
	t.Cleanup(a.Shutdown)
	return a, sessions, messages, created
}

func awaitIntegrationResult(t *testing.T, results <-chan AgentEvent) AgentEvent {
	t.Helper()
	select {
	case result, ok := <-results:
		if !ok {
			t.Fatal("agent result channel closed without a result")
		}
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for agent result")
		return AgentEvent{}
	}
}

func TestIntegrationPromptToolMessageLifecyclePersistsConversation(t *testing.T) {
	model := models.Model{
		ID:           "integration-model",
		Provider:     models.ProviderMock,
		CostPer1MIn:  1,
		CostPer1MOut: 2,
	}
	var toolSessionID, toolMessageID string
	tool := integrationTool{
		info: tools.ToolInfo{Name: "fixture_lookup", Risk: "low"},
		run: func(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
			toolSessionID, toolMessageID = tools.GetContextValues(ctx)
			if call.ID != "call-1" || call.Input != `{"query":"answer"}` {
				return tools.ToolResponse{}, fmt.Errorf("unexpected tool call: %+v", call)
			}
			return tools.WithResponseMetadata(tools.NewTextResponse("fixture output"), map[string]string{"source": "integration"}), nil
		},
	}
	p := &integrationProvider{model: model}
	p.streams = []integrationStream{
		func(context.Context, []message.Message, []tools.BaseTool) <-chan provider.ProviderEvent {
			return integrationEventStream(provider.ProviderEvent{
				Type: provider.EventComplete,
				Response: &provider.ProviderResponse{
					FinishReason: message.FinishReasonToolUse,
					ToolCalls:    []message.ToolCall{{ID: "call-1", Name: "fixture_lookup", Input: `{"query":"answer"}`, Finished: true}},
					Usage:        provider.TokenUsage{InputTokens: 10, OutputTokens: 2},
				},
			})
		},
		func(_ context.Context, history []message.Message, _ []tools.BaseTool) <-chan provider.ProviderEvent {
			var observed *message.ToolResult
			for _, item := range history {
				for _, result := range item.ToolResults() {
					copy := result
					observed = &copy
				}
			}
			if observed == nil || observed.ToolCallID != "call-1" || observed.Content != "fixture output" || observed.IsError {
				return integrationEventStream(provider.ProviderEvent{Type: provider.EventError, Error: fmt.Errorf("tool result missing from provider history: %+v", observed)})
			}
			return integrationEventStream(
				provider.ProviderEvent{Type: provider.EventContentDelta, Content: "verified: " + observed.Content},
				provider.ProviderEvent{Type: provider.EventComplete, Response: &provider.ProviderResponse{
					Content:      "verified: " + observed.Content,
					FinishReason: message.FinishReasonEndTurn,
					Usage:        provider.TokenUsage{InputTokens: 4, OutputTokens: 3},
				}},
			)
		},
	}
	a, sessions, messages, created := newPersistentIntegrationAgent(t, p, tool)

	results, err := a.Run(context.Background(), created.ID, "use the fixture tool")
	if err != nil {
		t.Fatal(err)
	}
	result := awaitIntegrationResult(t, results)
	if result.Error != nil || result.Type != AgentEventTypeResponse || result.Message.Content().Text != "verified: fixture output" {
		t.Fatalf("unexpected final result: %+v", result)
	}

	persisted, err := messages.List(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 4 {
		t.Fatalf("persisted message count = %d, want 4: %+v", len(persisted), persisted)
	}
	roles := []message.MessageRole{message.User, message.Assistant, message.Tool, message.Assistant}
	for i, want := range roles {
		if persisted[i].Role != want {
			t.Fatalf("message %d role = %q, want %q", i, persisted[i].Role, want)
		}
	}
	if persisted[1].FinishReason() != message.FinishReasonToolUse || len(persisted[1].ToolCalls()) != 1 {
		t.Fatalf("first assistant message lost its tool call: %+v", persisted[1])
	}
	toolResults := persisted[2].ToolResults()
	if len(toolResults) != 1 || toolResults[0].Content != "fixture output" || toolResults[0].Metadata == "" {
		t.Fatalf("tool result was not durably persisted: %+v", persisted[2])
	}
	if persisted[3].FinishReason() != message.FinishReasonEndTurn || persisted[3].Content().Text != "verified: fixture output" {
		t.Fatalf("final assistant message was not durably persisted: %+v", persisted[3])
	}
	if toolSessionID != created.ID || toolMessageID != persisted[1].ID {
		t.Fatalf("tool context = (%q, %q), want (%q, %q)", toolSessionID, toolMessageID, created.ID, persisted[1].ID)
	}

	updated, err := sessions.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.MessageCount != 4 || updated.PromptTokens != 14 || updated.CompletionTokens != 5 {
		t.Fatalf("unexpected persisted session accounting: %+v", updated)
	}
	usage, err := sessions.Usage(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 2 || usage[0].Model != string(model.ID) || usage[1].Provider != string(model.Provider) {
		t.Fatalf("unexpected detailed usage: %+v", usage)
	}
	calls, histories := p.snapshot()
	if calls != 2 || len(histories) != 2 || len(histories[1]) != 3 {
		t.Fatalf("provider lifecycle calls=%d histories=%v", calls, histories)
	}
}

func TestIntegrationCancellationPersistsInterruptedAssistant(t *testing.T) {
	p := &integrationProvider{model: models.Model{ID: "cancel-model", Provider: models.ProviderMock}}
	p.streams = []integrationStream{func(ctx context.Context, _ []message.Message, _ []tools.BaseTool) <-chan provider.ProviderEvent {
		stream := make(chan provider.ProviderEvent)
		go func() {
			<-ctx.Done()
			close(stream)
		}()
		return stream
	}}
	a, _, messages, created := newPersistentIntegrationAgent(t, p)
	messageContext, stopMessages := context.WithCancel(context.Background())
	t.Cleanup(stopMessages)
	messageEvents := messages.Subscribe(messageContext)

	results, err := a.Run(context.Background(), created.ID, "cancel this request")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	assistantPersisted := false
	for !assistantPersisted {
		select {
		case event, ok := <-messageEvents:
			if !ok {
				t.Fatal("message event stream closed")
			}
			assistantPersisted = event.Type == pubsub.CreatedEvent && event.Payload.Role == message.Assistant
		case <-deadline:
			t.Fatal("assistant message was not persisted before cancellation")
		}
	}
	a.Cancel(created.ID)
	result := awaitIntegrationResult(t, results)
	if !errors.Is(result.Error, ErrRequestCancelled) {
		t.Fatalf("cancellation result = %+v", result)
	}
	if a.IsSessionBusy(created.ID) || a.IsBusy() {
		t.Fatal("cancellation did not release agent ownership")
	}
	persisted, err := messages.List(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 2 || persisted[0].Role != message.User || persisted[1].Role != message.Assistant || persisted[1].FinishReason() != message.FinishReasonCanceled {
		t.Fatalf("canceled lifecycle was not persisted: %+v", persisted)
	}
}

func TestIntegrationMalformedStreamPersistsFailedAssistant(t *testing.T) {
	p := &integrationProvider{model: models.Model{ID: "malformed-model", Provider: models.ProviderMock}}
	p.streams = []integrationStream{func(context.Context, []message.Message, []tools.BaseTool) <-chan provider.ProviderEvent {
		return integrationEventStream(provider.ProviderEvent{Type: provider.EventComplete})
	}}
	a, _, messages, created := newPersistentIntegrationAgent(t, p)

	results, err := a.Run(context.Background(), created.ID, "handle malformed output")
	if err != nil {
		t.Fatal(err)
	}
	result := awaitIntegrationResult(t, results)
	if result.Error == nil || result.Type != AgentEventTypeError {
		t.Fatalf("malformed stream unexpectedly succeeded: %+v", result)
	}
	if got := result.Error.Error(); !strings.Contains(got, "completion is missing a response") {
		t.Fatalf("malformed stream error = %q", got)
	}
	persisted, err := messages.List(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 2 || persisted[1].Role != message.Assistant || persisted[1].FinishReason() != message.FinishReasonError {
		t.Fatalf("failed assistant state was not persisted: %+v", persisted)
	}
}

func TestIntegrationPermissionDenialPersistsToolFailureWithoutMutation(t *testing.T) {
	permissions := permission.NewPermissionService()
	permissionContext, stopPermissions := context.WithCancel(context.Background())
	t.Cleanup(stopPermissions)
	requests := permissions.Subscribe(permissionContext)
	target := filepath.Join(t.TempDir(), "protected.txt")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	var toolCalled atomic.Bool
	var mutated atomic.Bool
	tool := integrationTool{
		info: tools.ToolInfo{Name: "guarded_mutation", Risk: "high", Permissions: []string{"write"}},
		run: func(ctx context.Context, call tools.ToolCall) (tools.ToolResponse, error) {
			toolCalled.Store(true)
			sessionID, _ := tools.GetContextValues(ctx)
			if !permissions.RequestContext(ctx, permission.CreatePermissionRequest{
				SessionID: sessionID,
				ToolName:  call.Name,
				Action:    "write",
				Path:      target,
			}) {
				return tools.NewTextErrorResponse("permission denied"), permission.ErrorPermissionDenied
			}
			mutated.Store(true)
			return tools.NewTextResponse("mutated"), os.WriteFile(target, []byte("changed"), 0o600)
		},
	}
	p := &integrationProvider{model: models.Model{ID: "permission-model", Provider: models.ProviderMock}}
	p.streams = []integrationStream{func(context.Context, []message.Message, []tools.BaseTool) <-chan provider.ProviderEvent {
		return integrationEventStream(provider.ProviderEvent{Type: provider.EventComplete, Response: &provider.ProviderResponse{
			FinishReason: message.FinishReasonToolUse,
			ToolCalls:    []message.ToolCall{{ID: "denied-call", Name: "guarded_mutation", Input: `{}`}},
		}})
	}}
	a, _, messages, created := newPersistentIntegrationAgent(t, p, tool)

	results, err := a.Run(context.Background(), created.ID, "change the protected file")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case request, ok := <-requests:
		if !ok {
			t.Fatal("permission request stream closed")
		}
		if request.Payload.SessionID != created.ID || request.Payload.ToolName != "guarded_mutation" {
			t.Fatalf("unexpected permission request: %+v", request.Payload)
		}
		permissions.Deny(request.Payload)
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not request permission")
	}
	result := awaitIntegrationResult(t, results)
	if !errors.Is(result.Error, permission.ErrorPermissionDenied) {
		t.Fatalf("permission denial result = %+v", result)
	}
	if !toolCalled.Load() || mutated.Load() {
		t.Fatalf("tool called=%v mutated=%v", toolCalled.Load(), mutated.Load())
	}
	content, err := os.ReadFile(target)
	if err != nil || string(content) != "original" {
		t.Fatalf("denied mutation changed target: content=%q err=%v", content, err)
	}
	persisted, err := messages.List(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted) != 3 || persisted[1].FinishReason() != message.FinishReasonPermissionDenied {
		t.Fatalf("permission-denied lifecycle was not persisted: %+v", persisted)
	}
	toolResults := persisted[2].ToolResults()
	if len(toolResults) != 1 || !toolResults[0].IsError || toolResults[0].ToolCallID != "denied-call" {
		t.Fatalf("permission-denied tool result was not persisted: %+v", persisted[2])
	}
	calls, _ := p.snapshot()
	if calls != 1 {
		t.Fatalf("provider was called %d times after terminal permission denial", calls)
	}
}
