-- +goose Up
ALTER TABLE workflow_tasks ADD COLUMN command_json TEXT NOT NULL DEFAULT '[]';

-- +goose Down
ALTER TABLE workflow_tasks DROP COLUMN command_json;
