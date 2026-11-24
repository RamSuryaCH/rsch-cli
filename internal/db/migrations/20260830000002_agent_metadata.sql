-- +goose Up
ALTER TABLE workflow_agents ADD COLUMN skills_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE workflow_agents ADD COLUMN tools_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE workflow_agents ADD COLUMN permissions_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE workflow_agents ADD COLUMN worktree TEXT NOT NULL DEFAULT '';
ALTER TABLE workflow_agents ADD COLUMN tokens INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workflow_agents ADD COLUMN cost REAL NOT NULL DEFAULT 0.0;
ALTER TABLE workflow_agents ADD COLUMN duration_ns INTEGER NOT NULL DEFAULT 0;
ALTER TABLE workflow_agents ADD COLUMN files_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE workflow_agents ADD COLUMN errors_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE workflow_agents ADD COLUMN verification_json TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE workflow_agents DROP COLUMN verification_json;
ALTER TABLE workflow_agents DROP COLUMN errors_json;
ALTER TABLE workflow_agents DROP COLUMN files_json;
ALTER TABLE workflow_agents DROP COLUMN duration_ns;
ALTER TABLE workflow_agents DROP COLUMN cost;
ALTER TABLE workflow_agents DROP COLUMN tokens;
ALTER TABLE workflow_agents DROP COLUMN worktree;
ALTER TABLE workflow_agents DROP COLUMN permissions_json;
ALTER TABLE workflow_agents DROP COLUMN tools_json;
ALTER TABLE workflow_agents DROP COLUMN skills_json;
