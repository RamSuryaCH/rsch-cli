package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"google.golang.org/genai"
)

type geminiOptions struct {
	disableCache bool
}

type GeminiOption func(*geminiOptions)

type geminiClient struct {
	providerOptions providerClientOptions
	options         geminiOptions
	client          *genai.Client
	retryWait       func(context.Context, time.Duration) error
}

type GeminiClient ProviderClient

func newGeminiClient(opts providerClientOptions) (GeminiClient, error) {
	geminiOpts := geminiOptions{}
	for _, o := range opts.geminiOptions {
		o(&geminiOpts)
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{APIKey: opts.apiKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return nil, fmt.Errorf("initialize Gemini provider: %w", err)
	}

	return &geminiClient{
		providerOptions: opts,
		options:         geminiOpts,
		client:          client,
	}, nil
}

func (g *geminiClient) convertMessages(messages []message.Message) []*genai.Content {
	var history []*genai.Content
	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var parts []*genai.Part
			parts = append(parts, &genai.Part{Text: msg.Content().String()})
			for _, binaryContent := range msg.BinaryContent() {
				parts = append(parts, &genai.Part{InlineData: &genai.Blob{
					MIMEType: binaryContent.MIMEType,
					Data:     binaryContent.Data,
				}})
			}
			history = append(history, &genai.Content{
				Parts: parts,
				Role:  "user",
			})
		case message.Assistant:
			var assistantParts []*genai.Part

			if msg.Content().String() != "" {
				assistantParts = append(assistantParts, &genai.Part{Text: msg.Content().String()})
			}

			if len(msg.ToolCalls()) > 0 {
				for _, call := range msg.ToolCalls() {
					args, _ := parseJsonToMap(call.Input)
					assistantParts = append(assistantParts, &genai.Part{
						FunctionCall: &genai.FunctionCall{
							Name: call.Name,
							Args: args,
						},
					})
				}
			}

			if len(assistantParts) > 0 {
				history = append(history, &genai.Content{
					Role:  "model",
					Parts: assistantParts,
				})
			}

		case message.Tool:
			for _, result := range msg.ToolResults() {
				response := map[string]interface{}{"result": result.Content}
				parsed, err := parseJsonToMap(result.Content)
				if err == nil {
					response = parsed
				}

				var toolCall message.ToolCall
				for _, m := range messages {
					if m.Role == message.Assistant {
						for _, call := range m.ToolCalls() {
							if call.ID == result.ToolCallID {
								toolCall = call
								break
							}
						}
					}
				}

				toolName := result.Name
				if toolName == "" {
					toolName = toolCall.Name
				}
				parts := []*genai.Part{{FunctionResponse: &genai.FunctionResponse{
					Name:     toolName,
					Response: response,
				}}}
				if g.providerOptions.model.SupportsAttachments {
					remaining := tools.MaxToolMediaBytes
					for _, media := range result.Media {
						if media.MIMEType == "" || len(media.Data) == 0 || len(media.Data) > remaining {
							continue
						}
						parts = append(parts, &genai.Part{InlineData: &genai.Blob{MIMEType: media.MIMEType, Data: append([]byte(nil), media.Data...)}})
						remaining -= len(media.Data)
					}
				}

				history = append(history, &genai.Content{
					Parts: parts,
					Role:  "function",
				})
			}
		}
	}

	return history
}

func (g *geminiClient) convertTools(tools []tools.BaseTool) []*genai.Tool {
	geminiTool := &genai.Tool{}
	geminiTool.FunctionDeclarations = make([]*genai.FunctionDeclaration, 0, len(tools))

	for _, tool := range tools {
		info := tool.Info()
		declaration := &genai.FunctionDeclaration{
			Name:        info.Name,
			Description: info.Description,
			Parameters: &genai.Schema{
				Type:       genai.TypeObject,
				Properties: convertSchemaProperties(info.Parameters),
				Required:   info.Required,
			},
		}
		if len(info.OutputSchema) > 0 {
			declaration.Response = convertToSchema(info.OutputSchema)
		}

		geminiTool.FunctionDeclarations = append(geminiTool.FunctionDeclarations, declaration)
	}

	return []*genai.Tool{geminiTool}
}

func (g *geminiClient) finishReason(reason genai.FinishReason) message.FinishReason {
	switch {
	case reason == genai.FinishReasonStop:
		return message.FinishReasonEndTurn
	case reason == genai.FinishReasonMaxTokens:
		return message.FinishReasonMaxTokens
	default:
		return message.FinishReasonUnknown
	}
}

func (g *geminiClient) send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (*ProviderResponse, error) {
	if g == nil || g.client == nil {
		return nil, errors.New("gemini client is unavailable")
	}
	// Convert messages
	geminiMessages := g.convertMessages(messages)
	if len(geminiMessages) == 0 {
		return nil, errors.New("gemini request has no messages")
	}

	cfg := config.Get()
	if cfg != nil && cfg.Debug {
		jsonData, _ := json.Marshal(geminiMessages)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	history := geminiMessages[:len(geminiMessages)-1] // All but last message
	lastMsg := geminiMessages[len(geminiMessages)-1]
	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(g.providerOptions.maxTokens),
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: g.providerOptions.systemMessage}},
		},
	}
	if len(tools) > 0 {
		config.Tools = g.convertTools(tools)
	}
	chat, err := g.client.Chats.Create(ctx, g.providerOptions.model.APIModel, config, history)
	if err != nil {
		return nil, fmt.Errorf("create gemini chat: %w", err)
	}

	attempts := 0
	for {
		attempts++
		var toolCalls []message.ToolCall

		var lastMsgParts []genai.Part
		for _, part := range lastMsg.Parts {
			lastMsgParts = append(lastMsgParts, *part)
		}
		resp, err := chat.SendMessage(ctx, lastMsgParts...)
		// If there is an error we are going to see if we can retry the call
		if err != nil {
			retry, after, retryErr := g.shouldRetry(attempts, err)
			if retryErr != nil {
				return nil, retryErr
			}
			if retry {
				logging.WarnPersist(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, maxRetries), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
			}
			return nil, retryErr
		}

		content := ""

		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			for _, part := range resp.Candidates[0].Content.Parts {
				switch {
				case part.Text != "":
					content += part.Text
				case part.FunctionCall != nil:
					id := "call_" + uuid.New().String()
					args, _ := json.Marshal(part.FunctionCall.Args)
					toolCalls = append(toolCalls, message.ToolCall{
						ID:       id,
						Name:     part.FunctionCall.Name,
						Input:    string(args),
						Type:     "function",
						Finished: true,
					})
				}
			}
		}
		finishReason := message.FinishReasonEndTurn
		if len(resp.Candidates) > 0 {
			finishReason = g.finishReason(resp.Candidates[0].FinishReason)
		}
		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		return &ProviderResponse{
			Content:      content,
			ToolCalls:    toolCalls,
			Usage:        g.usage(resp),
			FinishReason: finishReason,
		}, nil
	}
}

func (g *geminiClient) stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	if g == nil || g.client == nil {
		return geminiErrorStream(errors.New("gemini client is unavailable"))
	}
	// Convert messages
	geminiMessages := g.convertMessages(messages)
	if len(geminiMessages) == 0 {
		return geminiErrorStream(errors.New("gemini request has no messages"))
	}

	cfg := config.Get()
	if cfg != nil && cfg.Debug {
		jsonData, _ := json.Marshal(geminiMessages)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	history := geminiMessages[:len(geminiMessages)-1] // All but last message
	lastMsg := geminiMessages[len(geminiMessages)-1]
	config := &genai.GenerateContentConfig{
		MaxOutputTokens: int32(g.providerOptions.maxTokens),
		SystemInstruction: &genai.Content{
			Parts: []*genai.Part{{Text: g.providerOptions.systemMessage}},
		},
	}
	if len(tools) > 0 {
		config.Tools = g.convertTools(tools)
	}
	chat, err := g.client.Chats.Create(ctx, g.providerOptions.model.APIModel, config, history)
	if err != nil {
		return geminiErrorStream(fmt.Errorf("create gemini chat: %w", err))
	}
	lastMsgParts := make([]genai.Part, 0, len(lastMsg.Parts))
	for _, part := range lastMsg.Parts {
		lastMsgParts = append(lastMsgParts, *part)
	}
	return g.streamFrom(ctx, func() iter.Seq2[*genai.GenerateContentResponse, error] {
		return chat.SendMessageStream(ctx, lastMsgParts...)
	})
}

func geminiErrorStream(err error) <-chan ProviderEvent {
	events := make(chan ProviderEvent, 1)
	events <- ProviderEvent{Type: EventError, Error: err}
	close(events)
	return events
}

func (g *geminiClient) streamFrom(ctx context.Context, next func() iter.Seq2[*genai.GenerateContentResponse, error]) <-chan ProviderEvent {
	eventChan := make(chan ProviderEvent)
	go func() {
		defer close(eventChan)
		for attempts := 1; ; attempts++ {
			if ctx.Err() != nil {
				return
			}
			currentContent := ""
			toolCalls := []message.ToolCall{}
			var finalResp *genai.GenerateContentResponse
			emitted := false
			retryAttempt := false

			for resp, streamErr := range next() {
				if ctx.Err() != nil {
					return
				}
				if streamErr != nil {
					retry, after, retryErr := g.shouldRetry(attempts, streamErr)
					if retry && !emitted {
						logging.WarnPersist(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, maxRetries), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
						if waitErr := g.waitForRetry(ctx, time.Duration(after)*time.Millisecond); waitErr != nil {
							sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventError, Error: waitErr})
							return
						}
						retryAttempt = true
						break
					}
					if retryErr == nil {
						retryErr = streamErr
					}
					sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventError, Error: retryErr})
					return
				}
				if resp == nil {
					sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventError, Error: errors.New("gemini stream returned a nil response")})
					return
				}
				if !emitted {
					if !sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventContentStart}) {
						return
					}
					emitted = true
				}
				finalResp = resp
				if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
					continue
				}
				for _, part := range resp.Candidates[0].Content.Parts {
					switch {
					case part.Text != "":
						delta := string(part.Text)
						if !sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventContentDelta, Content: delta}) {
							return
						}
						currentContent += delta
					case part.FunctionCall != nil:
						args, _ := json.Marshal(part.FunctionCall.Args)
						newCall := message.ToolCall{ID: "call_" + uuid.New().String(), Name: part.FunctionCall.Name, Input: string(args), Type: "function", Finished: true}
						isNew := true
						for _, existing := range toolCalls {
							if existing.Name == newCall.Name && existing.Input == newCall.Input {
								isNew = false
								break
							}
						}
						if isNew {
							toolCalls = append(toolCalls, newCall)
						}
					}
				}
			}

			if retryAttempt {
				continue
			}
			if finalResp == nil {
				sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventError, Error: errors.New("gemini stream ended without a response")})
				return
			}

			finishReason := message.FinishReasonEndTurn
			if len(finalResp.Candidates) > 0 {
				finishReason = g.finishReason(finalResp.Candidates[0].FinishReason)
			}
			if len(toolCalls) > 0 {
				finishReason = message.FinishReasonToolUse
			}
			if !sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventContentStop}) {
				return
			}
			sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventComplete, Response: &ProviderResponse{
				Content: currentContent, ToolCalls: toolCalls, Usage: g.usage(finalResp), FinishReason: finishReason,
			}})
			return
		}
	}()
	return eventChan
}

func (g *geminiClient) waitForRetry(ctx context.Context, delay time.Duration) error {
	if g.retryWait != nil {
		return g.retryWait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (g *geminiClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	// Check if error is a rate limit error
	if attempts > maxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", maxRetries)
	}

	// Gemini doesn't have a standard error type we can check against
	// So we'll check the error message for rate limit indicators
	if errors.Is(err, io.EOF) {
		return false, 0, err
	}

	errMsg := err.Error()
	isRateLimit := false

	// Check for common rate limit error messages
	if contains(errMsg, "rate limit", "quota exceeded", "too many requests") {
		isRateLimit = true
	}

	if !isRateLimit {
		return false, 0, err
	}

	// Calculate backoff with jitter
	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	retryMs := backoffMs + jitterMs

	return true, int64(retryMs), nil
}

func (g *geminiClient) toolCalls(resp *genai.GenerateContentResponse) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
		for _, part := range resp.Candidates[0].Content.Parts {
			if part.FunctionCall != nil {
				id := "call_" + uuid.New().String()
				args, _ := json.Marshal(part.FunctionCall.Args)
				toolCalls = append(toolCalls, message.ToolCall{
					ID:    id,
					Name:  part.FunctionCall.Name,
					Input: string(args),
					Type:  "function",
				})
			}
		}
	}

	return toolCalls
}

func (g *geminiClient) usage(resp *genai.GenerateContentResponse) TokenUsage {
	if resp == nil || resp.UsageMetadata == nil {
		return TokenUsage{}
	}

	// Gemini includes cached input in PromptTokenCount. Keep the categories
	// disjoint so prompt accounting and cached-token pricing count it once.
	cachedTokens := int64(resp.UsageMetadata.CachedContentTokenCount)
	return TokenUsage{
		InputTokens:         int64(resp.UsageMetadata.PromptTokenCount) - cachedTokens,
		OutputTokens:        int64(resp.UsageMetadata.CandidatesTokenCount),
		CacheCreationTokens: 0, // Not directly provided by Gemini
		CacheReadTokens:     cachedTokens,
	}
}

func WithGeminiDisableCache() GeminiOption {
	return func(options *geminiOptions) {
		options.disableCache = true
	}
}

// Helper functions
func parseJsonToMap(jsonStr string) (map[string]interface{}, error) {
	var result map[string]interface{}
	err := json.Unmarshal([]byte(jsonStr), &result)
	return result, err
}

func convertSchemaProperties(parameters map[string]interface{}) map[string]*genai.Schema {
	properties := make(map[string]*genai.Schema)

	for name, param := range parameters {
		properties[name] = convertToSchema(param)
	}

	return properties
}

func convertToSchema(param interface{}) *genai.Schema {
	schema := &genai.Schema{Type: genai.TypeString}

	paramMap, ok := param.(map[string]interface{})
	if !ok {
		return schema
	}

	if desc, ok := paramMap["description"].(string); ok {
		schema.Description = desc
	}
	if title, ok := paramMap["title"].(string); ok {
		schema.Title = title
	}
	if format, ok := paramMap["format"].(string); ok {
		schema.Format = format
	}
	if pattern, ok := paramMap["pattern"].(string); ok {
		schema.Pattern = pattern
	}
	if nullable, ok := paramMap["nullable"].(bool); ok {
		schema.Nullable = &nullable
	}
	if value, ok := paramMap["default"]; ok {
		schema.Default = value
	}
	if value, ok := paramMap["example"]; ok {
		schema.Example = value
	}
	schema.Enum = schemaStringSlice(paramMap["enum"])
	schema.Required = schemaStringSlice(paramMap["required"])
	schema.PropertyOrdering = schemaStringSlice(paramMap["propertyOrdering"])
	schema.Minimum = schemaFloat64(paramMap["minimum"])
	schema.Maximum = schemaFloat64(paramMap["maximum"])
	schema.MinLength = schemaInt64(paramMap["minLength"])
	schema.MaxLength = schemaInt64(paramMap["maxLength"])
	schema.MinItems = schemaInt64(paramMap["minItems"])
	schema.MaxItems = schemaInt64(paramMap["maxItems"])
	schema.MinProperties = schemaInt64(paramMap["minProperties"])
	schema.MaxProperties = schemaInt64(paramMap["maxProperties"])
	if variants, ok := paramMap["anyOf"].([]any); ok {
		for _, variant := range variants {
			schema.AnyOf = append(schema.AnyOf, convertToSchema(variant))
		}
	}

	typeVal, hasType := paramMap["type"]
	if !hasType {
		return schema
	}

	typeStr, ok := typeVal.(string)
	if !ok {
		return schema
	}

	schema.Type = mapJSONTypeToGenAI(typeStr)

	switch typeStr {
	case "array":
		schema.Items = processArrayItems(paramMap)
	case "object":
		if props, ok := paramMap["properties"].(map[string]interface{}); ok {
			schema.Properties = convertSchemaProperties(props)
		}
	}

	return schema
}

func schemaStringSlice(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		result := make([]string, 0, len(values))
		for _, value := range values {
			if stringValue, ok := value.(string); ok {
				result = append(result, stringValue)
			}
		}
		return result
	default:
		return nil
	}
}

func schemaFloat64(value any) *float64 {
	var result float64
	switch number := value.(type) {
	case float64:
		result = number
	case float32:
		result = float64(number)
	case int:
		result = float64(number)
	case int32:
		result = float64(number)
	case int64:
		result = float64(number)
	default:
		return nil
	}
	return &result
}

func schemaInt64(value any) *int64 {
	var result int64
	switch number := value.(type) {
	case float64:
		result = int64(number)
	case float32:
		result = int64(number)
	case int:
		result = int64(number)
	case int32:
		result = int64(number)
	case int64:
		result = number
	default:
		return nil
	}
	return &result
}

func processArrayItems(paramMap map[string]interface{}) *genai.Schema {
	items, ok := paramMap["items"].(map[string]interface{})
	if !ok {
		return nil
	}

	return convertToSchema(items)
}

func mapJSONTypeToGenAI(jsonType string) genai.Type {
	switch jsonType {
	case "string":
		return genai.TypeString
	case "number":
		return genai.TypeNumber
	case "integer":
		return genai.TypeInteger
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	case "object":
		return genai.TypeObject
	default:
		return genai.TypeString // Default to string for unknown types
	}
}

func contains(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if strings.Contains(strings.ToLower(s), strings.ToLower(substr)) {
			return true
		}
	}
	return false
}
