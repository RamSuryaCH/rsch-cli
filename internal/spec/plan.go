package spec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/config"
	"github.com/opencode-ai/opencode/internal/engineering"
	"github.com/opencode-ai/opencode/internal/execution"
	"github.com/opencode-ai/opencode/internal/orchestration"
)

const planFile = "tasks.json"

type PlanTask struct {
	ID           string        `json:"id"`
	Title        string        `json:"title"`
	Command      []string      `json:"command"`
	Dependencies []string      `json:"dependencies,omitempty"`
	RetryLimit   int           `json:"retryLimit,omitempty"`
	Timeout      time.Duration `json:"timeout,omitempty"`
}

type Plan struct {
	Version int        `json:"version"`
	Tasks   []PlanTask `json:"tasks"`
}

// MaxConcurrency controls independent plan tasks. It is deliberately bounded
// by the CLI/configuration layer rather than being an implicit unlimited fanout.
var MaxConcurrency = 1

type TaskResult struct {
	TaskID   string    `json:"taskId"`
	Passed   bool      `json:"passed"`
	Attempts int       `json:"attempts"`
	Output   string    `json:"output,omitempty"`
	Error    string    `json:"error,omitempty"`
	ExitCode int       `json:"exitCode"`
	Started  time.Time `json:"startedAt"`
	Finished time.Time `json:"finishedAt"`
}

func (m Manager) planPath(name string) (string, error) {
	path, err := m.Path(name)
	if err != nil {
		return "", err
	}
	return filepath.Join(path, planFile), nil
}

func (m Manager) LoadPlan(name string) (Plan, error) {
	path, err := m.planPath(name)
	if err != nil {
		return Plan{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, err
	}
	var plan Plan
	if err := json.Unmarshal(data, &plan); err != nil {
		return Plan{}, err
	}
	if err := validatePlan(plan); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func (m Manager) SavePlan(name string, plan Plan) error {
	if plan.Version == 0 {
		plan.Version = 1
	}
	if err := validatePlan(plan); err != nil {
		return err
	}
	path, err := m.planPath(name)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o600)
}

func validatePlan(plan Plan) error {
	if plan.Version != 1 {
		return errors.New("unsupported plan version")
	}
	for _, task := range plan.Tasks {
		if strings.TrimSpace(task.ID) == "" || strings.TrimSpace(task.Title) == "" || len(task.Command) == 0 || strings.TrimSpace(task.Command[0]) == "" {
			return errors.New("task id, title, and command are required")
		}
		if task.RetryLimit < 0 || task.Timeout < 0 {
			return fmt.Errorf("task %q retry limit and timeout must not be negative", task.ID)
		}
		for _, arg := range task.Command {
			if strings.ContainsRune(arg, '\x00') {
				return fmt.Errorf("task %q command contains a NUL byte", task.ID)
			}
		}
	}
	_, err := engineering.DAG(toTasks(plan.Tasks))
	return err
}

func (m Manager) AddPlanTask(name string, task PlanTask) error {
	plan, err := m.LoadPlan(name)
	if err != nil {
		return err
	}
	for _, existing := range plan.Tasks {
		if existing.ID == task.ID {
			return fmt.Errorf("task %q already exists", task.ID)
		}
	}
	plan.Tasks = append(plan.Tasks, task)
	return m.SavePlan(name, plan)
}

func (m Manager) RunPlan(ctx context.Context, workspace, name string) ([]TaskResult, error) {
	plan, err := m.LoadPlan(name)
	if err != nil {
		return nil, err
	}
	if len(plan.Tasks) == 0 {
		return nil, errors.New("approved task plan is empty")
	}
	tasks := toTasks(plan.Tasks)
	byID := make(map[string]PlanTask, len(plan.Tasks))
	for _, task := range plan.Tasks {
		byID[task.ID] = task
	}
	var resultsMu sync.Mutex
	latest := make(map[string]TaskResult, len(tasks))
	concurrency := MaxConcurrency
	if concurrency <= 0 {
		concurrency = 1
	}
	runResults, runErr := (orchestration.Scheduler{MaxConcurrency: concurrency}).Run(ctx, tasks, func(runCtx context.Context, task engineering.Task) error {
		planned := byID[task.ID]
		timeout := planned.Timeout
		if timeout <= 0 {
			timeout = 10 * time.Minute
		}
		taskCtx, cancel := context.WithTimeout(runCtx, timeout)
		defer cancel()
		started := time.Now().UTC()
		result, err := runPlanCommand(taskCtx, workspace, planned)
		taskResult := TaskResult{TaskID: task.ID, Passed: err == nil, Output: result.Output, ExitCode: result.ExitCode, Started: started, Finished: time.Now().UTC()}
		if err != nil {
			taskResult.Error = err.Error()
		}
		// The scheduler owns ordering/retries; retain command evidence for callers.
		resultsMu.Lock()
		latest[task.ID] = taskResult
		resultsMu.Unlock()
		return err
	})
	resultsMu.Lock()
	defer resultsMu.Unlock()
	snapshot := func(scheduled []orchestration.RunResult) []TaskResult {
		out := make([]TaskResult, 0, len(scheduled))
		for _, scheduledResult := range scheduled {
			result := latest[scheduledResult.TaskID]
			result.TaskID = scheduledResult.TaskID
			result.Attempts = scheduledResult.Attempts
			result.Passed = scheduledResult.Status == engineering.StatusPassed
			if scheduledResult.Err != nil {
				result.Error = scheduledResult.Err.Error()
			}
			out = append(out, result)
		}
		return out
	}
	if runErr != nil {
		return snapshot(runResults), runErr
	}
	for _, result := range runResults {
		if result.Status != engineering.StatusPassed {
			return snapshot(runResults), fmt.Errorf("task plan failed: %s", result.TaskID)
		}
	}
	return snapshot(runResults), nil
}

func runPlanCommand(ctx context.Context, workspace string, planned PlanTask) (execution.Result, error) {
	if len(planned.Command) == 0 || strings.TrimSpace(planned.Command[0]) == "" {
		return execution.Result{}, errors.New("plan task command is required")
	}
	allowed := map[string]bool{filepath.Base(planned.Command[0]): true}
	autonomy := execution.Autonomous
	if loaded := config.Get(); loaded != nil {
		switch loaded.Autonomy {
		case "manual":
			autonomy = execution.Manual
		case "assisted":
			autonomy = execution.Assisted
		case "autonomous":
			autonomy = execution.Autonomous
		}
	}
	executor, err := execution.New(execution.Config{
		Policy:     execution.DefaultPolicy{AllowedCommands: allowed},
		Sandbox:    execution.LocalSandbox{AllowedCommands: allowed, WorkingDirectory: workspace},
		Autonomy:   autonomy,
		MaxTimeout: planned.Timeout,
	})
	if err != nil {
		return execution.Result{}, err
	}
	return executor.Execute(ctx, execution.Request{
		Tool:    execution.Spec{Name: "spec.plan.command", Timeout: planned.Timeout, Capabilities: []string{"process"}},
		Path:    workspace,
		Command: planned.Command,
	})
}

func toTasks(plans []PlanTask) []engineering.Task {
	out := make([]engineering.Task, 0, len(plans))
	for _, task := range plans {
		out = append(out, engineering.Task{ID: task.ID, Title: task.Title, Dependencies: task.Dependencies, RetryLimit: task.RetryLimit})
	}
	return out
}

func planCommand(command []string) string { return strings.Join(command, " ") }
