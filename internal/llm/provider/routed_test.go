package provider

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
)

type routedTestProvider struct {
	model models.Model
}

func (p routedTestProvider) Model() models.Model { return p.model }
func (p routedTestProvider) SendMessages(context.Context, []message.Message, []tools.BaseTool) (*ProviderResponse, error) {
	return &ProviderResponse{Content: string(p.model.ID)}, nil
}
func (p routedTestProvider) StreamResponse(context.Context, []message.Message, []tools.BaseTool) <-chan ProviderEvent {
	channel := make(chan ProviderEvent, 2)
	channel <- ProviderEvent{Type: EventContentDelta, Content: string(p.model.ID)}
	channel <- ProviderEvent{Type: EventComplete, Model: p.model, Response: &ProviderResponse{Content: string(p.model.ID)}}
	close(channel)
	return channel
}

func TestRoutedProviderSelectsVisionCandidate(t *testing.T) {
	plain := routedTestProvider{model: models.Model{ID: "plain", ContextWindow: 32_000}}
	vision := routedTestProvider{model: models.Model{ID: "vision", SupportsAttachments: true}}
	routed, err := NewRoutedProvider(plain, vision)
	if err != nil {
		t.Fatal(err)
	}
	response, err := routed.SendMessages(context.Background(), []message.Message{{Parts: []message.ContentPart{message.BinaryContent{MIMEType: "image/png", Data: []byte("x")}}}}, nil)
	if err != nil || response.Content != "vision" {
		t.Fatalf("expected vision provider, response=%+v err=%v", response, err)
	}
}

func TestRoutedProviderSelectsVisionCandidateForToolResultMedia(t *testing.T) {
	plain := routedTestProvider{model: models.Model{ID: "plain", ContextWindow: 32_000}}
	vision := routedTestProvider{model: models.Model{ID: "vision", SupportsAttachments: true}}
	routed, err := NewRoutedProvider(plain, vision)
	if err != nil {
		t.Fatal(err)
	}
	messages := []message.Message{{
		Role: message.Tool,
		Parts: []message.ContentPart{message.ToolResult{
			ToolCallID: "browser-call",
			Name:       "browser",
			Content:    "Screenshot captured",
			Media:      []message.BinaryContent{{MIMEType: "image/png", Data: []byte("png")}},
		}},
	}}

	response, err := routed.SendMessages(context.Background(), messages, nil)
	if err != nil || response.Content != "vision" {
		t.Fatalf("expected tool-result media to select vision provider, response=%+v err=%v", response, err)
	}
}

func TestRoutedProviderReturnsErrorWithoutCandidates(t *testing.T) {
	routed, err := NewRoutedProvider(nil)
	if err == nil || routed != nil {
		t.Fatal("expected missing primary error")
	}
}

func TestRoutedProviderKeepsCandidateIdentityForDuplicateModelIDs(t *testing.T) {
	for _, providerName := range []models.ModelProvider{"different-provider", "same-provider"} {
		t.Run(string(providerName), func(t *testing.T) {
			vision := fallbackTestProvider{
				model: models.Model{ID: "shared", Provider: "same-provider", SupportsAttachments: true},
				send:  &ProviderResponse{Content: "vision"},
			}
			plain := fallbackTestProvider{
				model: models.Model{ID: "shared", Provider: providerName, SupportsAttachments: false},
				send:  &ProviderResponse{Content: "plain"},
			}
			service, err := NewRoutedProvider(vision, plain)
			if err != nil {
				t.Fatal(err)
			}
			response, err := service.SendMessages(context.Background(), []message.Message{{Role: message.User, Parts: []message.ContentPart{message.BinaryContent{MIMEType: "image/png", Data: []byte("png")}}}}, nil)
			if err != nil || response == nil || response.Content != "vision" {
				t.Fatalf("routing substituted an ineligible provider with the same ID: response=%+v err=%v", response, err)
			}
		})
	}
}

func TestRoutedProviderCancellationClosesWithoutConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		service, err := NewRoutedProvider(routedTestProvider{model: models.Model{ID: "selected"}})
		if err != nil {
			t.Fatal(err)
		}
		output := service.StreamResponse(ctx, nil, nil)
		synctest.Wait()
		cancel()
		synctest.Wait()
		assertCanceledStreamClosed(t, output)
	})
}
