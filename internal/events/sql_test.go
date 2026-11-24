package events

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "github.com/ncruces/go-sqlite3/driver"
	_ "github.com/ncruces/go-sqlite3/embed"
)

func newEventDatabase(t *testing.T) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(`CREATE TABLE event_log (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT NOT NULL UNIQUE, version INTEGER NOT NULL, type TEXT NOT NULL,
		occurred_at INTEGER NOT NULL, session_id TEXT, goal_id TEXT, task_id TEXT,
		actor TEXT, data_json TEXT NOT NULL DEFAULT '{}'
	); CREATE TABLE task_state (id TEXT PRIMARY KEY);`)
	if err != nil {
		t.Fatal(err)
	}
	return database
}

func TestAppendEventTxCommitsAndRollsBackWithDomainState(t *testing.T) {
	for _, commit := range []bool{false, true} {
		name := "rollback"
		if commit {
			name = "commit"
		}
		t.Run(name, func(t *testing.T) {
			database := newEventDatabase(t)
			ctx := context.Background()
			tx, err := database.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if _, err := tx.ExecContext(ctx, `INSERT INTO task_state(id) VALUES('task-1')`); err != nil {
				t.Fatal(err)
			}
			event := New(TaskCreated, nil)
			event.Data = map[string]any{"api_key": "plain-secret", "title": "visible-title"}
			if err := AppendEventTx(ctx, tx, &event); err != nil {
				t.Fatal(err)
			}
			if event.Sequence <= 0 || event.Data["api_key"] != "[REDACTED]" {
				t.Fatalf("append did not return a sanitized sequenced event: %+v", event)
			}
			if commit {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err != nil {
				t.Fatal(err)
			}
			var count int
			if err := database.QueryRow(`SELECT count(*) FROM task_state`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			items, err := NewSQLLog(database).Replay(ctx, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if commit {
				want = 1
			}
			if count != want || len(items) != want {
				t.Fatalf("domain/event commit mismatch: domain=%d events=%d want=%d", count, len(items), want)
			}
			if commit && (items[0].Data["api_key"] != "[REDACTED]" || items[0].Sequence != event.Sequence) {
				t.Fatalf("durable event differs from returned event: %+v", items[0])
			}
		})
	}
}

func TestAppendEventTxRejectsCanceledContext(t *testing.T) {
	database := newEventDatabase(t)
	tx, err := database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	event := New(TaskCreated, nil)
	if err := AppendEventTx(ctx, tx, &event); !errors.Is(err, context.Canceled) {
		t.Fatalf("append error = %v, want cancellation", err)
	}
	if event.Sequence != 0 {
		t.Fatalf("failed append assigned sequence %d", event.Sequence)
	}
}

func TestPublishTransactionDoesNotDuplicatePrimaryLogOrSkipOtherSinks(t *testing.T) {
	primary := newEventDatabase(t)
	secondary := newEventDatabase(t)
	ctx := context.Background()
	if err := NewSQLLog(secondary).Append(ctx, New(SessionStarted, nil)); err != nil {
		t.Fatal(err)
	}
	bus := NewBus()
	defer bus.Close()
	bus.AddSink(NewSQLLog(primary))
	bus.AddSink(NewSQLLog(secondary))
	observer := &recordingSink{}
	bus.AddSink(observer)
	stream, cancel := bus.Subscribe()
	defer cancel()

	event, delivered, err := bus.PublishTransaction(ctx, NewSQLLog(primary), func(ctx context.Context) (Event, error) {
		event := New(TaskCreated, map[string]any{"taskId": "task-1"})
		tx, err := primary.BeginTx(ctx, nil)
		if err != nil {
			return Event{}, err
		}
		defer tx.Rollback()
		if err := AppendEventTx(ctx, tx, &event); err != nil {
			return Event{}, err
		}
		return event, tx.Commit()
	})
	if err != nil || !delivered {
		t.Fatalf("committed publish failed: delivered=%t err=%v", delivered, err)
	}
	primaryItems, err := NewSQLLog(primary).Replay(ctx, 0, 10)
	if err != nil || len(primaryItems) != 1 {
		t.Fatalf("primary log = %+v, %v", primaryItems, err)
	}
	secondaryItems, err := NewSQLLog(secondary).Replay(ctx, 0, 10)
	if err != nil || len(secondaryItems) != 2 || secondaryItems[1].ID != event.ID {
		t.Fatalf("secondary log missed committed event: %+v, %v", secondaryItems, err)
	}
	live := <-stream
	if len(observer.items) != 1 || observer.items[0].ID != event.ID {
		t.Fatalf("ordinary sink missed committed event: %+v", observer.items)
	}
	if event.Sequence != primaryItems[0].Sequence || live.Sequence != event.Sequence || observer.items[0].Sequence != event.Sequence {
		t.Fatalf("primary cursor changed during delivery: returned=%d live=%d observer=%d", event.Sequence, live.Sequence, observer.items[0].Sequence)
	}
}

func TestPublishTransactionPreservesOrderWithConcurrentPublish(t *testing.T) {
	database := newEventDatabase(t)
	log := NewSQLLog(database)
	bus := NewBus()
	defer bus.Close()
	bus.AddSink(log)
	stream, cancel := bus.Subscribe()
	defer cancel()
	committed := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		_, _, err := bus.PublishTransaction(context.Background(), log, func(ctx context.Context) (Event, error) {
			event := New(TaskStarted, nil)
			if err := log.AppendEvent(ctx, &event); err != nil {
				return Event{}, err
			}
			close(committed)
			<-release
			return event, nil
		})
		firstDone <- err
	}()
	select {
	case <-committed:
	case err := <-firstDone:
		t.Fatalf("initial commit failed: %v", err)
	}
	secondDone := make(chan bool, 1)
	go func() { secondDone <- bus.Publish(New(TaskCompleted, nil)) }()
	select {
	case <-secondDone:
		close(release)
		<-firstDone
		t.Fatal("ordinary publish overtook the committed transaction")
	case <-time.After(100 * time.Millisecond):
		close(release)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if !<-secondDone {
		t.Fatal("second publish failed")
	}
	for want := int64(1); want <= 2; want++ {
		if event := <-stream; event.Sequence != want {
			t.Fatalf("live sequence=%d want=%d", event.Sequence, want)
		}
	}
}

func TestPublishTransactionPreservesCursorWithoutConfiguredPrimaryLog(t *testing.T) {
	database := newEventDatabase(t)
	bus := NewBus()
	defer bus.Close()
	bus.AddSink(NewSQLLog(database))
	stream, cancel := bus.Subscribe()
	defer cancel()
	event, delivered, err := bus.PublishTransaction(context.Background(), nil, func(context.Context) (Event, error) {
		event := New(TaskCreated, nil)
		event.Sequence = 42 // Assigned by the store's independently committed log.
		return event, nil
	})
	if err != nil || !delivered {
		t.Fatalf("publish failed: delivered=%t err=%v", delivered, err)
	}
	if live := <-stream; event.Sequence != 42 || live.Sequence != 42 {
		t.Fatalf("secondary log replaced primary cursor: returned=%d live=%d", event.Sequence, live.Sequence)
	}
	items, err := NewSQLLog(database).Replay(context.Background(), 0, 10)
	if err != nil || len(items) != 1 || items[0].Sequence != 1 {
		t.Fatalf("secondary log did not persist with its own cursor: %+v, %v", items, err)
	}
}
