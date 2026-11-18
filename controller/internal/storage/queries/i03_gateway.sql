-- name: I03Inventory :one
SELECT * FROM i03_node_inventory WHERE node_id=?;
-- name: I03PutInventory :exec
INSERT INTO i03_node_inventory(node_id,node_revision,dns_digest,dns,loops,observed_at,expires_at) VALUES (?,?,?,?,?,?,?)
ON CONFLICT(node_id) DO UPDATE SET node_revision=excluded.node_revision,dns_digest=excluded.dns_digest,dns=excluded.dns,loops=excluded.loops,observed_at=excluded.observed_at,expires_at=excluded.expires_at;
-- name: I03Sockets :many
SELECT * FROM i03_node_sockets WHERE node_id=? ORDER BY config_id,config_revision;
-- name: I03ClearSockets :exec
DELETE FROM i03_node_sockets WHERE node_id=?;
-- name: I03PutSocket :exec
INSERT INTO i03_node_sockets(node_id,config_id,config_revision,config_digest,spec,protocol,business_udp,pool_first,pool_last) VALUES (?,?,?,?,?,?,?,?,?);
-- name: I03ConfirmedArtifact :one
SELECT a.* FROM b04_artifacts a JOIN b04_node_state s ON s.node_id=a.node_id AND s.applied_revision=a.target_revision AND s.artifact_digest=a.target_digest
JOIN b04_targets t ON t.task_id=a.task_id AND t.node_id=a.node_id
WHERE a.node_id=? AND s.reconciliation='matched' AND t.phase='confirmed';
-- name: I03InsertV2Artifact :exec
INSERT INTO b04_artifacts(id,task_id,node_id,target_revision,target_digest,key_id,ciphertext,format) VALUES (?,?,?,?,?,?,?,2);
-- name: I03PutTask :exec
INSERT INTO i03_v2_tasks(task_id,subject_digest,entry_operation_id) VALUES (?,?,?);
-- name: I03Task :one
SELECT * FROM i03_v2_tasks WHERE task_id=?;
-- name: I03Lease :one
SELECT * FROM i03_ingress_leases WHERE operation_id=?;
-- name: I03PutLease :exec
INSERT INTO i03_ingress_leases(operation_id,request_digest,reservation_id,listener_id,config_id,config_revision,config_digest,preferred_port,actual_port) VALUES (?,?,?,?,?,?,?,?,?);
-- name: I03BlockLease :exec
UPDATE i03_ingress_leases SET blocked_at=coalesce(blocked_at,?) WHERE operation_id=?;
-- name: I03ReleaseLease :exec
UPDATE i03_ingress_leases SET released_at=coalesce(released_at,?) WHERE operation_id=? AND blocked_at IS NOT NULL;
-- name: I03PutTargetLink :exec
INSERT INTO i03_target_links(task_id,node_id,link_id,action) VALUES (?,?,?,?);
-- name: I03TargetLinks :many
SELECT l.*,t.action FROM i03_target_links t JOIN b06_internal_links l ON l.id=t.link_id WHERE t.task_id=? AND t.node_id=? ORDER BY l.id;
-- name: I03Origin :one
SELECT * FROM i03_internal_origins WHERE link_id=?;
-- name: I03PinOrigins :exec
INSERT INTO i03_internal_origins(link_id,task_id,node_id) SELECT link_id,task_id,node_id FROM i03_target_links
WHERE i03_target_links.task_id=? AND i03_target_links.node_id=? AND i03_target_links.action='include' ON CONFLICT(link_id) DO NOTHING;
-- name: I03PutEgress :exec
INSERT INTO b06_egress_evidence(node_id,node_revision,target_digest,dns_digest,expires_at,business_udp) VALUES (?,?,?,?,?,?)
ON CONFLICT(node_id,node_revision,target_digest,dns_digest) DO UPDATE SET expires_at=excluded.expires_at,business_udp=excluded.business_udp;
-- name: I03PutObservation :exec
INSERT INTO i03_egress_observations(node_id,node_revision,target_digest,dns_digest,observed_at,expires_at,interface_name,source_ip,probe_ip,probe_port) VALUES (?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(node_id,node_revision,target_digest,dns_digest) DO UPDATE SET observed_at=excluded.observed_at,expires_at=excluded.expires_at,interface_name=excluded.interface_name,source_ip=excluded.source_ip,probe_ip=excluded.probe_ip,probe_port=excluded.probe_port;
-- name: I03CleanupAttempt :one
SELECT * FROM i03_cleanup_attempts WHERE operation_id=? ORDER BY generation DESC LIMIT 1;
-- name: I03PutCleanupAttempt :exec
INSERT INTO i03_cleanup_attempts(operation_id,generation,task_id,prior_task_id) VALUES (?,?,?,?);
-- name: I03RecoverCleanup :execrows
UPDATE b08_intents SET cleanup_generation=cleanup_generation+1,cleanup_task_id=NULL,cleanup_epoch=?,cleanup_deadline=?
WHERE cleanup_id=? AND cleanup_task_id=? AND cleanup_state='pending' AND cleanup_generation=?;
-- name: I03PristineNode :one
SELECT NOT EXISTS(SELECT 1 FROM b04_targets b JOIN tasks t ON t.id=b.task_id WHERE b.node_id=sqlc.arg(node_id) AND (b.fetched=1 OR t.state IN ('queued','running')))
AND NOT EXISTS(SELECT 1 FROM listeners l WHERE l.node_id=sqlc.arg(node_id) AND NOT EXISTS(SELECT 1 FROM b06_internal_links il WHERE il.listener_id=l.id) AND NOT EXISTS(SELECT 1 FROM i03_ingress_leases el WHERE el.listener_id=l.id)) AS pristine;
-- name: I03PutLinkFence :exec
INSERT INTO i03_link_fences(task_id,node_id,receiver_id,link_id,target_revision,artifact_digest,epoch) VALUES (?,?,?,?,?,?,?);
-- name: I03LinkFences :many
SELECT * FROM i03_link_fences WHERE task_id=? AND node_id=?;
-- name: I03EntryAuthorized :one
SELECT EXISTS(SELECT 1 FROM b08_intents i JOIN subscriptions s ON s.id=i.subscription_id
JOIN entry_identities e ON e.id=i.identity_key JOIN subscription_entries se ON se.id=i.entry_key
WHERE i.identity_key=sqlc.arg(identity_id) AND i.revoked_at IS NULL AND s.revoked_at IS NULL
AND e.revoked_at IS NULL AND se.revoked_at IS NULL AND s.expires_at>sqlc.arg(now) AND i.expires_at>sqlc.arg(now)
AND NOT EXISTS(SELECT 1 FROM entry_identities newer WHERE newer.entry_id=e.entry_id AND newer.generation>e.generation)) AS authorized;
-- name: I03PublicationSubject :one
SELECT EXISTS(SELECT 1 FROM b06_publications p JOIN b06_route_versions v ON v.route_id=p.route_id AND v.revision=p.revision
JOIN routes r ON r.id=p.route_id AND r.revision=p.revision
WHERE p.route_id=? AND p.revision=? AND v.draft_digest=? AND p.state='prepared') AS authorized;
-- name: I03BaseUsable :one
SELECT NOT EXISTS(SELECT 1 FROM b04_targets b JOIN tasks t ON t.id=b.task_id JOIN task_nodes tn ON tn.task_id=b.task_id AND tn.node_id=b.node_id
WHERE b.node_id=sqlc.arg(node_id) AND tn.target_revision>sqlc.arg(target_revision)
AND (t.state IN ('queued','running') OR (b.fetched=1 AND tn.rollback_state!='confirmed'))) AS usable;
-- name: I03RouteGeneration :one
SELECT CAST(coalesce(max(generation),0) AS INTEGER) FROM b08_intents WHERE subscription_id=? AND route_id=?;
-- name: I03FenceOperation :exec
INSERT INTO i03_operation_fences(operation_id,blocked_at) VALUES (?,?) ON CONFLICT DO NOTHING;
-- name: I03OperationBlocked :one
SELECT EXISTS(SELECT 1 FROM i03_operation_fences WHERE operation_id=?) AS blocked;
-- name: I03UnarmedIdentity :one
SELECT NOT EXISTS(SELECT 1 FROM b04_target_grants g JOIN b04_targets t USING(task_id,node_id)
WHERE g.identity_id=? AND (t.phase!='queued' OR t.fetched!=0)) AS unarmed;
-- name: I03IdentityTasks :many
SELECT DISTINCT task_id FROM b04_target_grants WHERE identity_id=?;
-- name: I03ClearInterfaces :exec
DELETE FROM i03_node_interfaces WHERE node_id=?;
-- name: I03PutInterface :exec
INSERT INTO i03_node_interfaces(node_id,name,address,up) VALUES (?,?,?,?);
-- name: I03Interface :one
SELECT up FROM i03_node_interfaces WHERE node_id=? AND name=? AND address=?;
-- name: I03LiveLinks :many
SELECT o.*,l.state,l.listener_id FROM i03_internal_origins o JOIN b06_internal_links l ON l.id=o.link_id WHERE o.node_id=? AND l.state!='released';
-- name: I03SettledCleanupWork :many
SELECT i.id FROM b08_intents i JOIN i03_ingress_leases l ON l.operation_id=i.id
WHERE i.cleanup_state='confirmed' AND l.released_at IS NULL ORDER BY i.id LIMIT 64;
-- name: I03PutDNS :exec
INSERT INTO node_dns_revisions(node_id,revision,outbound,digest) VALUES (?,?,'node_default',?) ON CONFLICT(node_id,revision) DO NOTHING;
-- name: I03DNS :one
SELECT digest FROM node_dns_revisions WHERE node_id=? AND revision=?;
-- name: I03PutDNSUpstream :exec
INSERT INTO node_dns_upstreams(node_id,revision,kind,position,address) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING;
-- name: I03RetiredTasks :many
SELECT l.id,l.revision,l.suffix_digest,t.task_id FROM i03_target_links t JOIN b06_internal_links l ON l.id=t.link_id
JOIN tasks k ON k.id=t.task_id JOIN task_nodes n ON n.task_id=k.id AND n.node_id=t.node_id
WHERE t.action='retire' AND l.state='retiring' AND k.state='succeeded' AND k.subject_id=l.id
AND n.applied=1 AND n.cleanup_state='confirmed' ORDER BY l.id LIMIT 64;
-- name: I03Revocations :many
SELECT g.identity_id,g.generation,g.expires_at,i.revoked_at FROM b04_grants g
JOIN b08_intents i ON i.identity_key=g.identity_id
WHERE g.node_id=? AND (i.revoked_at IS NOT NULL OR i.expires_at<=sqlc.arg(now)) ORDER BY g.identity_id;
-- name: I03PutAction :exec
INSERT INTO i03_v2_actions(task_id,source_task_id,source_revision,action) VALUES (?,?,?,?);
-- name: I03RollbackArtifact :one
SELECT a.* FROM b04_artifacts a JOIN b04_targets t USING(task_id,node_id)
WHERE a.node_id=? AND a.target_revision<? AND a.format=2 AND t.phase='confirmed'
ORDER BY a.target_revision DESC LIMIT 1;
-- name: I03IntentByIdentity :one
SELECT * FROM b08_intents WHERE identity_key=?;
-- name: I03Observation :one
SELECT * FROM i03_egress_observations WHERE node_id=? AND node_revision=? AND target_digest=? AND dns_digest=?;
