package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/logging"
	"github.com/opencode-ai/opencode/internal/security"
)

type taskMutationError struct {
	status int
	err    error
}

func (e *taskMutationError) Error() string { return e.err.Error() }
func (e *taskMutationError) Unwrap() error { return e.err }

func writeTaskMutationError(w http.ResponseWriter, fallbackStatus int, err error) {
	var mutationErr *taskMutationError
	if errors.As(err, &mutationErr) {
		fallbackStatus = mutationErr.status
	}
	writeError(w, fallbackStatus, err)
}

// mutateTaskCollection uses storage-level isolation when available. SQL stores
// commit task state and its event together; the bus delivers only after commit.
// Legacy stores retain the existing server-level locking and publishing path.
func (s *Server) mutateTaskCollection(ctx context.Context, goalID string, mutation engineering.TaskMutation) ([]engineering.Task, error) {
	if store, ok := s.store.(engineering.TransactionalTaskStore); ok {
		var tasks []engineering.Task
		event, delivered, err := s.bus.PublishTransaction(ctx, s.log, func(ctx context.Context) (events.Event, error) {
			var event events.Event
			var err error
			tasks, event, err = store.MutateTasks(ctx, goalID, mutation)
			if err != nil {
				return events.Event{}, err
			}
			// Memory stores do not have a durable transaction. If configured,
			// their independent event log retains the existing best-effort path.
			if event.Sequence == 0 && s.log != nil {
				if err := s.log.AppendEvent(ctx, &event); err != nil {
					logging.Error("Server event persistence failed after mutation", "eventId", security.RedactText(event.ID), "eventType", security.RedactText(string(event.Type)), "error", security.RedactText(err.Error()))
				}
			}
			return event, nil
		})
		if err != nil {
			return nil, err
		}
		if !delivered {
			logging.Error("Server event delivery failed after mutation", "eventId", security.RedactText(event.ID), "eventType", security.RedactText(string(event.Type)))
		}
		return tasks, nil
	}
	tasks, err := s.store.LoadTasks(ctx, goalID)
	if err != nil {
		return nil, &taskMutationError{http.StatusInternalServerError, err}
	}
	tasks, event, err := mutation(tasks)
	if err != nil {
		return nil, err
	}
	if err := s.store.ReplaceTasks(ctx, goalID, tasks); err != nil {
		return nil, err
	}
	s.publish(ctx, event)
	return tasks, nil
}

func (s *Server) replaceTaskCollection(ctx context.Context, goalID string, tasks []engineering.Task, event events.Event) ([]engineering.Task, error) {
	return s.mutateTaskCollection(ctx, goalID, func([]engineering.Task) ([]engineering.Task, events.Event, error) {
		return tasks, event, nil
	})
}

func taskFromCollection(tasks []engineering.Task, id string) engineering.Task {
	for _, task := range tasks {
		if task.ID == id {
			return task
		}
	}
	return engineering.Task{}
}

func (s *Server) transitionTaskAtomically(ctx context.Context, goalID, taskID string, next engineering.Status) (engineering.Task, bool, error) {
	if _, ok := s.store.(engineering.TransactionalTaskStore); !ok {
		return engineering.Task{}, false, nil
	}
	event := events.New(taskEventType(next), map[string]any{"status": next})
	event.GoalID, event.TaskID = goalID, taskID
	tasks, err := s.mutateTaskCollection(ctx, goalID, func(tasks []engineering.Task) ([]engineering.Task, events.Event, error) {
		for i := range tasks {
			if tasks[i].ID != taskID {
				continue
			}
			if err := engineering.TransitionTask(tasks[i].Status, next); err != nil {
				return nil, events.Event{}, &taskMutationError{http.StatusConflict, err}
			}
			tasks[i].Status = next
			return tasks, event, nil
		}
		return nil, events.Event{}, &taskMutationError{http.StatusNotFound, fmt.Errorf("task %q is not part of goal %q", taskID, goalID)}
	})
	return taskFromCollection(tasks, taskID), true, err
}

func taskEventType(status engineering.Status) events.Type {
	switch status {
	case engineering.StatusCompleted, engineering.StatusVerified:
		return events.TaskCompleted
	case engineering.StatusFailed:
		return events.TaskFailed
	case engineering.StatusCanceled:
		return events.TaskCanceled
	case engineering.StatusBlocked:
		return events.TaskBlocked
	case engineering.StatusRetrying:
		return events.TaskRetrying
	case engineering.StatusPaused:
		return events.TaskPaused
	default:
		return events.TaskStarted
	}
}
