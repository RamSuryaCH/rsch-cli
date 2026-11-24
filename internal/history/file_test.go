package history

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/db"
	"github.com/opencode-ai/opencode/internal/pubsub"
)

func TestLatestFilesAndVersionsAreScopedToSession(t *testing.T) {
	connection, err := db.ConnectAt(filepath.Join(t.TempDir(), ".rsch"))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	queries := db.New(connection)
	for _, id := range []string{"first", "second"} {
		if _, err := queries.CreateSession(context.Background(), db.CreateSessionParams{ID: id, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(queries, connection)
	if _, err := service.Create(context.Background(), "first", "main.go", "first-v0"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(context.Background(), "second", "main.go", "second-v0"); err != nil {
		t.Fatal(err)
	}
	latest, err := service.ListLatestSessionFiles(context.Background(), "first")
	if err != nil || len(latest) != 1 || latest[0].Content != "first-v0" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
	version, err := service.CreateVersion(context.Background(), "first", "main.go", "first-v1")
	if err != nil || version.Version != "v1" {
		t.Fatalf("version=%+v err=%v", version, err)
	}
	latest, err = service.ListLatestSessionFiles(context.Background(), "first")
	if err != nil || len(latest) != 1 || latest[0].Content != "first-v1" {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestGetByPathAndSessionReturnsLatestSameSecondVersion(t *testing.T) {
	connection, err := db.ConnectAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx := context.Background()
	queries := db.New(connection)
	if _, err := queries.CreateSession(ctx, db.CreateSessionParams{ID: "session", Title: "history"}); err != nil {
		t.Fatal(err)
	}
	service := NewService(queries, connection)
	if _, err := service.Create(ctx, "session", "main.go", "initial"); err != nil {
		t.Fatal(err)
	}
	want, err := service.CreateVersion(ctx, "session", "main.go", "latest")
	if err != nil {
		t.Fatal(err)
	}
	// Force the tie independently of how quickly the test machine writes rows.
	if _, err := connection.Exec(`UPDATE files SET created_at = 100 WHERE session_id = 'session'`); err != nil {
		t.Fatal(err)
	}
	got, err := service.GetByPathAndSession(ctx, "main.go", "session")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != want.ID || got.Content != "latest" {
		t.Fatalf("latest file = %+v, want version %q", got, want.ID)
	}
}

func TestCreateHonorsCancellationWhileWaitingForConnection(t *testing.T) {
	connection, err := db.ConnectAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	queries := db.New(connection)
	if _, err := queries.CreateSession(context.Background(), db.CreateSessionParams{ID: "session", Title: "history"}); err != nil {
		t.Fatal(err)
	}
	held, err := connection.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service := NewService(queries, connection)
	result := make(chan error, 1)
	go func() {
		_, err := service.Create(ctx, "session", "main.go", "canceled")
		result <- err
	}()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Create error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		// Release the connection and join the writer even when testing broken code.
		_ = held.Close()
		<-result
		t.Fatal("canceled Create waited for a database connection")
	}
}

func TestDeleteSessionFilesIsAtomicAndPublishesAfterSuccess(t *testing.T) {
	connection, err := db.ConnectAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	queries := db.New(connection)
	for _, id := range []string{"target", "unrelated"} {
		if _, err := queries.CreateSession(ctx, db.CreateSessionParams{ID: id, Title: id}); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(queries, connection)
	for _, path := range []string{"first.go", "second.go"} {
		if _, err := service.Create(ctx, "target", path, "history"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Create(ctx, "unrelated", "keep.go", "keep"); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`UPDATE files SET created_at = CASE path WHEN 'first.go' THEN 100 ELSE 101 END;
		CREATE TRIGGER block_history_delete BEFORE DELETE ON files WHEN OLD.path = 'second.go'
		BEGIN SELECT RAISE(ABORT, 'simulated delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	events := service.Subscribe(ctx)
	if err := service.DeleteSessionFiles(ctx, "target"); err == nil {
		t.Fatal("expected delete failure")
	}
	items, err := service.ListBySession(ctx, "target")
	if err != nil || len(items) != 2 {
		t.Fatalf("failed delete left %d files, want 2: %v", len(items), err)
	}
	select {
	case event := <-events:
		t.Fatalf("failed delete published event: %+v", event)
	default:
	}
	if _, err := connection.Exec(`DROP TRIGGER block_history_delete`); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteSessionFiles(ctx, "target"); err != nil {
		t.Fatal(err)
	}
	if items, err := service.ListBySession(ctx, "target"); err != nil || len(items) != 0 {
		t.Fatalf("deleted files = %+v, err = %v", items, err)
	}
	if items, err := service.ListBySession(ctx, "unrelated"); err != nil || len(items) != 1 {
		t.Fatalf("unrelated files = %+v, err = %v", items, err)
	}
	for range 2 {
		select {
		case event := <-events:
			if event.Type != pubsub.DeletedEvent || event.Payload.SessionID != "target" {
				t.Fatalf("unexpected deletion event: %+v", event)
			}
		default:
			t.Fatal("missing deletion event")
		}
	}
}
