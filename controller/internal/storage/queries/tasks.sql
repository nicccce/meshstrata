-- B04 owns the Controller task ledger queries after B00.
-- name: InsertTask :exec
INSERT INTO tasks (id, kind, subject_id, target_revision, request_digest, idempotency_scope, idempotency_key, deadline, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (idempotency_scope, idempotency_key) DO NOTHING;

-- name: GetTaskByKey :one
SELECT * FROM tasks WHERE idempotency_scope = ? AND idempotency_key = ?;

-- name: GetTask :one
SELECT * FROM tasks WHERE id = ?;

-- name: ListTasks :many
SELECT * FROM tasks WHERE id > ? ORDER BY id LIMIT ?;

-- name: InsertTaskNode :exec
INSERT INTO task_nodes (task_id, node_id, target_revision, target_digest, revocation_epoch) VALUES (?, ?, ?, ?, ?);

-- name: InsertTaskDependency :exec
INSERT INTO task_dependencies (task_id, node_id, requires_node_id) VALUES (?, ?, ?);

-- name: GetTaskNode :one
SELECT * FROM task_nodes WHERE task_id = ? AND node_id = ?;

-- name: RecordTaskResult :execrows
UPDATE task_nodes SET applied = ?, verification = ?, rollback_state = ?, cleanup_state = ?, result_sequence = ?, result_digest = ?, error_code = ?, confirmed_at = ?
WHERE task_id = ? AND node_id = ? AND target_revision = ? AND target_digest = ? AND result_sequence < ?;
