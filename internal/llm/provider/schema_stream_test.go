package provider

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/llm/models"
	toolsPkg "github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/message"
	"google.golang.org/genai"
)

type schemaTestTool struct {
	info toolsPkg.ToolInfo
}

func (t schemaTestTool) Info() toolsPkg.ToolInfo { return t.info }
func (schemaTestTool) Run(context.Context, toolsPkg.ToolCall) (toolsPkg.ToolResponse, error) {
	return toolsPkg.ToolResponse{}, nil
}

func richSchemaTool() schemaTestTool {
	return schemaTestTool{info: toolsPkg.ToolInfo{
		Name:        "inspect",
		Description: "Inspect a target",
		Required:    []string{"mode", "options"},
		Parameters: map[string]any{
			"mode": map[string]any{
				"type":      "string",
				"enum":      []any{"safe", "fast"},
				"minLength": float64(1),
			},
			"options": map[string]any{
				"type":     "object",
				"required": []any{"depth"},
				"properties": map[string]any{
					"depth": map[string]any{"type": "integer", "minimum": float64(1), "maximum": float64(3)},
				},
			},
		},
		OutputSchema: map[string]any{
			"type":     "object",
			"required": []any{"summary"},
			"properties": map[string]any{
				"summary": map[string]any{"type": "string", "maxLength": float64(200)},
			},
		},
	}}
}

func TestAnthropicToolSchemaPreservesRequiredAndNestedConstraints(t *testing.T) {
	client := &anthropicClient{}
	converted := client.convertTools([]toolsPkg.BaseTool{richSchemaTool()})
	if len(converted) != 1 || converted[0].OfTool == nil {
		t.Fatalf("unexpected converted tools: %+v", converted)
	}
	schema := converted[0].OfTool.InputSchema
	if strings.Join(schema.Required, ",") != "mode,options" {
		t.Fatalf("required fields were not preserved: %v", schema.Required)
	}
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"enum":["safe","fast"]`, `"required":["depth"]`, `"minimum":1`, `"maximum":3`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("schema lost %s: %s", expected, encoded)
		}
	}
}

func TestAnthropicDisableCacheAppliesToSystemBlock(t *testing.T) {
	client := &anthropicClient{providerOptions: providerClientOptions{maxTokens: 100}, options: anthropicOptions{disableCache: true}}
	params := client.preparedMessages(client.convertMessages([]message.Message{{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "hello"}}}}), nil)
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "cache_control") {
		t.Fatalf("cache control remained enabled: %s", encoded)
	}
}

func TestAnthropicThinkingDeltaSupportsExistingAndNewConsumers(t *testing.T) {
	event := anthropicThinkingDelta("reasoning")
	if event.Type != EventThinkingDelta || event.Content != "reasoning" || event.Thinking != "reasoning" {
		t.Fatalf("unexpected thinking event: %+v", event)
	}
}

func TestGeminiSchemaPreservesEnumsNestedConstraintsAndOutput(t *testing.T) {
	client := &geminiClient{}
	converted := client.convertTools([]toolsPkg.BaseTool{richSchemaTool()})
	declaration := converted[0].FunctionDeclarations[0]
	if got := declaration.Parameters.Required; strings.Join(got, ",") != "mode,options" {
		t.Fatalf("top-level required fields were not preserved: %v", got)
	}
	mode := declaration.Parameters.Properties["mode"]
	if strings.Join(mode.Enum, ",") != "safe,fast" || mode.MinLength == nil || *mode.MinLength != 1 {
		t.Fatalf("enum/string constraints were not preserved: %+v", mode)
	}
	depth := declaration.Parameters.Properties["options"].Properties["depth"]
	if depth.Minimum == nil || *depth.Minimum != 1 || depth.Maximum == nil || *depth.Maximum != 3 {
		t.Fatalf("nested numeric constraints were not preserved: %+v", depth)
	}
	if declaration.Response == nil || strings.Join(declaration.Response.Required, ",") != "summary" {
		t.Fatalf("output schema was not preserved: %+v", declaration.Response)
	}
	summary := declaration.Response.Properties["summary"]
	if summary.MaxLength == nil || *summary.MaxLength != 200 {
		t.Fatalf("output schema constraints were not preserved: %+v", summary)
	}
}

func TestToolResultNativeMediaProviderContracts(t *testing.T) {
	toolMessage := message.Message{Role: message.Tool, Parts: []message.ContentPart{message.ToolResult{
		ToolCallID: "call-1",
		Name:       "browser",
		Content:    "visual fallback",
		Media:      []message.BinaryContent{{MIMEType: "image/png", Data: []byte("png")}},
	}}}

	t.Run("OpenAI compatible providers append native image after tool text", func(t *testing.T) {
		clients := []struct {
			name    string
			convert func([]message.Message) any
		}{
			{name: "openai", convert: func(messages []message.Message) any {
				client := &openaiClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: true}}}
				return client.convertMessages(messages)
			}},
			{name: "copilot", convert: func(messages []message.Message) any {
				client := &copilotClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: true}}}
				return client.convertMessages(messages)
			}},
		}
		for _, tc := range clients {
			t.Run(tc.name, func(t *testing.T) {
				encoded, err := json.Marshal(tc.convert([]message.Message{toolMessage}))
				if err != nil {
					t.Fatal(err)
				}
				body := string(encoded)
				for _, expected := range []string{`"role":"tool"`, `"role":"user"`, "visual fallback", "data:image/png;base64,cG5n", "call-1"} {
					if !strings.Contains(body, expected) {
						t.Fatalf("%s conversion lacks %q: %s", tc.name, expected, body)
					}
				}
			})
		}
	})

	t.Run("OpenAI compatible providers keep parallel tool responses contiguous", func(t *testing.T) {
		parallel := message.Message{Role: message.Tool, Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call-1", Name: "browser", Content: "first", Media: []message.BinaryContent{{MIMEType: "image/png", Data: []byte("one")}}},
			message.ToolResult{ToolCallID: "call-2", Name: "browser", Content: "second", Media: []message.BinaryContent{{MIMEType: "image/png", Data: []byte("two")}}},
		}}
		client := &openaiClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: true}}}
		encoded, err := json.Marshal(client.convertMessages([]message.Message{parallel}))
		if err != nil {
			t.Fatal(err)
		}
		var converted []struct {
			Role string `json:"role"`
		}
		if err := json.Unmarshal(encoded, &converted); err != nil {
			t.Fatal(err)
		}
		wantRoles := []string{"system", "tool", "tool", "user", "user"}
		if len(converted) != len(wantRoles) {
			t.Fatalf("converted message count = %d, want %d: %s", len(converted), len(wantRoles), encoded)
		}
		for i, want := range wantRoles {
			if converted[i].Role != want {
				t.Fatalf("message %d role = %q, want %q: %s", i, converted[i].Role, want, encoded)
			}
		}
	})

	t.Run("Anthropic nests image inside tool result", func(t *testing.T) {
		client := &anthropicClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: true}}}
		converted := client.convertMessages([]message.Message{toolMessage})
		if len(converted) != 1 || len(converted[0].Content) != 1 || converted[0].Content[0].OfToolResult == nil {
			t.Fatalf("unexpected Anthropic conversion: %+v", converted)
		}
		result := converted[0].Content[0].OfToolResult
		if len(result.Content) != 2 || result.Content[0].OfText == nil || result.Content[1].OfImage == nil {
			t.Fatalf("Anthropic tool result did not contain text and image: %+v", result.Content)
		}
		encoded, err := json.Marshal(converted)
		if err != nil || !strings.Contains(string(encoded), `"data":"cG5n"`) {
			t.Fatalf("Anthropic image payload missing: %s (%v)", encoded, err)
		}
	})

	t.Run("Gemini follows function response with inline image", func(t *testing.T) {
		client := &geminiClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: true}}}
		converted := client.convertMessages([]message.Message{toolMessage})
		if len(converted) != 1 || converted[0].Role != "function" || len(converted[0].Parts) != 2 || converted[0].Parts[0].FunctionResponse == nil || converted[0].Parts[1].InlineData == nil {
			t.Fatalf("unexpected Gemini conversion: %+v", converted)
		}
		if converted[0].Parts[1].InlineData.MIMEType != "image/png" || string(converted[0].Parts[1].InlineData.Data) != "png" {
			t.Fatalf("Gemini image payload changed: %+v", converted[0].Parts[1].InlineData)
		}
	})

	t.Run("Non vision models receive only textual fallback", func(t *testing.T) {
		openAICompatible := []struct {
			name    string
			convert func([]message.Message) any
		}{
			{name: "openai", convert: func(messages []message.Message) any {
				client := &openaiClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: false}}}
				return client.convertMessages(messages)
			}},
			{name: "copilot", convert: func(messages []message.Message) any {
				client := &copilotClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: false}}}
				return client.convertMessages(messages)
			}},
		}
		for _, tc := range openAICompatible {
			encoded, err := json.Marshal(tc.convert([]message.Message{toolMessage}))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), "visual fallback") || strings.Contains(string(encoded), "data:image/png") {
				t.Fatalf("%s non-vision fallback contract violated: %s", tc.name, encoded)
			}
		}

		anthropicClient := &anthropicClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: false}}}
		anthropicMessages := anthropicClient.convertMessages([]message.Message{toolMessage})
		if got := anthropicMessages[0].Content[0].OfToolResult.Content; len(got) != 1 || got[0].OfText == nil || got[0].OfImage != nil {
			t.Fatalf("Anthropic non-vision fallback contract violated: %+v", got)
		}

		geminiClient := &geminiClient{providerOptions: providerClientOptions{model: models.Model{SupportsAttachments: false}}}
		geminiMessages := geminiClient.convertMessages([]message.Message{toolMessage})
		if got := geminiMessages[0].Parts; len(got) != 1 || got[0].FunctionResponse == nil || got[0].InlineData != nil {
			t.Fatalf("Gemini non-vision fallback contract violated: %+v", got)
		}
	})
}

func TestGeminiStreamRetriesNilRateLimitResponseBeforeEmission(t *testing.T) {
	attempts := 0
	client := &geminiClient{retryWait: func(context.Context, time.Duration) error { return nil }}
	next := func() iter.Seq2[*genai.GenerateContentResponse, error] {
		attempts++
		return func(yield func(*genai.GenerateContentResponse, error) bool) {
			if attempts == 1 {
				yield(nil, errors.New("rate limit exceeded"))
				return
			}
			yield(geminiTextResponse("complete"), nil)
		}
	}
	events := collectProviderEvents(client.streamFrom(context.Background(), next))
	if attempts != 2 {
		t.Fatalf("expected two attempts, got %d", attempts)
	}
	if len(events) != 4 || events[0].Type != EventContentStart || events[1].Content != "complete" || events[2].Type != EventContentStop || events[3].Type != EventComplete {
		t.Fatalf("unexpected events: %+v", events)
	}
}

func TestGeminiStreamEndsWithErrorWithoutResponse(t *testing.T) {
	client := &geminiClient{}
	next := func() iter.Seq2[*genai.GenerateContentResponse, error] {
		return func(func(*genai.GenerateContentResponse, error) bool) {}
	}
	events := collectProviderEvents(client.streamFrom(context.Background(), next))
	if len(events) != 1 || events[0].Type != EventError || events[0].Error == nil {
		t.Fatalf("expected one terminal error, got %+v", events)
	}
}

func TestGeminiStreamDoesNotRetryAfterPartialOutput(t *testing.T) {
	attempts := 0
	client := &geminiClient{retryWait: func(context.Context, time.Duration) error { return nil }}
	next := func() iter.Seq2[*genai.GenerateContentResponse, error] {
		attempts++
		return func(yield func(*genai.GenerateContentResponse, error) bool) {
			if !yield(geminiTextResponse("partial"), nil) {
				return
			}
			yield(nil, errors.New("rate limit exceeded"))
		}
	}
	events := collectProviderEvents(client.streamFrom(context.Background(), next))
	if attempts != 1 || len(events) != 3 || events[2].Type != EventError {
		t.Fatalf("partial output must fail rather than be duplicated by retry: attempts=%d events=%+v", attempts, events)
	}
}

func geminiTextResponse(text string) *genai.GenerateContentResponse {
	return &genai.GenerateContentResponse{Candidates: []*genai.Candidate{{
		Content:      &genai.Content{Parts: []*genai.Part{{Text: text}}},
		FinishReason: genai.FinishReasonStop,
	}}}
}

func collectProviderEvents(stream <-chan ProviderEvent) []ProviderEvent {
	var events []ProviderEvent
	for event := range stream {
		events = append(events, event)
	}
	return events
}
