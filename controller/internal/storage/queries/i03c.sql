-- name: I03CPreparation :one
SELECT * FROM i03c_preparations WHERE node_id=? ORDER BY revision DESC LIMIT 1;
-- name: I03CPutPreparation :exec
INSERT INTO i03c_preparations(node_id,revision,digest,dns,sockets) VALUES(?,?,?,?,?);
-- name: I03CPrepareNode :execrows
UPDATE nodes SET revision=revision+1,target_dns_revision=?,updated_at=? WHERE id=? AND revision=? AND removed_at IS NULL;
-- name: I03CMutation :one
SELECT * FROM i03c_mutations WHERE operation=? AND idem_key=?;
-- name: I03CPutMutation :exec
INSERT INTO i03c_mutations(operation,idem_key,digest,result) VALUES(?,?,?,?);
-- name: I03CArchive :exec
INSERT INTO i03c_archives(kind,id,revision,archived_at) VALUES(?,?,?,?);
-- name: I03CArchived :one
SELECT EXISTS(SELECT 1 FROM i03c_archives WHERE kind=? AND id=?) AS archived;
-- name: I03CListRoutes :many
SELECT r.*,v.draft_digest,v.egress_id,v.egress_revision,v.egress_digest,v.node_default,v.business_udp,h.revision AS confirmed_revision,p.route_digest FROM routes r
JOIN b06_route_versions v ON v.route_id=r.id AND v.revision=r.revision
LEFT JOIN b06_route_heads h ON h.route_id=r.id
LEFT JOIN b06_published_versions p ON p.route_id=r.id AND p.revision=h.revision AND p.confirmed=1
WHERE r.id>sqlc.arg(after_id) AND NOT EXISTS(SELECT 1 FROM i03c_archives a WHERE a.kind='route' AND a.id=r.id)
ORDER BY r.id LIMIT sqlc.arg(page_size);
-- name: I03CRouteVersions :many
SELECT v.*,p.route_digest,p.confirmed,k.task_id,k.state FROM b06_route_versions v
LEFT JOIN b06_published_versions p ON p.route_id=v.route_id AND p.revision=v.revision
LEFT JOIN b06_publications k ON k.id=(SELECT id FROM b06_publications k2 WHERE k2.route_id=v.route_id AND k2.revision=v.revision ORDER BY k2.rowid DESC LIMIT 1)
WHERE v.route_id=sqlc.arg(route_id) AND v.revision<sqlc.arg(before_revision) ORDER BY v.revision DESC LIMIT sqlc.arg(page_size);
-- name: I03CListProfiles :many
SELECT r.*,p.digest FROM rule_profiles r JOIN b07_profile_pins p ON p.profile_id=r.id AND p.revision=r.revision
WHERE r.id>sqlc.arg(after_id) AND NOT EXISTS(SELECT 1 FROM i03c_archives a WHERE a.kind='profile' AND a.id=r.id)
ORDER BY r.id LIMIT sqlc.arg(page_size);
-- name: I03CProfile :one
SELECT r.*,p.digest,v.source_text FROM rule_profiles r JOIN b07_profile_pins p ON p.profile_id=r.id AND p.revision=r.revision
JOIN rule_profile_revisions v ON v.profile_id=r.id AND v.revision=r.revision WHERE r.id=?;
-- name: I03CListSets :many
SELECT r.*,p.digest FROM domain_sets r JOIN b07_domain_pins p ON p.domain_set_id=r.id AND p.revision=r.revision
WHERE r.id>sqlc.arg(after_id) AND NOT EXISTS(SELECT 1 FROM i03c_archives a WHERE a.kind='domain_set' AND a.id=r.id)
ORDER BY r.id LIMIT sqlc.arg(page_size);
-- name: I03CSet :one
SELECT r.*,p.digest FROM domain_sets r JOIN b07_domain_pins p ON p.domain_set_id=r.id AND p.revision=r.revision WHERE r.id=?;
-- name: I03CListEgress :many
SELECT r.*,v.digest FROM b06_egress_objects r JOIN b06_egress_versions v ON v.id=r.id AND v.revision=r.revision
WHERE r.id>sqlc.arg(after_id) AND r.kind=sqlc.arg(kind) AND (CAST(sqlc.arg(node_id) AS TEXT)='' OR r.node_id=CAST(sqlc.arg(node_id) AS TEXT))
AND NOT EXISTS(SELECT 1 FROM i03c_archives a WHERE a.kind=r.kind AND a.id=r.id)
ORDER BY r.id LIMIT sqlc.arg(page_size);
-- name: I03CEgress :one
SELECT r.*,v.digest,v.spec FROM b06_egress_objects r JOIN b06_egress_versions v ON v.id=r.id AND v.revision=r.revision WHERE r.id=?;
-- name: I03CListSubscriptions :many
SELECT id FROM subscriptions WHERE id>sqlc.arg(after_id) AND revoked_at IS NULL ORDER BY id LIMIT sqlc.arg(page_size);
-- name: I03CPublicationWork :many
SELECT id,task_id FROM b06_publications WHERE id>sqlc.arg(after_id) AND state IN ('preparing','prepared','queued','running','rollback_pending','rollback_failed') ORDER BY id LIMIT sqlc.arg(page_size);
-- name: I03CObservationWork :many
SELECT n.id,n.revision,p.dns,p.sockets FROM nodes n JOIN i03c_preparations p ON p.node_id=n.id
WHERE p.revision=(SELECT max(v.revision) FROM i03c_preparations v WHERE v.node_id=n.id)
AND n.removed_at IS NULL AND n.id>sqlc.arg(after_id)
ORDER BY n.id LIMIT sqlc.arg(page_size);
-- name: I03CNodeDependencies :many
SELECT DISTINCT v.route_id FROM b06_route_hops v JOIN b06_route_heads h ON h.route_id=v.route_id AND h.revision=v.revision WHERE v.node_id=? ORDER BY v.route_id LIMIT 201;
-- name: I03CActiveListeners :one
SELECT count(*) FROM listeners WHERE node_id=? AND cleanup_state!='confirmed';
-- name: I03CProfileReferences :many
SELECT DISTINCT v.subscription_id FROM b08_revisions v JOIN subscriptions s ON s.id=v.subscription_id
JOIN b08_heads h ON h.subscription_id=s.id WHERE v.profile_id=sqlc.arg(resource_id) AND v.subscription_id>sqlc.arg(after_id) AND s.revoked_at IS NULL AND (v.revision=h.desired_revision OR v.revision=h.confirmed_revision)
ORDER BY v.subscription_id LIMIT sqlc.arg(page_size);
-- name: I03CSetReferences :many
SELECT DISTINCT p.profile_id FROM b07_profile_set_pins p
JOIN i03c_live_profile_versions r ON r.id=p.profile_id AND r.revision=p.profile_revision
WHERE p.domain_set_id=sqlc.arg(resource_id) AND p.profile_id>sqlc.arg(after_id)
ORDER BY p.profile_id LIMIT sqlc.arg(page_size);
-- name: I03CTargetReferences :many
SELECT DISTINCT p.policy_id FROM b06_policy_targets p
JOIN i03c_live_policy_versions r ON r.id=p.policy_id AND r.revision=p.policy_revision
WHERE p.target_id=sqlc.arg(resource_id) AND p.policy_id>sqlc.arg(after_id)
ORDER BY p.policy_id LIMIT sqlc.arg(page_size);
-- name: I03CPolicyReferences :many
SELECT DISTINCT v.route_id FROM i03c_live_route_versions v
WHERE v.egress_id=sqlc.arg(resource_id) AND v.route_id>sqlc.arg(after_id)
ORDER BY v.route_id LIMIT sqlc.arg(page_size);
-- name: I03CRouteReferences :many
SELECT DISTINCT subscription_id FROM b08_intents WHERE route_id=sqlc.arg(resource_id) AND subscription_id>sqlc.arg(after_id) AND (revoked_at IS NULL OR cleanup_state!='confirmed') ORDER BY subscription_id LIMIT sqlc.arg(page_size);
-- name: I03CSubscriptionWork :many
SELECT entry_key,generation,state,cleanup_state,task_id,cleanup_task_id FROM b08_intents WHERE subscription_id=? ORDER BY id LIMIT 1024;
-- name: I03CDropRouteHead :exec
DELETE FROM b06_route_heads WHERE route_id=?;
-- name: I03CRouteLinkReferences :many
SELECT r.operation_id FROM b06_link_references r JOIN b06_publications p ON p.id=r.publication_id
WHERE p.route_id=? AND r.released_at IS NULL ORDER BY r.operation_id LIMIT 1024;
-- name: I03CRouteBusy :one
SELECT EXISTS(SELECT 1 FROM b06_publications WHERE route_id=? AND state IN ('preparing','prepared','queued','running','rollback_pending','rollback_failed')) AS busy;
-- name: I03CRetiringLinks :many
SELECT id,revision,suffix_digest,node_id FROM b06_internal_links WHERE state='retiring' AND id>sqlc.arg(after_id) ORDER BY id LIMIT sqlc.arg(page_size);
-- name: I03CInternalTask :one
SELECT t.id,t.revision,t.state FROM tasks t JOIN i03_v2_tasks v ON v.task_id=t.id WHERE t.subject_id=? AND t.kind='cleanup' AND v.entry_operation_id='' ORDER BY t.rowid DESC LIMIT 1;

-- name: I03CTargetObservationWork :many
SELECT t.id,t.revision,t.digest,e.node_id,CAST(t.id||'/'||printf('%016d',t.revision) AS TEXT) AS cursor_key
FROM b06_egress_versions t JOIN b06_egress_objects e ON e.id=t.id AND e.kind='target'
WHERE t.id||'/'||printf('%016d',t.revision)>sqlc.arg(after_key)
AND EXISTS(SELECT 1 FROM b06_policy_targets p JOIN i03c_live_policy_versions v ON v.id=p.policy_id AND v.revision=p.policy_revision
 WHERE p.target_id=t.id AND p.target_revision=t.revision)
AND EXISTS(SELECT 1 FROM i03_egress_observations o WHERE o.node_id=e.node_id AND o.target_digest=t.digest)
ORDER BY cursor_key LIMIT sqlc.arg(page_size);

-- name: I03CEntryPlanFence :one
SELECT plan_digest FROM b06_entry_plans WHERE operation_id=? AND kind=? ORDER BY recovery_generation DESC LIMIT 1;
-- name: I03CRememberEntryPlan :exec
INSERT INTO i03c_entry_plan_history(kind,idem_key,digest,secret_ref,key_id,ciphertext) VALUES (?,?,?,?,?,?) ON CONFLICT DO NOTHING;
-- name: I03CSeedEntryPlanDigest :execrows
UPDATE b06_entry_plans SET plan_digest=? WHERE kind=? AND idem_key=? AND plan_digest='';
-- name: I03CRebaseEntryPlan :execrows
UPDATE b06_entry_plans SET plan_digest=sqlc.arg(new_digest),secret_ref=sqlc.arg(secret_ref),key_id=sqlc.arg(key_id),ciphertext=sqlc.arg(ciphertext)
WHERE b06_entry_plans.kind=sqlc.arg(kind) AND b06_entry_plans.idem_key=sqlc.arg(idem_key) AND plan_digest=sqlc.arg(old_digest) AND task_id IS NULL
AND NOT EXISTS(SELECT 1 FROM tasks WHERE idempotency_scope='i03:v2:submit' AND idempotency_key=sqlc.arg(task_key));

-- name: I03CConfirmedRestrictionBase :one
SELECT EXISTS(SELECT 1 FROM b04_node_state s JOIN b04_results r
 ON r.node_id=s.node_id AND r.task_id=s.evidence_task_id AND r.sequence=s.evidence_sequence
 WHERE s.node_id=? AND s.reconciliation='matched' AND r.cleanup_state='confirmed'
 AND r.error_code IS NULL AND r.confirmed_at IS NOT NULL
 AND EXISTS(SELECT 1 FROM b04_restrictions x WHERE x.node_id=s.node_id AND x.task_id=r.task_id AND x.sequence=r.sequence AND x.effective_digest=s.effective_digest)
 AND NOT EXISTS(SELECT 1 FROM b04_restrictions x JOIN b04_grants g ON g.identity_id=x.identity_id
 WHERE x.node_id=s.node_id AND x.task_id=r.task_id AND x.sequence=r.sequence AND g.confirmed_at IS NULL)) AS confirmed;
