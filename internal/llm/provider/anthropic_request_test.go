package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
)

func TestAnthropicRejectsEmptyConvertedHistory(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []message.Message
	}{
		{name: "empty history"},
		{name: "empty message", messages: []message.Message{{Role: message.User}}},
		{name: "system message only", messages: []message.Message{{Role: message.System, Parts: []message.ContentPart{message.TextContent{Text: "system"}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise baseProvider's cleaning as well as Anthropic's role conversion.
			service, err := NewProvider(models.ProviderAnthropic)
			if err != nil {
				t.Fatal(err)
			}
			response, err := service.SendMessages(context.Background(), tc.messages, nil)
			if response != nil || err == nil || !strings.Contains(err.Error(), "no messages") {
				t.Fatalf("expected empty history error, response=%+v err=%v", response, err)
			}
			events := collectProviderEvents(service.StreamResponse(context.Background(), tc.messages, nil))
			if len(events) != 1 || events[0].Type != EventError || events[0].Error == nil || !strings.Contains(events[0].Error.Error(), "no messages") {
				t.Fatalf("expected one empty history error event, got %+v", events)
			}
		})
	}
}
