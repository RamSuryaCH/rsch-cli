package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/orchestration"
	"github.com/opencode-ai/opencode/internal/session"
)

func newServerRequest(method, target string, body io.Reader) *http.Request {
	request := httptest.NewRequest(method, target, body)
	request.Host = "127.0.0.1"
	if body != nil && methodRequiresJSON(method) {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func TestWorkspaceInspectionAPI(t *testing.T) {
	root := t.TempDir()
	connection, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	service := NewWithWorkspace(events.NewBus(), engineering.NewSQLStore(connection), events.NewSQLLog(connection), root, connection)
	ts := httptest.NewServer(service)
	defer ts.Close()
	for _, resource := range []string{"audit", "memory", "steering", "release", "jobs", "cost", "agents", "browser", "notifications", "dependencies", "security", "requirements", "confidence", "database", "evidence", "evidence/links", "architecture", "performance", "capabilities", "team"} {
		response, err := http.Get(ts.URL + "/v1/workspace/" + resource)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d", resource, response.StatusCode)
		}
		response.Body.Close()
	}
	response, err := http.Get(ts.URL + "/v1/workspace/context?q=workflow&budget=200")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("context status=%d", response.StatusCode)
	}
	response.Body.Close()
}

func TestWorkspaceCostIncludesDetailedUsageTelemetry(t *testing.T) {
	root := t.TempDir()
	connection, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	sessions := session.NewServiceWithDB(db.New(connection), connection)
	created, err := sessions.Create(context.Background(), "cost")
	if err != nil {
		t.Fatal(err)
	}
	if err := sessions.RecordUsage(context.Background(), session.Usage{SessionID: created.ID, TaskID: "task-1", Model: "model-1", Provider: "provider-1", Cost: 1.25, Duration: time.Second}); err != nil {
		t.Fatal(err)
	}
	service := NewWithWorkspace(events.NewBus(), engineering.NewSQLStore(connection), events.NewSQLLog(connection), root, connection)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodGet, "/v1/workspace/cost", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("cost status=%d body=%s", response.Code, response.Body.String())
	}
	var value map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value["usageEvidence"] != true || value["durationNs"].(float64) != float64(time.Second) {
		t.Fatalf("detailed usage telemetry missing: %+v", value)
	}
	taskCosts, ok := value["taskCosts"].(map[string]any)
	if !ok || taskCosts["task-1"].(float64) != 1.25 {
		t.Fatalf("task cost telemetry missing: %+v", value["taskCosts"])
	}
}

func TestWorkspaceExternalSecurityScanRequiresPolicyApproval(t *testing.T) {
	root := t.TempDir()
	connection, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	service := NewWithWorkspace(events.NewBus(), engineering.NewSQLStore(connection), events.NewSQLLog(connection), root, connection).SetMutationPolicy(func(_ *http.Request, action string) error {
		if action == "security.external" {
			return fmt.Errorf("approval required")
		}
		return nil
	})
	response := httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodGet, "/v1/workspace/security?external=trivy", nil))
	if response.Code != http.StatusForbidden {
		t.Fatalf("external scanner policy status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestWorkspaceAgentsAndReplayExposeStructuredState(t *testing.T) {
	root := t.TempDir()
	connection, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := orchestration.NewAgentStore(connection)
	if err := store.Save(context.Background(), orchestration.Agent{
		ID: "agent-1", Role: "security", Goal: "goal-1", TaskID: "task-1", Model: "model-1", Provider: "provider-1",
		Skills: []string{"review"}, Tools: []string{"read"}, Permissions: []string{"inspect"}, Risk: "high", Budget: 42,
		Worktree: "/tmp/agent-1", Tokens: 12, Cost: 0.25, Duration: time.Second, Files: []string{"a.go"}, Errors: []string{"warning"}, Verification: []string{"go test"}, Status: orchestration.AgentCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	log := events.NewSQLLog(connection)
	message := events.New(events.AgentMessage, map[string]any{"from": "security", "to": "backend", "type": "finding", "message": "review"})
	message.GoalID, message.TaskID = "goal-1", "task-1"
	if err := log.Append(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	tool := events.New(events.ToolAfter, map[string]any{"tool": "read"})
	tool.TaskID = "task-1"
	if err := log.Append(context.Background(), tool); err != nil {
		t.Fatal(err)
	}
	service := NewWithWorkspace(events.NewBus(), engineering.NewSQLStore(connection), log, root, connection)
	request := newServerRequest(http.MethodGet, "/v1/workspace/agents", nil)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"skills":["review"]`) || !strings.Contains(response.Body.String(), `"verification":["go test"]`) {
		t.Fatalf("full agent metadata missing: status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodGet, "/v1/workspace/messages?taskId=task-1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"type":"agent.message"`) {
		t.Fatalf("structured messages missing: status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodGet, "/v1/workspace/replay?taskId=task-1", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"toolActions"`) || !strings.Contains(response.Body.String(), `"finalResult"`) {
		t.Fatalf("replay detail missing: status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestAgentLifecycleAPIValidatesAndPersistsTransitions(t *testing.T) {
	root := t.TempDir()
	connection, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	service := NewWithWorkspace(events.NewBus(), engineering.NewSQLStore(connection), events.NewSQLLog(connection), root, connection)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodPost, "/v1/agents", bytes.NewBufferString(`{"role":"backend","taskId":"task-1"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}
	var created orchestration.Agent
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.ID == "" || created.Status != orchestration.AgentCreated {
		t.Fatalf("created agent=%+v err=%v", created, err)
	}
	transition := func(status orchestration.AgentStatus) {
		payload := fmt.Sprintf(`{"status":%q}`, status)
		result := httptest.NewRecorder()
		service.ServeHTTP(result, newServerRequest(http.MethodPost, "/v1/agents/"+created.ID+"/transition", bytes.NewBufferString(payload)))
		if result.Code != http.StatusOK {
			t.Fatalf("transition %s status=%d body=%s", status, result.Code, result.Body.String())
		}
		if err := json.Unmarshal(result.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
	}
	transition(orchestration.AgentRunning)
	transition(orchestration.AgentPaused)
	transition(orchestration.AgentResumed)
	transition(orchestration.AgentRunning)
	transition(orchestration.AgentCompleted)
	invalid := httptest.NewRecorder()
	service.ServeHTTP(invalid, newServerRequest(http.MethodPost, "/v1/agents/"+created.ID+"/transition", bytes.NewBufferString(`{"status":"running"}`)))
	if invalid.Code != http.StatusConflict {
		t.Fatalf("invalid transition status=%d body=%s", invalid.Code, invalid.Body.String())
	}
	if events, err := events.NewSQLLog(connection).Replay(context.Background(), 0, 20); err != nil || len(events) < 6 {
		t.Fatalf("agent lifecycle events=%d err=%v", len(events), err)
	}
}

func TestSessionPersistenceAPIExposesUsageAndDeletion(t *testing.T) {
	root := t.TempDir()
	connection, err := db.ConnectAt(filepath.Join(root, ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	service := NewWithWorkspace(events.NewBus(), engineering.NewSQLStore(connection), events.NewSQLLog(connection), root, connection)
	response := httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodPost, "/v1/sessions", bytes.NewBufferString(`{"title":"API session"}`)))
	if response.Code != http.StatusCreated {
		t.Fatalf("session create status=%d body=%s", response.Code, response.Body.String())
	}
	var created session.Session
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("created session=%+v err=%v", created, err)
	}
	sessions := session.NewServiceWithDB(db.New(connection), connection)
	if err := sessions.RecordUsage(context.Background(), session.Usage{SessionID: created.ID, TaskID: "task-1", Model: "model", Provider: "provider", Cost: 0.5, Duration: time.Second}); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodGet, "/v1/sessions/"+created.ID+"/usage", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"taskId":"task-1"`) {
		t.Fatalf("usage status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	service.ServeHTTP(response, newServerRequest(http.MethodDelete, "/v1/sessions/"+created.ID, nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("session delete status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestGoalAPIAndHealth(t *testing.T) {
	s := New(events.NewBus(), engineering.NewStore())
	ts := httptest.NewServer(s)
	defer ts.Close()
	response, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	response, err = http.Post(ts.URL+"/v1/goals", "application/json", bytes.NewBufferString(`{"id":"g1","title":"OAuth"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", response.StatusCode)
	}
}

func TestMutationPolicyDeniesAndCallerCannotSelfApprove(t *testing.T) {
	var policySawApprovalHeader bool
	service := New(events.NewBus(), engineering.NewStore()).SetMutationPolicy(func(r *http.Request, action string) error {
		if action == "goal.create" && r.Header.Get("X-Policy-Deny") == "yes" {
			return fmt.Errorf("configured policy denied %s", action)
		}
		if action == "tasks.replace" {
			policySawApprovalHeader = r.Header.Get(approvalHeader) != ""
			if !policySawApprovalHeader {
				return fmt.Errorf("approval required for %s", action)
			}
		}
		return nil
	})
	ts := httptest.NewServer(service)
	defer ts.Close()

	denied, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/goals", bytes.NewBufferString(`{"id":"denied","title":"Denied"}`))
	if err != nil {
		t.Fatal(err)
	}
	denied.Header.Set("Content-Type", "application/json")
	denied.Header.Set("X-Policy-Deny", "yes")
	response, err := http.DefaultClient.Do(denied)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("denied mutation status=%d", response.StatusCode)
	}

	response, err = http.Post(ts.URL+"/v1/goals", "application/json", bytes.NewBufferString(`{"id":"approved-goal","title":"Approved"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("goal creation status=%d", response.StatusCode)
	}

	response, err = http.Post(ts.URL+"/v1/goals/approved-goal/tasks", "application/json", bytes.NewBufferString(`[{"id":"task-1","title":"implement"}]`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("unapproved task replacement status=%d", response.StatusCode)
	}

	request, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/goals/approved-goal/tasks", bytes.NewBufferString(`[{"id":"task-1","title":"implement"}]`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-RSCH-Approval", "approved")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("caller-controlled approval header bypassed policy: status=%d", response.StatusCode)
	}
	if policySawApprovalHeader {
		t.Fatal("mutation policy received caller-controlled approval header")
	}
}

func TestNestedTaskTransitionRejectsWrongGoalBeforeMutation(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := engineering.NewSQLStore(connection)
	ctx := context.Background()
	for _, goal := range []engineering.Goal{{ID: "goal-a", Title: "A"}, {ID: "goal-b", Title: "B"}} {
		if err := store.SaveGoal(ctx, goal); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceTasks(ctx, "goal-a", []engineering.Task{{ID: "task-a", Title: "A task", Status: engineering.StatusPending}}); err != nil {
		t.Fatal(err)
	}
	service := New(events.NewBus(), store)
	response := httptest.NewRecorder()
	request := newServerRequest(http.MethodPost, "/v1/goals/goal-b/tasks/task-a/transition", bytes.NewBufferString(`{"status":"ready"}`))
	service.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("wrong-goal transition status=%d body=%s", response.Code, response.Body.String())
	}
	items, err := store.LoadTasks(ctx, "goal-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Status != engineering.StatusPending {
		t.Fatalf("wrong-goal transition mutated task: %+v", items)
	}
}

func TestGoalAPIUsesDurableStore(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := engineering.NewSQLStore(connection)
	service := NewWithLog(events.NewBus(), store, events.NewSQLLog(connection))
	ts := httptest.NewServer(service)
	defer ts.Close()
	response, err := http.Post(ts.URL+"/v1/goals", "application/json", bytes.NewBufferString(`{"id":"persisted","title":"Durable goal"}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = http.Post(ts.URL+"/v1/goals/persisted/requirements", "application/json", bytes.NewBufferString(`{"id":"req-1","statement":"must be verified","confirmed":true}`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = http.Post(ts.URL+"/v1/goals/persisted/tasks", "application/json", bytes.NewBufferString(`[{"id":"task-1","title":"implement"}]`))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response, err = http.Get(ts.URL + "/v1/goals/persisted/tasks")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("tasks status=%d", response.StatusCode)
	}
	response, err = http.Get(ts.URL + "/v1/goals/persisted/tasks/task-1")
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("task detail status=%v err=%v", response.StatusCode, err)
	}
	response.Body.Close()
	response, err = http.Post(ts.URL+"/v1/goals/persisted/transition", "application/json", bytes.NewBufferString(`{"status":"analyzing"}`))
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("goal transition status=%v err=%v", response.StatusCode, err)
	}
	response.Body.Close()
	response, err = http.Post(ts.URL+"/v1/goals/persisted/tasks/task-1/transition", "application/json", bytes.NewBufferString(`{"status":"ready"}`))
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("task transition status=%v err=%v", response.StatusCode, err)
	}
	response.Body.Close()
	if got := len(store.Goals()); got != 1 {
		t.Fatalf("stored goals=%d", got)
	}
	if events, err := events.NewSQLLog(connection).Replay(context.Background(), 0, 10); err != nil || len(events) != 5 {
		t.Fatalf("events=%d err=%v", len(events), err)
	}
}

func TestTopLevelTaskAPIProvidesCreateEditAndTransitionParity(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := engineering.NewSQLStore(connection)
	if err := store.SaveGoal(context.Background(), engineering.Goal{ID: "goal-tasks", Title: "Task API"}); err != nil {
		t.Fatal(err)
	}
	service := NewWithLog(events.NewBus(), store, events.NewSQLLog(connection))
	create := httptest.NewRecorder()
	service.ServeHTTP(create, newServerRequest(http.MethodPost, "/v1/tasks", bytes.NewBufferString(`{"goalId":"goal-tasks","title":"first task"}`)))
	if create.Code != http.StatusCreated {
		t.Fatalf("task create status=%d body=%s", create.Code, create.Body.String())
	}
	var task engineering.Task
	if err := json.Unmarshal(create.Body.Bytes(), &task); err != nil || task.ID == "" || task.GoalID != "goal-tasks" {
		t.Fatalf("created task=%+v err=%v", task, err)
	}
	list := httptest.NewRecorder()
	service.ServeHTTP(list, newServerRequest(http.MethodGet, "/v1/tasks?goalId=goal-tasks", nil))
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), task.ID) {
		t.Fatalf("task list status=%d body=%s", list.Code, list.Body.String())
	}
	edit := httptest.NewRecorder()
	service.ServeHTTP(edit, newServerRequest(http.MethodPut, "/v1/tasks/"+task.ID, bytes.NewBufferString(`{"title":"renamed task"}`)))
	if edit.Code != http.StatusOK || !strings.Contains(edit.Body.String(), "renamed task") {
		t.Fatalf("task edit status=%d body=%s", edit.Code, edit.Body.String())
	}
	transition := httptest.NewRecorder()
	service.ServeHTTP(transition, newServerRequest(http.MethodPost, "/v1/tasks/"+task.ID+"/transition", bytes.NewBufferString(`{"status":"ready"}`)))
	if transition.Code != http.StatusOK || !strings.Contains(transition.Body.String(), `"status":"ready"`) {
		t.Fatalf("task transition status=%d body=%s", transition.Code, transition.Body.String())
	}
	bulk := httptest.NewRecorder()
	service.ServeHTTP(bulk, newServerRequest(http.MethodPut, "/v1/tasks?goalId=goal-tasks", bytes.NewBufferString(`[{"id":"bulk-task","title":"bulk replacement"}]`)))
	if bulk.Code != http.StatusOK || !strings.Contains(bulk.Body.String(), "bulk replacement") {
		t.Fatalf("task bulk replacement status=%d body=%s", bulk.Code, bulk.Body.String())
	}
}

func TestDurableBusAssignsReplaySequenceToLiveEvents(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	bus := events.NewBus()
	bus.AddSink(events.NewSQLLog(connection))
	stream, cancel := bus.Subscribe()
	defer cancel()
	if !bus.Publish(events.New(events.ToolAfter, map[string]any{"requestId": "sequenced"})) {
		t.Fatal("durable publish failed")
	}
	live := <-stream
	if live.Sequence <= 0 {
		t.Fatalf("live event did not receive durable sequence: %+v", live)
	}
	items, err := events.NewSQLLog(connection).Replay(context.Background(), live.Sequence-1, 10)
	if err != nil || len(items) != 1 || items[0].ID != live.ID || items[0].Sequence != live.Sequence {
		t.Fatalf("replay mismatch live=%+v items=%+v err=%v", live, items, err)
	}
}

func TestBearerAuthenticationProtectsNonHealthEndpoints(t *testing.T) {
	service := New(events.NewBus(), engineering.NewStore()).SetAuthToken("test-token")
	ts := httptest.NewServer(service)
	defer ts.Close()
	response, err := http.Get(ts.URL + "/v1/status")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}
	response, err = http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status=%d", response.StatusCode)
	}
	request, err := http.NewRequest(http.MethodGet, ts.URL+"/v1/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer test-token")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("authenticated status=%d", response.StatusCode)
	}
}

func TestSSEReplaysPersistedEventsAfterSequence(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	log := events.NewSQLLog(connection)
	event := events.New(events.TaskCompleted, map[string]any{"taskId": "task-1"})
	event.TaskID = "task-1"
	if err := log.Append(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	service := NewWithLog(events.NewBus(), engineering.NewStore(), log)
	recorder := httptest.NewRecorder()
	request := newServerRequest(http.MethodGet, "/v1/events?after=0", nil)
	requestContext, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	request = request.WithContext(requestContext)
	done := make(chan struct{})
	go func() {
		service.ServeHTTP(recorder, request)
		close(done)
	}()
	// The catch-up event is written before the handler waits for live events.
	time.Sleep(20 * time.Millisecond)
	cancelRequest()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after request cancellation")
	}
	if !strings.Contains(recorder.Body.String(), "event: task.completed") {
		t.Fatalf("missing replayed SSE event: %s", recorder.Body.String())
	}
}
