-- +goose Up
ALTER TABLE messages ADD COLUMN sequence INTEGER NOT NULL DEFAULT 0;
ALTER TABLE messages ADD COLUMN state TEXT NOT NULL DEFAULT 'complete'
    CHECK (state IN ('pending', 'complete', 'interrupted'));

UPDATE messages
SET state = 'pending'
WHERE role = 'assistant' AND finished_at IS NULL;

-- Give existing messages a stable per-session order. The UUID tie-breaker is
-- deterministic for legacy rows that were written in the same second.
UPDATE messages
SET sequence = (
    SELECT COUNT(*)
    FROM messages AS earlier
    WHERE earlier.session_id = messages.session_id
      AND (
          earlier.created_at < messages.created_at OR
          (earlier.created_at = messages.created_at AND earlier.id <= messages.id)
      )
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_messages_session_sequence
    ON messages(session_id, sequence);

-- SQLite cannot add a self-referential foreign key with ALTER TABLE without
-- rebuilding all tables that already reference sessions. These guards provide
-- the same parent-existence invariant for new and updated session rows. Session
-- deletion uses a recursive CTE so descendants are removed in one statement.
-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS validate_session_parent_on_insert
BEFORE INSERT ON sessions
WHEN NEW.parent_session_id IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM sessions WHERE id = NEW.parent_session_id)
BEGIN
    SELECT RAISE(ABORT, 'parent session does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS validate_session_parent_on_update
BEFORE UPDATE OF parent_session_id ON sessions
WHEN NEW.parent_session_id IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM sessions WHERE id = NEW.parent_session_id)
BEGIN
    SELECT RAISE(ABORT, 'parent session does not exist');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER IF NOT EXISTS mark_message_complete
AFTER UPDATE OF finished_at ON messages
WHEN NEW.finished_at IS NOT NULL AND NEW.state != 'complete'
BEGIN
    UPDATE messages SET state = 'complete' WHERE id = NEW.id;
END;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER IF EXISTS mark_message_complete;
DROP TRIGGER IF EXISTS validate_session_parent_on_update;
DROP TRIGGER IF EXISTS validate_session_parent_on_insert;
DROP INDEX IF EXISTS idx_messages_session_sequence;
ALTER TABLE messages DROP COLUMN state;
ALTER TABLE messages DROP COLUMN sequence;
