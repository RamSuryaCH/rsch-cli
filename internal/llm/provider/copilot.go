package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/llm/models"
	toolsPkg "github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
)

type copilotOptions struct {
	reasoningEffort string
	extraHeaders    map[string]string
	bearerToken     string
}

type CopilotOption func(*copilotOptions)

type copilotClient struct {
	providerOptions  providerClientOptions
	options          copilotOptions
	clientMu         sync.RWMutex
	refreshOnce      sync.Once
	refreshGate      chan struct{}
	client           openai.Client
	httpClient       *http.Client
	apiBaseURL       string
	tokenExchangeURL string
	initErr          error
}

type copilotCompletionStream interface {
	Next() bool
	Current() openai.ChatCompletionChunk
	Err() error
	Close() error
}

type CopilotClient ProviderClient

const (
	defaultCopilotAPIBaseURL       = "https://api.githubcopilot.com"
	defaultCopilotTokenExchangeURL = "https://api.github.com/copilot_internal/v2/token"
)

// CopilotTokenResponse represents the response from GitHub's token exchange endpoint
type CopilotTokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

func (c *copilotClient) isAnthropicModel() bool {
	for _, modelId := range models.CopilotAnthropicModels {
		if c.providerOptions.model.ID == modelId {
			return true
		}
	}
	return false
}

// exchangeGitHubToken exchanges a GitHub token for a Copilot bearer token
func (c *copilotClient) exchangeGitHubToken(ctx context.Context, githubToken string) (string, error) {
	tokenExchangeURL := c.tokenExchangeURL
	if tokenExchangeURL == "" {
		tokenExchangeURL = defaultCopilotTokenExchangeURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tokenExchangeURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create token exchange request: %w", err)
	}

	req.Header.Set("Authorization", "Token "+githubToken)
	req.Header.Set("User-Agent", "OpenCode/1.0")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to exchange GitHub token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Error responses may echo credentials. Report only the status and
		// close the body without consuming untrusted, potentially large data.
		return "", fmt.Errorf("Copilot token exchange failed (HTTP %d)", resp.StatusCode)
	}

	var tokenResp CopilotTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}
	if tokenResp.Token == "" {
		return "", errors.New("Copilot token exchange returned an empty token")
	}

	return tokenResp.Token, nil
}

func newCopilotAPIClient(baseURL, bearerToken string, headers map[string]string) openai.Client {
	openaiClientOptions := append(withoutOpenAICredentials(),
		option.WithBaseURL(baseURL),
		option.WithAPIKey(bearerToken),
		option.WithHeader("Editor-Version", "OpenCode/1.0"),
		option.WithHeader("Editor-Plugin-Version", "OpenCode/1.0"),
		option.WithHeader("Copilot-Integration-Id", "vscode-chat"),
	)
	for key, value := range headers {
		openaiClientOptions = append(openaiClientOptions, option.WithHeader(key, value))
	}
	return openai.NewClient(openaiClientOptions...)
}

func (c *copilotClient) currentClient() openai.Client {
	c.clientMu.RLock()
	defer c.clientMu.RUnlock()
	return c.client
}

func (c *copilotClient) replaceBearerToken(bearerToken string) {
	baseURL := c.apiBaseURL
	if baseURL == "" {
		baseURL = defaultCopilotAPIBaseURL
	}
	client := newCopilotAPIClient(baseURL, bearerToken, c.options.extraHeaders)
	c.clientMu.Lock()
	c.client = client
	c.options.bearerToken = bearerToken
	c.clientMu.Unlock()
}

func newCopilotClient(opts providerClientOptions) CopilotClient {
	copilotOpts := copilotOptions{
		reasoningEffort: "medium",
	}
	// Apply copilot-specific options
	for _, o := range opts.copilotOptions {
		o(&copilotOpts)
	}

	// Create HTTP client for token exchange
	httpClient := &http.Client{
		Timeout: 30 * time.Second,
	}
	client := &copilotClient{
		providerOptions:  opts,
		options:          copilotOpts,
		httpClient:       httpClient,
		apiBaseURL:       defaultCopilotAPIBaseURL,
		tokenExchangeURL: defaultCopilotTokenExchangeURL,
	}

	var bearerToken string

	// If bearer token is already provided, use it
	if copilotOpts.bearerToken != "" {
		bearerToken = copilotOpts.bearerToken
	} else {
		// Try to get GitHub token from multiple sources
		var githubToken string

		// 1. Environment variable
		githubToken = os.Getenv("GITHUB_TOKEN")

		// 2. API key from options
		if githubToken == "" {
			githubToken = opts.apiKey
		}

		// 3. Standard GitHub CLI/Copilot locations
		if githubToken == "" {
			var err error
			githubToken, err = config.LoadGitHubToken()
			if err != nil {
				logging.Debug("Failed to load GitHub token from standard locations", "error", err)
			}
		}

		if githubToken == "" {
			logging.Error("GitHub token is required for Copilot provider. Set GITHUB_TOKEN environment variable, configure it in opencode.json, or ensure GitHub CLI/Copilot is properly authenticated.")
			client.initErr = errors.New("GitHub token is required for Copilot provider")
			return client
		}

		// Exchange GitHub token for bearer token
		var err error
		bearerToken, err = client.exchangeGitHubToken(context.Background(), githubToken)
		if err != nil {
			logging.Error("Failed to exchange GitHub token for Copilot bearer token", "error", err)
			client.initErr = fmt.Errorf("initialize Copilot credentials: %w", err)
			return client
		}
	}

	client.replaceBearerToken(bearerToken)
	return client
}

func (c *copilotClient) convertMessages(messages []message.Message) (copilotMessages []openai.ChatCompletionMessageParamUnion) {
	// Add system message first
	copilotMessages = append(copilotMessages, openai.SystemMessage(c.providerOptions.systemMessage))

	for _, msg := range messages {
		switch msg.Role {
		case message.User:
			var content []openai.ChatCompletionContentPartUnionParam
			textBlock := openai.ChatCompletionContentPartTextParam{Text: msg.Content().String()}
			content = append(content, openai.ChatCompletionContentPartUnionParam{OfText: &textBlock})

			for _, binaryContent := range msg.BinaryContent() {
				imageURL := openai.ChatCompletionContentPartImageImageURLParam{URL: binaryContent.DataURL()}
				imageBlock := openai.ChatCompletionContentPartImageParam{ImageURL: imageURL}
				content = append(content, openai.ChatCompletionContentPartUnionParam{OfImageURL: &imageBlock})
			}

			copilotMessages = append(copilotMessages, openai.UserMessage(content))

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

			copilotMessages = append(copilotMessages, openai.ChatCompletionMessageParamUnion{
				OfAssistant: &assistantMsg,
			})

		case message.Tool:
			copilotMessages = appendOpenAIToolResultMessages(copilotMessages, msg.ToolResults(), c.providerOptions.model.SupportsAttachments)
		}
	}

	return
}

func (c *copilotClient) convertTools(tools []toolsPkg.BaseTool) []openai.ChatCompletionToolParam {
	copilotTools := make([]openai.ChatCompletionToolParam, len(tools))

	for i, tool := range tools {
		info := tool.Info()
		copilotTools[i] = openai.ChatCompletionToolParam{
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

	return copilotTools
}

func (c *copilotClient) finishReason(reason string) message.FinishReason {
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

func (c *copilotClient) preparedParams(messages []openai.ChatCompletionMessageParamUnion, tools []openai.ChatCompletionToolParam) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(c.providerOptions.model.APIModel),
		Messages: messages,
		Tools:    tools,
	}

	if c.providerOptions.model.CanReason == true {
		params.MaxCompletionTokens = openai.Int(c.providerOptions.maxTokens)
		switch c.options.reasoningEffort {
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
		params.MaxTokens = openai.Int(c.providerOptions.maxTokens)
	}

	return params
}

func (c *copilotClient) send(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) (response *ProviderResponse, err error) {
	if c == nil || c.initErr != nil {
		if c == nil {
			return nil, errors.New("Copilot provider is not initialized")
		}
		return nil, c.initErr
	}
	params := c.preparedParams(c.convertMessages(messages), c.convertTools(tools))
	cfg := config.Get()
	var sessionId string
	requestSeqId := (len(messages) + 1) / 2
	if cfg != nil && cfg.Debug {
		// jsonData, _ := json.Marshal(params)
		// logging.Debug("Prepared messages", "messages", string(jsonData))
		if sid, ok := ctx.Value(toolsPkg.SessionIDContextKey).(string); ok {
			sessionId = sid
		}
		jsonData, _ := json.Marshal(params)
		if sessionId != "" {
			filepath := logging.WriteRequestMessageJson(sessionId, requestSeqId, params)
			logging.Debug("Prepared messages", "filepath", filepath)
		} else {
			logging.Debug("Prepared messages", "messages", string(jsonData))
		}
	}

	attempts := 0
	for {
		attempts++
		client := c.currentClient()
		copilotResponse, err := client.Chat.Completions.New(
			ctx,
			params,
		)

		// If there is an error we are going to see if we can retry the call
		if err != nil {
			retry, after, retryErr := c.shouldRetry(ctx, attempts, err)
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

		if copilotResponse == nil || len(copilotResponse.Choices) == 0 {
			return nil, errors.New("copilot response ended without a completion choice")
		}

		content := ""
		if copilotResponse.Choices[0].Message.Content != "" {
			content = copilotResponse.Choices[0].Message.Content
		}

		toolCalls := c.toolCalls(*copilotResponse)
		finishReason := c.finishReason(string(copilotResponse.Choices[0].FinishReason))

		if len(toolCalls) > 0 {
			finishReason = message.FinishReasonToolUse
		}

		return &ProviderResponse{
			Content:      content,
			ToolCalls:    toolCalls,
			Usage:        c.usage(*copilotResponse),
			FinishReason: finishReason,
		}, nil
	}
}

func (c *copilotClient) stream(ctx context.Context, messages []message.Message, tools []toolsPkg.BaseTool) <-chan ProviderEvent {
	if c == nil || c.initErr != nil {
		events := make(chan ProviderEvent, 1)
		err := errors.New("Copilot provider is not initialized")
		if c != nil {
			err = c.initErr
		}
		events <- ProviderEvent{Type: EventError, Error: err}
		close(events)
		return events
	}
	params := c.preparedParams(c.convertMessages(messages), c.convertTools(tools))
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}

	cfg := config.Get()
	var sessionId string
	requestSeqId := (len(messages) + 1) / 2
	if cfg != nil && cfg.Debug {
		if sid, ok := ctx.Value(toolsPkg.SessionIDContextKey).(string); ok {
			sessionId = sid
		}
		jsonData, _ := json.Marshal(params)
		if sessionId != "" {
			filepath := logging.WriteRequestMessageJson(sessionId, requestSeqId, params)
			logging.Debug("Prepared messages", "filepath", filepath)
		} else {
			logging.Debug("Prepared messages", "messages", string(jsonData))
		}

	}

	return c.streamFrom(ctx, func() copilotCompletionStream {
		client := c.currentClient()
		return client.Chat.Completions.NewStreaming(ctx, params)
	}, cfg != nil && cfg.Debug, sessionId, requestSeqId)
}

func (c *copilotClient) streamFrom(
	ctx context.Context,
	next func() copilotCompletionStream,
	debug bool,
	sessionId string,
	requestSeqId int,
) <-chan ProviderEvent {
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
		attempts := 0
		for {
			attempts++
			if ctx.Err() != nil {
				return
			}
			copilotStream := next()
			activeStream = copilotStream

			acc := openai.ChatCompletionAccumulator{}
			currentContent := ""
			toolCalls := make([]message.ToolCall, 0)
			emitted := false

			var currentToolCallId string
			var currentToolCall openai.ChatCompletionMessageToolCall
			var msgToolCalls []openai.ChatCompletionMessageToolCall
			for ctx.Err() == nil && copilotStream.Next() {
				chunk := copilotStream.Current()
				acc.AddChunk(chunk)

				if debug {
					logging.AppendToStreamSessionLogJson(sessionId, requestSeqId, chunk)
				}

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

				if c.isAnthropicModel() {
					// Monkeypatch adapter for Sonnet-4 multi-tool use
					for _, choice := range chunk.Choices {
						if choice.Delta.ToolCalls != nil && len(choice.Delta.ToolCalls) > 0 {
							toolCall := choice.Delta.ToolCalls[0]
							// Detect tool use start
							if currentToolCallId == "" {
								if toolCall.ID != "" {
									currentToolCallId = toolCall.ID
									currentToolCall = openai.ChatCompletionMessageToolCall{
										ID:   toolCall.ID,
										Type: "function",
										Function: openai.ChatCompletionMessageToolCallFunction{
											Name:      toolCall.Function.Name,
											Arguments: toolCall.Function.Arguments,
										},
									}
								}
							} else {
								// Delta tool use
								if toolCall.ID == "" {
									currentToolCall.Function.Arguments += toolCall.Function.Arguments
								} else {
									// Detect new tool use
									if toolCall.ID != currentToolCallId {
										msgToolCalls = append(msgToolCalls, currentToolCall)
										currentToolCallId = toolCall.ID
										currentToolCall = openai.ChatCompletionMessageToolCall{
											ID:   toolCall.ID,
											Type: "function",
											Function: openai.ChatCompletionMessageToolCallFunction{
												Name:      toolCall.Function.Name,
												Arguments: toolCall.Function.Arguments,
											},
										}
									}
								}
							}
						}
						if choice.FinishReason == "tool_calls" {
							msgToolCalls = append(msgToolCalls, currentToolCall)
							if len(acc.ChatCompletion.Choices) > 0 {
								acc.ChatCompletion.Choices[0].Message.ToolCalls = msgToolCalls
							}
						}
					}
				}
			}

			err := copilotStream.Err()
			_ = copilotStream.Close()
			activeStream = nil
			if ctx.Err() != nil {
				return
			}
			if err == nil || errors.Is(err, io.EOF) {
				if debug {
					respFilepath := logging.WriteChatResponseJson(sessionId, requestSeqId, acc.ChatCompletion)
					logging.Debug("Chat completion response", "filepath", respFilepath)
				}
				if len(acc.ChatCompletion.Choices) == 0 {
					emit(ProviderEvent{Type: EventError, Error: errors.New("copilot stream ended without a completion choice")})
					return
				}
				// Stream completed successfully
				if acc.ChatCompletion.Choices[0].FinishReason == "" {
					emit(ProviderEvent{Type: EventError, Error: errors.New("copilot stream ended before a finish reason")})
					return
				}
				finishReason := c.finishReason(string(acc.ChatCompletion.Choices[0].FinishReason))
				toolCalls = append(toolCalls, c.toolCalls(acc.ChatCompletion)...)
				if len(toolCalls) > 0 {
					finishReason = message.FinishReasonToolUse
				}

				emit(ProviderEvent{
					Type: EventComplete,
					Response: &ProviderResponse{
						Content:      currentContent,
						ToolCalls:    toolCalls,
						Usage:        c.usage(acc.ChatCompletion),
						FinishReason: finishReason,
					},
				})
				return
			}

			// If there is an error we are going to see if we can retry the call
			retry, after, retryErr := c.shouldRetry(ctx, attempts, err)
			if retryErr != nil {
				emit(ProviderEvent{Type: EventError, Error: retryErr})
				return
			}
			if retry && !emitted {
				logging.WarnPersist(fmt.Sprintf("Retrying due to rate limit... attempt %d of %d (paused for %d ms)", attempts, maxRetries, after), logging.PersistTimeArg, time.Millisecond*time.Duration(after+100))
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Duration(after) * time.Millisecond):
					continue
				}
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

func (c *copilotClient) shouldRetry(ctx context.Context, attempts int, err error) (bool, int64, error) {
	if ctx.Err() != nil {
		return false, 0, ctx.Err()
	}
	var apierr *openai.Error
	if !errors.As(err, &apierr) {
		return false, 0, err
	}
	if attempts > maxRetries {
		return false, 0, fmt.Errorf("maximum retry attempts reached: %d retries", maxRetries)
	}

	// Check for token expiration (401 Unauthorized)
	if apierr.StatusCode == 401 {
		c.refreshOnce.Do(func() { c.refreshGate = make(chan struct{}, 1) })
		select {
		case <-ctx.Done():
			return false, 0, ctx.Err()
		case c.refreshGate <- struct{}{}:
		}
		defer func() { <-c.refreshGate }()
		if ctx.Err() != nil {
			return false, 0, ctx.Err()
		}
		// Try to refresh the bearer token
		var githubToken string

		// 1. Environment variable
		githubToken = os.Getenv("GITHUB_TOKEN")

		// 2. API key from options
		if githubToken == "" {
			githubToken = c.providerOptions.apiKey
		}

		// 3. Standard GitHub CLI/Copilot locations
		if githubToken == "" {
			var err error
			githubToken, err = config.LoadGitHubToken()
			if err != nil {
				logging.Debug("Failed to load GitHub token from standard locations during retry", "error", err)
			}
		}

		if githubToken != "" {
			newBearerToken, tokenErr := c.exchangeGitHubToken(ctx, githubToken)
			if tokenErr == nil {
				c.replaceBearerToken(newBearerToken)
				logging.Info("Refreshed Copilot bearer token")
				return true, 0, nil
			}
			if ctx.Err() != nil {
				return false, 0, ctx.Err()
			}
			return false, 0, fmt.Errorf("refresh Copilot credentials: %w", tokenErr)
		}
		return false, 0, copilotAuthenticationError(apierr.StatusCode)
	}
	if apierr.StatusCode == http.StatusForbidden {
		return false, 0, copilotAuthenticationError(apierr.StatusCode)
	}
	logging.Debug("Copilot API Error", "status", apierr.StatusCode, "headers", apierr.Response.Header, "body", apierr.RawJSON())

	if apierr.StatusCode != 429 && apierr.StatusCode != 500 {
		return false, 0, err
	}

	if apierr.StatusCode == 500 {
		logging.Warn("Copilot API returned 500 error, retrying", "error", err)
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

func copilotAuthenticationError(status int) error {
	return fmt.Errorf("copilot authentication failed (HTTP %d): verify GITHUB_TOKEN or providers.copilot.apiKey", status)
}

func (c *copilotClient) toolCalls(completion openai.ChatCompletion) []message.ToolCall {
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

func (c *copilotClient) usage(completion openai.ChatCompletion) TokenUsage {
	cachedTokens := completion.Usage.PromptTokensDetails.CachedTokens
	inputTokens := completion.Usage.PromptTokens - cachedTokens

	return TokenUsage{
		InputTokens:         inputTokens,
		OutputTokens:        completion.Usage.CompletionTokens,
		CacheCreationTokens: 0, // GitHub Copilot doesn't provide this directly
		CacheReadTokens:     cachedTokens,
	}
}

func WithCopilotReasoningEffort(effort string) CopilotOption {
	return func(options *copilotOptions) {
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

func WithCopilotExtraHeaders(headers map[string]string) CopilotOption {
	return func(options *copilotOptions) {
		options.extraHeaders = headers
	}
}

func WithCopilotBearerToken(bearerToken string) CopilotOption {
	return func(options *copilotOptions) {
		options.bearerToken = bearerToken
	}
}
