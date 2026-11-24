// Package orchestration contains bounded, dependency-aware execution and
// validated agent lifecycle state. It does not decide which model to use.
package orchestration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/engineering"
)

type AgentStatus string

const (
	AgentCreated   AgentStatus = "created"
	AgentQueued    AgentStatus = "queued"
	AgentStarting  AgentStatus = "starting"
	AgentRunning   AgentStatus = "running"
	AgentWaiting   AgentStatus = "waiting"
	AgentBlocked   AgentStatus = "blocked"
	AgentPaused    AgentStatus = "paused"
	AgentResumed   AgentStatus = "resumed"
	AgentCanceled  AgentStatus = "canceled"
	AgentCancelled AgentStatus = "cancelled"
	AgentCompleted AgentStatus = "completed"
	AgentFailed    AgentStatus = "failed"
)

type Agent struct {
	ID           string        `json:"id"`
	Role         string        `json:"role"`
	Goal         string        `json:"goal"`
	TaskID       string        `json:"taskId"`
	Parent       string        `json:"parent,omitempty"`
	Model        string        `json:"model,omitempty"`
	Provider     string        `json:"provider,omitempty"`
	Skills       []string      `json:"skills,omitempty"`
	Tools        []string      `json:"tools,omitempty"`
	Permissions  []string      `json:"permissions,omitempty"`
	Risk         string        `json:"risk,omitempty"`
	Budget       int64         `json:"budget,omitempty"`
	Timeout      time.Duration `json:"timeout,omitempty"`
	Worktree     string        `json:"worktree,omitempty"`
	Tokens       int64         `json:"tokens,omitempty"`
	Cost         float64       `json:"cost,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
	Files        []string      `json:"files,omitempty"`
	Errors       []string      `json:"errors,omitempty"`
	Verification []string      `json:"verification,omitempty"`
	Status       AgentStatus   `json:"status"`
}

var ErrInvalidTransition = errors.New("invalid agent lifecycle transition")

func NewAgent(role, taskID string) Agent {
	return Agent{ID: uuid.NewString(), Role: role, TaskID: taskID, Status: AgentCreated}
}
func (a *Agent) Transition(next AgentStatus) error {
	valid := map[AgentStatus]map[AgentStatus]bool{
		AgentCreated:  {AgentQueued: true, AgentStarting: true, AgentRunning: true, AgentCanceled: true, AgentCancelled: true},
		AgentQueued:   {AgentStarting: true, AgentRunning: true, AgentCanceled: true, AgentCancelled: true},
		AgentStarting: {AgentRunning: true, AgentWaiting: true, AgentBlocked: true, AgentCanceled: true, AgentCancelled: true, AgentFailed: true},
		AgentRunning:  {AgentPaused: true, AgentWaiting: true, AgentBlocked: true, AgentCanceled: true, AgentCancelled: true, AgentCompleted: true, AgentFailed: true},
		AgentWaiting:  {AgentRunning: true, AgentBlocked: true, AgentCanceled: true, AgentCancelled: true},
		AgentBlocked:  {AgentQueued: true, AgentStarting: true, AgentCanceled: true, AgentCancelled: true},
		AgentPaused:   {AgentResumed: true, AgentRunning: true, AgentCanceled: true, AgentCancelled: true},
		AgentResumed:  {AgentRunning: true, AgentCanceled: true, AgentCancelled: true},
		AgentFailed:   {AgentQueued: true, AgentStarting: true, AgentRunning: true, AgentCanceled: true, AgentCancelled: true},
	}
	if !valid[a.Status][next] {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, a.Status, next)
	}
	a.Status = next
	return nil
}

type RunResult struct {
	TaskID     string
	Status     engineering.Status
	Err        error
	Attempts   int
	StartedAt  time.Time
	FinishedAt time.Time
}
type Runner func(context.Context, engineering.Task) error
type Scheduler struct {
	MaxConcurrency int
	RetryLimit     int
}

func (s Scheduler) Run(ctx context.Context, tasks []engineering.Task, runner Runner) ([]RunResult, error) {
	if runner == nil {
		return nil, errors.New("task runner is required")
	}
	if s.MaxConcurrency <= 0 {
		s.MaxConcurrency = 1
	}
	if s.RetryLimit < 0 {
		s.RetryLimit = 0
	}
	ordered, err := engineering.DAG(tasks)
	if err != nil {
		return nil, err
	}
	byID := map[string]engineering.Task{}
	for _, task := range ordered {
		byID[task.ID] = task
	}
	state := map[string]engineering.Status{}
	for id := range byID {
		state[id] = engineering.StatusPending
	}
	results := map[string]RunResult{}
	type completed struct{ result RunResult }
	done := make(chan completed, len(ordered))
	active := 0
	finishCanceled := func() ([]RunResult, error) {
		// Every cancellation path must wait for the context-aware workers to
		// finish cleanup before returning control to the caller.
		for active > 0 {
			item := <-done
			active--
			state[item.result.TaskID] = item.result.Status
			results[item.result.TaskID] = item.result
		}
		for _, task := range ordered {
			if _, ok := results[task.ID]; !ok {
				results[task.ID] = RunResult{TaskID: task.ID, Status: engineering.StatusCanceled, Err: ctx.Err()}
			}
		}
		return flatten(ordered, results), ctx.Err()
	}
	for len(results) < len(byID) {
		if err := ctx.Err(); err != nil {
			return finishCanceled()
		}
		for active < s.MaxConcurrency {
			var next engineering.Task
			found := false
			for _, task := range ordered {
				if state[task.ID] != engineering.StatusPending {
					continue
				}
				ready := true
				for _, dep := range task.Dependencies {
					if state[dep] != engineering.StatusPassed && state[dep] != engineering.StatusVerified && state[dep] != engineering.StatusCompleted {
						ready = false
						break
					}
				}
				if ready {
					next = task
					found = true
					break
				}
			}
			if !found {
				break
			}
			state[next.ID] = engineering.StatusRunning
			active++
			go func(task engineering.Task) {
				started := time.Now().UTC()
				attempts := 0
				var runErr error
				limit := s.RetryLimit
				if task.RetryLimit > limit {
					limit = task.RetryLimit
				}
				for attempt := 0; attempt <= limit; attempt++ {
					attempts = attempt + 1
					attemptCtx := ctx
					cancel := func() {}
					if task.Timeout > 0 {
						attemptCtx, cancel = context.WithTimeout(ctx, task.Timeout)
					}
					runErr = runner(attemptCtx, task)
					cancel()
					if runErr == nil {
						break
					}
					if ctx.Err() != nil {
						break
					}
				}
				status := engineering.StatusPassed
				if runErr != nil {
					status = engineering.StatusFailed
				}
				done <- completed{RunResult{TaskID: task.ID, Status: status, Err: runErr, Attempts: attempts, StartedAt: started, FinishedAt: time.Now().UTC()}}
			}(next)
		}
		if active == 0 {
			for id, status := range state {
				if status == engineering.StatusPending {
					state[id] = engineering.StatusBlocked
					results[id] = RunResult{TaskID: id, Status: engineering.StatusBlocked, Err: errors.New("blocked by failed or unresolved dependency")}
				}
			}
			break
		}
		select {
		case item := <-done:
			active--
			state[item.result.TaskID] = item.result.Status
			results[item.result.TaskID] = item.result
		case <-ctx.Done():
			return finishCanceled()
		}
	}
	return flatten(ordered, results), nil
}
func flatten(ordered []engineering.Task, results map[string]RunResult) []RunResult {
	out := make([]RunResult, 0, len(results))
	for _, task := range ordered {
		if result, ok := results[task.ID]; ok {
			out = append(out, result)
		}
	}
	return out
}
