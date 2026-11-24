package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

type openaiOptions struct {
	baseURL         string
	disableCache    bool
	reasoningEffort string
	extraHeaders    map[string]string
}

type OpenAIOption func(*openaiOptions)

type openaiClient struct {
	providerOptions providerClientOptions
	options         openaiOptions
	client          openai.Client
	retryWait       func(context.Context, time.Duration) error
}

type openAIChatCompletionStream interface {
	Next() bool
	Current() openai.ChatCompletionChunk
	Err() error
	Close() error
}

type OpenAIClient ProviderClient

func newOpenAIClient(opts providerClientOptions) OpenAIClient {
	openaiOpts := openaiOptions{
		reasoningEffort: "medium",
	}
	for _, o := range opts.openaiOptions {
		o(&openaiOpts)
	}
	applyOpenAIProviderDefaults(opts.provider, &openaiOpts)

	openaiClientOptions := []option.RequestOption{}
	if opts.provider != "" && opts.provider != models.ProviderOpenAI {
		openaiClientOptions = append(openaiClientOptions, withoutOpenAICredentials()...)
	}
	if opts.apiKey != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithAPIKey(opts.apiKey))
	}
	if openaiOpts.baseURL != "" {
		openaiClientOptions = append(openaiClientOptions, option.WithBaseURL(openaiOpts.baseURL))
	}

	if openaiOpts.extraHeaders != nil {
		for key, value := range openaiOpts.extraHeaders {
			openaiClientOptions = append(openaiClientOptions, option.WithHeader(key, value))
		}
	}

	client := openai.NewClient(openaiClientOptions...)
	return &openaiClient{
		providerOptions: opts,
		options:         openaiOpts,
		client:          client,
	}
}

// The SDK loads OpenAI credentials and account headers from the environment.
// Compatible providers must only receive their own explicitly configured auth.
func withoutOpenAICredentials() []option.RequestOption {
	return []option.RequestOption{
		option.WithAPIKey(""),
		option.WithHeaderDel("Authorization"),
		option.WithOrganization(""),
		option.WithHeaderDel("OpenAI-Organization"),
		option.WithProject(""),
		option.WithHeaderDel("OpenAI-Project"),
	}
}

// applyOpenAIProviderDefaults keeps an OpenAI-compatible provider's identity,
// endpoint, and required headers together. In particular, an OpenRouter key
// must never fall back to the OpenAI SDK's default api.openai.com endpoint.
func applyOpenAIProviderDefaults(providerName models.ModelProvider, options *openaiOptions) {
	switch providerName {
	case models.ProviderGROQ:
		options.baseURL = "https://api.groq.com/openai/v1"
	case models.ProviderOpenRouter:
		options.baseURL = "https://openrouter.ai/api/v1"
		if options.extraHeaders == nil {
			options.extraHeaders = make(map[string]string, 2)
		}
		options.extraHeaders["HTTP-Referer"] = "https://github.com/RamSuryaCH/rsch"
		options.extraHeaders["X-Title"] = "RSCH"
	case models.ProviderXAI:
		options.baseURL = "https://api.x.ai/v1"
	case models.ProviderLocal:
		options.baseURL = models.LocalEndpoint
		if options.baseURL == "" {
			options.baseURL = os.Getenv("LOCAL_ENDPOINT")
		}
	}
}

func (o *openaiClient) convertMessages(messages []message.Message) (openaiMessages []openai.ChatCompletionMessageParamUnion) {
	// Add system message first
	openaiMessages = append(openaiMessages, openai.SystemMessage(o.providerOptions.systemMessage))

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var content []openai.ChatCompletionContentPartUnionParam
			textBlock := openai.ChatCompletionContentPartTextParam{Text: msg.Content().String()}
			content = append(content, openai.ChatCompletionContentPartUnionParam{OfText: &textBlock})
			for _, binaryContent := range msg.BinaryContent() {
				imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: binaryContent.String(models.ProviderOpenAI)}
				imageBlock := openai.ChatCompletionContentPartImageParam{ImageURL: imageURL}

				content = append(content, openai.ChatCompletionContentPartUnionParam{OfImageURL: &imageBlock})
			}

			openaiMessages = append(openaiMessages, openai.UserMessage(content))

		case message.Assistant:
			assistantMsg := openai.ChatCompletionAssistantMessageParam{
				Role: "assistant",
			}

			if msg.Content().String() != "" {
				assistantMsg.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(msg.Content().String()),
				}
			}

			if len(msg.ToolCalls()) > 0 {
				assistantMsg.ToolCalls = make([]openai.ChatCompletionMessageToolCallParam, len(msg.ToolCalls()))
				for i, call := range msg.ToolCalls() {
					assistantMsg.ToolCalls[i] = openai.ChatCompletionMessageToolCallParam{
						ID:   call.ID,
						Type: "function",
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      call.Name,
							Arguments: call.Input,
						},
					}
				}
			}

			openaiMessages = append(openaiMessages, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &assistantMsg,
			})

		case message.Tool:
			openaiMessages = appendOpenAIToolResultMessages(openaiMessages, msg.ToolResults(), o.providerOptions.model.SupportsAttachments)
		}
	}

	return
}

// OpenAI-compatible chat APIs only accept text in a role=tool message. Native
// visual evidence therefore follows the correlated tool result as a user
// multimodal message. The provenance label keeps the image tied to the tool
// call while preserving the protocol-required tool response ordering.
func appendOpenAIToolResultMessages(messages []openai.ChatCompletionMessageParamUnion, results []message.ToolResult, includeMedia bool) []openai.ChatCompletionMessageParamUnion {
	// Keep all protocol-required tool responses contiguous. This matters when
	// the preceding assistant message issued parallel tool calls.
	for _, result := range results {
		messages = append(messages, openai.ToolMessage(result.Content, result.ToolCallID))
	}
	if !includeMedia {
		return messages
	}
	for _, result := range results {
		if len(result.Media) == 0 {
			continue
		}

		label := fmt.Sprintf("Native visual evidence returned by tool %q for call %q.", result.Name, result.ToolCallID)
		textBlock := openai.ChatCompletionContentPartTextParam{Text: label}
		content := []openai.ChatCompletionContentPartUnionParam{{OfText: &textBlock}}
		remaining := tools.MaxToolMediaBytes
		for _, media := range result.Media {
			if media.MIMEType == "" || len(media.Data) == 0 || len(media.Data) > remaining {
				continue
			}
			imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: media.DataURL()}
			imageBlock := openai.ChatCompletionContentPartImageParam{ImageURL: imageURL}
			content = append(content, openai.ChatCompletionContentPartUnionParam{OfImageURL: &imageBlock})
			remaining -= len(media.Data)
		}
		if len(content) > 1 {
			messages = append(messages, openai.UserMessage(content))
		}
	}
	return messages
}

func (o *openaiClient) convertTools(tools []tools.BaseTool) []openai.ChatCompletionToolParam {
	openaiTools := make([]openai.ChatCompletionToolParam, len(tools))

	for i, tool := range tools {
		info := tool.Info()
		openaiTools[i] = openai.ChatCompletionToolParam{
			Function: openai.FunctionDefinitionParam{
				Name:        info.Name,
				Description: openai.String(info.Description),
				Parameters: openai.FunctionParameters{
					"type":       "object",
					"properties": info.Parameters,
					"required":   info.Required,
				},
			},
		}
	}

	return openaiTools
}

func (o *openaiClient) finishReason(reason string) message.FinishReason {
	switch reason {
	case "stop":
		return message.FinishReasonEndTurn
	case "length":
		return message.FinishReasonMaxTokens
	case "tool_calls":
		return message.FinishReasonToolUse
	default:
		return message.FinishReasonUnknown
	}
}

func (o *openaiClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(o.providerOptions.model.APIModel),
		Messages: messages,
		Tools:    tools,
	}

	if o.providerOptions.model.CanReason == true {
		params.MaxCompletionTokens = openai.Int(o.providerOptions.maxTokens)
		switch o.options.reasoningEffort {
		case "low":
			params.ReasoningEffort = shared.ReasoningEffortLow
		case "medium":
			params.ReasoningEffort = shared.ReasoningEffortMedium
		case "high":
			params.ReasoningEffort = shared.ReasoningEffortHigh
		default:
			params.ReasoningEffort = shared.ReasoningEffortMedium
		}
	} else {
		params.MaxTokens = openai.Int(o.providerOptions.maxTokens)
	}

	return params
}

func (o *openaiClient) send(ctx context.Context, messages []message.Message, tools []tools.BaseTool) (response *ProviderResponse, err error) {
	params := o.preparedParams(o.convertMessages(messages), o.convertTools(tools))
	cfg := config.Get()
	if cfg != nil && cfg.Debug {
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}
	attempts := 0
	for {
		attempts++
		openaiResponse, err := o.client.Chat.Completions.New(
			ctx,
			params,
		)
		// If there is an error we are going to see if we can retry the call
		if err != nil {
			retry, after, retryErr := o.shouldRetry(attempts, err)
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

		if openaiResponse == nil || len(openaiResponse.Choices) == 0 {
			return nil, errors.New("openai response ended without a completion choice")
		}

		content := ""
		if openaiResponse.Choices[0].Message.Content != "" {
			content = openaiResponse.Choices[0].Message.Content
		}

		toolCalls := o.toolCalls(*openaiResponse)
		finishReason := o.finishReason(string(openaiResponse.Choices[0].FinishReason))

		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		return &ProviderResponse{
			Content:      content,
			ToolCalls:    toolCalls,
			Usage:        o.usage(*openaiResponse),
			FinishReason: finishReason,
		}, nil
	}
}

func (o *openaiClient) stream(ctx context.Context, messages []message.Message, tools []tools.BaseTool) <-chan ProviderEvent {
	params := o.preparedParams(o.convertMessages(messages), o.convertTools(tools))
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	cfg := config.Get()
	if cfg != nil && cfg.Debug {
		jsonData, _ := json.Marshal(params)
		logging.Debug("Prepared messages", "messages", string(jsonData))
	}

	return o.streamFrom(ctx, func() openAIChatCompletionStream {
		return o.client.Chat.Completions.NewStreaming(
			ctx,
			params,
		)
	})
}

func (o *openaiClient) streamFrom(ctx context.Context, next func() openAIChatCompletionStream) <-chan ProviderEvent {
	eventChan := make(chan ProviderEvent)
	emit := func(event ProviderEvent) bool {
		select {
		case <-ctx.Done():
			return false
		case eventChan <- event:
			return true
		}
	}

	go func() {
		defer close(eventChan)
		var activeStream io.Closer
		defer func() {
			if activeStream != nil {
				_ = activeStream.Close()
			}
		}()
		for attempts := 1; ; attempts++ {
			if ctx.Err() != nil {
				return
			}
			openaiStream := next()
			activeStream = openaiStream

			acc := openai.ChatCompletionAccumulator{}
			currentContent := ""
			toolCalls := make([]message.ToolCall, 0)
			emitted := false
			receivedChunk := false

			for ctx.Err() == nil && openaiStream.Next() {
				receivedChunk = true
				chunk := openaiStream.Current()
				acc.AddChunk(chunk)

				for _, choice := range chunk.Choices {
					if choice.Delta.Content != "" {
						if !emit(ProviderEvent{
							Type:    EventContentDelta,
							Content: choice.Delta.Content,
						}) {
							return
						}
						emitted = true
						currentContent += choice.Delta.Content
					}
				}
			}

			err := openaiStream.Err()
			_ = openaiStream.Close()
			activeStream = nil
			if ctx.Err() != nil {
				return
			}
			if err == nil || errors.Is(err, io.EOF) {
				if !receivedChunk {
					emit(ProviderEvent{Type: EventError, Error: errors.New("openai stream ended without a response")})
					return
				}
				if len(acc.ChatCompletion.Choices) == 0 {
					emit(ProviderEvent{Type: EventError, Error: errors.New("openai stream ended without a completion choice")})
					return
				}

				if acc.ChatCompletion.Choices[0].FinishReason == "" {
					emit(ProviderEvent{Type: EventError, Error: errors.New("openai stream ended before a finish reason")})
					return
				}
				finishReason := o.finishReason(string(acc.ChatCompletion.Choices[0].FinishReason))
				if len(acc.ChatCompletion.Choices[0].Message.ToolCalls) > 0 {
					toolCalls = append(toolCalls, o.toolCalls(acc.ChatCompletion)...)
				}
				if len(toolCalls) > 0 {
					finishReason = message.FinishReasonToolUse
				}

				emit(ProviderEvent{
					Type: EventComplete,
					Response: &ProviderResponse{
						Content:      currentContent,
						ToolCalls:    toolCalls,
						Usage:        o.usage(acc.ChatCompletion),
						FinishReason: finishReason,
					},
				})
				return
			}

			retry, after, retryErr := o.shouldRetry(attempts, err)
			if retry && !emitted {
				logging.WarnPersist(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d", attempts, maxRetries), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
				if waitErr := o.waitForRetry(ctx, time.Duration(after)*time.Millisecond); waitErr != nil {
					emit(ProviderEvent{Type: EventError, Error: waitErr})
					return
				}
				continue
			}
			if retryErr == nil {
				retryErr = err
			}
			emit(ProviderEvent{Type: EventError, Error: retryErr})
			return
		}
	}()

	return eventChan
}

func (o *openaiClient) waitForRetry(ctx context.Context, delay time.Duration) error {
	if o.retryWait != nil {
		return o.retryWait(ctx, delay)
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

func (o *openaiClient) shouldRetry(attempts int, err error) (bool, int64, error) {
	var apierr *openai.Error
	if !errors.As(err, &apierr) {
		return false, 0, err
	}
	if apierr.StatusCode == 401 || apierr.StatusCode == 403 {
		providerName := o.providerOptions.provider
		if providerName == "" {
			providerName = models.ProviderOpenAI
		}
		return false, 0, fmt.Errorf(
			"%s authentication failed (HTTP %d): verify %s",
			providerName,
			apierr.StatusCode,
			providerCredentialHint(providerName),
		)
	}

	if apierr.StatusCode != 429 && apierr.StatusCode != 500 {
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

func providerCredentialHint(providerName models.ModelProvider) string {
	switch providerName {
	case models.ProviderGROQ:
		return "GROQ_API_KEY or providers.groq.apiKey"
	case models.ProviderOpenRouter:
		return "OPENROUTER_API_KEY or providers.openrouter.apiKey"
	case models.ProviderXAI:
		return "XAI_API_KEY or providers.xai.apiKey"
	case models.ProviderLocal:
		return "providers.local.apiKey"
	default:
		return "OPENAI_API_KEY or providers.openai.apiKey"
	}
}

func (o *openaiClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
	var toolCalls []message.ToolCall

	if len(completion.Choices) > 0 && len(completion.Choices[0].Message.ToolCalls) > 0 {
		for _, call := range completion.Choices[0].Message.ToolCalls {
			toolCall := message.ToolCall{
				ID:       call.ID,
				Name:     call.Function.Name,
				Input:    call.Function.Arguments,
				Type:     "function",
				Finished: true,
			}
			toolCalls = append(toolCalls, toolCall)
		}
	}

	return toolCalls
}

func (o *openaiClient) usage(completion openai.ChatCompletion) TokenUsage {
	cachedTokens := completion.Usage.PromptTokensDetails.CachedTokens
	inputTokens := completion.Usage.PromptTokens - cachedTokens

	return TokenUsage{
		InputTokens:         inputTokens,
		OutputTokens:        completion.Usage.CompletionTokens,
		CacheCreationTokens: 0, // OpenAI doesn't provide this directly
		CacheReadTokens:     cachedTokens,
	}
}

func WithOpenAIBaseURL(baseURL string) OpenAIOption {
	return func(options *openaiOptions) {
		options.baseURL = baseURL
	}
}

func WithOpenAIExtraHeaders(headers map[string]string) OpenAIOption {
	return func(options *openaiOptions) {
		options.extraHeaders = headers
	}
}

func WithOpenAIDisableCache() OpenAIOption {
	return func(options *openaiOptions) {
		options.disableCache = true
	}
}

func WithReasoningEffort(effort string) OpenAIOption {
	return func(options *openaiOptions) {
		defaultReasoningEffort := "medium"
		switch effort {
		case "low", "medium", "high":
			defaultReasoningEffort = effort
		default:
			logging.Warn("Invalid reasoning effort, using default: medium")
		}
		options.reasoningEffort = defaultReasoningEffort
	}
}
