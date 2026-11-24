package provider

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"

	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
	"google.golang.org/genai"
)

func TestGeminiSendPreservesAllTextParts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"candidates":[{"content":{"role":"model","parts":[{"text":"First paragraph. "},{"functionCall":{"name":"inspect","args":{"path":"main.go"}}},{"text":"Second paragraph."}]},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	api, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey: "test-key", Backend: genai.BackendGeminiAPI,
		HTTPClient: server.Client(), HTTPOptions: genai.HTTPOptions{BaseURL: server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	client := &geminiClient{
		client:          api,
		providerOptions: providerClientOptions{model: models.Model{APIModel: "test-model"}, maxTokens: 100},
	}
	response, err := client.send(context.Background(), []message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "inspect"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.Content != "First paragraph. Second paragraph." {
		t.Fatalf("lost text in multipart response: %q", response.Content)
	}
	if response.FinishReason != message.FinishReasonToolUse || len(response.ToolCalls) != 1 || response.ToolCalls[0].Name != "inspect" {
		t.Fatalf("tool call was not preserved: %+v", response)
	}
}

func TestGeminiStreamCancellationClosesWithoutConsumer(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		client := &geminiClient{}
		output := client.streamFrom(ctx, func() iter.Seq2[*genai.GenerateContentResponse, error] {
			return func(yield func(*genai.GenerateContentResponse, error) bool) {
				yield(geminiTextResponse("text"), nil)
			}
		})
		synctest.Wait()
		cancel()
		synctest.Wait()
		assertCanceledStreamClosed(t, output)
	})
}

func TestGeminiUsageSeparatesCachedPromptTokens(t *testing.T) {
	client := &geminiClient{}
	for _, cached := range []int32{0, 40, 100} {
		response := &genai.GenerateContentResponse{UsageMetadata: &genai.GenerateContentResponseUsageMetadata{
			PromptTokenCount:        100,
			CachedContentTokenCount: cached,
			CandidatesTokenCount:    12,
		}}
		usage := client.usage(response)
		if usage.InputTokens != 100-int64(cached) || usage.CacheReadTokens != int64(cached) || usage.OutputTokens != 12 || usage.CacheCreationTokens != 0 {
			t.Fatalf("cached=%d: unexpected normalized usage %+v", cached, usage)
		}
		if usage.InputTokens+usage.CacheCreationTokens+usage.CacheReadTokens != 100 {
			t.Fatalf("cached=%d: prompt tokens counted more than once: %+v", cached, usage)
		}
	}
}
