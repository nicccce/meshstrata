-- name: GetMetadata :one
SELECT value FROM app_metadata WHERE key = ?;

-- name: CreateMetadata :exec
INSERT INTO app_metadata (key, value) VALUES (?, ?) ON CONFLICT(key) DO NOTHING;
