-- B02 owns node persistence queries after B00.
-- name: CreateNode :exec
INSERT INTO nodes (id, name, connection_mode, role, control_address, proxy_address, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetNode :one
SELECT * FROM nodes WHERE id = ?;

-- name: ListNodes :many
SELECT * FROM nodes WHERE id > ? ORDER BY id LIMIT ?;

-- name: RenameNode :execrows
UPDATE nodes SET name = ?, revision = revision + 1, updated_at = ?
WHERE id = ? AND revision = ? AND revision < 9007199254740991 AND removed_at IS NULL;
