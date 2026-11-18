-- name: B06Mutation :one
SELECT * FROM b06_mutations WHERE operation=? AND idem_key=?;
-- name: B06InsertMutation :exec
INSERT INTO b06_mutations(operation,idem_key,request_digest,result) VALUES (?,?,?,?);

-- name: B06CreateRoute :exec
INSERT INTO routes(id,name,revision,updated_at) VALUES (?,?,1,?);
-- name: B06RouteRevision :one
SELECT revision FROM routes WHERE id=?;
-- name: B06UpdateRoute :execrows
UPDATE routes SET name=sqlc.arg(name), revision=revision+1, updated_at=sqlc.arg(updated_at)
WHERE id=sqlc.arg(id) AND revision=sqlc.arg(revision) ;
-- name: B06InsertRouteVersion :exec
INSERT INTO b06_route_versions(route_id,revision,name,draft_digest,egress_id,egress_revision,egress_digest,node_default,business_udp)
VALUES (?,?,?,?,?,?,?,?,?);
-- name: B06InsertRouteHop :exec
INSERT INTO b06_route_hops(route_id,revision,position,node_id,node_config,dns,endpoint,dial_ip)
VALUES (?,?,?,?,?,?,?,?);
-- name: B06RouteVersion :one
SELECT * FROM b06_route_versions WHERE route_id=? AND revision=?;
-- name: B06RouteHops :many
SELECT * FROM b06_route_hops WHERE route_id=? AND revision=? ORDER BY position;
-- name: B06PortsForNode :many
SELECT p.address,p.transport,p.port,p.listener_id AS listener FROM port_reservations p WHERE p.node_id=?
AND p.state!='released';
-- name: B06ModernPortsForNode :many
SELECT p.address,p.transport,p.port,p.listener_id AS listener FROM b04_ports p WHERE p.node_id=?
AND p.state!='released';

-- name: B06ActivePublication :one
SELECT id FROM b06_publications WHERE route_id=? AND state IN ('preparing','prepared','queued','running','rollback_pending','rollback_failed');
-- name: B06InsertPublication :exec
INSERT INTO b06_publications(id,route_id,revision,idem_key,deadline) VALUES (?,?,?,?,?);
-- name: B06Publication :one
SELECT * FROM b06_publications WHERE id=?;
-- name: B06TaskPublication :one
SELECT * FROM b06_publications WHERE task_id=?;
-- name: B06PublicationNodes :many
SELECT * FROM b06_publication_nodes WHERE publication_id=? ORDER BY position;
-- name: B06InsertPublicationNode :exec
INSERT INTO b06_publication_nodes(publication_id,position,node_id,target_revision,secret_ref,key_id,ciphertext) VALUES (?,?,?,?,?,?,?);
-- name: B06SetPublicationState :execrows
UPDATE b06_publications SET state=sqlc.arg(new_state)
WHERE id=sqlc.arg(id) AND state=sqlc.arg(old_state);
-- name: B06BindPublicationTask :execrows
UPDATE b06_publications SET task_id=sqlc.arg(task_id), state='queued'
WHERE id=sqlc.arg(id) AND task_id IS NULL AND state='prepared';
-- name: B06InsertPublishedVersion :exec
INSERT INTO b06_published_versions(route_id,revision,publication_id,route_digest) VALUES (?,?,?,?)
ON CONFLICT(route_id,revision) DO UPDATE SET publication_id=excluded.publication_id,route_digest=excluded.route_digest WHERE b06_published_versions.confirmed=0;
-- name: B06StorePublishedHop :exec
INSERT INTO b06_published_hops(route_id,revision,position,port) VALUES (?,?,?,?)
ON CONFLICT(route_id,revision,position) DO UPDATE SET port=excluded.port;
-- name: B06PublishedVersion :one
SELECT * FROM b06_published_versions WHERE route_id=? AND revision=? AND confirmed=1;
-- name: B06PublishedHops :many
SELECT port FROM b06_published_hops WHERE route_id=? AND revision=? ORDER BY position;
-- name: B06ConfirmVersion :execrows
UPDATE b06_published_versions SET confirmed=1 WHERE publication_id=? AND confirmed=0;
-- name: B06AdvanceHead :exec
INSERT INTO b06_route_heads(route_id,revision) VALUES (?,?)
ON CONFLICT(route_id) DO UPDATE SET revision=excluded.revision WHERE excluded.revision > b06_route_heads.revision;
-- name: B06RouteHead :one
SELECT revision FROM b06_route_heads WHERE route_id=?;

-- name: B06Ingress :one
SELECT * FROM b06_ingress WHERE operation_id=?;
-- name: B06InsertIngress :exec
INSERT INTO b06_ingress(operation_id,request_digest,reservation,identity_id,credential_ref,route_id,revision) VALUES (?,?,?,?,?,?,?);
-- name: B06BindIngress :execrows
UPDATE b06_ingress SET lease=sqlc.arg(lease),listener_id=sqlc.arg(listener_id) WHERE operation_id=sqlc.arg(operation_id) AND lease IS NULL;
-- name: B06EntryPlan :one
SELECT * FROM b06_entry_plans WHERE kind=? AND idem_key=?;
-- name: B06EntryOperationPlan :one
SELECT * FROM b06_entry_plans WHERE operation_id=? AND kind=? AND recovery_generation=?;
-- name: B06InsertEntryPlan :exec
INSERT INTO b06_entry_plans(operation_id,origin_operation_id,kind,idem_key,request_digest,secret_ref,key_id,ciphertext,recovery_generation) VALUES (?,?,?,?,?,?,?,?,?);
-- name: B06BindEntryTask :execrows
UPDATE b06_entry_plans SET task_id=sqlc.arg(task_id) WHERE kind=sqlc.arg(kind) AND idem_key=sqlc.arg(idem_key) AND task_id IS NULL;
-- name: B06IngressByIdentity :one
SELECT * FROM b06_ingress WHERE identity_id=?;

-- name: B06AttachEntryReference :exec
INSERT INTO b06_entry_link_references(identity_id,link_id) VALUES (?,?) ON CONFLICT DO NOTHING;
-- name: B06DropEntryReferences :exec
UPDATE b06_entry_link_references SET released_at=coalesce(released_at,sqlc.arg(at)) WHERE identity_id=sqlc.arg(identity_id);

-- name: B06IngressByCredential :one
SELECT operation_id FROM b06_ingress WHERE credential_ref=?;

-- name: B06FailPublication :execrows
UPDATE b06_publications SET state=sqlc.arg(new_state),failure=sqlc.arg(failure) WHERE id=sqlc.arg(id) AND state=sqlc.arg(old_state) AND task_id IS NULL;

-- name: B06ResumablePublications :many
SELECT id FROM b06_publications WHERE state IN ('preparing','prepared') AND task_id IS NULL ORDER BY id LIMIT ?;
