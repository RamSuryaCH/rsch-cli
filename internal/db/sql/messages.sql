-- name: GetMessage :one
SELECT *
FROM messages
WHERE id = ? LIMIT 1;

-- name: ListMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ?
ORDER BY sequence ASC;

-- name: CreateMessage :one
INSERT INTO messages (
    id,
    session_id,
    role,
    parts,
    model,
    sequence,
    state,
    created_at,
    updated_at
) SELECT
    sqlc.arg(id), sqlc.arg(session_id), sqlc.arg(role), sqlc.arg(parts), sqlc.arg(model),
    COALESCE(MAX(existing.sequence), 0) + 1,
    CASE WHEN sqlc.arg(role) = 'assistant' THEN 'pending' ELSE 'complete' END,
    strftime('%s', 'now'), strftime('%s', 'now')
FROM messages AS existing
WHERE existing.session_id = sqlc.arg(session_id)
RETURNING *;

-- name: MarkInterruptedSessionMessages :execrows
UPDATE messages
SET state = 'interrupted',
    updated_at = strftime('%s', 'now')
WHERE session_id = ? AND state = 'pending';

-- name: UpdateMessage :exec
UPDATE messages
SET
    parts = ?,
    model = ?,
    finished_at = ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?;


-- name: DeleteMessage :exec
DELETE FROM messages
WHERE id = ?;

-- name: DeleteSessionMessages :exec
DELETE FROM messages
WHERE session_id = ?;
