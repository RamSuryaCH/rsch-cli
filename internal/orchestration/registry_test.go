package orchestration

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/events"
)

func TestRegistryValidatesLifecycleAndMessages(t *testing.T) {
	bus := events.NewBus()
	stream, cancel := bus.Subscribe()
	defer cancel()
	registry := NewRegistry(bus)
	agent, err := registry.Create(NewAgent("backend", "task-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Transition(agent.ID, AgentRunning); err != nil {
		t.Fatal(err)
	}
	if err := registry.Transition(agent.ID, AgentCompleted); err != nil {
		t.Fatal(err)
	}
	if err := registry.Transition(agent.ID, AgentPaused); err == nil {
		t.Fatal("expected invalid transition")
	}
	if err := (Message{From: "a", To: "b", Type: "finding", Message: "missing check"}).Validate(); err != nil {
		t.Fatal(err)
	}
	if err := registry.SendMessage(Message{From: agent.ID, To: "b", Type: "finding", Severity: "high", Message: "missing check"}); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-stream:
		if event.Type != events.AgentCreated {
			t.Fatalf("unexpected first event: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("agent creation event not published")
	}
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case event := <-stream:
			if event.Type == events.AgentMessage {
				if event.Data["type"] != "finding" || event.Data["severity"] != "high" {
					t.Fatalf("unexpected agent message: %+v", event)
				}
				return
			}
		case <-timer.C:
			t.Fatal("agent message event not published")
		}
	}
}

func TestRegistryEnforcesActiveAgentLimit(t *testing.T) {
	registry := NewRegistry(nil, 1)
	first, err := registry.Create(NewAgent("one", "task"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Create(NewAgent("two", "task")); err == nil {
		t.Fatal("expected agent limit")
	}
	if err := registry.Transition(first.ID, AgentRunning); err != nil {
		t.Fatal(err)
	}
	if err := registry.Transition(first.ID, AgentCompleted); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Create(NewAgent("two", "task")); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryTreatsCancelledAliasAsTerminal(t *testing.T) {
	registry := NewRegistry(nil, 1)
	first := NewAgent("one", "task")
	first.Status = AgentCancelled
	if _, err := registry.Create(first); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Create(NewAgent("two", "task")); err != nil {
		t.Fatalf("cancelled agent should release capacity: %v", err)
	}
}

func TestRegistryEnforcesCapacityWhenRetryingFailedAgent(t *testing.T) {
	for _, next := range []AgentStatus{AgentQueued, AgentStarting, AgentRunning} {
		t.Run(string(next), func(t *testing.T) {
			connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			registry, err := NewPersistentRegistry(context.Background(), connection, nil, 1)
			if err != nil {
				t.Fatal(err)
			}
			failed := NewAgent("retry", "task")
			failed.Status = AgentFailed
			if _, err := registry.Create(failed); err != nil {
				t.Fatal(err)
			}
			active, err := registry.Create(NewAgent("active", "task"))
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Transition(failed.ID, next); !errors.Is(err, ErrAgentLimit) {
				t.Fatalf("retry should respect occupied capacity: %v", err)
			}
			if got, ok := registry.Get(failed.ID); !ok || got.Status != AgentFailed {
				t.Fatalf("rejected retry changed in-memory state: %+v", got)
			}
			persisted, err := NewAgentStore(connection).List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, agent := range persisted {
				if agent.ID == failed.ID && agent.Status != AgentFailed {
					t.Fatalf("rejected retry changed persisted state: %+v", agent)
				}
			}
			if err := registry.Transition(active.ID, AgentCanceled); err != nil {
				t.Fatal(err)
			}
			if err := registry.Transition(failed.ID, next); err != nil {
				t.Fatalf("retry should succeed after capacity is released: %v", err)
			}
		})
	}
}

func TestRegistryRollsBackTransitionWhenPersistenceFails(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	registry, err := NewPersistentRegistry(context.Background(), connection, nil)
	if err != nil {
		connection.Close()
		t.Fatal(err)
	}
	agent, err := registry.Create(NewAgent("role", "task"))
	if err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := registry.Transition(agent.ID, AgentRunning); err == nil {
		t.Fatal("expected persistence failure")
	}
	current, ok := registry.Get(agent.ID)
	if !ok || current.Status != AgentCreated {
		t.Fatalf("transition was not rolled back: %+v", current)
	}
}

func TestAgentStorePersistsAgentContractMetadata(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := NewAgentStore(connection)
	original := Agent{
		ID:           "agent-metadata",
		Role:         "security",
		Goal:         "review",
		TaskID:       "task-1",
		Parent:       "parent-1",
		Model:        "model-1",
		Provider:     "provider-1",
		Skills:       []string{"security"},
		Tools:        []string{"view"},
		Permissions:  []string{"read"},
		Risk:         "high",
		Budget:       1000,
		Timeout:      2 * time.Second,
		Worktree:     "/tmp/worktree",
		Tokens:       42,
		Cost:         0.25,
		Duration:     3 * time.Second,
		Files:        []string{"auth.go"},
		Errors:       []string{"one retry"},
		Verification: []string{"go test ./..."},
		Status:       AgentCompleted,
	}
	if err := store.Save(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	got := items[0]
	if got.Worktree != original.Worktree || got.Tokens != original.Tokens || got.Cost != original.Cost || got.Duration != original.Duration || len(got.Skills) != 1 || len(got.Verification) != 1 {
		t.Fatalf("agent metadata mismatch: got=%+v want=%+v", got, original)
	}
	registry, err := NewPersistentRegistry(context.Background(), connection, nil)
	if err != nil {
		t.Fatal(err)
	}
	updated := original
	updated.Cost = 0.75
	updated.Files = []string{"auth.go", "policy.go"}
	if err := registry.UpdateMetadata(original.ID, updated); err != nil {
		t.Fatal(err)
	}
	persisted, ok := registry.Get(original.ID)
	if !ok || persisted.Cost != 0.75 || len(persisted.Files) != 2 || persisted.Status != original.Status {
		t.Fatalf("metadata update was not persisted safely: %+v", persisted)
	}
}

func TestAgentStoreRedactsCredentialMaterial(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := NewAgentStore(connection)
	secret := "super-" + "secret-value"
	otherSecret := "another-" + "secret-value"
	goal := "token" + ": \"" + secret + "\""
	errors := []string{"password" + ": \"" + otherSecret + "\""}
	if err := store.Save(context.Background(), Agent{ID: "secret-agent", Role: "role", Goal: goal, TaskID: "task", Errors: errors, Status: AgentCreated}); err != nil {
		t.Fatal(err)
	}
	items, err := store.List(context.Background())
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if strings.Contains(items[0].Goal, secret) || strings.Contains(strings.Join(items[0].Errors, " "), otherSecret) {
		t.Fatalf("credential material persisted: %+v", items[0])
	}
}

func TestAgentStoreEnsuresPersistedCapacityAcrossLifecycleUpdates(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	store := NewAgentStore(connection)
	first := Agent{ID: "first", Role: "role", Status: AgentCreated}
	second := Agent{ID: "second", Role: "role", Status: AgentCreated}
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureCapacity(context.Background(), 1, second.ID, second.Status); err == nil {
		t.Fatal("expected capacity error for a second active agent")
	}
	first.Status = AgentCompleted
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureCapacity(context.Background(), 1, second.ID, second.Status); err != nil {
		t.Fatalf("completed agent should release capacity: %v", err)
	}
	if err := store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureCapacity(context.Background(), 1, second.ID, AgentRunning); err != nil {
		t.Fatalf("updating an already-active candidate should retain its slot: %v", err)
	}
	if err := store.EnsureCapacity(context.Background(), 1, second.ID, AgentCancelled); err != nil {
		t.Fatalf("terminal cancellation should not be blocked by capacity: %v", err)
	}
}
