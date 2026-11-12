-- name: B02GetCA :one
SELECT c.certificate, s.id, s.key_id, s.ciphertext FROM b02_management_ca c
JOIN credentials s ON s.id=c.credential_ref WHERE c.singleton=1 AND s.purpose='management_ca' AND s.revoked_at IS NULL;

-- name: B02CAExists :one
SELECT count(*) FROM b02_management_ca;

-- name: B02InsertCredential :exec
INSERT INTO credentials(id,purpose,key_id,ciphertext,created_at) VALUES(?,?,?,?,?);

-- name: B02InsertCA :exec
INSERT INTO b02_management_ca(singleton,certificate,credential_ref) VALUES(1,?,?);

-- name: B02InsertEnrollment :exec
INSERT INTO b02_node_enrollment(node_id,relay_node_id) VALUES(?,?);

-- name: B02GetEnrollment :one
SELECT * FROM b02_node_enrollment WHERE node_id=?;

-- name: B02AdvanceGeneration :execrows
UPDATE b02_node_enrollment SET generation=generation+1 WHERE node_id=? AND generation=?;

-- name: B02InsertToken :exec
INSERT INTO b02_tokens(id,node_id,generation,token_hash,expires_at) VALUES(?,?,?,?,?);

-- name: B02RevokeTokens :exec
UPDATE b02_tokens SET revoked_at=? WHERE node_id=? AND revoked_at IS NULL;

-- name: B02GetToken :one
SELECT * FROM b02_tokens WHERE token_hash=?;

-- name: B02GetTokenByID :one
SELECT t.*,c.key_id,c.ciphertext FROM b02_tokens t JOIN credentials c ON c.id=t.id
WHERE t.id=? AND c.purpose='certificate' AND c.revoked_at IS NULL;

-- name: B02LatestToken :one
SELECT id,expires_at,revoked_at,csr_hash FROM b02_tokens WHERE node_id=? ORDER BY generation DESC LIMIT 1;

-- name: B02ConsumeToken :execrows
UPDATE b02_tokens SET csr_hash=?,certificate=? WHERE id=? AND csr_hash IS NULL AND revoked_at IS NULL AND expires_at>?;

-- name: B02InsertCertificate :exec
INSERT INTO b02_node_certificates(fingerprint,node_id,certificate,csr_hash,not_after) VALUES(?,?,?,?,?);

-- name: B02GetCertificate :one
SELECT * FROM b02_node_certificates WHERE fingerprint=?;

-- name: B02RevokeCertificates :exec
UPDATE b02_node_certificates SET revoked_at=? WHERE node_id=? AND revoked_at IS NULL;

-- name: B02RenewCertificate :execrows
UPDATE b02_node_certificates SET renewed_to=? WHERE fingerprint=? AND renewed_to IS NULL AND revoked_at IS NULL;

-- name: B02GetMutation :one
SELECT * FROM b02_mutations WHERE scope=? AND idempotency_key=?;

-- name: B02InsertMutation :exec
INSERT INTO b02_mutations(scope,idempotency_key,request_digest,node_id,revision,token_ref,task_id) VALUES(?,?,?,?,?,?,?);

-- name: B02BumpNode :execrows
UPDATE nodes SET revision=revision+1,updated_at=? WHERE id=? AND revision=? AND removed_at IS NULL AND revision<9007199254740991;

-- name: B02RemoveNode :execrows
UPDATE nodes SET revision=revision+1,revocation_epoch=revocation_epoch+1,removed_at=?,updated_at=?,control_state='offline'
WHERE id=? AND revision=? AND removed_at IS NULL AND revision<9007199254740991;


-- name: B02GetRemovalIntent :one
SELECT node_id,revision,task_id FROM b02_mutations WHERE task_id=? AND scope='remove:'||node_id;

-- name: B02MarkRemovalPending :exec
UPDATE task_nodes SET cleanup_state='pending' WHERE task_id=? AND node_id=? AND result_sequence=0;
