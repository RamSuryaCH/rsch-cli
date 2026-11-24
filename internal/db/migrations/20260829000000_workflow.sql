-- +goose Up
CREATE TABLE IF NOT EXISTS goals (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS requirements (
    id TEXT PRIMARY KEY,
    goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE CASCADE,
    statement TEXT NOT NULL,
    priority TEXT NOT NULL DEFAULT 'medium',
    acceptance_json TEXT NOT NULL DEFAULT '[]',
    dependencies_json TEXT NOT NULL DEFAULT '[]',
    risk TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    evidence_json TEXT NOT NULL DEFAULT '[]',
    confirmed INTEGER NOT NULL DEFAULT 0 CHECK (confirmed IN (0, 1))
);
CREATE INDEX IF NOT EXISTS idx_requirements_goal_id ON requirements(goal_id);

CREATE TABLE IF NOT EXISTS workflow_tasks (
    id TEXT PRIMARY KEY,
    goal_id TEXT NOT NULL REFERENCES goals(id) ON DELETE CASCADE,
    title TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    dependencies_json TEXT NOT NULL DEFAULT '[]',
    status TEXT NOT NULL,
    agent TEXT NOT NULL DEFAULT '',
    skills_json TEXT NOT NULL DEFAULT '[]',
    risk TEXT NOT NULL DEFAULT '',
    expected_files_json TEXT NOT NULL DEFAULT '[]',
    verification_json TEXT NOT NULL DEFAULT '[]',
    retry_limit INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_workflow_tasks_goal_id ON workflow_tasks(goal_id);

CREATE TABLE IF NOT EXISTS evidence (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES workflow_tasks(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    command TEXT NOT NULL DEFAULT '',
    passed INTEGER NOT NULL DEFAULT 0 CHECK (passed IN (0, 1)),
    observed_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_evidence_task_id ON evidence(task_id);

CREATE TABLE IF NOT EXISTS event_log (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    version INTEGER NOT NULL,
    type TEXT NOT NULL,
    occurred_at INTEGER NOT NULL,
    session_id TEXT,
    goal_id TEXT,
    task_id TEXT,
    actor TEXT,
    data_json TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS idx_event_log_occurred_at ON event_log(occurred_at);
CREATE INDEX IF NOT EXISTS idx_event_log_goal_id ON event_log(goal_id);

CREATE TABLE IF NOT EXISTS workflow_agents (
    id TEXT PRIMARY KEY,
    role TEXT NOT NULL,
    goal TEXT NOT NULL DEFAULT '',
    task_id TEXT NOT NULL DEFAULT '',
    parent TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    risk TEXT NOT NULL DEFAULT '',
    budget INTEGER NOT NULL DEFAULT 0,
    timeout_ns INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_workflow_agents_task_id ON workflow_agents(task_id);
CREATE INDEX IF NOT EXISTS idx_workflow_agents_status ON workflow_agents(status);

-- +goose Down
DROP TABLE IF EXISTS workflow_agents;
DROP TABLE IF EXISTS event_log;
DROP TABLE IF EXISTS evidence;
DROP TABLE IF EXISTS workflow_tasks;
DROP TABLE IF EXISTS requirements;
DROP TABLE IF EXISTS goals;
