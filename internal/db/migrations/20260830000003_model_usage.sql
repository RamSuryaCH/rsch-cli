-- +goose Up
CREATE TABLE IF NOT EXISTS model_usage (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL DEFAULT '',
    agent_id TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cost REAL NOT NULL DEFAULT 0.0,
    duration_ns INTEGER NOT NULL DEFAULT 0,
    started_at INTEGER NOT NULL,
    finished_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_model_usage_session_id ON model_usage(session_id);
CREATE INDEX IF NOT EXISTS idx_model_usage_task_id ON model_usage(task_id);
CREATE INDEX IF NOT EXISTS idx_model_usage_provider_model ON model_usage(provider, model);

-- +goose Down
DROP TABLE IF EXISTS model_usage;
