package orchestration

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/opencode-ai/opencode/internal/engineering"
)

func TestSchedulerRetriesWithOneBasedAttempts(t *testing.T) {
	attempts := 0
	results, err := (Scheduler{MaxConcurrency: 1}).Run(context.Background(), []engineering.Task{{ID: "task", Title: "retry", RetryLimit: 2}}, func(context.Context, engineering.Task) error {
		attempts++
		if attempts < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil || len(results) != 1 || results[0].Attempts != 3 || attempts != 3 {
		t.Fatalf("unexpected retry result: %+v err=%v attempts=%d", results, err, attempts)
	}
}

func TestSchedulerUsesFreshTimeoutPerAttempt(t *testing.T) {
	attempts := 0
	results, err := (Scheduler{MaxConcurrency: 1}).Run(context.Background(), []engineering.Task{{ID: "task", Title: "timeout", RetryLimit: 1, Timeout: 20 * time.Millisecond}}, func(ctx context.Context, _ engineering.Task) error {
		attempts++
		if attempts == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	})
	if err != nil || len(results) != 1 || results[0].Attempts != 2 || attempts != 2 || results[0].Status != engineering.StatusPassed {
		t.Fatalf("unexpected timeout retry result: %+v err=%v attempts=%d", results, err, attempts)
	}
}

func TestSchedulerWaitsForContextAwareWorkersAndReturnsDeterministicOrder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		_, _ = (Scheduler{MaxConcurrency: 1}).Run(ctx, []engineering.Task{{ID: "b"}, {ID: "a"}}, func(ctx context.Context, task engineering.Task) error {
			close(started)
			<-ctx.Done()
			close(done)
			return ctx.Err()
		})
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("runner did not observe cancellation")
	}
}

// Cancel after the scheduler receives its first result, at the next context
// check. This exercises cancellation between iterations deterministically.
type cancelBetweenResultsContext struct {
	context.Context
	cancel context.CancelFunc
	checks atomic.Int32
}

func (c *cancelBetweenResultsContext) Err() error {
	if c.checks.Add(1) == 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestSchedulerDrainsWorkersWhenCanceledBetweenResults(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := &cancelBetweenResultsContext{Context: base, cancel: cancel}
	workerStarted := make(chan struct{})
	workerCanceled := make(chan struct{})
	finishCleanup := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(finishCleanup) }) }
	defer release()
	type outcome struct {
		results []RunResult
		err     error
	}
	returned := make(chan outcome, 1)
	go func() {
		results, err := (Scheduler{MaxConcurrency: 2}).Run(ctx, []engineering.Task{
			{ID: "a"}, {ID: "b"}, {ID: "c", Dependencies: []string{"b"}},
		}, func(ctx context.Context, task engineering.Task) error {
			if task.ID == "a" {
				<-workerStarted
				return nil
			}
			close(workerStarted)
			<-ctx.Done()
			close(workerCanceled)
			<-finishCleanup
			return context.Canceled
		})
		returned <- outcome{results: results, err: err}
	}()
	select {
	case <-workerCanceled:
	case <-time.After(time.Second):
		t.Fatal("worker did not observe cancellation between scheduler iterations")
	}
	select {
	case <-returned:
		t.Fatal("scheduler returned before active worker finished cleanup")
	default:
	}
	release()
	select {
	case got := <-returned:
		if !errors.Is(got.err, context.Canceled) || len(got.results) != 3 {
			t.Fatalf("cancellation should retain every task's result: %+v, err=%v", got.results, got.err)
		}
		for i, id := range []string{"a", "b", "c"} {
			if got.results[i].TaskID != id {
				t.Fatalf("results not in dependency order: %+v", got.results)
			}
		}
		if got.results[1].Attempts != 1 || !errors.Is(got.results[1].Err, context.Canceled) {
			t.Fatalf("running worker result was lost: %+v", got.results[1])
		}
		if got.results[2].Status != engineering.StatusCanceled || got.results[2].Attempts != 0 {
			t.Fatalf("pending task should be canceled without running: %+v", got.results[2])
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not return after worker cleanup")
	}
}

func TestSchedulerReturnsCanceledResultsForInitiallyCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results, err := (Scheduler{}).Run(ctx, []engineering.Task{{ID: "task"}}, func(context.Context, engineering.Task) error {
		t.Error("canceled scheduler must not run tasks")
		return nil
	})
	if !errors.Is(err, context.Canceled) || len(results) != 1 || results[0].Status != engineering.StatusCanceled {
		t.Fatalf("unexpected canceled results: %+v err=%v", results, err)
	}
}

func TestAgentLifecycleSupportsOperationalStates(t *testing.T) {
	agent := NewAgent("reviewer", "task")
	if err := agent.Transition(AgentQueued); err != nil {
		t.Fatal(err)
	}
	if err := agent.Transition(AgentStarting); err != nil {
		t.Fatal(err)
	}
	if err := agent.Transition(AgentWaiting); err != nil {
		t.Fatal(err)
	}
	if err := agent.Transition(AgentRunning); err != nil {
		t.Fatal(err)
	}
	if err := agent.Transition(AgentCompleted); err != nil {
		t.Fatal(err)
	}
}
