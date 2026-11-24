package orchestration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/opencode-ai/opencode/internal/events"
	"github.com/opencode-ai/opencode/internal/security"
)

type Message struct {
	ID       string `json:"id"`
	From     string `json:"from"`
	To       string `json:"to"`
	GoalID   string `json:"goalId,omitempty"`
	TaskID   string `json:"taskId,omitempty"`
	Type     string `json:"type"`
	Severity string `json:"severity,omitempty"`
	Message  string `json:"message"`
	Artifact string `json:"artifact,omitempty"`
}

// SendMessage publishes a validated, structured agent handoff. The event bus
// is the durable transport when an event sink is configured, so communication
// remains inspectable after the conversational context is gone.
func (r *Registry) SendMessage(message Message) error {
	if err := message.Validate(); err != nil {
		return err
	}
	if message.ID == "" {
		message.ID = uuid.NewString()
	}
	event := events.New(events.AgentMessage, map[string]any{
		"messageId": message.ID,
		"from":      message.From,
		"to":        message.To,
		"type":      message.Type,
		"severity":  message.Severity,
		"message":   message.Message,
		"artifact":  message.Artifact,
	})
	event.GoalID, event.TaskID = message.GoalID, message.TaskID
	event.Actor = message.From
	r.bus.Publish(event)
	return nil
}

func (m Message) Validate() error {
	if m.From == "" || m.To == "" || m.Type == "" || m.Message == "" {
		return errors.New("agent message requires from, to, type, and message")
	}
	switch m.Type {
	case "finding", "warning", "dependency", "handoff", "artifact", "question", "status", "result":
		return nil
	default:
		return fmt.Errorf("unsupported agent message type %q", m.Type)
	}
}

type Registry struct {
	mu     sync.RWMutex
	agents map[string]Agent
	bus    *events.Bus
	store  *AgentStore
	limit  int
}

var ErrAgentLimit = errors.New("agent resource limit reached")

func NewRegistry(bus *events.Bus, limits ...int) *Registry {
	if bus == nil {
		bus = events.NewBus()
	}
	limit := 0
	if len(limits) > 0 && limits[0] > 0 {
		limit = limits[0]
	}
	return &Registry{agents: make(map[string]Agent), bus: bus, limit: limit}
}
func NewPersistentRegistry(ctx context.Context, db *sql.DB, bus *events.Bus, limits ...int) (*Registry, error) {
	r := NewRegistry(bus, limits...)
	r.store = &AgentStore{db: db}
	agents, err := r.store.List(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	if r.limit > 0 && activeAgentCount(agents) > r.limit {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: persisted-active=%d limit=%d", ErrAgentLimit, activeAgentCount(agents), r.limit)
	}
	for _, agent := range agents {
		r.agents[agent.ID] = agent
	}
	r.mu.Unlock()
	return r, nil
}
func (r *Registry) Create(agent Agent) (Agent, error) {
	if agent.ID == "" {
		return Agent{}, errors.New("agent id is required")
	}
	if agent.Role == "" {
		return Agent{}, errors.New("agent role is required")
	}
	agent = sanitizeAgent(agent)
	r.mu.Lock()
	active := activeAgentCount(r.ListLocked())
	if r.limit > 0 && !isTerminal(agent.Status) && active >= r.limit {
		r.mu.Unlock()
		return Agent{}, fmt.Errorf("%w: limit=%d", ErrAgentLimit, r.limit)
	}
	if _, exists := r.agents[agent.ID]; exists {
		r.mu.Unlock()
		return Agent{}, errors.New("agent already exists")
	}
	r.agents[agent.ID] = agent
	if r.store != nil {
		if err := r.store.Save(context.Background(), agent); err != nil {
			delete(r.agents, agent.ID)
			r.mu.Unlock()
			return Agent{}, err
		}
	}
	r.mu.Unlock()
	event := events.New(events.AgentCreated, map[string]any{"role": agent.Role, "taskId": agent.TaskID})
	event.Actor = agent.ID
	r.bus.Publish(event)
	return agent, nil
}

func activeAgentCount(agents []Agent) int {
	count := 0
	for _, agent := range agents {
		if !isTerminal(agent.Status) {
			count++
		}
	}
	return count
}

func isTerminal(status AgentStatus) bool {
	switch status {
	case AgentCompleted, AgentFailed, AgentCanceled, AgentCancelled:
		return true
	default:
		return false
	}
}

func agentEventType(status AgentStatus) events.Type {
	switch status {
	case AgentPaused:
		return events.AgentPaused
	case AgentResumed:
		return events.AgentResumed
	case AgentWaiting:
		return events.AgentWaiting
	case AgentBlocked:
		return events.AgentBlocked
	case AgentCompleted:
		return events.AgentCompleted
	case AgentFailed:
		return events.AgentFailed
	case AgentCanceled, AgentCancelled:
		return events.AgentCanceled
	default:
		return events.AgentStarted
	}
}

// ListLocked returns a snapshot for callers that already hold the registry
// mutex. Keeping this separate avoids a self-deadlock in Create while making
// the active-agent accounting rule explicit.
func (r *Registry) ListLocked() []Agent {
	out := make([]Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		out = append(out, agent)
	}
	return out
}
func (r *Registry) Transition(id string, next AgentStatus) error {
	r.mu.Lock()
	agent, ok := r.agents[id]
	if !ok {
		r.mu.Unlock()
		return errors.New("agent not found")
	}
	if err := agent.Transition(next); err != nil {
		r.mu.Unlock()
		return err
	}
	previous := r.agents[id]
	if r.limit > 0 && isTerminal(previous.Status) && !isTerminal(agent.Status) && activeAgentCount(r.ListLocked()) >= r.limit {
		r.mu.Unlock()
		return fmt.Errorf("%w: limit=%d", ErrAgentLimit, r.limit)
	}
	r.agents[id] = agent
	if r.store != nil {
		if err := r.store.Save(context.Background(), agent); err != nil {
			r.agents[id] = previous
			r.mu.Unlock()
			return err
		}
	}
	r.mu.Unlock()
	kind := agentEventType(next)
	event := events.New(kind, map[string]any{"status": next})
	event.Actor = id
	event.TaskID = agent.TaskID
	r.bus.Publish(event)
	return nil
}

// UpdateMetadata records observable execution details without allowing a
// caller to mutate the lifecycle state through the metadata path.
func (r *Registry) UpdateMetadata(id string, update Agent) error {
	r.mu.Lock()
	agent, ok := r.agents[id]
	if !ok {
		r.mu.Unlock()
		return errors.New("agent not found")
	}
	update.ID = agent.ID
	update.Status = agent.Status
	if update.Role == "" {
		update.Role = agent.Role
	}
	if update.TaskID == "" {
		update.TaskID = agent.TaskID
	}
	if update.Goal == "" {
		update.Goal = agent.Goal
	}
	if update.Parent == "" {
		update.Parent = agent.Parent
	}
	if update.Model == "" {
		update.Model = agent.Model
	}
	if update.Provider == "" {
		update.Provider = agent.Provider
	}
	if update.Risk == "" {
		update.Risk = agent.Risk
	}
	if update.Worktree == "" {
		update.Worktree = agent.Worktree
	}
	if update.Budget == 0 {
		update.Budget = agent.Budget
	}
	if update.Timeout == 0 {
		update.Timeout = agent.Timeout
	}
	if update.Tokens == 0 {
		update.Tokens = agent.Tokens
	}
	if update.Cost == 0 {
		update.Cost = agent.Cost
	}
	if update.Duration == 0 {
		update.Duration = agent.Duration
	}
	if update.Skills == nil {
		update.Skills = agent.Skills
	}
	if update.Tools == nil {
		update.Tools = agent.Tools
	}
	if update.Permissions == nil {
		update.Permissions = agent.Permissions
	}
	if update.Files == nil {
		update.Files = agent.Files
	}
	if update.Errors == nil {
		update.Errors = agent.Errors
	}
	if update.Verification == nil {
		update.Verification = agent.Verification
	}
	update = sanitizeAgent(update)
	r.agents[id] = update
	if r.store != nil {
		if err := r.store.Save(context.Background(), update); err != nil {
			r.agents[id] = agent
			r.mu.Unlock()
			return err
		}
	}
	r.mu.Unlock()
	return nil
}

// RecoverInterrupted makes a restart explicit: agents that were running when
// the process stopped become paused and can be resumed by the coordinator.
func (r *Registry) RecoverInterrupted() []Agent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []Agent{}
	for id, agent := range r.agents {
		if agent.Status == AgentRunning {
			agent.Status = AgentPaused
			r.agents[id] = agent
			if r.store != nil {
				_ = r.store.Save(context.Background(), agent)
			}
			out = append(out, agent)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func (r *Registry) Get(id string) (Agent, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[id]
	return agent, ok
}
func (r *Registry) List() []Agent {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Agent, 0, len(r.agents))
	for _, agent := range r.agents {
		out = append(out, agent)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

type AgentStore struct{ db *sql.DB }

func NewAgentStore(db *sql.DB) *AgentStore { return &AgentStore{db: db} }

// EnsureCapacity validates the persisted active-agent limit for a prospective
// save. The candidate is excluded from the current count so lifecycle updates
// for an already-active agent do not consume a second slot.
func (s *AgentStore) EnsureCapacity(ctx context.Context, limit int, id string, desired AgentStatus) error {
	if limit <= 0 || isTerminal(desired) {
		return nil
	}
	items, err := s.List(ctx)
	if err != nil {
		return err
	}
	active := 0
	candidateActive := false
	for _, item := range items {
		if item.ID == id {
			candidateActive = !isTerminal(item.Status)
			continue
		}
		if !isTerminal(item.Status) {
			active++
		}
	}
	if candidateActive {
		active++
	}
	if active >= limit && !candidateActive {
		return fmt.Errorf("%w: running=%d limit=%d", ErrAgentLimit, active, limit)
	}
	return nil
}

func (s *AgentStore) Save(ctx context.Context, agent Agent) error {
	if s == nil || s.db == nil {
		return errors.New("agent store database is required")
	}
	agent = sanitizeAgent(agent)
	skills, err := json.Marshal(agent.Skills)
	if err != nil {
		return err
	}
	tools, err := json.Marshal(agent.Tools)
	if err != nil {
		return err
	}
	permissions, err := json.Marshal(agent.Permissions)
	if err != nil {
		return err
	}
	files, err := json.Marshal(agent.Files)
	if err != nil {
		return err
	}
	errorsJSON, err := json.Marshal(agent.Errors)
	if err != nil {
		return err
	}
	verification, err := json.Marshal(agent.Verification)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Unix()
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_agents (id,role,goal,task_id,parent,model,provider,skills_json,tools_json,permissions_json,risk,budget,timeout_ns,worktree,tokens,cost,duration_ns,files_json,errors_json,verification_json,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET role=excluded.role,goal=excluded.goal,task_id=excluded.task_id,parent=excluded.parent,model=excluded.model,provider=excluded.provider,skills_json=excluded.skills_json,tools_json=excluded.tools_json,permissions_json=excluded.permissions_json,risk=excluded.risk,budget=excluded.budget,timeout_ns=excluded.timeout_ns,worktree=excluded.worktree,tokens=excluded.tokens,cost=excluded.cost,duration_ns=excluded.duration_ns,files_json=excluded.files_json,errors_json=excluded.errors_json,verification_json=excluded.verification_json,status=excluded.status,updated_at=excluded.updated_at`, agent.ID, agent.Role, agent.Goal, agent.TaskID, agent.Parent, agent.Model, agent.Provider, string(skills), string(tools), string(permissions), agent.Risk, agent.Budget, int64(agent.Timeout), agent.Worktree, agent.Tokens, agent.Cost, int64(agent.Duration), string(files), string(errorsJSON), string(verification), agent.Status, now, now)
	return err
}

func sanitizeAgent(agent Agent) Agent {
	agent.Role = security.RedactText(agent.Role)
	agent.Goal = security.RedactText(agent.Goal)
	agent.Parent = security.RedactText(agent.Parent)
	agent.Model = security.RedactText(agent.Model)
	agent.Provider = security.RedactText(agent.Provider)
	agent.Risk = security.RedactText(agent.Risk)
	agent.Worktree = security.RedactText(agent.Worktree)
	for i := range agent.Skills {
		agent.Skills[i] = security.RedactText(agent.Skills[i])
	}
	for i := range agent.Tools {
		agent.Tools[i] = security.RedactText(agent.Tools[i])
	}
	for i := range agent.Permissions {
		agent.Permissions[i] = security.RedactText(agent.Permissions[i])
	}
	for i := range agent.Files {
		agent.Files[i] = security.RedactText(agent.Files[i])
	}
	for i := range agent.Errors {
		agent.Errors[i] = security.RedactText(agent.Errors[i])
	}
	for i := range agent.Verification {
		agent.Verification[i] = security.RedactText(agent.Verification[i])
	}
	return agent
}
func (s *AgentStore) List(ctx context.Context) ([]Agent, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("agent store database is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,role,goal,task_id,parent,model,provider,skills_json,tools_json,permissions_json,risk,budget,timeout_ns,worktree,tokens,cost,duration_ns,files_json,errors_json,verification_json,status FROM workflow_agents ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		var a Agent
		var skills, tools, permissions, files, errorsJSON, verification string
		var timeout, duration int64
		if err := rows.Scan(&a.ID, &a.Role, &a.Goal, &a.TaskID, &a.Parent, &a.Model, &a.Provider, &skills, &tools, &permissions, &a.Risk, &a.Budget, &timeout, &a.Worktree, &a.Tokens, &a.Cost, &duration, &files, &errorsJSON, &verification, &a.Status); err != nil {
			return nil, err
		}
		a.Timeout = time.Duration(timeout)
		a.Duration = time.Duration(duration)
		if err := json.Unmarshal([]byte(skills), &a.Skills); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(tools), &a.Tools); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(permissions), &a.Permissions); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(files), &a.Files); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(errorsJSON), &a.Errors); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(verification), &a.Verification); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
