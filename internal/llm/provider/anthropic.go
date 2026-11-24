package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/bedrock"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	toolsPkg "github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

type anthropicOptions struct {
	useBedrock   bool
	disableCache bool
	shouldThink  func(userMessage string) bool
}

type AnthropicOption func(*anthropicOptions)

type anthropicClient struct {
	providerOptions providerClientOptions
	options         anthropicOptions
	client          anthropic.Client
	retryWait       func(context.Context, time.Duration) error
}

type anthropicMessageStream interface {
	Next() bool
	Current() anthropic.MessageStreamEventUnion
	Err() error
	Close() error
}

type AnthropicClient ProviderClient

func newAnthropicClient(opts providerClientOptions) AnthropicClient {
	anthropicOpts := anthropicOptions{}
	for _, o := range opts.anthropicOptions {
		o(&anthropicOpts)
	}

	anthropicClientOptions := []option.RequestOption{}
	if opts.apiKey != "" {
		anthropicClientOptions = append(anthropicClientOptions, option.WithAPIKey(opts.apiKey))
	}
	if anthropicOpts.useBedrock {
		anthropicClientOptions = append(anthropicClientOptions, bedrock.WithLoadDefaultConfig(context.Background()))
	}

	client := anthropic.NewClient(anthropicClientOptions...)
	return &anthropicClient{
		providerOptions: opts,
		options:         anthropicOpts,
		client:          client,
	}
}

func (a *anthropicClient) convertMessages(messages []message.Message) (anthropicMessages []anthropic.MessageParam) {
	for i, msg := range messages {
		cache := false
		if i > len(messages)-3 {
			cache = true
		}
		switch msg.Role {
		case message.User:
			content := anthropic.NewTextBlock(msg.Content().String())
			if cache && !a.options.disableCache {
				content.OfText.CacheControl = anthropic.CacheControlEphemeralParam{
					Type: "ephemeral",
				}
			}
			var contentBlocks []anthropic.ContentBlockParamUnion
			contentBlocks = append(contentBlocks, content)
			for _, binaryContent := range msg.BinaryContent() {
				base64Image := binaryContent.String(models.ProviderAnthropic)
				imageBlock := anthropic.NewImageBlockBase64(binaryContent.MIMEType, base64Image)
				contentBlocks = append(contentBlocks, imageBlock)
			}
			anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(contentBlocks...))

		case message.Assistant:
			blocks := []anthropic.ContentBlockParamUnion{}
			if msg.Content().String() != "" {
				content := anthropic.NewTextBlock(msg.Content().String())
				if cache && !a.options.disableCache {
					content.OfText.CacheControl = anthropic.CacheControlEphemeralParam{
						Type: "ephemeral",
					}
				}
				blocks = append(blocks, content)
			}

			for _, toolCall := range msg.ToolCalls() {
				var inputMap map[string]any
				err := json.Unmarshal([]byte(toolCall.Input), &inputMap)
				if err != nil {
					continue
				}
				blocks = append(blocks, anthropic.NewToolUseBlock(toolCall.ID, inputMap, toolCall.Name))
			}

			if len(blocks) == 0 {
				logging.Warn("There is a message without content, investigate, this should not happen")
				continue
			}
			anthropicMessages = append(anthropicMessages, anthropic.NewAssistantMessage(blocks...))

		case message.Tool:
			results := make([]anthropic.ContentBlockParamUnion, len(msg.ToolResults()))
			for i, toolResult := range msg.ToolResults() {
				content := []anthropic.ToolResultBlockParamContentUnion{{OfText: &anthropic.TextBlockParam{Text: toolResult.Content}}}
				if a.providerOptions.model.SupportsAttachments {
					remaining := toolsPkg.MaxToolMediaBytes
					for _, media := range toolResult.Media {
						if media.MIMEType == "" || len(media.Data) == 0 || len(media.Data) > remaining {
							continue
						}
						image := anthropic.NewImageBlockBase64(media.MIMEType, base64.StdEncoding.EncodeToString(media.Data))
						content = append(content, anthropic.ToolResultBlockParamContentUnion{OfImage: image.OfImage})
						remaining -= len(media.Data)
					}
				}
				results[i] = anthropic.ContentBlockParamUnion{OfToolResult: &anthropic.ToolResultBlockParam{
					ToolUseID: toolResult.ToolCallID,
					Content:   content,
					IsError:   anthropic.Bool(toolResult.IsError),
				}}
			}
			anthropicMessages = append(anthropicMessages, anthropic.NewUserMessage(results...))
		}
	}
	return
}

func (a *anthropicClient) convertTools(tools []toolsPkg.BaseTool) []anthropic.ToolUnionParam {
	anthropicTools := make([]anthropic.ToolUnionParam, len(tools))

	for i, tool := range tools {
		info := tool.Info()
		toolParam := anthropic.ToolParam{
			Name:        info.Name,
			Description: anthropic.String(info.Description),
			InputSchema: anthropic.ToolInputSchemaParam{
				Properties: info.Parameters,
				Required:   append([]string(nil), info.Required...),
			},
		}

		if i == len(tools)-1 && !a.options.disableCache {
			toolParam.CacheControl = anthropic.CacheControlEphemeralParam{
				Type: "ephemeral",
			}
		}

		anthropicTools[i] = anthropic.ToolUnionParam{OfTool: &toolParam}
	}

	return anthropicTools
}

func (a *anthropicClient) finishReason(reason string) message.FinishReason {
	switch reason {
	case "end_turn":
		return message.FinishReasonEndTurn
	case "max_tokens":
		return message.FinishReasonMaxTokens
	case "tool_use":
		return message.FinishReasonToolUse
	case "stop_sequence":
		return message.FinishReasonEndTurn
	default:
		return message.FinishReasonUnknown
	}
}

func (a *anthropicClient) preparedMessages(messages []anthropic.MessageParam, tools []anthropic.ToolUnionParam) anthropic.MessageNewParams {
	var thinkingParam anthropic.ThinkingConfigParamUnion
	lastMessage := messages[len(messages)-1]
	isUser := lastMessage.Role == anthropic.MessageParamRoleUser
	messageContent := ""
	temperature := anthropic.Float(0)
	if isUser {
		for _, m := range lastMessage.Content {
			if m.OfText != nil && m.OfText.Text != "" {
				messageContent = m.OfText.Text
			}
		}
		if messageContent != "" && a.options.shouldThink != nil && a.options.shouldThink(messageContent) {
			thinkingParam = anthropic.ThinkingConfigParamOfEnabled(int64(float64(a.providerOptions.maxTokens) * 0.8))
			temperature = anthropic.Float(1)
		}
	}

	system := anthropic.TextBlockParam{Text: a.providerOptions.systemMessage}
	if !a.options.disableCache {
		system.CacheControl = anthropic.CacheControlEphemeralParam{Type: "ephemeral"}
	}

	return anthropic.MessageNewParams{
		Model:       anthropic.Model(a.providerOptions.model.APIModel),
		MaxTokens:   a.providerOptions.maxTokens,
		Temperature: temperature,
		Messages:    messages,
		Tools:       tools,
		Thinking:    thinkingParam,
		System:      []anthropic.TextBlockParam{system},
	}
}

func (a *anthropicClient) send(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) (resposne *ProviderResponse, err error) {
	convertedMessages := a.convertMessages(messages)
	if len(convertedMessages) == 0 {
		return nil, errors.New("anthropic request has no messages")
	}
	preparedMessages := a.preparedMessages(convertedMessages, a.convertTools(tools))
	cfg := config.Get()
	if cfg != nil && cfg.Debug {
		jsonData, _ := json.Marshal(preparedMessages)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	attempts := 0
	for {
		attempts++
		anthropicResponse, err := a.client.Messages.New(
			ctx,
			preparedMessages,
		)
		// If there is an error we are going to see if we can retry the call
		if err != nil {
			logging.Error("Error in Anthropic API call", "error", err)
			retry, after, retryErr := a.shouldRetry(attempts, err)
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
		for _, block := range anthropicResponse.Content {
			if text, ok := block.AsAny().(anthropic.TextBlock); ok {
				content += text.Text
			}
		}

		return &ProviderResponse{
			Content:      content,
			ToolCalls:    a.toolCalls(*anthropicResponse),
			Usage:        a.usage(*anthropicResponse),
			FinishReason: a.finishReason(string(anthropicResponse.StopReason)),
		}, nil
	}
}

func (a *anthropicClient) stream(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) <-chan ProviderEvent {
	convertedMessages := a.convertMessages(messages)
	if len(convertedMessages) == 0 {
		events := make(chan ProviderEvent, 1)
		events <- ProviderEvent{Type: EventError, Error: errors.New("anthropic request has no messages")}
		close(events)
		return events
	}
	preparedMessages := a.preparedMessages(convertedMessages, a.convertTools(tools))
	cfg := config.Get()

	var sessionId string
	requestSeqId := (len(messages) + 1) / 2
	if cfg != nil && cfg.Debug {
		if sid, ok := ctx.Value(toolsPkg.SessionIDContextKey).(string); ok {
			sessionId = sid
		}
		jsonData, _ := json.Marshal(preparedMessages)
		if sessionId != "" {
			filepath := logging.WriteRequestMessageJson(sessionId, requestSeqId, preparedMessages)
			logging.Debug("Prepared messages", "filepath", filepath)
		} else {
			logging.Debug("Prepared messages", "messages", string(jsonData))
		}

	}
	return a.streamFrom(ctx, func() anthropicMessageStream {
		return a.client.Messages.NewStreaming(
			ctx,
			preparedMessages,
		)
	})
}

func (a *anthropicClient) streamFrom(ctx context.Context, newStream func() anthropicMessageStream) <-chan ProviderEvent {
	eventChan := make(chan ProviderEvent)
	go func() {
		defer close(eventChan)
		var activeStream anthropicMessageStream
		defer func() {
			if activeStream != nil {
				_ = activeStream.Close()
			}
		}()
		for attempts := 1; ; attempts++ {
			if ctx.Err() != nil {
				return
			}
			anthropicStream := newStream()
			activeStream = anthropicStream
			accumulatedMessage := anthropic.Message{}
			emitted := false
			emit := func(event ProviderEvent) bool {
				if !sendProviderEvent(ctx, eventChan, event) {
					return false
				}
				emitted = true
				return true
			}

			currentToolCallID := ""
			for ctx.Err() == nil && anthropicStream.Next() {
				event := anthropicStream.Current()
				err := accumulatedMessage.Accumulate(event)
				if err != nil {
					logging.Warn("Error accumulating message", "error", err)
					continue
				}

				switch event := event.AsAny().(type) {
				case anthropic.ContentBlockStartEvent:
					if event.ContentBlock.Type == "text" {
						if !emit(ProviderEvent{Type: EventContentStart}) {
							return
						}
					} else if event.ContentBlock.Type == "tool_use" {
						currentToolCallID = event.ContentBlock.ID
						if !emit(ProviderEvent{
							Type: EventToolUseStart,
							ToolCall: &message.ToolCall{
								ID:       event.ContentBlock.ID,
								Name:     event.ContentBlock.Name,
								Finished: false,
							},
						}) {
							return
						}
					}

				case anthropic.ContentBlockDeltaEvent:
					if event.Delta.Type == "thinking_delta" && event.Delta.Thinking != "" {
						if !emit(anthropicThinkingDelta(event.Delta.Thinking)) {
							return
						}
					} else if event.Delta.Type == "text_delta" && event.Delta.Text != "" {
						if !emit(ProviderEvent{
							Type:    EventContentDelta,
							Content: event.Delta.Text,
						}) {
							return
						}
					} else if event.Delta.Type == "input_json_delta" {
						if currentToolCallID != "" {
							if !emit(ProviderEvent{
								Type: EventToolUseDelta,
								ToolCall: &message.ToolCall{
									ID:       currentToolCallID,
									Finished: false,
									Input:    event.Delta.PartialJSON,
								},
							}) {
								return
							}
						}
					}
				case anthropic.ContentBlockStopEvent:
					if currentToolCallID != "" {
						if !emit(ProviderEvent{
							Type: EventToolUseStop,
							ToolCall: &message.ToolCall{
								ID: currentToolCallID,
							},
						}) {
							return
						}
						currentToolCallID = ""
					} else {
						if !emit(ProviderEvent{Type: EventContentStop}) {
							return
						}
					}

				case anthropic.MessageStopEvent:
					content := ""
					for _, block := range accumulatedMessage.Content {
						if text, ok := block.AsAny().(anthropic.TextBlock); ok {
							content += text.Text
						}
					}

					if !emit(ProviderEvent{
						Type: EventComplete,
						Response: &ProviderResponse{
							Content:      content,
							ToolCalls:    a.toolCalls(accumulatedMessage),
							Usage:        a.usage(accumulatedMessage),
							FinishReason: a.finishReason(string(accumulatedMessage.StopReason)),
						},
					}) {
						return
					}
				}
			}

			err := anthropicStream.Err()
			_ = anthropicStream.Close()
			activeStream = nil
			if ctx.Err() != nil {
				return
			}
			if err == nil || errors.Is(err, io.EOF) {
				return
			}
			// If there is an error we are going to see if we can retry the call
			retry, after, retryErr := a.shouldRetry(attempts, err)
			if retry && !emitted {
				logging.WarnPersist(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, maxRetries), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
				if waitErr := a.waitForRetry(ctx, time.Duration(after)*time.Millisecond); waitErr != nil {
					sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventError, Error: waitErr})
					return
				}
				continue
			}
			if retryErr == nil {
				retryErr = err
			}
			sendProviderEvent(ctx, eventChan, ProviderEvent{Type: EventError, Error: retryErr})
			return
		}
	}()
	return eventChan
}

func (a *anthropicClient) waitForRetry(ctx context.Context, delay time.Duration) error {
	if a.retryWait != nil {
		return a.retryWait(ctx, delay)
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

// anthropicThinkingDelta populates both fields during the transition to the
// dedicated Thinking field. Existing event consumers historically read
// Content, while routing/fallback layers account for Thinking separately.
func anthropicThinkingDelta(delta string) ProviderEvent {
	return ProviderEvent{Type: EventThinkingDelta, Content: delta, Thinking: delta}
}

func (a *anthropicClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	var apierr *anthropic.Error
	if !errors.As(err, &apierr) {
		return false, 0, err
	}

	if apierr.StatusCode != 429 && apierr.StatusCode != 529 {
		return false, 0, err
	}

	if attempts > maxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached for rate limit: %d retries", maxRetries)
	}

	retryMs := 0
	retryAfterValues := apierr.Response.Header.Values("Retry-After")

	backoffMs := 2000 * (1 << (attempts - 1))
	jitterMs := int(float64(backoffMs) * 0.2)
	retryMs = backoffMs + jitterMs
	if len(retryAfterValues) > 0 {
		if _, err := fmt.Sscanf(retryAfterValues[0], "%d", &retryMs); err == nil {
			retryMs = retryMs * 1000
		}
	}
	return true, int64(retryMs), nil
}

func (a *anthropicClient) toolCalls(msg anthropic.Message) []message.ToolCall {
	var toolCalls []message.ToolCall

	for _, block := range msg.Content {
		switch variant := block.AsAny().(type) {
		case anthropic.ToolUseBlock:
			toolCall := message.ToolCall{
				ID:       variant.ID,
				Name:     variant.Name,
				Input:    string(variant.Input),
				Type:     string(variant.Type),
				Finished: true,
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return toolCalls
}

func (a *anthropicClient) usage(msg anthropic.Message) TokenUsage {
	return TokenUsage{
		InputTokens:         msg.Usage.InputTokens,
		OutputTokens:        msg.Usage.OutputTokens,
		CacheCreationTokens: msg.Usage.CacheCreationInputTokens,
		CacheReadTokens:     msg.Usage.CacheReadInputTokens,
	}
}

func WithAnthropicBedrock(useBedrock bool) AnthropicOption {
	return func(options *anthropicOptions) {
		options.useBedrock = useBedrock
	}
}

func WithAnthropicDisableCache() AnthropicOption {
	return func(options *anthropicOptions) {
		options.disableCache = true
	}
}

func DefaultShouldThinkFn(s string) bool {
	return strings.Contains(strings.ToLower(s), "think")
}

func WithAnthropicShouldThinkFn(fn func(string) bool) AnthropicOption {
	return func(options *anthropicOptions) {
		options.shouldThink = fn
	}
}
