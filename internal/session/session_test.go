package session

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
)

func TestDeleteRemovesSessionDescendantsAndTheirData(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	queries := db.New(connection)
	service := NewServiceWithDB(queries, connection)
	parent, err := service.Create(context.Background(), "parent")
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTaskSession(context.Background(), "child", parent.ID, "child")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := service.CreateTaskSession(context.Background(), "grandchild", child.ID, "grandchild")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateMessage(context.Background(), db.CreateMessageParams{ID: "message", SessionID: grandchild.ID, Role: "user", Parts: "[]"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(context.Background(), parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, child.ID, grandchild.ID} {
		if _, err := service.Get(context.Background(), id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("session %q still exists: %v", id, err)
		}
	}
	var messages int
	if err := connection.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&messages); err != nil || messages != 0 {
		t.Fatalf("messages=%d err=%v", messages, err)
	}
}

func TestQueryOnlyDeleteRemovesDescendantsAndPreservesOtherSessions(t *testing.T) {
	connection, err := db.ConnectAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx := context.Background()
	queries := db.New(connection)
	service := NewService(queries)
	parent, err := service.Create(ctx, "parent")
	if err != nil {
		t.Fatal(err)
	}
	child, err := service.CreateTaskSession(ctx, "child", parent.ID, "child")
	if err != nil {
		t.Fatal(err)
	}
	grandchild, err := service.CreateTaskSession(ctx, "grandchild", child.ID, "grandchild")
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := service.Create(ctx, "unrelated")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateMessage(ctx, db.CreateMessageParams{ID: "message", SessionID: grandchild.ID, Role: "user", Parts: "[]"}); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.CreateFile(ctx, db.CreateFileParams{ID: "file", SessionID: child.ID, Path: "main.go", Content: "history", Version: "initial"}); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, parent.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{parent.ID, child.ID, grandchild.ID} {
		if _, err := service.Get(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("session %q still exists: %v", id, err)
		}
	}
	if _, err := service.Get(ctx, unrelated.ID); err != nil {
		t.Fatalf("unrelated session was removed: %v", err)
	}
	if _, err := queries.GetMessage(ctx, "message"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("descendant message still exists: %v", err)
	}
	if _, err := queries.GetFile(ctx, "file"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("descendant history still exists: %v", err)
	}
}

func TestResumeMarksPendingAssistantMessagesInterrupted(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	queries := db.New(connection)
	service := NewServiceWithDB(queries, connection)
	created, err := service.Create(context.Background(), "resume")
	if err != nil {
		t.Fatal(err)
	}
	stored, err := queries.CreateMessage(context.Background(), db.CreateMessageParams{ID: "pending", SessionID: created.ID, Role: "assistant", Parts: "[]"})
	if err != nil || stored.State != "pending" {
		t.Fatalf("created=%+v err=%v", stored, err)
	}
	if _, err := service.Resume(context.Background(), created.ID); err != nil {
		t.Fatal(err)
	}
	stored, err = queries.GetMessage(context.Background(), stored.ID)
	if err != nil || stored.State != "interrupted" {
		t.Fatalf("resumed=%+v err=%v", stored, err)
	}
}

func TestMessagesReceiveStablePerSessionSequence(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	queries := db.New(connection)
	service := NewServiceWithDB(queries, connection)
	created, err := service.Create(context.Background(), "ordered")
	if err != nil {
		t.Fatal(err)
	}
	for index, id := range []string{"third-lexically", "first-lexically", "second-lexically"} {
		stored, err := queries.CreateMessage(context.Background(), db.CreateMessageParams{ID: id, SessionID: created.ID, Role: "user", Parts: "[]"})
		if err != nil {
			t.Fatal(err)
		}
		if stored.Sequence != int64(index+1) {
			t.Fatalf("message %q sequence=%d", id, stored.Sequence)
		}
	}
	items, err := queries.ListMessagesBySession(context.Background(), created.ID)
	if err != nil || len(items) != 3 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	if items[0].ID != "third-lexically" || items[1].ID != "first-lexically" || items[2].ID != "second-lexically" {
		t.Fatalf("unexpected order: %+v", items)
	}
}

func TestUsagePersistsModelProviderAndCostTelemetry(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	service := NewServiceWithDB(db.New(connection), connection)
	created, err := service.Create(context.Background(), "usage")
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Second)
	if err := service.RecordUsage(context.Background(), Usage{SessionID: created.ID, TaskID: "task-1", AgentID: "agent-1", Model: "model-1", Provider: "provider-1", InputTokens: 10, OutputTokens: 20, Cost: 0.5, Duration: time.Second, StartedAt: started, FinishedAt: started.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := service.RecordUsage(context.Background(), Usage{SessionID: created.ID, TaskID: "task-1", AgentID: "agent-1", Model: "model-1", Provider: "provider-1", InputTokens: 3, OutputTokens: 4, Cost: 0.25, Duration: 2 * time.Second, StartedAt: started.Add(2 * time.Second), FinishedAt: started.Add(4 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	items, err := service.Usage(context.Background(), created.ID)
	if err != nil || len(items) != 2 {
		t.Fatalf("usage=%+v err=%v", items, err)
	}
	if items[0].Provider != "provider-1" || items[0].Model != "model-1" || items[0].TaskID != "task-1" || items[0].Cost != 0.5 || items[0].Duration != time.Second || items[1].Cost != 0.25 {
		t.Fatalf("unexpected usage record: %+v", items[0])
	}
}
