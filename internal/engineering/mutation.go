package engineering

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/opencode-ai/opencode/internal/events"
)

// TaskMutation receives an isolated snapshot of one goal's tasks and returns
// its replacement and the event describing that change. It runs while the
// store is locked: callbacks must not reenter the store or perform external
// side effects. Returning an error leaves the tasks and event log unchanged.
type TaskMutation func([]Task) ([]Task, events.Event, error)

// TransactionalTaskStore atomically reads and mutates a task collection. SQL
// stores also commit its event in the same transaction. A returned event with
// a positive sequence is already durable and must not be appended again.
type TransactionalTaskStore interface {
	MutateTasks(context.Context, string, TaskMutation) ([]Task, events.Event, error)
}

func validateTaskMutation(goalID string, mutate TaskMutation) error {
	if goalID == "" {
		return errors.New("goal id is required")
	}
	if mutate == nil {
		return errors.New("task mutation is required")
	}
	return nil
}

func prepareTaskMutation(goalID string, tasks []Task, event events.Event) ([]Task, events.Event, error) {
	ordered, err := DAG(tasks)
	if err != nil {
		return nil, events.Event{}, err
	}
	for i := range ordered {
		ordered[i] = prepareTask(ordered[i])
		ordered[i].GoalID = goalID
	}
	if event.ID == "" || event.Type == "" {
		return nil, events.Event{}, errors.New("task mutation event id and type are required")
	}
	if event.GoalID != "" && event.GoalID != goalID {
		return nil, events.Event{}, fmt.Errorf("task mutation event belongs to another goal")
	}
	event.GoalID = goalID
	event.Sequence = 0
	if _, err := event.JSON(); err != nil {
		return nil, events.Event{}, err
	}
	return ordered, event, nil
}

func (s *Store) MutateTasks(ctx context.Context, goalID string, mutate TaskMutation) ([]Task, events.Event, error) {
	if err := validateTaskMutation(goalID, mutate); err != nil {
		return nil, events.Event{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, events.Event{}, err
	}
	tasks, event, err := mutate(s.loadTasksLocked(goalID))
	if err != nil {
		return nil, events.Event{}, err
	}
	ordered, event, err := prepareTaskMutation(goalID, tasks, event)
	if err != nil {
		return nil, events.Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, events.Event{}, err
	}
	if err := s.replaceTasksLocked(goalID, ordered); err != nil {
		return nil, events.Event{}, err
	}
	return s.loadTasksLocked(goalID), event, nil
}

func (s *SQLStore) MutateTasks(ctx context.Context, goalID string, mutate TaskMutation) ([]Task, events.Event, error) {
	if err := validateTaskMutation(goalID, mutate); err != nil {
		return nil, events.Event{}, err
	}
	// SQLite's serializable transaction acquires the write reservation before
	// reading, so separate stores and connections cannot overwrite stale input.
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, events.Event{}, err
	}
	defer tx.Rollback()
	tasks, err := loadTasks(ctx, tx, goalID)
	if err != nil {
		return nil, events.Event{}, err
	}
	tasks, event, err := mutate(tasks)
	if err != nil {
		return nil, events.Event{}, err
	}
	ordered, event, err := prepareTaskMutation(goalID, tasks, event)
	if err != nil {
		return nil, events.Event{}, err
	}
	if err := replaceTasksTx(ctx, tx, goalID, ordered); err != nil {
		return nil, events.Event{}, err
	}
	if err := events.AppendEventTx(ctx, tx, &event); err != nil {
		return nil, events.Event{}, err
	}
	persisted, err := loadTasks(ctx, tx, goalID)
	if err != nil {
		return nil, events.Event{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, events.Event{}, err
	}
	return persisted, event, nil
}
