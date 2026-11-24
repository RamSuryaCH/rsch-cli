// Package hooks dispatches repository-defined hooks from the versioned event bus.
// Hook code is always executed by project.Runtime, so it inherits the same
// capability, approval, timeout, and sandbox policy as other resources.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/project"
	"github.com/opencode-ai/opencode/internal/security"
)

var (
	ErrFailedClosed          = errors.New("hook failure blocked event dispatch")
	ErrPreToolEventRequired  = errors.New("pre-tool interceptor requires a tool.before event")
	ErrToolOperationRequired = errors.New("pre-tool interceptor requires a tool operation")
)

const (
	asyncFailureHistoryLimit = 64
	interceptedEventLimit    = 256
)

type Result struct {
	ResourceID string         `json:"resourceId"`
	EventType  events.Type    `json:"eventType"`
	Execution  project.Result `json:"execution"`
	Err        string         `json:"error,omitempty"`
}

// DispatchFailure is a redacted, bounded record of an asynchronous dispatch
// error. Synchronous callers receive errors directly from Dispatch or the
// pre-tool interceptor and therefore do not need this side channel.
type DispatchFailure struct {
	EventID   string      `json:"eventId,omitempty"`
	EventType events.Type `json:"eventType"`
	At        time.Time   `json:"at"`
	Results   []Result    `json:"results,omitempty"`
	Error     string      `json:"error"`
}

// ErrorReporter receives asynchronous dispatch failures. Reporters should be
// non-blocking; they run on the manager's event-consumer goroutine.
type ErrorReporter func(DispatchFailure)

// ToolOperation is invoked only after all matching pre-tool hooks permit the
// operation. A closure can capture the caller's typed execution request.
type ToolOperation func(context.Context) error

// ToolInterceptor is suitable for wiring directly around a tool execution
// boundary. A fail-closed hook error prevents the operation from running.
type ToolInterceptor func(context.Context, events.Event, ToolOperation) error

type Manager struct {
	runtime          project.Runtime
	bus              *events.Bus
	hooks            []project.Resource
	mu               sync.RWMutex
	subscription     *subscription
	reporter         ErrorReporter
	failures         []DispatchFailure
	intercepted      map[string]struct{}
	interceptedOrder []string
}

type subscription struct {
	stop func()
	done chan struct{}
}

func New(root string, runtime project.Runtime, bus *events.Bus) (*Manager, error) {
	resources, err := project.Discover(root)
	if err != nil {
		return nil, err
	}
	items := append([]project.Resource(nil), resources.Hooks...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Path < items[j].Path })
	return &Manager{runtime: runtime, bus: bus, hooks: items, intercepted: make(map[string]struct{})}, nil
}

// Dispatch runs matching hooks in path order. A hook defaults to fail-closed;
// failurePolicy=fail-open records the failure and permits later hooks.
func (m *Manager) Dispatch(ctx context.Context, event events.Event) ([]Result, error) {
	if m == nil {
		return nil, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	payload, err := event.JSON()
	if err != nil {
		return nil, err
	}
	// Event payloads may contain provider output or user-supplied metadata.
	eventJSON := security.RedactText(string(payload))
	m.mu.RLock()
	items := append([]project.Resource(nil), m.hooks...)
	m.mu.RUnlock()
	results := make([]Result, 0)
	for _, resource := range items {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		if !matches(resource.Manifest, event.Type) {
			continue
		}
		if m.bus != nil {
			m.bus.Publish(events.New(events.HookStarted, map[string]any{"hookId": resource.Manifest.ID, "eventType": event.Type}))
		}
		invocation := project.Invocation{ResourceID: resource.Manifest.ID, Kind: resource.Kind, EventJSON: eventJSON}
		execution, runErr := m.runtime.Run(ctx, resource, invocation)
		execution.Output = security.RedactText(execution.Output)
		result := Result{ResourceID: resource.Manifest.ID, EventType: event.Type, Execution: execution}
		if runErr != nil {
			result.Err = security.RedactText(runErr.Error())
			results = append(results, result)
			if m.bus != nil {
				m.bus.Publish(events.New(events.HookFailed, map[string]any{"hookId": resource.Manifest.ID, "eventType": event.Type, "error": result.Err}))
			}
			if failurePolicy(resource.Manifest) != "fail-open" {
				return results, fmt.Errorf("%w: %s: %s", ErrFailedClosed, resource.Manifest.ID, result.Err)
			}
			continue
		}
		results = append(results, result)
		if m.bus != nil {
			m.bus.Publish(events.New(events.HookCompleted, map[string]any{"hookId": resource.Manifest.ID, "eventType": event.Type, "durationMs": execution.FinishedAt.Sub(execution.StartedAt).Milliseconds()}))
		}
	}
	return results, ctx.Err()
}

// BeforeTool synchronously dispatches a tool.before event. The event should be
// the same value the caller later publishes to the event bus; its ID is used to
// prevent the backward-compatible async subscriber from running it twice.
func (m *Manager) BeforeTool(ctx context.Context, event events.Event) error {
	if event.Type != events.ToolBefore {
		return ErrPreToolEventRequired
	}
	if m == nil {
		return nil
	}
	m.markIntercepted(event.ID)
	_, err := m.Dispatch(ctx, event)
	return err
}

// InterceptTool runs pre-tool hooks and then invokes operation only when they
// permit execution. Hook failures are fail-closed unless the manifest opts in
// to failurePolicy=fail-open.
func (m *Manager) InterceptTool(ctx context.Context, event events.Event, operation ToolOperation) error {
	if operation == nil {
		return ErrToolOperationRequired
	}
	if err := m.BeforeTool(ctx, event); err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return operation(ctx)
}

// Interceptor returns the manager's pre-tool middleware as a first-class value
// for execution pipelines that accept an interceptor callback.
func (m *Manager) Interceptor() ToolInterceptor {
	return m.InterceptTool
}

// SetErrorReporter installs an optional observer for asynchronous dispatch
// failures. Failures are recorded even when no reporter is configured.
func (m *Manager) SetErrorReporter(reporter ErrorReporter) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.reporter = reporter
	m.mu.Unlock()
}

// Failures returns a snapshot of recent asynchronous dispatch failures.
func (m *Manager) Failures() []DispatchFailure {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	failures := make([]DispatchFailure, len(m.failures))
	for index, failure := range m.failures {
		failures[index] = cloneDispatchFailure(failure)
	}
	return failures
}

func cloneDispatchFailure(failure DispatchFailure) DispatchFailure {
	failure.Results = append([]Result(nil), failure.Results...)
	return failure
}

func (m *Manager) markIntercepted(eventID string) {
	if eventID == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.intercepted == nil {
		m.intercepted = make(map[string]struct{})
	}
	if _, exists := m.intercepted[eventID]; exists {
		return
	}
	m.intercepted[eventID] = struct{}{}
	m.interceptedOrder = append(m.interceptedOrder, eventID)
	if len(m.interceptedOrder) > interceptedEventLimit {
		oldest := m.interceptedOrder[0]
		m.interceptedOrder = m.interceptedOrder[1:]
		delete(m.intercepted, oldest)
	}
}

func (m *Manager) consumeIntercepted(eventID string) bool {
	if eventID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.intercepted[eventID]; !exists {
		return false
	}
	delete(m.intercepted, eventID)
	for index, candidate := range m.interceptedOrder {
		if candidate == eventID {
			m.interceptedOrder = append(m.interceptedOrder[:index], m.interceptedOrder[index+1:]...)
			break
		}
	}
	return true
}

func (m *Manager) recordAsyncFailure(event events.Event, results []Result, err error) {
	if m == nil || err == nil {
		return
	}
	failure := DispatchFailure{
		EventID:   event.ID,
		EventType: event.Type,
		At:        time.Now().UTC(),
		Results:   append([]Result(nil), results...),
		Error:     security.RedactText(err.Error()),
	}
	m.mu.Lock()
	m.failures = append(m.failures, failure)
	if len(m.failures) > asyncFailureHistoryLimit {
		copy(m.failures, m.failures[len(m.failures)-asyncFailureHistoryLimit:])
		m.failures = m.failures[:asyncFailureHistoryLimit]
	}
	reporter := m.reporter
	m.mu.Unlock()
	logging.Error("asynchronous hook dispatch failed", "eventId", event.ID, "eventType", event.Type, "error", failure.Error)
	if reporter != nil {
		reportAsyncFailure(reporter, cloneDispatchFailure(failure))
	}
}

func reportAsyncFailure(reporter ErrorReporter, failure DispatchFailure) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logging.Error("asynchronous hook error reporter panicked", "panic", fmt.Sprint(recovered))
		}
	}()
	reporter(failure)
}

// Start subscribes the manager to the event bus. Dispatch remains available
// for synchronous callers that need fail-closed semantics.
func (m *Manager) Start(ctx context.Context) {
	if m == nil || m.bus == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	if m.subscription != nil {
		m.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(ctx)
	ch, unsubscribe := m.bus.Subscribe()
	current := &subscription{
		stop: func() {
			cancel()
			unsubscribe()
		},
		done: make(chan struct{}),
	}
	m.subscription = current
	m.mu.Unlock()
	go func() {
		defer func() {
			current.stop()
			m.mu.Lock()
			if m.subscription == current {
				m.subscription = nil
			}
			m.mu.Unlock()
			close(current.done)
		}()
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-ch:
				if !ok || ctx.Err() != nil {
					return
				}
				if strings.HasPrefix(string(event.Type), "hook.") {
					continue
				}
				if event.Type == events.ToolBefore && m.consumeIntercepted(event.ID) {
					continue
				}
				results, err := m.Dispatch(ctx, event)
				if err != nil && ctx.Err() == nil {
					m.recordAsyncFailure(event, results, err)
				}
			}
		}
	}()
}

// Close cancels active asynchronous hooks and discards queued dispatches.
// A later Start creates an independent subscription.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	current := m.subscription
	m.subscription = nil
	m.mu.Unlock()
	if current != nil {
		current.stop()
	}
}

func matches(manifest *project.Manifest, typ events.Type) bool {
	if manifest == nil {
		return false
	}
	for _, candidate := range manifest.Events {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "*" || candidate == strings.ToLower(string(typ)) || aliasMatches(candidate, typ) {
			return true
		}
	}
	return false
}

func aliasMatches(alias string, typ events.Type) bool {
	aliases := map[string]events.Type{
		"beforesession": events.SessionStarted, "aftersession": events.SessionCompleted,
		"beforegoal": events.GoalStarted, "aftergoal": events.GoalCompleted,
		"beforeagent": events.AgentStarted, "afteragent": events.AgentCompleted,
		"beforetool": events.ToolBefore, "aftertool": events.ToolAfter,
		"beforetask": events.TaskStarted, "aftertask": events.TaskCompleted,
		"beforetest": events.TestStarted, "aftertest": events.TestPassed,
		"beforeverification": events.TestStarted, "afterverification": events.TestPassed,
		"beforecommit": events.ToolBefore, "aftercommit": events.ToolAfter,
		"beforereview": events.ToolBefore, "afterreview": events.ToolAfter,
		"beforewrite": events.ToolBefore, "afterwrite": events.ToolAfter,
		"beforecommand": events.ToolBefore, "aftercommand": events.ToolAfter,
	}
	want, ok := aliases[strings.ReplaceAll(alias, "-", "")]
	return ok && want == typ
}

func failurePolicy(manifest *project.Manifest) string {
	if manifest == nil || strings.TrimSpace(manifest.FailurePolicy) == "" {
		return "fail-closed"
	}
	policy := strings.ToLower(strings.TrimSpace(manifest.FailurePolicy))
	if policy == "fail-open" {
		return policy
	}
	return "fail-closed"
}

// ValidateEventJSON is useful to callers that persist hook evidence.
func ValidateEventJSON(value string) error {
	var event events.Event
	if err := json.Unmarshal([]byte(value), &event); err != nil {
		return err
	}
	if event.Type == "" {
		return errors.New("event type is required")
	}
	return nil
}
