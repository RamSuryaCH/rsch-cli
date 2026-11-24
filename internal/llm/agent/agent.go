package agent

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/llm/prompt"
	"github.com/opencode-ai/opencode/internal/llm/provider"
	"github.com/opencode-ai/opencode/internal/llm/tools"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/routing"
	"github.com/opencode-ai/opencode/internal/session"
	"github.com/opencode-ai/opencode/internal/trust"
)

// Common errors
var (
	ErrRequestCancelled = errors.New("request cancelled by user")
	ErrSessionBusy      = errors.New("session is currently processing another request")
	ErrStepLimit        = errors.New("agent step limit reached")
	ErrNoProgress       = errors.New("agent repeated the same tool request without progress")
	ErrIncomplete       = errors.New("agent response was incomplete")
)

const (
	defaultMaxAgentSteps     = 64
	defaultMaxAgentToolCalls = 256
	defaultMaxParallelTools  = 4
	maxRepeatedToolBatches   = 3
)

type activeRequest struct {
	cancel context.CancelFunc
}

type generationGuard struct {
	steps             int
	toolCalls         int
	lastToolBatch     [sha256.Size]byte
	hasLastToolBatch  bool
	repeatedToolBatch int
}

func (g *generationGuard) beginStep() error {
	if g.steps >= defaultMaxAgentSteps {
		return fmt.Errorf("%w: maximum %d", ErrStepLimit, defaultMaxAgentSteps)
	}
	g.steps++
	return nil
}

func (g *generationGuard) observeToolCalls(calls []message.ToolCall) error {
	if len(calls) == 0 {
		return errors.New("provider requested tool use without any tool calls")
	}
	if g.toolCalls+len(calls) > defaultMaxAgentToolCalls {
		return fmt.Errorf("%w: maximum %d tool calls", ErrStepLimit, defaultMaxAgentToolCalls)
	}
	g.toolCalls += len(calls)

	seenIDs := make(map[string]struct{}, len(calls))
	hasher := sha256.New()
	for _, call := range calls {
		if strings.TrimSpace(call.ID) == "" {
			return errors.New("provider emitted a tool call without an id")
		}
		if _, exists := seenIDs[call.ID]; exists {
			return fmt.Errorf("provider emitted duplicate tool call id %q", call.ID)
		}
		seenIDs[call.ID] = struct{}{}
		_, _ = hasher.Write([]byte(call.Name))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(call.Input))
		_, _ = hasher.Write([]byte{0})
	}
	var signature [sha256.Size]byte
	copy(signature[:], hasher.Sum(nil))
	if g.hasLastToolBatch && signature == g.lastToolBatch {
		g.repeatedToolBatch++
	} else {
		g.lastToolBatch = signature
		g.hasLastToolBatch = true
		g.repeatedToolBatch = 1
	}
	if g.repeatedToolBatch >= maxRepeatedToolBatches {
		return fmt.Errorf("%w: repeated %d times", ErrNoProgress, g.repeatedToolBatch)
	}
	return nil
}

type AgentEventType string

const (
	AgentEventTypeError     AgentEventType = "error"
	AgentEventTypeResponse  AgentEventType = "response"
	AgentEventTypeSummarize AgentEventType = "summarize"
)

type AgentEvent struct {
	Type    AgentEventType
	Message message.Message
	Error   error

	// When summarizing
	SessionID string
	Progress  string
	Done      bool
}

type Service interface {
	pubsub.Suscriber[AgentEvent]
	Model() models.Model
	Run(ctx context.Context, sessionID string, content string, attachments ...message.Attachment) (<-chan AgentEvent, error)
	Cancel(sessionID string)
	IsSessionBusy(sessionID string) bool
	IsBusy() bool
	Update(agentName config.AgentName, modelID models.ModelID) (models.Model, error)
	Summarize(ctx context.Context, sessionID string) error
}

type agent struct {
	*pubsub.Broker[AgentEvent]
	sessions session.Service
	messages message.Service

	tools    []tools.BaseTool
	provider provider.Provider

	titleProvider     provider.Provider
	summarizeProvider provider.Provider

	activeRequests sync.Map
	modelSlots     chan struct{}
	bus            *events.Bus
}

func NewAgent(
	agentName config.AgentName,
	sessions session.Service,
	messages message.Service,
	agentTools []tools.BaseTool,
	buses ...*events.Bus,
) (Service, error) {
	agentProvider, err := createAgentProvider(agentName)
	if err != nil {
		return nil, err
	}
	var titleProvider provider.Provider
	// Only generate titles for the coder agent
	if agentName == config.AgentCoder {
		titleProvider, err = createAgentProvider(config.AgentTitle)
		if err != nil {
			return nil, err
		}
	}
	var summarizeProvider provider.Provider
	if agentName == config.AgentCoder {
		summarizeProvider, err = createAgentProvider(config.AgentSummarizer)
		if err != nil {
			return nil, err
		}
	}

	agent := &agent{
		Broker:            pubsub.NewBroker[AgentEvent](),
		provider:          agentProvider,
		messages:          messages,
		sessions:          sessions,
		tools:             agentTools,
		titleProvider:     titleProvider,
		summarizeProvider: summarizeProvider,
		activeRequests:    sync.Map{},
	}
	maxRequests := 1
	if loaded := config.Get(); loaded != nil && loaded.Resources.MaxModelRequests > 0 {
		maxRequests = loaded.Resources.MaxModelRequests
	}
	agent.modelSlots = make(chan struct{}, maxRequests)
	if len(buses) > 0 {
		agent.bus = buses[0]
	}

	return agent, nil
}

func (a *agent) Model() models.Model {
	return a.provider.Model()
}

func (a *agent) Cancel(sessionID string) {
	if value, exists := a.activeRequests.Load(sessionID); exists {
		if request, ok := value.(*activeRequest); ok && request.cancel != nil {
			logging.InfoPersist(fmt.Sprintf("Request cancellation initiated for session: %s", sessionID))
			request.cancel()
		}
	}
}

func (a *agent) IsBusy() bool {
	busy := false
	a.activeRequests.Range(func(key, value interface{}) bool {
		if request, ok := value.(*activeRequest); ok && request.cancel != nil {
			busy = true
			return false
		}
		return true
	})
	return busy
}

func (a *agent) IsSessionBusy(sessionID string) bool {
	_, busy := a.activeRequests.Load(sessionID)
	return busy
}

func (a *agent) generateTitle(ctx context.Context, sessionID string, content string) error {
	if content == "" {
		return nil
	}
	if a.titleProvider == nil {
		return nil
	}
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	release, err := a.acquireModel(ctx)
	if err != nil {
		return err
	}
	defer release()
	parts := []message.ContentPart{message.TextContent{Text: content}}
	response, err := a.titleProvider.SendMessages(
		ctx,
		[]message.Message{
			{
				Role:  message.User,
				Parts: parts,
			},
		},
		make([]tools.BaseTool, 0),
	)
	if err != nil {
		return err
	}

	title := strings.TrimSpace(strings.ReplaceAll(response.Content, "\n", " "))
	if title == "" {
		return nil
	}

	// Re-read immediately before saving so an asynchronous title request cannot
	// overwrite usage or summary fields written while the model was running.
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	session.Title = title
	_, err = a.sessions.Save(ctx, session)
	return err
}

func (a *agent) err(err error) AgentEvent {
	return AgentEvent{
		Type:  AgentEventTypeError,
		Error: err,
	}
}

func (a *agent) acquireModel(ctx context.Context) (func(), error) {
	if a.modelSlots == nil {
		return func() {}, nil
	}
	select {
	case a.modelSlots <- struct{}{}:
		return func() { <-a.modelSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *agent) Run(ctx context.Context, sessionID string, content string, attachments ...message.Attachment) (<-chan AgentEvent, error) {
	if !a.provider.Model().SupportsAttachments && attachments != nil {
		attachments = nil
	}
	eventCh := make(chan AgentEvent, 1)
	genCtx, cancel := context.WithCancel(ctx)
	request := &activeRequest{cancel: cancel}
	if _, loaded := a.activeRequests.LoadOrStore(sessionID, request); loaded {
		cancel()
		return nil, ErrSessionBusy
	}
	go func() {
		logging.Debug("Request started", "sessionID", sessionID)
		result := a.err(errors.New("agent request ended without a result"))
		defer func() {
			if recovered := recover(); recovered != nil {
				logging.ErrorPersist(fmt.Sprintf("Panic in agent.Run: %v", recovered))
				result = a.err(fmt.Errorf("panic while running the agent"))
			}
			cancel()
			a.activeRequests.CompareAndDelete(sessionID, request)
			a.Publish(pubsub.CreatedEvent, result)
			eventCh <- result
			close(eventCh)
			logging.Debug("Request completed", "sessionID", sessionID)
		}()
		var attachmentParts []message.ContentPart
		for _, attachment := range attachments {
			attachmentParts = append(attachmentParts, message.BinaryContent{Path: attachment.FilePath, MIMEType: attachment.MimeType, Data: attachment.Content})
		}
		result = a.processGeneration(genCtx, sessionID, content, attachmentParts)
		if result.Error != nil && !errors.Is(result.Error, ErrRequestCancelled) && !errors.Is(result.Error, context.Canceled) {
			logging.ErrorPersist(result.Error.Error())
		}
	}()
	return eventCh, nil
}

func (a *agent) processGeneration(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) AgentEvent {
	cfg := config.Get()
	// List existing messages; if none, start title generation asynchronously.
	msgs, err := a.messages.List(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to list messages: %w", err))
	}
	if len(msgs) == 0 {
		go func() {
			defer logging.RecoverPanic("agent.Run", func() {
				logging.ErrorPersist("panic while generating title")
			})
			titleErr := a.generateTitle(context.Background(), sessionID, content)
			if titleErr != nil {
				logging.ErrorPersist(fmt.Sprintf("failed to generate title: %v", titleErr))
			}
		}()
	}
	session, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return a.err(fmt.Errorf("failed to get session: %w", err))
	}
	if session.SummaryMessageID != "" {
		summaryMsgIndex := -1
		for i, msg := range msgs {
			if msg.ID == session.SummaryMessageID {
				summaryMsgIndex = i
				break
			}
		}
		if summaryMsgIndex != -1 {
			msgs = append([]message.Message(nil), msgs[summaryMsgIndex:]...)
			preparedSummary, prepareErr := prepareSummaryForHistory(msgs[0])
			if prepareErr != nil {
				return a.err(fmt.Errorf("failed to prepare session summary: %w", prepareErr))
			}
			msgs[0] = preparedSummary
		}
	}

	userMsg, err := a.createUserMessage(ctx, sessionID, content, attachmentParts)
	if err != nil {
		return a.err(fmt.Errorf("failed to create user message: %w", err))
	}
	// Append the new user message to the conversation history.
	msgHistory := append(msgs, userMsg)
	guard := generationGuard{}

	for {
		if err := ctx.Err(); err != nil {
			return a.err(err)
		}
		if err := guard.beginStep(); err != nil {
			return a.err(err)
		}
		agentMessage, toolResults, err := a.streamAndHandleEvents(ctx, sessionID, msgHistory, &guard)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				agentMessage.AddFinish(message.FinishReasonCanceled)
				_ = a.messages.Update(context.Background(), agentMessage)
				return a.err(ErrRequestCancelled)
			}
			return a.err(fmt.Errorf("failed to process events: %w", err))
		}
		if cfg != nil && cfg.Debug {
			seqId := (len(msgHistory) + 1) / 2
			toolResultFilepath := logging.WriteToolResultsJson(sessionID, seqId, toolResults)
			logging.Info("Result", "message", agentMessage.FinishReason(), "toolResults", "{}", "filepath", toolResultFilepath)
		} else {
			logging.Info("Result", "message", agentMessage.FinishReason(), "toolResults", toolResults)
		}
		switch agentMessage.FinishReason() {
		case message.FinishReasonToolUse:
			if toolResults == nil {
				return a.err(fmt.Errorf("%w: tool-use response had no tool results", ErrIncomplete))
			}
			msgHistory = append(msgHistory, agentMessage, *toolResults)
			continue
		case message.FinishReasonEndTurn:
			return AgentEvent{
				Type:    AgentEventTypeResponse,
				Message: agentMessage,
				Done:    true,
			}
		case message.FinishReasonMaxTokens:
			return a.err(fmt.Errorf("%w: model reached its output-token limit", ErrIncomplete))
		case message.FinishReasonPermissionDenied:
			return a.err(permission.ErrorPermissionDenied)
		case message.FinishReasonCanceled:
			return a.err(ErrRequestCancelled)
		case message.FinishReasonError, message.FinishReasonUnknown, "":
			return a.err(fmt.Errorf("%w: invalid finish reason %q", ErrIncomplete, agentMessage.FinishReason()))
		default:
			return a.err(fmt.Errorf("%w: unsupported finish reason %q", ErrIncomplete, agentMessage.FinishReason()))
		}
	}
}

func prepareSummaryForHistory(summary message.Message) (message.Message, error) {
	if summary.Role != message.Assistant {
		return message.Message{}, fmt.Errorf("summary message has unexpected role %q", summary.Role)
	}
	prepared := summary
	prepared.Parts = append([]message.ContentPart(nil), summary.Parts...)
	for index, part := range prepared.Parts {
		text, ok := part.(message.TextContent)
		if !ok {
			continue
		}
		envelope, err := trust.DeriveUntrusted(text.Text, "agent.session-summary")
		if err != nil {
			return message.Message{}, err
		}
		block, err := envelope.PromptBlock()
		if err != nil {
			return message.Message{}, err
		}
		prepared.Parts[index] = message.TextContent{Text: "# Conversation Summary (Untrusted Derived Data)\n" + block}
		return prepared, nil
	}
	return message.Message{}, errors.New("summary message has no text content")
}

func (a *agent) createUserMessage(ctx context.Context, sessionID, content string, attachmentParts []message.ContentPart) (message.Message, error) {
	parts := []message.ContentPart{message.TextContent{Text: content}}
	parts = append(parts, attachmentParts...)
	return a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.User,
		Parts: parts,
	})
}

func (a *agent) streamAndHandleEvents(ctx context.Context, sessionID string, msgHistory []message.Message, guard *generationGuard) (message.Message, *message.Message, error) {
	ctx = context.WithValue(ctx, tools.SessionIDContextKey, sessionID)
	release, err := a.acquireModel(ctx)
	if err != nil {
		return message.Message{}, nil, err
	}
	defer release()
	started := time.Now().UTC()
	model := a.provider.Model()
	activeModel := model
	completed := false
	if a.bus != nil {
		requestEvent := events.New(events.ModelRequest, map[string]any{"sessionId": sessionID, "model": model.ID, "provider": model.Provider})
		requestEvent.SessionID, requestEvent.TaskID = sessionID, tools.GetTaskID(ctx)
		a.bus.Publish(requestEvent)
		defer func() {
			kind := events.ModelFailed
			data := map[string]any{"sessionId": sessionID, "model": activeModel.ID, "provider": activeModel.Provider, "durationMs": time.Since(started).Milliseconds()}
			if completed {
				kind = events.ModelResponse
			}
			responseEvent := events.New(kind, data)
			responseEvent.SessionID, responseEvent.TaskID = sessionID, tools.GetTaskID(ctx)
			a.bus.Publish(responseEvent)
		}()
	}
	eventChan := a.provider.StreamResponse(ctx, msgHistory, a.tools)

	assistantMsg, err := a.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{},
		Model: a.provider.Model().ID,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create assistant message: %w", err)
	}

	// Add the session and message ID into the context if needed by tools.
	ctx = context.WithValue(ctx, tools.MessageIDContextKey, assistantMsg.ID)

	// Process each event in the stream. A provider stream is successful only
	// after exactly one complete event and a subsequent channel close.
	terminalSeen := false
streamEvents:
	for {
		var event provider.ProviderEvent
		select {
		case <-ctx.Done():
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled)
			return assistantMsg, nil, ctx.Err()
		case next, open := <-eventChan:
			if !open {
				break streamEvents
			}
			event = next
		}
		if terminalSeen {
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonError)
			return assistantMsg, nil, errors.New("provider emitted an event after completion")
		}
		if event.Model.ID != "" && event.Model.ID != activeModel.ID {
			if a.bus != nil {
				fallbackEvent := events.New(events.ModelRequest, map[string]any{
					"sessionId":    sessionID,
					"model":        event.Model.ID,
					"provider":     event.Model.Provider,
					"fallback":     true,
					"fromModel":    activeModel.ID,
					"fromProvider": activeModel.Provider,
				})
				fallbackEvent.SessionID, fallbackEvent.TaskID = sessionID, tools.GetTaskID(ctx)
				a.bus.Publish(fallbackEvent)
			}
			activeModel = event.Model
		}
		if processErr := a.processEvent(ctx, sessionID, &assistantMsg, event, time.Since(started)); processErr != nil {
			finishReason := message.FinishReasonError
			if errors.Is(processErr, context.Canceled) || errors.Is(processErr, context.DeadlineExceeded) {
				finishReason = message.FinishReasonCanceled
			}
			a.finishMessage(context.Background(), &assistantMsg, finishReason)
			return assistantMsg, nil, processErr
		}
		if event.Type == provider.EventComplete {
			terminalSeen = true
		}
		if ctx.Err() != nil {
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled)
			return assistantMsg, nil, ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled)
		return assistantMsg, nil, err
	}
	if !terminalSeen {
		a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonError)
		return assistantMsg, nil, errors.New("provider stream ended without a completion event")
	}
	completed = true

	toolCalls := assistantMsg.ToolCalls()
	if assistantMsg.FinishReason() != message.FinishReasonToolUse {
		if len(toolCalls) > 0 {
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonError)
			return assistantMsg, nil, errors.New("provider returned tool calls without a tool-use finish reason")
		}
		return assistantMsg, nil, nil
	}
	if guard == nil {
		guard = &generationGuard{}
	}
	if err := guard.observeToolCalls(toolCalls); err != nil {
		a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonError)
		return assistantMsg, nil, err
	}
	toolResults, toolErr := a.executeToolCalls(ctx, toolCalls)
	if len(toolResults) == 0 {
		return assistantMsg, nil, toolErr
	}
	parts := make([]message.ContentPart, 0, len(toolResults))
	for _, tr := range toolResults {
		parts = append(parts, tr)
	}
	msg, err := a.messages.Create(context.Background(), assistantMsg.SessionID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: parts,
	})
	if err != nil {
		return assistantMsg, nil, fmt.Errorf("failed to create cancelled tool message: %w", err)
	}

	if toolErr != nil {
		if errors.Is(toolErr, permission.ErrorPermissionDenied) {
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonPermissionDenied)
		} else if errors.Is(toolErr, context.Canceled) || errors.Is(toolErr, context.DeadlineExceeded) {
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonCanceled)
		} else {
			a.finishMessage(context.Background(), &assistantMsg, message.FinishReasonError)
		}
		return assistantMsg, &msg, toolErr
	}

	return assistantMsg, &msg, nil
}

type preparedToolCall struct {
	call message.ToolCall
	tool tools.BaseTool
}

func (a *agent) executeToolCalls(ctx context.Context, calls []message.ToolCall) ([]message.ToolResult, error) {
	results := make([]message.ToolResult, len(calls))
	prepared := make([]preparedToolCall, len(calls))
	for i, call := range calls {
		prepared[i] = preparedToolCall{call: call, tool: a.lookupTool(call.Name)}
	}

	for i := 0; i < len(prepared); {
		if err := ctx.Err(); err != nil {
			fillCanceledToolResults(results, calls, i)
			return results, err
		}

		if !parallelSafeTool(prepared[i].tool) {
			result, terminalErr := executePreparedToolCall(ctx, prepared[i])
			results[i] = result
			i++
			if terminalErr != nil {
				fillCanceledToolResults(results, calls, i)
				return results, terminalErr
			}
			continue
		}

		end := i + 1
		for end < len(prepared) && parallelSafeTool(prepared[end].tool) {
			end++
		}
		terminalErr := a.executeParallelToolBatch(ctx, prepared[i:end], results[i:end])
		i = end
		if terminalErr != nil {
			fillCanceledToolResults(results, calls, i)
			return results, terminalErr
		}
	}
	return results, nil
}

func (a *agent) lookupTool(name string) tools.BaseTool {
	for _, availableTool := range a.tools {
		if availableTool.Info().Name == name {
			return availableTool
		}
	}
	return nil
}

func parallelSafeTool(tool tools.BaseTool) bool {
	if tool == nil {
		return true
	}
	info := tool.Info()
	if info.Name == AgentToolName || strings.EqualFold(info.Risk, "low") {
		return true
	}
	for _, required := range info.Permissions {
		if strings.EqualFold(required, "read-only") {
			return true
		}
	}
	return false
}

func (a *agent) executeParallelToolBatch(ctx context.Context, batch []preparedToolCall, results []message.ToolResult) error {
	limit := defaultMaxParallelTools
	if loaded := config.Get(); loaded != nil && loaded.Resources.MaxConcurrentTools > 0 {
		limit = loaded.Resources.MaxConcurrentTools
	}
	if limit > len(batch) {
		limit = len(batch)
	}
	if limit <= 1 {
		for i, prepared := range batch {
			result, err := executePreparedToolCall(ctx, prepared)
			results[i] = result
			if err != nil {
				for remaining := i + 1; remaining < len(batch); remaining++ {
					results[remaining] = canceledToolResult(batch[remaining].call.ID)
				}
				return err
			}
		}
		return nil
	}

	semaphore := make(chan struct{}, limit)
	errorsByIndex := make([]error, len(batch))
	var wait sync.WaitGroup
	for i, prepared := range batch {
		i, prepared := i, prepared
		wait.Add(1)
		go func() {
			defer wait.Done()
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results[i] = canceledToolResult(prepared.call.ID)
				errorsByIndex[i] = ctx.Err()
				return
			}
			results[i], errorsByIndex[i] = executePreparedToolCall(ctx, prepared)
		}()
	}
	wait.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			return err
		}
	}
	return nil
}

func executePreparedToolCall(ctx context.Context, prepared preparedToolCall) (result message.ToolResult, err error) {
	// Parallel tools run outside Run's goroutine, so recover at the tool
	// boundary and persist a terminal error with the original call identity.
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("tool %q panicked", prepared.call.Name)
			result = message.ToolResult{
				ToolCallID: prepared.call.ID,
				Name:       prepared.call.Name,
				Content:    err.Error(),
				IsError:    true,
			}
		}
	}()
	if prepared.tool == nil {
		return message.ToolResult{
			ToolCallID: prepared.call.ID,
			Content:    fmt.Sprintf("Tool not found: %s", prepared.call.Name),
			IsError:    true,
		}, nil
	}
	response, err := prepared.tool.Run(ctx, tools.ToolCall{
		ID:    prepared.call.ID,
		Name:  prepared.call.Name,
		Input: prepared.call.Input,
	})
	result = message.ToolResult{
		ToolCallID: prepared.call.ID,
		Name:       prepared.call.Name,
		Content:    response.Content,
		Metadata:   response.Metadata,
		IsError:    response.IsError,
	}
	remainingMediaBytes := tools.MaxToolMediaBytes
	for _, media := range response.Media {
		if media.MIMEType == "" || len(media.Data) == 0 || len(media.Data) > remainingMediaBytes {
			continue
		}
		data := append([]byte(nil), media.Data...)
		result.Media = append(result.Media, message.BinaryContent{MIMEType: media.MIMEType, Data: data})
		remainingMediaBytes -= len(data)
	}
	if err == nil {
		return result, nil
	}
	if errors.Is(err, permission.ErrorPermissionDenied) {
		result.Content = "Permission denied"
		result.Metadata = ""
		result.Media = nil
		result.IsError = true
		return result, err
	}
	if result.Content == "" {
		result.Content = err.Error()
	}
	result.IsError = true
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return result, err
	}
	// Ordinary tool failures are observations for the model to recover from,
	// rather than fatal errors that abort the entire agent turn.
	return result, nil
}

func fillCanceledToolResults(results []message.ToolResult, calls []message.ToolCall, start int) {
	for i := start; i < len(results); i++ {
		results[i] = canceledToolResult(calls[i].ID)
	}
}

func canceledToolResult(callID string) message.ToolResult {
	return message.ToolResult{
		ToolCallID: callID,
		Content:    "Tool execution canceled",
		IsError:    true,
	}
}

func (a *agent) finishMessage(ctx context.Context, msg *message.Message, finishReson message.FinishReason) {
	msg.AddFinish(finishReson)
	_ = a.messages.Update(ctx, *msg)
}

func (a *agent) processEvent(ctx context.Context, sessionID string, assistantMsg *message.Message, event provider.ProviderEvent, duration time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		// Continue processing.
	}

	switch event.Type {
	case provider.EventThinkingDelta:
		delta := event.Thinking
		if delta == "" {
			delta = event.Content
		}
		assistantMsg.AppendReasoningContent(delta)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventContentDelta:
		assistantMsg.AppendContent(event.Content)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventToolUseStart:
		if event.ToolCall == nil {
			return errors.New("tool-use start is missing tool call metadata")
		}
		assistantMsg.AddToolCall(*event.ToolCall)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventToolUseDelta:
		if event.ToolCall == nil {
			return errors.New("tool input delta is missing tool call metadata")
		}
		assistantMsg.AppendToolCallInput(event.ToolCall.ID, event.ToolCall.Input)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventToolUseStop:
		if event.ToolCall == nil {
			return errors.New("tool-use stop is missing tool call metadata")
		}
		assistantMsg.FinishToolCall(event.ToolCall.ID)
		return a.messages.Update(ctx, *assistantMsg)
	case provider.EventError:
		if event.Error == nil {
			return errors.New("provider returned an unspecified streaming error")
		}
		if errors.Is(event.Error, context.Canceled) {
			logging.InfoPersist(fmt.Sprintf("Event processing canceled for session: %s", sessionID))
			return context.Canceled
		}
		logging.ErrorPersist(event.Error.Error())
		return event.Error
	case provider.EventComplete:
		if event.Response == nil {
			return errors.New("provider completion is missing a response")
		}
		if event.Response.FinishReason == "" || event.Response.FinishReason == message.FinishReasonUnknown {
			return fmt.Errorf("provider completion has invalid finish reason %q", event.Response.FinishReason)
		}
		// Some transports deliver the text only in their completion envelope.
		// Preserve already streamed text without appending the full answer twice.
		if assistantMsg.Content().Text == "" && event.Response.Content != "" {
			assistantMsg.AppendContent(event.Response.Content)
		}
		assistantMsg.SetToolCalls(event.Response.ToolCalls)
		assistantMsg.AddFinish(event.Response.FinishReason)
		if event.Model.ID != "" {
			assistantMsg.Model = event.Model.ID
		}
		if err := a.messages.Update(ctx, *assistantMsg); err != nil {
			return fmt.Errorf("failed to update message: %w", err)
		}
		model := a.provider.Model()
		if event.Model.ID != "" {
			model = event.Model
		}
		return a.TrackUsageWithDuration(ctx, sessionID, model, event.Response.Usage, duration)
	}

	return nil
}

func (a *agent) TrackUsage(ctx context.Context, sessionID string, model models.Model, usage provider.TokenUsage) error {
	return a.TrackUsageWithDuration(ctx, sessionID, model, usage, 0)
}

func (a *agent) TrackUsageWithDuration(ctx context.Context, sessionID string, model models.Model, usage provider.TokenUsage, duration time.Duration) error {
	sess, err := a.sessions.Get(ctx, sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	cost := model.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
		model.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
		model.CostPer1MIn/1e6*float64(usage.InputTokens) +
		model.CostPer1MOut/1e6*float64(usage.OutputTokens)

	sess.Cost += cost
	sess.CompletionTokens += usage.OutputTokens
	sess.PromptTokens += usage.InputTokens + usage.CacheCreationTokens + usage.CacheReadTokens
	taskID := tools.GetTaskID(ctx)
	if recorder, ok := a.sessions.(interface {
		RecordUsage(context.Context, session.Usage) error
	}); ok {
		// Usage persistence is additive telemetry. A legacy or in-memory
		// session implementation may not provide it, so aggregate session
		// accounting remains authoritative when the detailed store is absent.
		_ = recorder.RecordUsage(ctx, session.Usage{
			SessionID:    sessionID,
			TaskID:       taskID,
			Model:        string(model.ID),
			Provider:     string(model.Provider),
			InputTokens:  usage.InputTokens + usage.CacheCreationTokens + usage.CacheReadTokens,
			OutputTokens: usage.OutputTokens,
			Cost:         cost,
			Duration:     duration,
			FinishedAt:   time.Now().UTC(),
		})
	}
	costBudgetExceeded := false
	taskCostBudgetExceeded := false
	taskCost := cost
	budget := 0.0
	taskBudget := 0.0
	if loaded := config.Get(); loaded != nil {
		budget = loaded.Governance.MaxCost
		if loaded.Budget.Session > 0 && (budget == 0 || loaded.Budget.Session < budget) {
			budget = loaded.Budget.Session
		}
		taskBudget = loaded.Budget.Task
	}
	if budget > 0 && sess.Cost > budget {
		costBudgetExceeded = true
	}
	if taskBudget > 0 {
		if recorder, ok := a.sessions.(interface {
			Usage(context.Context, string) ([]session.Usage, error)
		}); ok {
			if usageItems, usageErr := recorder.Usage(ctx, sessionID); usageErr == nil {
				taskCost = 0
				for _, item := range usageItems {
					if item.TaskID == taskID {
						taskCost += item.Cost
					}
				}
			}
		}
		taskCostBudgetExceeded = taskID != "" && taskCost > taskBudget
	}

	_, err = a.sessions.Save(ctx, sess)
	if err != nil {
		return fmt.Errorf("failed to save session: %w", err)
	}
	if costBudgetExceeded || taskCostBudgetExceeded {
		if taskCostBudgetExceeded {
			return fmt.Errorf("task cost budget exceeded: %.6f > %.6f", taskCost, taskBudget)
		}
		return fmt.Errorf("session cost budget exceeded: %.6f > %.6f", sess.Cost, budget)
	}
	return nil
}

func (a *agent) Update(agentName config.AgentName, modelID models.ModelID) (models.Model, error) {
	if a.IsBusy() {
		return models.Model{}, fmt.Errorf("cannot change model while processing requests")
	}

	if err := config.UpdateAgentModel(agentName, modelID); err != nil {
		return models.Model{}, fmt.Errorf("failed to update config: %w", err)
	}

	provider, err := createAgentProvider(agentName)
	if err != nil {
		return models.Model{}, fmt.Errorf("failed to create provider for model %s: %w", modelID, err)
	}

	a.provider = provider

	return a.provider.Model(), nil
}

func (a *agent) Summarize(ctx context.Context, sessionID string) error {
	if a.summarizeProvider == nil {
		return fmt.Errorf("summarize provider not available")
	}
	summarizeCtx, cancel := context.WithCancel(ctx)
	request := &activeRequest{cancel: cancel}
	if _, loaded := a.activeRequests.LoadOrStore(sessionID, request); loaded {
		cancel()
		return ErrSessionBusy
	}

	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				logging.ErrorPersist(fmt.Sprintf("Panic in agent.Summarize: %v", recovered))
				a.Publish(pubsub.CreatedEvent, AgentEvent{
					Type:  AgentEventTypeError,
					Error: fmt.Errorf("panic while summarizing the session"),
					Done:  true,
				})
			}
			cancel()
			a.activeRequests.CompareAndDelete(sessionID, request)
		}()
		event := AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Starting summarization...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		// Get all messages from the session
		msgs, err := a.messages.List(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to list messages: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		summarizeCtx = context.WithValue(summarizeCtx, tools.SessionIDContextKey, sessionID)

		if len(msgs) == 0 {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("no messages to summarize"),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Analyzing conversation...",
		}
		a.Publish(pubsub.CreatedEvent, event)

		// Add a system message to guide the summarization
		summarizePrompt := "Provide a detailed but concise summary of our conversation above. Focus on information that would be helpful for continuing the conversation, including what we did, what we're doing, which files we're working on, and what we're going to do next."

		// Create a new message with the summarize prompt
		promptMsg := message.Message{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: summarizePrompt}},
		}

		// Append the prompt to the messages
		msgsWithPrompt := append(msgs, promptMsg)

		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Generating summary...",
		}

		a.Publish(pubsub.CreatedEvent, event)

		// Send the messages to the summarize provider
		release, acquireErr := a.acquireModel(summarizeCtx)
		if acquireErr != nil {
			event = AgentEvent{Type: AgentEventTypeError, Error: acquireErr, Done: true}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		response, err := func() (*provider.ProviderResponse, error) {
			defer release()
			return a.summarizeProvider.SendMessages(
				summarizeCtx,
				msgsWithPrompt,
				make([]tools.BaseTool, 0),
			)
		}()
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to summarize: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		summary := strings.TrimSpace(response.Content)
		if summary == "" {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("empty summary returned"),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		event = AgentEvent{
			Type:     AgentEventTypeSummarize,
			Progress: "Creating new session...",
		}

		a.Publish(pubsub.CreatedEvent, event)
		oldSession, err := a.sessions.Get(summarizeCtx, sessionID)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to get session: %w", err),
				Done:  true,
			}

			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		// Create a message in the new session with the summary
		msg, err := a.messages.Create(summarizeCtx, oldSession.ID, message.CreateMessageParams{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.TextContent{Text: summary},
				message.Finish{
					Reason: message.FinishReasonEndTurn,
					Time:   time.Now().Unix(),
				},
			},
			Model: a.summarizeProvider.Model().ID,
		})
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to create summary message: %w", err),
				Done:  true,
			}

			a.Publish(pubsub.CreatedEvent, event)
			return
		}
		oldSession.SummaryMessageID = msg.ID
		oldSession.CompletionTokens = response.Usage.OutputTokens
		oldSession.PromptTokens = 0
		model := a.summarizeProvider.Model()
		usage := response.Usage
		cost := model.CostPer1MInCached/1e6*float64(usage.CacheCreationTokens) +
			model.CostPer1MOutCached/1e6*float64(usage.CacheReadTokens) +
			model.CostPer1MIn/1e6*float64(usage.InputTokens) +
			model.CostPer1MOut/1e6*float64(usage.OutputTokens)
		oldSession.Cost += cost
		_, err = a.sessions.Save(summarizeCtx, oldSession)
		if err != nil {
			event = AgentEvent{
				Type:  AgentEventTypeError,
				Error: fmt.Errorf("failed to save session: %w", err),
				Done:  true,
			}
			a.Publish(pubsub.CreatedEvent, event)
			return
		}

		event = AgentEvent{
			Type:      AgentEventTypeSummarize,
			SessionID: oldSession.ID,
			Progress:  "Summary complete",
			Done:      true,
		}
		a.Publish(pubsub.CreatedEvent, event)
		// Send final success event with the new session ID
	}()

	return nil
}

func createAgentProvider(agentName config.AgentName) (provider.Provider, error) {
	cfg := config.Get()
	agentConfig, ok := cfg.Agents[agentName]
	if !ok {
		return nil, fmt.Errorf("agent %s not found", agentName)
	}
	candidateIDs := []models.ModelID{agentConfig.Model}
	candidateIDs = append(candidateIDs, agentConfig.FallbackModels...)
	class := routingClassForAgent(agentName)
	routed := false
	requestTimeout := time.Duration(agentConfig.TimeoutMS) * time.Millisecond
	if rule, configured := cfg.Routing[string(class)]; configured && len(rule.Models) > 0 {
		routed = true
		if rule.MaxLatencyMS > 0 {
			routingTimeout := time.Duration(rule.MaxLatencyMS) * time.Millisecond
			if requestTimeout <= 0 || routingTimeout < requestTimeout {
				requestTimeout = routingTimeout
			}
		}
		candidates := make([]models.Model, 0, len(rule.Models))
		for _, modelID := range rule.Models {
			model, exists := models.SupportedModels[modelID]
			if !exists {
				return nil, fmt.Errorf("routing class %s specifies unsupported model %s", class, modelID)
			}
			candidates = append(candidates, model)
		}
		signals := routing.Signals{LatencyBudget: time.Duration(agentConfig.TimeoutMS) * time.Millisecond, CostBudget: rule.MaxCostPerMillion}
		ordered := routing.OrderModels(class, candidates, signals)
		candidateIDs = candidateIDs[:0]
		for _, model := range ordered {
			candidateIDs = append(candidateIDs, model.ID)
		}
	}
	if len(candidateIDs) == 0 {
		return nil, fmt.Errorf("agent %s has no model candidates", agentName)
	}
	primary, err := newConfiguredProvider(cfg, agentName, agentConfig, candidateIDs[0])
	if err != nil {
		return nil, err
	}
	primary = provider.WithTimeout(primary, requestTimeout)
	fallbacks := make([]provider.Provider, 0, len(candidateIDs)-1)
	for _, fallbackModel := range candidateIDs[1:] {
		fallback, fallbackErr := newConfiguredProvider(cfg, agentName, agentConfig, fallbackModel)
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		fallback = provider.WithTimeout(fallback, requestTimeout)
		fallbacks = append(fallbacks, fallback)
	}
	if len(fallbacks) == 0 {
		return primary, nil
	}
	if routed {
		combined, routeErr := provider.NewRoutedProvider(primary, fallbacks...)
		if routeErr != nil {
			return nil, fmt.Errorf("could not configure task-class routing: %w", routeErr)
		}
		return combined, nil
	}
	combined, err := provider.NewFallbackProvider(primary, fallbacks...)
	if err != nil {
		return nil, fmt.Errorf("could not configure model fallback: %w", err)
	}
	return combined, nil
}

func routingClassForAgent(agentName config.AgentName) routing.TaskClass {
	switch agentName {
	case config.AgentTitle, config.AgentSummarizer:
		return routing.Fast
	case config.AgentTask:
		return routing.Reasoning
	default:
		return routing.Coding
	}
}

func newConfiguredProvider(cfg *config.Config, agentName config.AgentName, agentConfig config.Agent, modelID models.ModelID) (provider.Provider, error) {
	model, ok := models.SupportedModels[modelID]
	if !ok {
		return nil, fmt.Errorf("model %s not supported", modelID)
	}
	providerCfg, ok := cfg.Providers[model.Provider]
	if !ok {
		return nil, fmt.Errorf("provider %s not supported", model.Provider)
	}
	if providerCfg.Disabled {
		return nil, fmt.Errorf("provider %s is not enabled", model.Provider)
	}
	// Provider construction does not yet know the rendered request's token
	// footprint, so request-time context clamping remains a separate concern.
	// Enforce the candidate's declared static limits here so a fallback never
	// inherits a larger primary-model output budget.
	maxTokens := model.EffectiveMaxTokens(agentConfig.MaxTokens, 0)
	if maxTokens <= 0 {
		return nil, fmt.Errorf(
			"model %s has unusable token limits (context window %d, default max tokens %d)",
			modelID,
			model.ContextWindow,
			model.DefaultMaxTokens,
		)
	}
	opts := []provider.ProviderClientOption{
		provider.WithAPIKey(providerCfg.APIKey),
		provider.WithModel(model),
		provider.WithSystemMessage(prompt.GetAgentPrompt(agentName, model.Provider)),
		provider.WithMaxTokens(maxTokens),
	}
	if model.Provider == models.ProviderOpenAI || model.Provider == models.ProviderLocal && model.CanReason {
		opts = append(
			opts,
			provider.WithOpenAIOptions(
				provider.WithReasoningEffort(agentConfig.ReasoningEffort),
			),
		)
	} else if model.Provider == models.ProviderAnthropic && model.CanReason && agentName == config.AgentCoder {
		opts = append(
			opts,
			provider.WithAnthropicOptions(
				provider.WithAnthropicShouldThinkFn(provider.DefaultShouldThinkFn),
			),
		)
	}
	agentProvider, err := provider.NewProvider(
		model.Provider,
		opts...,
	)
	if err != nil {
		return nil, fmt.Errorf("could not create provider for %s: %v", modelID, err)
	}

	return agentProvider, nil
}
