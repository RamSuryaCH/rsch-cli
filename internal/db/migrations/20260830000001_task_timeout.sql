-- +goose Up
ALTER TABLE workflow_tasks ADD COLUMN timeout_ns INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE workflow_tasks DROP COLUMN timeout_ns;
