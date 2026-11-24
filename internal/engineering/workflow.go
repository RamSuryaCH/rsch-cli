// Package engineering contains provider- and UI-independent engineering workflow
// primitives.  It deliberately has no dependency on the TUI or LLM packages so
// that the same state machine can be used by the CLI, future API clients, and
// background workers.
package engineering

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/opencode-ai/opencode/internal/security"
)

type Status string

const (
	StatusPending   Status = "pending"
	StatusCreated   Status = "created"
	StatusAnalyzing Status = "analyzing"
	StatusPlanning  Status = "planning"
	StatusExecuting Status = "executing"
	StatusVerifying Status = "verifying"
	StatusReady     Status = "ready"
	StatusRunning   Status = "running"
	StatusBlocked   Status = "blocked"
	StatusPassed    Status = "passed"
	StatusFailed    Status = "failed"
	StatusRetrying  Status = "retrying"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusVerified  Status = "verified"
	StatusCanceled  Status = "canceled"
)

type Goal struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type Requirement struct {
	ID           string   `json:"id"`
	GoalID       string   `json:"goalId"`
	Statement    string   `json:"statement"`
	Priority     string   `json:"priority,omitempty"`
	Acceptance   []string `json:"acceptance"`
	Dependencies []string `json:"dependencies,omitempty"`
	Risk         string   `json:"risk,omitempty"`
	Status       Status   `json:"status"`
	Evidence     []string `json:"evidence,omitempty"`
	Confirmed    bool     `json:"confirmed"`
}

type Task struct {
	ID            string        `json:"id"`
	GoalID        string        `json:"goalId,omitempty"`
	Title         string        `json:"title"`
	Description   string        `json:"description"`
	Command       []string      `json:"command,omitempty"`
	Dependencies  []string      `json:"dependencies"`
	Status        Status        `json:"status"`
	Agent         string        `json:"agent,omitempty"`
	Skills        []string      `json:"skills,omitempty"`
	Risk          string        `json:"risk,omitempty"`
	ExpectedFiles []string      `json:"expectedFiles,omitempty"`
	Verification  []string      `json:"verification,omitempty"`
	RetryLimit    int           `json:"retryLimit,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
}

type Evidence struct {
	ID          string    `json:"id"`
	TaskID      string    `json:"taskId"`
	Kind        string    `json:"kind"`
	Description string    `json:"description"`
	Command     string    `json:"command,omitempty"`
	Passed      bool      `json:"passed"`
	ObservedAt  time.Time `json:"observedAt"`
}

func TransitionGoal(current, next Status) error {
	allowed := map[Status]map[Status]bool{
		StatusCreated:   map[Status]bool{StatusAnalyzing: true, StatusCanceled: true},
		StatusAnalyzing: map[Status]bool{StatusPlanning: true, StatusFailed: true, StatusCanceled: true},
		StatusPlanning:  map[Status]bool{StatusExecuting: true, StatusFailed: true, StatusCanceled: true},
		StatusExecuting: map[Status]bool{StatusVerifying: true, StatusFailed: true, StatusCanceled: true},
		StatusVerifying: map[Status]bool{StatusCompleted: true, StatusFailed: true, StatusCanceled: true},
		StatusFailed:    map[Status]bool{StatusAnalyzing: true, StatusCanceled: true},
	}
	if !allowed[current][next] {
		return fmt.Errorf("invalid goal transition: %s -> %s", current, next)
	}
	return nil
}

func TransitionTask(current, next Status) error {
	allowed := map[Status]map[Status]bool{
		StatusPending:   map[Status]bool{StatusReady: true, StatusCanceled: true},
		StatusReady:     map[Status]bool{StatusRunning: true, StatusCanceled: true, StatusBlocked: true},
		StatusRunning:   map[Status]bool{StatusBlocked: true, StatusFailed: true, StatusRetrying: true, StatusPaused: true, StatusCompleted: true, StatusCanceled: true},
		StatusBlocked:   map[Status]bool{StatusReady: true, StatusCanceled: true},
		StatusFailed:    map[Status]bool{StatusRunning: true, StatusRetrying: true, StatusCanceled: true},
		StatusRetrying:  map[Status]bool{StatusReady: true, StatusRunning: true, StatusCanceled: true},
		StatusPaused:    map[Status]bool{StatusReady: true, StatusCanceled: true},
		StatusCompleted: map[Status]bool{StatusVerified: true},
		StatusVerified:  map[Status]bool{},
	}
	if !allowed[current][next] {
		return fmt.Errorf("invalid task transition: %s -> %s", current, next)
	}
	return nil
}

// DAG validates dependencies and returns tasks in a deterministic dependency
// order. It rejects missing dependencies and cycles instead of silently
// scheduling unsafe work.
func DAG(tasks []Task) ([]Task, error) {
	byID := make(map[string]Task, len(tasks))
	for _, task := range tasks {
		if task.ID == "" {
			return nil, errors.New("task id is required")
		}
		if _, exists := byID[task.ID]; exists {
			return nil, fmt.Errorf("duplicate task id %q", task.ID)
		}
		byID[task.ID] = task
	}
	state := make(map[string]uint8, len(tasks))
	ordered := make([]Task, 0, len(tasks))
	var visit func(string) error
	visit = func(id string) error {
		switch state[id] {
		case 1:
			return fmt.Errorf("task dependency cycle includes %q", id)
		case 2:
			return nil
		}
		task, ok := byID[id]
		if !ok {
			return fmt.Errorf("task %q depends on missing task", id)
		}
		state[id] = 1
		deps := append([]string(nil), task.Dependencies...)
		sort.Strings(deps)
		for _, dep := range deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		ordered = append(ordered, task)
		return nil
	}
	ids := make([]string, 0, len(tasks))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return ordered, nil
}

// Store is an in-memory, concurrency-safe workflow store. Persistence is kept
// behind this interface so SQLite and API-backed stores can be added without
// coupling workflow rules to a client.
type Store struct {
	mu           sync.RWMutex
	goals        map[string]Goal
	requirements map[string]Requirement
	tasks        map[string]Task
	evidence     map[string]Evidence
}

func NewStore() *Store {
	return &Store{goals: map[string]Goal{}, requirements: map[string]Requirement{}, tasks: map[string]Task{}, evidence: map[string]Evidence{}}
}

func PrepareGoal(goal Goal) Goal {
	goal.Title = security.RedactText(goal.Title)
	goal.Description = security.RedactText(goal.Description)
	now := time.Now().UTC()
	if goal.CreatedAt.IsZero() {
		goal.CreatedAt = now
	}
	goal.UpdatedAt = now
	if goal.Status == "" {
		goal.Status = StatusCreated
	}
	return goal
}
func (s *Store) PutGoal(goal Goal) error {
	if goal.ID == "" || goal.Title == "" {
		return errors.New("goal id and title are required")
	}
	goal = PrepareGoal(goal)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.goals[goal.ID] = goal
	return nil
}
func (s *Store) PutRequirement(req Requirement) error {
	if req.ID == "" || req.GoalID == "" || req.Statement == "" {
		return errors.New("requirement id, goal id, and statement are required")
	}
	req.Statement = security.RedactText(req.Statement)
	req.Risk = security.RedactText(req.Risk)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requirements[req.ID] = req
	return nil
}
func (s *Store) SaveRequirement(_ context.Context, req Requirement) error {
	return s.PutRequirement(req)
}
func (s *Store) Requirements(_ context.Context, goalID string) ([]Requirement, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Requirement, 0)
	for _, req := range s.requirements {
		if req.GoalID == goalID {
			out = append(out, req)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (s *Store) PutTasks(tasks []Task) error {
	ordered, err := DAG(tasks)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.putTasksLocked(ordered)
	return nil
}

func (s *Store) putTasksLocked(tasks []Task) {
	for _, task := range tasks {
		s.tasks[task.ID] = prepareTask(task)
	}
}

func cloneTask(task Task) Task {
	task.Command = slices.Clone(task.Command)
	task.Dependencies = slices.Clone(task.Dependencies)
	task.Skills = slices.Clone(task.Skills)
	task.ExpectedFiles = slices.Clone(task.ExpectedFiles)
	task.Verification = slices.Clone(task.Verification)
	return task
}

func prepareTask(task Task) Task {
	task = cloneTask(task)
	task.Title = security.RedactText(task.Title)
	task.Description = security.RedactText(task.Description)
	task.Risk = security.RedactText(task.Risk)
	task.Agent = security.RedactText(task.Agent)
	if task.Status == "" {
		task.Status = StatusPending
	}
	return task
}

func (s *Store) ReplaceTasks(_ context.Context, goalID string, tasks []Task) error {
	if goalID == "" {
		return errors.New("goal id is required")
	}
	ordered, err := DAG(tasks)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaceTasksLocked(goalID, ordered)
}

func (s *Store) replaceTasksLocked(goalID string, ordered []Task) error {
	// Match the SQL store's globally unique task IDs without transferring
	// another goal's tasks or partially replacing this goal on failure.
	for i := range ordered {
		if existing, ok := s.tasks[ordered[i].ID]; ok && existing.GoalID != goalID {
			return fmt.Errorf("task %q belongs to another goal", ordered[i].ID)
		}
		ordered[i].GoalID = goalID
	}
	for id, task := range s.tasks {
		if task.GoalID == goalID {
			delete(s.tasks, id)
		}
	}
	s.putTasksLocked(ordered)
	return nil
}
func (s *Store) LoadTasks(_ context.Context, goalID string) ([]Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loadTasksLocked(goalID), nil
}

func (s *Store) loadTasksLocked(goalID string) []Task {
	out := make([]Task, 0)
	for _, task := range s.tasks {
		if goalID == "" || task.GoalID == goalID {
			out = append(out, cloneTask(task))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) TransitionTask(_ context.Context, id string, next Status) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task, ok := s.tasks[id]
	if !ok {
		return Task{}, fmt.Errorf("task %q not found", id)
	}
	if err := TransitionTask(task.Status, next); err != nil {
		return Task{}, err
	}
	task.Status = next
	s.tasks[id] = task
	return cloneTask(task), nil
}
func (s *Store) PutEvidence(e Evidence) error {
	if e.ID == "" || e.TaskID == "" || e.Kind == "" {
		return errors.New("evidence id, task id, and kind are required")
	}
	if e.ObservedAt.IsZero() {
		e.ObservedAt = time.Now().UTC()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evidence[e.ID] = e
	return nil
}
func (s *Store) Goals() []Goal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Goal, 0, len(s.goals))
	for _, v := range s.goals {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (s *Store) Tasks() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.tasks))
	for _, v := range s.tasks {
		out = append(out, cloneTask(v))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ValidateCompletion enforces evidence-based completion. A goal cannot be
// reported complete while any requirement is unconfirmed, any task is not in
// a successful terminal state, or a task has no passing evidence.
func ValidateCompletion(goal Goal, requirements []Requirement, tasks []Task, evidence []Evidence) error {
	if goal.ID == "" {
		return errors.New("goal id is required")
	}
	for _, req := range requirements {
		if req.GoalID == goal.ID && !req.Confirmed {
			return fmt.Errorf("requirement %q is not confirmed", req.ID)
		}
	}
	passed := make(map[string]bool, len(evidence))
	for _, item := range evidence {
		if item.Passed {
			passed[item.TaskID] = true
		}
	}
	for _, task := range tasks {
		if task.Status != StatusPassed && task.Status != StatusCompleted && task.Status != StatusVerified {
			return fmt.Errorf("task %q is not passed", task.ID)
		}
		if !passed[task.ID] {
			return fmt.Errorf("task %q has no passing evidence", task.ID)
		}
	}
	return nil
}
