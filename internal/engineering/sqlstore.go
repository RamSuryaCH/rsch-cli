package engineering

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/opencode-ai/opencode/internal/security"
)

// SQLStore persists workflow state in the application's SQLite database. It
// uses context-aware statements so shutdown and cancellation remain reliable.
type SQLStore struct{ db *sql.DB }

func NewSQLStore(db *sql.DB) *SQLStore { return &SQLStore{db: db} }

func (s *SQLStore) PutGoal(goal Goal) error { return s.SaveGoal(context.Background(), goal) }
func (s *SQLStore) Goals() []Goal {
	rows, err := s.db.Query(`SELECT id,title,description,status,created_at,updated_at FROM goals ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]Goal, 0)
	for rows.Next() {
		var goal Goal
		var created, updated int64
		if err := rows.Scan(&goal.ID, &goal.Title, &goal.Description, &goal.Status, &created, &updated); err != nil {
			return out
		}
		goal.CreatedAt = time.UnixMilli(created).UTC()
		goal.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, goal)
	}
	return out
}
func (s *SQLStore) Tasks() []Task {
	rows, err := s.db.Query(`SELECT id,goal_id,title,description,dependencies_json,status,agent,skills_json,risk,expected_files_json,verification_json,retry_limit,command_json,timeout_ns FROM workflow_tasks ORDER BY id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := make([]Task, 0)
	for rows.Next() {
		var task Task
		var deps, skills, expected, verification, command string
		var timeout int64
		if err := rows.Scan(&task.ID, &task.GoalID, &task.Title, &task.Description, &deps, &task.Status, &task.Agent, &skills, &task.Risk, &expected, &verification, &task.RetryLimit, &command, &timeout); err != nil {
			return out
		}
		task.Timeout = time.Duration(timeout)
		_ = json.Unmarshal([]byte(command), &task.Command)
		_ = json.Unmarshal([]byte(deps), &task.Dependencies)
		_ = json.Unmarshal([]byte(skills), &task.Skills)
		_ = json.Unmarshal([]byte(expected), &task.ExpectedFiles)
		_ = json.Unmarshal([]byte(verification), &task.Verification)
		out = append(out, task)
	}
	return out
}

func (s *SQLStore) SaveGoal(ctx context.Context, goal Goal) error {
	if goal.ID == "" || goal.Title == "" {
		return fmt.Errorf("goal id and title are required")
	}
	goal = PrepareGoal(goal)
	_, err := s.db.ExecContext(ctx, `INSERT INTO goals(id,title,description,status,created_at,updated_at) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,description=excluded.description,status=excluded.status,updated_at=excluded.updated_at`, goal.ID, goal.Title, goal.Description, goal.Status, goal.CreatedAt.UnixMilli(), goal.UpdatedAt.UnixMilli())
	return err
}

func (s *SQLStore) SaveRequirement(ctx context.Context, req Requirement) error {
	if req.ID == "" || req.GoalID == "" || req.Statement == "" {
		return fmt.Errorf("requirement id, goal id, and statement are required")
	}
	req.Statement = security.RedactText(req.Statement)
	req.Risk = security.RedactText(req.Risk)
	acceptance, err := json.Marshal(req.Acceptance)
	if err != nil {
		return err
	}
	dependencies, err := json.Marshal(req.Dependencies)
	if err != nil {
		return err
	}
	evidence, err := json.Marshal(req.Evidence)
	if err != nil {
		return err
	}
	status := req.Status
	if status == "" {
		status = StatusPending
	}
	priority := req.Priority
	if priority == "" {
		priority = "medium"
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO requirements(id,goal_id,statement,priority,acceptance_json,dependencies_json,risk,status,evidence_json,confirmed) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET statement=excluded.statement,priority=excluded.priority,acceptance_json=excluded.acceptance_json,dependencies_json=excluded.dependencies_json,risk=excluded.risk,status=excluded.status,evidence_json=excluded.evidence_json,confirmed=excluded.confirmed`, req.ID, req.GoalID, req.Statement, priority, string(acceptance), string(dependencies), req.Risk, status, string(evidence), boolInt(req.Confirmed))
	return err
}
func (s *SQLStore) Requirements(ctx context.Context, goalID string) ([]Requirement, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,goal_id,statement,priority,acceptance_json,dependencies_json,risk,status,evidence_json,confirmed FROM requirements WHERE goal_id=? ORDER BY id`, goalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Requirement, 0)
	for rows.Next() {
		var req Requirement
		var acceptance, dependencies, evidence string
		var confirmed int
		if err := rows.Scan(&req.ID, &req.GoalID, &req.Statement, &req.Priority, &acceptance, &dependencies, &req.Risk, &req.Status, &evidence, &confirmed); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(acceptance), &req.Acceptance); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(dependencies), &req.Dependencies); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(evidence), &req.Evidence); err != nil {
			return nil, err
		}
		req.Confirmed = confirmed == 1
		out = append(out, req)
	}
	return out, rows.Err()
}

func (s *SQLStore) ReplaceTasks(ctx context.Context, goalID string, tasks []Task) error {
	if goalID == "" {
		return fmt.Errorf("goal id is required")
	}
	ordered, err := DAG(tasks)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := replaceTasksTx(ctx, tx, goalID, ordered); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceTasksTx(ctx context.Context, tx *sql.Tx, goalID string, ordered []Task) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM workflow_tasks WHERE goal_id=?`, goalID)
	if err != nil {
		return err
	}
	remove := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		remove[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, task := range ordered {
		task = prepareTask(task)
		delete(remove, task.ID)
		deps, err := json.Marshal(task.Dependencies)
		if err != nil {
			return err
		}
		skills, err := json.Marshal(task.Skills)
		if err != nil {
			return err
		}
		expected, err := json.Marshal(task.ExpectedFiles)
		if err != nil {
			return err
		}
		verification, err := json.Marshal(task.Verification)
		if err != nil {
			return err
		}
		command, err := json.Marshal(task.Command)
		if err != nil {
			return err
		}
		// Updating retained rows preserves their evidence. A task ID owned by
		// another goal must never be transferred through collection replacement.
		result, err := tx.ExecContext(ctx, `INSERT INTO workflow_tasks(id,goal_id,title,description,dependencies_json,status,agent,skills_json,risk,expected_files_json,verification_json,retry_limit,command_json,timeout_ns) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET title=excluded.title,description=excluded.description,dependencies_json=excluded.dependencies_json,status=excluded.status,agent=excluded.agent,skills_json=excluded.skills_json,risk=excluded.risk,expected_files_json=excluded.expected_files_json,verification_json=excluded.verification_json,retry_limit=excluded.retry_limit,command_json=excluded.command_json,timeout_ns=excluded.timeout_ns WHERE workflow_tasks.goal_id=excluded.goal_id`, task.ID, goalID, task.Title, task.Description, string(deps), task.Status, task.Agent, string(skills), task.Risk, string(expected), string(verification), task.RetryLimit, string(command), int64(task.Timeout))
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			return fmt.Errorf("task %q belongs to another goal", task.ID)
		}
	}
	for id := range remove {
		if _, err := tx.ExecContext(ctx, `DELETE FROM workflow_tasks WHERE id=? AND goal_id=?`, id, goalID); err != nil {
			return err
		}
	}
	return nil
}

func (s *SQLStore) AddEvidence(ctx context.Context, evidence Evidence) error {
	if evidence.ID == "" || evidence.TaskID == "" || evidence.Kind == "" {
		return fmt.Errorf("evidence id, task id, and kind are required")
	}
	if evidence.ObservedAt.IsZero() {
		evidence.ObservedAt = time.Now().UTC()
	}
	evidence.Kind = security.RedactText(evidence.Kind)
	evidence.Description = security.RedactText(evidence.Description)
	evidence.Command = security.RedactText(evidence.Command)
	_, err := s.db.ExecContext(ctx, `INSERT INTO evidence(id,task_id,kind,description,command,passed,observed_at) VALUES(?,?,?,?,?,?,?)`, evidence.ID, evidence.TaskID, evidence.Kind, evidence.Description, evidence.Command, boolInt(evidence.Passed), evidence.ObservedAt.UnixMilli())
	return err
}

func (s *SQLStore) LoadEvidence(ctx context.Context, taskID string) ([]Evidence, error) {
	if taskID == "" {
		return nil, fmt.Errorf("task id is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,task_id,kind,description,command,passed,observed_at FROM evidence WHERE task_id=? ORDER BY observed_at,id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Evidence, 0)
	for rows.Next() {
		var item Evidence
		var passed int
		var observed int64
		if err := rows.Scan(&item.ID, &item.TaskID, &item.Kind, &item.Description, &item.Command, &passed, &observed); err != nil {
			return nil, err
		}
		item.Passed = passed == 1
		item.ObservedAt = time.UnixMilli(observed).UTC()
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *SQLStore) LoadTasks(ctx context.Context, goalID string) ([]Task, error) {
	return loadTasks(ctx, s.db, goalID)
}

type taskQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadTasks(ctx context.Context, queryer taskQuerier, goalID string) ([]Task, error) {
	query := `SELECT id,title,description,dependencies_json,status,agent,skills_json,risk,expected_files_json,verification_json,retry_limit,command_json,timeout_ns FROM workflow_tasks WHERE goal_id=? ORDER BY id`
	args := []any{goalID}
	if goalID == "" {
		query = `SELECT id,goal_id,title,description,dependencies_json,status,agent,skills_json,risk,expected_files_json,verification_json,retry_limit,command_json,timeout_ns FROM workflow_tasks ORDER BY id`
		args = nil
	}
	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]Task, 0)
	for rows.Next() {
		var task Task
		task.GoalID = goalID
		var deps, skills, expected, verification, command string
		var timeout int64
		var scanErr error
		if goalID == "" {
			scanErr = rows.Scan(&task.ID, &task.GoalID, &task.Title, &task.Description, &deps, &task.Status, &task.Agent, &skills, &task.Risk, &expected, &verification, &task.RetryLimit, &command, &timeout)
		} else {
			task.GoalID = goalID
			scanErr = rows.Scan(&task.ID, &task.Title, &task.Description, &deps, &task.Status, &task.Agent, &skills, &task.Risk, &expected, &verification, &task.RetryLimit, &command, &timeout)
		}
		if scanErr != nil {
			return nil, scanErr
		}
		task.Timeout = time.Duration(timeout)
		if err := json.Unmarshal([]byte(deps), &task.Dependencies); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(skills), &task.Skills); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(expected), &task.ExpectedFiles); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(verification), &task.Verification); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(command), &task.Command); err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, rows.Err()
}

// TransitionTask validates and persists a single task state transition.
func (s *SQLStore) TransitionTask(ctx context.Context, id string, next Status) (Task, error) {
	if id == "" {
		return Task{}, fmt.Errorf("task id is required")
	}
	items, err := s.LoadTasks(ctx, "")
	if err != nil {
		return Task{}, err
	}
	for _, task := range items {
		if task.ID != id {
			continue
		}
		if err := TransitionTask(task.Status, next); err != nil {
			return Task{}, err
		}
		// Another caller may have transitioned or removed the task since it
		// was read. Only persist the transition from the validated state.
		result, err := s.db.ExecContext(ctx, `UPDATE workflow_tasks SET status=? WHERE id=? AND status=?`, next, id, task.Status)
		if err != nil {
			return Task{}, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return Task{}, err
		}
		if changed != 1 {
			return Task{}, fmt.Errorf("task %q changed during transition", id)
		}
		task.Status = next
		return task, nil
	}
	return Task{}, fmt.Errorf("task %q not found", id)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
