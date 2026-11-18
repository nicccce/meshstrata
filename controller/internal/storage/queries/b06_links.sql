-- name: B06LinkCandidate :one
SELECT * FROM b06_link_candidates WHERE operation_id=?;
-- name: B06InsertLinkCandidate :exec
INSERT INTO b06_link_candidates(operation_id,publication_id,position,suffix_digest,proposed_link,credential_ref,key_id,ciphertext) VALUES (?,?,?,?,?,?,?,?)
ON CONFLICT(operation_id) DO NOTHING;
-- name: B06LinkBySuffix :one
SELECT * FROM b06_internal_links WHERE suffix_digest=? AND state!='released';
-- name: B06LinkByID :one
SELECT * FROM b06_internal_links WHERE id=?;
-- name: B06LinkReservation :one
SELECT l.*,r.reservation_id,r.request_digest,r.released_at FROM b06_link_references r JOIN b06_internal_links l ON l.id=r.link_id WHERE r.operation_id=?;
-- name: B06InsertInternalLink :exec
INSERT INTO b06_internal_links(id,revision,suffix_digest,tail,node_id,listener_id,credential_ref) VALUES (?,1,?,?,?,?,?);
-- name: B06InsertLinkReference :exec
INSERT INTO b06_link_references(operation_id,publication_id,position,link_id,reservation_id,request_digest) VALUES (?,?,?,?,?,?);
-- name: B06InternalListener :one
SELECT * FROM listeners WHERE id=?;
-- name: B06InsertInternalListener :exec
INSERT INTO listeners(id,node_id,name,revision,protocol,listen_address,listen_port,outer_transport,credential_ref,config_digest,dns_revision,updated_at)
VALUES (?,?,'internal link',1,?,?,?,?,?,?,?,?);
-- name: B06InsertInternalPort :exec
INSERT INTO port_reservations(id,node_id,listener_id,purpose,address,transport,port) VALUES (?,?,?,'listener',?,?,?);
-- name: B06DropLinkReference :exec
UPDATE b06_link_references SET released_at=coalesce(released_at,sqlc.arg(at)) WHERE operation_id=sqlc.arg(operation_id) AND link_id=sqlc.arg(link_id);
-- name: B06RetireInternalLink :execrows
UPDATE b06_internal_links SET state='retiring' WHERE id=? AND state='active'
AND NOT EXISTS (SELECT 1 FROM b06_link_references WHERE link_id=b06_internal_links.id AND released_at IS NULL)
AND NOT EXISTS (SELECT 1 FROM b06_entry_link_references WHERE link_id=b06_internal_links.id AND released_at IS NULL);
-- name: B06SetInternalListenerPending :exec
UPDATE listeners SET cleanup_state='pending' WHERE id=? AND cleanup_state='none';
-- name: B06SetInternalPortsPending :exec
UPDATE port_reservations SET state='pending_cleanup' WHERE listener_id=? AND state='reserved';
-- name: B06InternalCleanupEvidence :one
SELECT EXISTS(SELECT 1 FROM tasks t JOIN task_nodes tn ON tn.task_id=t.id
WHERE t.id=sqlc.arg(task_id) AND tn.node_id=sqlc.arg(node_id) AND t.kind='cleanup' AND t.subject_id=sqlc.arg(link_id)
AND t.target_revision=1 AND t.state='succeeded' AND tn.applied=1 AND tn.cleanup_state='confirmed') AS confirmed;
-- name: B06ReleaseInternalLink :execrows
UPDATE b06_internal_links SET state='released',cleanup_task_id=sqlc.arg(task_id) WHERE id=sqlc.arg(id) AND state='retiring';
-- name: B06ConfirmInternalListener :exec
UPDATE listeners SET cleanup_state='confirmed' WHERE id=? AND cleanup_state='pending';
-- name: B06ReleaseInternalPorts :exec
UPDATE port_reservations SET state='released',cleanup_confirmed_at=sqlc.arg(at) WHERE listener_id=sqlc.arg(listener_id) AND state='pending_cleanup';

-- name: B06PublicationLinkAt :one
SELECT l.* FROM b06_link_references r JOIN b06_internal_links l ON l.id=r.link_id WHERE r.publication_id=? AND r.position=?;

-- name: B06ReferencePublication :one
SELECT p.* FROM b06_publications p JOIN b06_link_references r ON r.publication_id=p.id WHERE r.operation_id=?;
-- name: B06ActiveEntryLinkIDs :many
SELECT link_id FROM b06_entry_link_references WHERE identity_id=? AND released_at IS NULL ORDER BY link_id;
