package agent

import (
	"context"
	"math"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestRunPreservesCompletionResponseText(t *testing.T) {
	for _, test := range []struct {
		name, delta, final, want string
	}{
		{name: "completion only", final: "final answer", want: "final answer"},
		{name: "stream and completion", delta: "final answer", final: "final answer", want: "final answer"},
		{name: "stream only", delta: "streamed answer", want: "streamed answer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := &testProvider{
				model: models.Model{ID: "response-model"},
				stream: func(context.Context, []message.Message, []tools.BaseTool) <-chan provider.ProviderEvent {
					events := make(chan provider.ProviderEvent, 2)
					if test.delta != "" {
						events <- provider.ProviderEvent{Type: provider.EventContentDelta, Content: test.delta}
					}
					events <- provider.ProviderEvent{Type: provider.EventComplete, Response: &provider.ProviderResponse{
						Content: test.final, FinishReason: message.FinishReasonEndTurn,
					}}
					close(events)
					return events
				},
			}
			a := newTestAgent(p)
			defer a.Shutdown()
			results, err := a.Run(context.Background(), "session-1", "hello")
			if err != nil {
				t.Fatal(err)
			}
			result := awaitAgentEvent(t, results)
			if result.Error != nil || result.Message.Content().Text != test.want {
				t.Fatalf("result text = %q, error = %v; want %q", result.Message.Content().Text, result.Error, test.want)
			}
			stored, err := a.messages.Get(context.Background(), result.Message.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Content().Text != test.want {
				t.Fatalf("persisted text = %q, want %q", stored.Content().Text, test.want)
			}
		})
	}
}

func TestTrackUsageCountsCachedReadsAsPromptTokens(t *testing.T) {
	a := newTestAgent(&testProvider{model: models.Model{ID: "usage-model"}})
	defer a.Shutdown()
	ctx := context.Background()
	stored, err := a.sessions.Get(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	stored.PromptTokens, stored.CompletionTokens = 7, 11
	if _, err := a.sessions.Save(ctx, stored); err != nil {
		t.Fatal(err)
	}
	model := models.Model{
		ID: "usage-model", CostPer1MIn: 1, CostPer1MOut: 2,
		CostPer1MInCached: 3, CostPer1MOutCached: 4,
	}
	if err := a.TrackUsage(ctx, "session-1", model, provider.TokenUsage{
		InputTokens: 10, OutputTokens: 4, CacheCreationTokens: 20, CacheReadTokens: 30,
	}); err != nil {
		t.Fatal(err)
	}
	stored, err = a.sessions.Get(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PromptTokens != 67 || stored.CompletionTokens != 15 {
		t.Errorf("session token counts = input %d/output %d, want input 67/output 15", stored.PromptTokens, stored.CompletionTokens)
	}
	if math.Abs(stored.Cost-0.000198) > 1e-12 {
		t.Errorf("session cost = %g, want 0.000198", stored.Cost)
	}
	usage, err := a.sessions.Usage(ctx, "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(usage) != 1 || usage[0].InputTokens != 60 || usage[0].OutputTokens != 4 {
		t.Fatalf("detailed usage = %+v, want input 60/output 4", usage)
	}
}
