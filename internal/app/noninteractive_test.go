package app

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/format"
	"github.com/opencode-ai/opencode/internal/llm/agent"
	"github.com/opencode-ai/opencode/internal/llm/models"
	"github.com/opencode-ai/opencode/internal/message"
	"github.com/opencode-ai/opencode/internal/permission"
	"github.com/opencode-ai/opencode/internal/pubsub"
	"github.com/opencode-ai/opencode/internal/session"
)

type nonInteractiveAgent struct {
	broker       *pubsub.Broker[agent.AgentEvent]
	sessions     session.Service
	result       agent.AgentEvent
	block        bool
	cancelled    atomic.Bool
	lastPrompt   string
	beforeResult func(context.Context, string) error
}

func (a *nonInteractiveAgent) Subscribe(ctx context.Context) <-chan pubsub.Event[agent.AgentEvent] {
	return a.broker.Subscribe(ctx)
}
func (a *nonInteractiveAgent) Model() models.Model {
	return models.Model{ID: "test-model", Provider: models.ProviderMock}
}
func (a *nonInteractiveAgent) Run(ctx context.Context, sessionID, content string, _ ...message.Attachment) (<-chan agent.AgentEvent, error) {
	a.lastPrompt = content
	if a.beforeResult != nil {
		if err := a.beforeResult(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	result := make(chan agent.AgentEvent, 1)
	if a.block {
		return result, nil
	}
	item, err := a.sessions.Get(context.Background(), sessionID)
	if err != nil {
		return nil, err
	}
	item.PromptTokens = 12
	item.CompletionTokens = 4
	item.Cost = 0.125
	if _, err := a.sessions.Save(context.Background(), item); err != nil {
		return nil, err
	}
	result <- a.result
	close(result)
	return result, nil
}
func (a *nonInteractiveAgent) Cancel(string)             { a.cancelled.Store(true) }
func (a *nonInteractiveAgent) IsSessionBusy(string) bool { return false }
func (a *nonInteractiveAgent) IsBusy() bool              { return false }
func (a *nonInteractiveAgent) Update(_ config.AgentName, _ models.ModelID) (models.Model, error) {
	return a.Model(), nil
}
func (a *nonInteractiveAgent) Summarize(context.Context, string) error { return nil }

func newNonInteractiveTestApp(t *testing.T, fake *nonInteractiveAgent) *App {
	t.Helper()
	application, _ := newNonInteractiveTestAppWithDB(t, fake)
	return application
}

func newNonInteractiveTestAppWithDB(t *testing.T, fake *nonInteractiveAgent) (*App, *sql.DB) {
	t.Helper()
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	sessions := session.NewServiceWithDB(db.New(connection), connection)
	fake.sessions = sessions
	return &App{
		Sessions:    sessions,
		Permissions: permission.NewPermissionService(),
		CoderAgent:  fake,
		Workflow:    engineering.NewSQLStore(connection),
		EventBus:    events.NewBus(),
	}, connection
}

func TestRunNonInteractiveJSONLWritesVersionedTerminalMetadata(t *testing.T) {
	fake := &nonInteractiveAgent{
		broker: pubsub.NewBroker[agent.AgentEvent](),
		result: agent.AgentEvent{Type: agent.AgentEventTypeResponse, Done: true, Message: message.Message{
			ID: "message-1",
			Parts: []message.ContentPart{
				message.TextContent{Text: "complete"},
				message.Finish{Reason: message.FinishReasonEndTurn},
			},
		}},
	}
	application := newNonInteractiveTestApp(t, fake)
	defer application.EventBus.Close()
	var stdout, stderr bytes.Buffer
	if err := application.RunNonInteractiveWithOptions(context.Background(), "do work", NonInteractiveOptions{
		OutputFormat: format.JSONL,
		Stdout:       &stdout,
		Stderr:       &stderr,
		AutoApprove:  true,
	}); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("structured mode wrote progress to stderr: %q", stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected start and terminal records, got %d: %q", len(lines), stdout.String())
	}
	var started, completed format.ExecutionEvent
	if err := json.Unmarshal([]byte(lines[0]), &started); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &completed); err != nil {
		t.Fatal(err)
	}
	if started.Type != "run.started" || started.Sequence != 1 || started.Status != "in_progress" {
		t.Fatalf("unexpected start event: %+v", started)
	}
	if completed.Type != "run.completed" || completed.Sequence != 2 || completed.Status != "completed" {
		t.Fatalf("unexpected terminal event: %+v", completed)
	}
	if completed.SchemaVersion != format.ExecutionSchemaVersion || completed.SessionID == "" || completed.GoalID == "" || completed.TaskID == "" {
		t.Fatalf("terminal event lacks stable identity: %+v", completed)
	}
	if completed.MessageID != "message-1" || completed.Response != "complete" || completed.FinishReason != "end_turn" {
		t.Fatalf("terminal result metadata is incomplete: %+v", completed)
	}
	if completed.Usage.InputTokens != 12 || completed.Usage.OutputTokens != 4 || completed.Usage.CostUSD != 0.125 {
		t.Fatalf("unexpected usage: %+v", completed.Usage)
	}
}

func TestRunNonInteractiveCancellationIsNonZeroAndTerminatesJSONL(t *testing.T) {
	fake := &nonInteractiveAgent{broker: pubsub.NewBroker[agent.AgentEvent](), block: true}
	application := newNonInteractiveTestApp(t, fake)
	defer application.EventBus.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var stdout bytes.Buffer
	err := application.RunNonInteractiveWithOptions(ctx, "wait forever", NonInteractiveOptions{
		OutputFormat: format.JSONL,
		Stdout:       &stdout,
		Stderr:       &bytes.Buffer{},
		AutoApprove:  true,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if !fake.cancelled.Load() {
		t.Fatal("agent was not cancelled")
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected start and terminal records, got %q", stdout.String())
	}
	var terminal format.ExecutionEvent
	if err := json.Unmarshal([]byte(lines[1]), &terminal); err != nil {
		t.Fatal(err)
	}
	if terminal.Type != "run.cancelled" || terminal.Status != "cancelled" || terminal.Error == "" {
		t.Fatalf("unexpected cancellation event: %+v", terminal)
	}
}

func TestRunNonInteractiveDeniesEveryApprovalRequest(t *testing.T) {
	for _, parallel := range []bool{false, true} {
		name := "sequential"
		if parallel {
			name = "parallel"
		}
		t.Run(name, func(t *testing.T) {
			fake := &nonInteractiveAgent{broker: pubsub.NewBroker[agent.AgentEvent]()}
			application := newNonInteractiveTestApp(t, fake)
			defer application.EventBus.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var approved atomic.Bool
			fake.beforeResult = func(ctx context.Context, sessionID string) error {
				var requests sync.WaitGroup
				request := func() {
					defer requests.Done()
					if application.Permissions.RequestContext(ctx, permission.CreatePermissionRequest{
						SessionID: sessionID, ToolName: "write", Action: "write", Path: "/workspace/file.go",
					}) {
						approved.Store(true)
					}
				}
				for range 3 {
					requests.Add(1)
					if parallel {
						go request()
					} else {
						request()
					}
				}
				requests.Wait()
				return ctx.Err()
			}
			var stdout bytes.Buffer
			err := application.RunNonInteractiveWithOptions(ctx, "write files", NonInteractiveOptions{
				OutputFormat: format.JSONL, Stdout: &stdout,
			})
			if ctx.Err() != nil {
				t.Fatalf("approval requests waited until timeout: %v", err)
			}
			if !errors.Is(err, permission.ErrorPermissionDenied) || approved.Load() {
				t.Fatalf("error = %v, approved = %v; expected every request denied", err, approved.Load())
			}
		})
	}
}

func TestRunNonInteractiveCancellationMetadataMatchesWorkflow(t *testing.T) {
	customCause := errors.New("caller stopped the run")
	for _, test := range []struct {
		name        string
		runError    error
		cancelCause error
	}{
		{name: "agent deadline", runError: context.DeadlineExceeded},
		{name: "startup cancellation", runError: context.Canceled},
		{name: "custom cancellation cause", cancelCause: customCause},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := &nonInteractiveAgent{broker: pubsub.NewBroker[agent.AgentEvent]()}
			application := newNonInteractiveTestApp(t, fake)
			defer application.EventBus.Close()
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(nil)
			wantError := test.runError
			switch test.name {
			case "agent deadline":
				fake.result.Error = test.runError
			case "startup cancellation":
				fake.beforeResult = func(context.Context, string) error { return test.runError }
			default:
				fake.block = true
				wantError = context.Canceled
				fake.beforeResult = func(context.Context, string) error { cancel(test.cancelCause); return nil }
			}
			var stdout bytes.Buffer
			err := application.RunNonInteractiveWithOptions(ctx, "run until cancelled", NonInteractiveOptions{
				OutputFormat: format.JSONL, Stdout: &stdout,
			})
			if !errors.Is(err, wantError) {
				t.Errorf("error = %v, want %v", err, wantError)
			}
			if test.cancelCause != nil && !errors.Is(err, test.cancelCause) {
				t.Errorf("error = %v, want preserved cause %v", err, test.cancelCause)
			}
			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			var terminal format.ExecutionEvent
			if len(lines) != 2 {
				t.Fatalf("expected start and terminal records, got %q", stdout.String())
			}
			if err := json.Unmarshal([]byte(lines[1]), &terminal); err != nil {
				t.Fatal(err)
			}
			if terminal.Type != "run.cancelled" || terminal.Status != "cancelled" {
				t.Errorf("unexpected cancellation event: %+v", terminal)
			}
			goals := application.Workflow.Goals()
			if len(goals) != 1 || goals[0].Status != engineering.StatusCanceled {
				t.Errorf("goals = %+v, want cancelled", goals)
			}
		})
	}
}

func TestRunNonInteractiveNormalizesAcceptedOutputFormat(t *testing.T) {
	for _, outputFormat := range []format.OutputFormat{" JSON ", "JSONL", " Text "} {
		t.Run(string(outputFormat), func(t *testing.T) {
			fake := &nonInteractiveAgent{broker: pubsub.NewBroker[agent.AgentEvent](), result: agent.AgentEvent{
				Type: agent.AgentEventTypeResponse, Done: true,
				Message: message.Message{Parts: []message.ContentPart{message.TextContent{Text: "complete"}}},
			}}
			application := newNonInteractiveTestApp(t, fake)
			defer application.EventBus.Close()
			var stdout bytes.Buffer
			if err := application.RunNonInteractiveWithOptions(context.Background(), "do work", NonInteractiveOptions{
				OutputFormat: outputFormat, Stdout: &stdout, Quiet: true,
			}); err != nil {
				t.Fatal(err)
			}
			normalized, _ := format.Parse(string(outputFormat))
			if normalized == format.Text {
				if stdout.String() != "complete\n" {
					t.Fatalf("output = %q", stdout.String())
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
			wantLines := 1
			if normalized == format.JSONL {
				wantLines = 2
			}
			if len(lines) != wantLines {
				t.Fatalf("expected %d records, got %q", wantLines, stdout.String())
			}
			var terminal format.ExecutionEvent
			if err := json.Unmarshal([]byte(lines[len(lines)-1]), &terminal); err != nil {
				t.Fatal(err)
			}
			if terminal.Type != "run.completed" || terminal.Response != "complete" {
				t.Fatalf("unexpected terminal event: %+v", terminal)
			}
		})
	}
}

type terminalFailWriter struct {
	err    error
	writes int
}

func (w *terminalFailWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, w.err
	}
	return len(p), nil
}

func TestRunNonInteractiveReportsFailureFinalizationErrors(t *testing.T) {
	providerErr := errors.New("provider failed")
	for _, failure := range []string{"evidence", "terminal output"} {
		t.Run(failure, func(t *testing.T) {
			fake := &nonInteractiveAgent{broker: pubsub.NewBroker[agent.AgentEvent](), result: agent.AgentEvent{Error: providerErr}}
			application, connection := newNonInteractiveTestAppWithDB(t, fake)
			defer application.EventBus.Close()
			failureText := "evidence storage unavailable"
			opts := NonInteractiveOptions{OutputFormat: format.JSONL, Stdout: &bytes.Buffer{}}
			var outputErr error
			if failure == "evidence" {
				_, err := connection.Exec(`CREATE TRIGGER reject_evidence BEFORE INSERT ON evidence BEGIN SELECT RAISE(FAIL, 'evidence storage unavailable'); END`)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				failureText = "terminal output unavailable"
				outputErr = errors.New(failureText)
				opts.Stdout = &terminalFailWriter{err: outputErr}
			}
			err := application.RunNonInteractiveWithOptions(context.Background(), "do work", opts)
			if !errors.Is(err, providerErr) || !strings.Contains(err.Error(), failureText) {
				t.Fatalf("error = %v, want provider failure and %q", err, failureText)
			}
			if outputErr != nil && !errors.Is(err, outputErr) {
				t.Fatalf("output error identity was lost: %v", err)
			}
		})
	}
}
