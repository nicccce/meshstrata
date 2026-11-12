-- name: B01InsertCredential :exec
INSERT INTO credentials(id, purpose, key_id, ciphertext, created_at) VALUES (?, ?, ?, ?, ?);

-- name: B01GetCredential :one
SELECT * FROM credentials WHERE id = ? AND purpose = ? AND revoked_at IS NULL;

-- name: B01RevokeCredential :execrows
UPDATE credentials SET revoked_at = ? WHERE id = ? AND purpose = ? AND revoked_at IS NULL;

-- name: B01RotateCredential :execrows
UPDATE credentials SET key_id = sqlc.arg(new_key_id), ciphertext = sqlc.arg(new_ciphertext)
WHERE id = sqlc.arg(id) AND purpose = sqlc.arg(purpose) AND revoked_at IS NULL
AND key_id = sqlc.arg(old_key_id) AND ciphertext = sqlc.arg(old_ciphertext);
