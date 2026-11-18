-- name: B06EgressObject :one
SELECT * FROM b06_egress_objects WHERE id=?;
-- name: B06InsertEgressObject :exec
INSERT INTO b06_egress_objects(id,node_id,kind,name,revision) VALUES (?,?,?,?,1);
-- name: B06UpdateEgressObject :execrows
UPDATE b06_egress_objects SET name=sqlc.arg(name),revision=revision+1 WHERE id=sqlc.arg(id) AND revision=sqlc.arg(revision);
-- name: B06EgressVersion :one
SELECT v.*,o.kind,o.node_id FROM b06_egress_versions v JOIN b06_egress_objects o USING(id)
WHERE v.id=? AND v.revision=?;
-- name: B06InsertEgressVersion :exec
INSERT INTO b06_egress_versions(id,revision,digest,spec) VALUES (?,?,?,?);
-- name: B06EgressCredential :one
SELECT * FROM b06_egress_credentials WHERE id=? AND target_id=?;
-- name: B06InsertEgressCredential :exec
INSERT INTO b06_egress_credentials(id,target_id,origin_revision,key_id,ciphertext) VALUES (?,?,?,?,?);
-- name: B06EgressEvidence :one
SELECT * FROM b06_egress_evidence WHERE node_id=? AND node_revision=? AND target_digest=? AND dns_digest=?;
-- name: B06PutEgressEvidence :exec
INSERT INTO b06_egress_evidence(node_id,node_revision,target_digest,dns_digest,expires_at,business_udp) VALUES (?,?,?,?,?,?)
ON CONFLICT(node_id,node_revision,target_digest,dns_digest) DO UPDATE SET expires_at=excluded.expires_at,business_udp=excluded.business_udp;

-- name: B06InsertPolicyTarget :exec
INSERT INTO b06_policy_targets(policy_id,policy_revision,target_id,target_revision) VALUES (?,?,?,?);
-- name: B06PolicyTargets :many
SELECT refs.target_id,refs.target_revision,t.digest,t.spec,obj.kind,obj.node_id FROM b06_policy_targets refs
JOIN b06_egress_versions t ON t.id=refs.target_id AND t.revision=refs.target_revision
JOIN b06_egress_objects obj ON obj.id=t.id
WHERE refs.policy_id=? AND refs.policy_revision=? ORDER BY refs.target_id;
