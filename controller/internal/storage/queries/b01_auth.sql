-- name: B01ConsumeBootstrap :execrows
UPDATE b01_auth_state SET initialized = 1 WHERE id = 1 AND initialized = 0;

-- name: B01CreateAdministrator :exec
INSERT INTO b01_administrator (id, username, password_hash, created_at, updated_at) VALUES (1, ?, ?, ?, ?);

-- name: B01GetAdministrator :one
SELECT * FROM b01_administrator WHERE id = 1;

-- name: B01ChangePassword :execrows
UPDATE b01_administrator SET password_hash = sqlc.arg(new_hash), revision = revision + 1, updated_at = sqlc.arg(now)
WHERE id = 1 AND revision = sqlc.arg(expected_revision) AND password_hash = sqlc.arg(old_hash);

-- name: B01CreateSession :exec
INSERT INTO b01_sessions(token_hash, admin_id, admin_revision, created_at, expires_at)
VALUES (?, 1, ?, ?, ?);

-- name: B01GetSession :one
SELECT s.token_hash, s.expires_at, a.username, a.revision FROM b01_sessions s
JOIN b01_administrator a ON a.id = s.admin_id AND a.revision = s.admin_revision
WHERE s.token_hash = ? AND s.expires_at > ?;

-- name: B01DeleteSession :exec
DELETE FROM b01_sessions WHERE token_hash = ?;

-- name: B01DeleteAllSessions :exec
DELETE FROM b01_sessions;

-- name: B01DeleteExpiredSessions :exec
DELETE FROM b01_sessions WHERE expires_at <= ?;

-- name: B01GetAttempt :one
SELECT * FROM b01_auth_limits WHERE bucket = ?;

-- name: B01SetAttempt :exec
INSERT INTO b01_auth_limits(bucket, window_start, attempts) VALUES (?, ?, ?)
ON CONFLICT(bucket) DO UPDATE SET window_start = excluded.window_start, attempts = excluded.attempts;
