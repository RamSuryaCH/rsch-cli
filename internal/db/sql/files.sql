-- name: GetFile :one
SELECT *
FROM files
WHERE id = ? LIMIT 1;

-- name: GetFileByPathAndSession :one
SELECT *
FROM files
WHERE path = ? AND session_id = ?
ORDER BY created_at DESC, rowid DESC
LIMIT 1;

-- name: ListFilesBySession :many
SELECT *
FROM files
WHERE session_id = ?
ORDER BY created_at ASC;

-- name: ListFilesByPath :many
SELECT *
FROM files
WHERE path = ? AND session_id = ?
ORDER BY created_at DESC, rowid DESC;

-- name: CreateFile :one
INSERT INTO files (
    id,
    session_id,
    path,
    content,
    version,
    created_at,
    updated_at
) VALUES (
    ?, ?, ?, ?, ?, strftime('%s', 'now'), strftime('%s', 'now')
)
RETURNING *;

-- name: UpdateFile :one
UPDATE files
SET
    content = ?,
    version = ?,
    updated_at = strftime('%s', 'now')
WHERE id = ?
RETURNING *;

-- name: DeleteFile :exec
DELETE FROM files
WHERE id = ?;

-- name: DeleteSessionFiles :exec
DELETE FROM files
WHERE session_id = ?;

-- name: ListLatestSessionFiles :many
SELECT f.*
FROM files f
WHERE f.session_id = ?
  AND NOT EXISTS (
      SELECT 1
      FROM files AS newer
      WHERE newer.session_id = f.session_id
        AND newer.path = f.path
        AND (
            newer.created_at > f.created_at OR
            (newer.created_at = f.created_at AND newer.rowid > f.rowid)
        )
  )
ORDER BY f.path;

-- name: ListNewFiles :many
SELECT *
FROM files
WHERE is_new = 1
ORDER BY created_at DESC;
