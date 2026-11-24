// Package events defines the versioned event contract shared by the CLI, TUI,
// workflow runtime, HTTP/SSE clients, hooks, and replay tooling.
package events

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/security"
)

const Version = 1

type Type string

const (
	SessionStarted        Type = "session.started"
	SessionCompleted      Type = "session.completed"
	SessionDeleted        Type = "session.deleted"
	GoalCreated           Type = "goal.created"
	GoalStarted           Type = "goal.started"
	GoalCompleted         Type = "goal.completed"
	GoalFailed            Type = "goal.failed"
	TaskCreated           Type = "task.created"
	TaskStarted           Type = "task.started"
	TaskCompleted         Type = "task.completed"
	TaskFailed            Type = "task.failed"
	TaskBlocked           Type = "task.blocked"
	TaskCanceled          Type = "task.cancelled"
	TaskRetrying          Type = "task.retrying"
	TaskPaused            Type = "task.paused"
	RequirementUpdated    Type = "requirement.updated"
	TasksUpdated          Type = "tasks.updated"
	AgentCreated          Type = "agent.created"
	AgentStarted          Type = "agent.started"
	AgentPaused           Type = "agent.paused"
	AgentCompleted        Type = "agent.completed"
	AgentFailed           Type = "agent.failed"
	AgentCanceled         Type = "agent.cancelled"
	ToolBefore            Type = "tool.before"
	ToolAfter             Type = "tool.after"
	ToolFailed            Type = "tool.failed"
	TestStarted           Type = "test.started"
	TestPassed            Type = "test.passed"
	TestFailed            Type = "test.failed"
	CheckpointCreated     Type = "checkpoint.created"
	CheckpointRestored    Type = "checkpoint.restored"
	SecurityFinding       Type = "security.finding"
	ModelRequest          Type = "model.request"
	ModelResponse         Type = "model.response"
	ModelFailed           Type = "model.failed"
	SandboxAllowed        Type = "sandbox.allowed"
	SandboxRequested      Type = "sandbox.request"
	SandboxDenied         Type = "sandbox.denied"
	SandboxFailed         Type = "sandbox.failed"
	HookStarted           Type = "hook.started"
	HookCompleted         Type = "hook.completed"
	HookFailed            Type = "hook.failed"
	InterviewStarted      Type = "interview.started"
	InterviewCompleted    Type = "interview.completed"
	PlanCreated           Type = "plan.created"
	PlanUpdated           Type = "plan.updated"
	SpecCreated           Type = "spec.created"
	SpecUpdated           Type = "spec.updated"
	SpecDriftDetected     Type = "spec.drift_detected"
	AgentResumed          Type = "agent.resumed"
	AgentWaiting          Type = "agent.waiting"
	AgentBlocked          Type = "agent.blocked"
	AgentMessage          Type = "agent.message"
	AgentProgress         Type = "agent.progress"
	MemoryStored          Type = "memory.stored"
	MemoryRetrieved       Type = "memory.retrieved"
	MemoryUpdated         Type = "memory.updated"
	SourceDiscovered      Type = "source.discovered"
	EvidenceAdded         Type = "evidence.added"
	ResearchCompleted     Type = "research.completed"
	ReviewStarted         Type = "review.started"
	ReviewCompleted       Type = "review.completed"
	FileBeforeWrite       Type = "file.before_write"
	FileAfterWrite        Type = "file.after_write"
	SecurityScanStarted   Type = "security.scan.started"
	SecurityScanCompleted Type = "security.scan.completed"
	GitOperation          Type = "git.operation"
	GitConflict           Type = "git.conflict"
	PermissionRequested   Type = "permission.requested"
	PermissionGranted     Type = "permission.granted"
	PermissionDenied      Type = "permission.denied"
	PermissionAutoGranted Type = "permission.auto_granted"
)

type Event struct {
	ID        string         `json:"id"`
	Sequence  int64          `json:"sequence,omitempty"`
	Version   int            `json:"version"`
	Type      Type           `json:"type"`
	At        time.Time      `json:"at"`
	SessionID string         `json:"sessionId,omitempty"`
	GoalID    string         `json:"goalId,omitempty"`
	TaskID    string         `json:"taskId,omitempty"`
	Actor     string         `json:"actor,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// Sink receives the same versioned event stream as interactive subscribers.
// Implementations may persist, notify, or export events, but they must not
// mutate the event or bypass the policy boundary that produced it.
type Sink interface {
	Append(context.Context, Event) error
}

// SequencedSink may assign the durable sequence to the event before the bus
// broadcasts it. This lets live clients resume from the same cursor used by
// durable replay without exposing storage details to ordinary sinks.
type SequencedSink interface {
	AppendEvent(context.Context, *Event) error
}

func New(kind Type, data map[string]any) Event {
	return Event{ID: uuid.NewString(), Version: Version, Type: kind, At: time.Now().UTC(), Data: redactMap(data)}
}
func (e Event) JSON() ([]byte, error) { return json.Marshal(e) }

func redactMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		if sensitiveKey(key) {
			result[key] = "[REDACTED]"
			continue
		}
		result[key] = redactValue(value)
	}
	return result
}

func sensitiveKey(key string) bool {
	// Credential field names commonly use snake_case, kebab-case, or dotted
	// names in addition to camelCase.
	lower := strings.Map(func(r rune) rune {
		switch r {
		case '_', '-', '.', ' ':
			return -1
		default:
			return r
		}
	}, strings.ToLower(key))
	for _, marker := range []string{"secret", "password", "credential", "privatekey", "apikey", "authorization", "accesstoken", "refreshtoken"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return lower == "token"
}

func redactValue(value any) any {
	switch typed := value.(type) {
	case string:
		return security.RedactText(typed)
	case map[string]any:
		return redactMap(typed)
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = redactValue(item)
		}
		return result
	default:
		// Payloads also contain typed maps, slices, structs, and JSON marshalers.
		// Inspect their JSON representation so nested credentials receive the
		// same protection as map[string]any without mutating the caller's data.
		data, err := json.Marshal(value)
		if err != nil {
			return value
		}
		var normalized any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&normalized); err != nil {
			return value
		}
		switch normalized := normalized.(type) {
		case map[string]any, []any:
			return redactValue(normalized)
		case string:
			if redacted := security.RedactText(normalized); redacted != normalized {
				return redacted
			}
		}
		return value
	}
}

type Bus struct {
	mu          sync.RWMutex
	subscribers map[chan Event]struct{}
	sinks       []Sink
	closed      bool
}

func NewBus() *Bus { return &Bus{subscribers: make(map[chan Event]struct{})} }

// AddSink attaches a durable or external event sink. It is safe to call
// during application startup before agents begin publishing events.
func (b *Bus) AddSink(sink Sink) {
	if b == nil || sink == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.sinks = append(b.sinks, sink)
	}
}
func (b *Bus) Subscribe() (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 128)
	if b.closed {
		close(ch)
		return ch, func() {}
	}
	b.subscribers[ch] = struct{}{}
	return ch, func() { b.remove(ch) }
}
func (b *Bus) remove(ch chan Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.subscribers[ch]; ok {
		delete(b.subscribers, ch)
		close(ch)
	}
}
func (b *Bus) Publish(event Event) bool {
	return b.PublishContext(context.Background(), event)
}

// PublishContext persists an event before delivering it to subscribers. A
// failed sink is reported through the return value, while subscribers still
// receive the event so a telemetry failure cannot silently stop the workflow.
func (b *Bus) PublishContext(ctx context.Context, event Event) bool {
	// Serialize persistence and broadcast together. Concurrent readers could
	// otherwise publish a later durable sequence before an earlier sink call
	// returns, causing clients that resume from a cursor to skip events.
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return false
	}
	return b.publishLocked(ctx, &event, nil)
}

// PublishTransaction serializes a domain mutation and its committed event with
// other publications. The callback must return only after committing and must
// not call back into this bus. An error means the mutation did not commit;
// deliveryOK reports postcommit sink failures without making a committed
// mutation look safe to retry. A canceled context prevents the callback from
// starting, but cancellation after commit does not suppress live delivery.
//
// When the returned event has a durable sequence, SQLLog sinks using the same
// database as committedLog are skipped because the callback already persisted
// that event. Other sinks still receive it. Without a sequence all sinks run.
func (b *Bus) PublishTransaction(ctx context.Context, committedLog *SQLLog, commit func(context.Context) (Event, error)) (event Event, deliveryOK bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return Event{}, false, errors.New("event bus is closed")
	}
	if err := ctx.Err(); err != nil {
		return Event{}, false, err
	}
	if commit == nil {
		return Event{}, false, errors.New("event transaction callback is required")
	}
	event, err = commit(ctx)
	if err != nil {
		return Event{}, false, err
	}
	deliveryOK = b.publishLocked(ctx, &event, committedLog)
	return event, deliveryOK, nil
}

// publishLocked requires b.mu and receives only events ready for delivery.
func (b *Bus) publishLocked(ctx context.Context, event *Event, committedLog *SQLLog) bool {
	event.Data = redactMap(event.Data)
	committed := event.Sequence > 0
	persisted := true
	for _, sink := range b.sinks {
		if log, ok := sink.(*SQLLog); ok && committed && committedLog != nil && log.db == committedLog.db {
			continue
		}
		var err error
		if sequenced, ok := sink.(SequencedSink); ok {
			if committed {
				// Additional stores may have different local sequences. Keep the
				// committed primary cursor for live clients and ordinary sinks.
				copy := *event
				err = sequenced.AppendEvent(ctx, &copy)
			} else {
				err = sequenced.AppendEvent(ctx, event)
			}
		} else {
			err = sink.Append(ctx, *event)
		}
		if err != nil {
			persisted = false
		}
	}
	for ch := range b.subscribers {
		select {
		case ch <- *event:
		default:
		}
	}
	return persisted
}
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for ch := range b.subscribers {
		close(ch)
	}
	b.subscribers = make(map[chan Event]struct{})
}
