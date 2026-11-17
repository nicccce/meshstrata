-- name: B04BumpNode :execrows
UPDATE nodes SET revision=revision+1, updated_at=sqlc.arg(now)
WHERE id=sqlc.arg(node_id) AND removed_at IS NULL AND revision=sqlc.arg(expected) AND revocation_epoch=sqlc.arg(epoch);

-- name: B04InsertTarget :exec
INSERT INTO b04_targets(task_id,node_id) VALUES (?,?);

-- name: B04GetTarget :one
SELECT * FROM b04_targets WHERE task_id=? AND node_id=?;

-- name: B04TaskNodes :many
SELECT * FROM task_nodes WHERE task_id=? ORDER BY node_id;

-- name: B04Dependencies :many
SELECT requires_node_id FROM task_dependencies WHERE task_id=? AND node_id=? ORDER BY requires_node_id;

-- name: B04InsertArtifact :exec
INSERT INTO b04_artifacts(id,task_id,node_id,target_revision,target_digest,key_id,ciphertext) VALUES (?,?,?,?,?,?,?);

-- name: B04GetArtifact :one
SELECT * FROM b04_artifacts WHERE task_id=? AND node_id=?;

-- name: B04ArtifactAtRevision :one
SELECT task_id,target_digest FROM b04_artifacts WHERE node_id=? AND target_revision=?;

-- name: B04ListReady :many
SELECT b.task_id,b.node_id
FROM b04_targets b JOIN tasks t ON t.id=b.task_id
JOIN nodes n ON n.id=b.node_id JOIN task_nodes tn ON tn.task_id=b.task_id AND tn.node_id=b.node_id
WHERE t.state IN ('queued','running') AND t.deadline>sqlc.arg(now)
AND b.phase IN ('queued','armed') AND b.next_attempt<=sqlc.arg(tick)
AND n.removed_at IS NULL AND n.revocation_epoch=tn.revocation_epoch
AND NOT EXISTS (
    SELECT 1 FROM b04_targets older JOIN tasks ot ON ot.id=older.task_id
    WHERE older.node_id=b.node_id AND ot.rowid<t.rowid AND (ot.state IN ('queued','running') OR (older.phase='armed' AND ot.deadline>sqlc.arg(now)))
)
AND NOT EXISTS (
    SELECT 1 FROM task_dependencies d JOIN b04_targets need ON need.task_id=d.task_id AND need.node_id=d.requires_node_id
    JOIN task_nodes nt ON nt.task_id=need.task_id AND nt.node_id=need.node_id
    JOIN nodes nn ON nn.id=need.node_id
    LEFT JOIN b04_node_state ns ON ns.node_id=nn.id
    WHERE d.task_id=t.id AND d.node_id=b.node_id AND
    (need.phase!='confirmed' OR nn.removed_at IS NOT NULL OR nn.revocation_epoch!=nt.revocation_epoch
      OR ns.node_id IS NULL OR ns.applied_revision!=nt.target_revision OR ns.artifact_digest!=nt.target_digest
      OR ns.reconciliation!='matched'
      OR EXISTS (SELECT 1 FROM b04_target_grants tg JOIN b04_grants g USING(identity_id)
          WHERE tg.task_id=d.task_id AND tg.node_id=d.requires_node_id AND tg.action='include'
          AND (g.restricted_at IS NOT NULL OR g.expires_at<=sqlc.arg(now))))
)
ORDER BY b.next_attempt,t.rowid,b.node_id LIMIT 100;

-- name: B04ArmTarget :execrows
UPDATE b04_targets SET phase='armed',next_attempt=sqlc.arg(next_attempt)
WHERE b04_targets.task_id=sqlc.arg(task_id) AND b04_targets.node_id=sqlc.arg(node_id) AND b04_targets.phase IN ('queued','armed') AND b04_targets.next_attempt<=sqlc.arg(tick)
AND EXISTS (SELECT 1 FROM tasks t WHERE t.id=b04_targets.task_id AND t.state IN ('queued','running') AND t.deadline>sqlc.arg(now));

-- name: B04DependencyBlocked :one
SELECT EXISTS (
    SELECT 1 FROM task_dependencies d JOIN b04_targets need ON need.task_id=d.task_id AND need.node_id=d.requires_node_id
    JOIN task_nodes nt ON nt.task_id=need.task_id AND nt.node_id=need.node_id
    JOIN nodes nn ON nn.id=need.node_id
    LEFT JOIN b04_node_state ns ON ns.node_id=nn.id
    WHERE d.task_id=sqlc.arg(task_id) AND d.node_id=sqlc.arg(node_id) AND
    (need.phase!='confirmed' OR nn.removed_at IS NOT NULL OR nn.revocation_epoch!=nt.revocation_epoch
      OR ns.node_id IS NULL OR ns.applied_revision!=nt.target_revision OR ns.artifact_digest!=nt.target_digest
      OR ns.reconciliation!='matched'
      OR EXISTS (SELECT 1 FROM b04_target_grants tg JOIN b04_grants g USING(identity_id)
          WHERE tg.task_id=d.task_id AND tg.node_id=d.requires_node_id AND tg.action='include'
          AND (g.restricted_at IS NOT NULL OR g.expires_at<=sqlc.arg(now))))
);


-- name: B04DeferTarget :exec
UPDATE b04_targets SET next_attempt=sqlc.arg(next_attempt)
WHERE b04_targets.task_id=sqlc.arg(task_id) AND b04_targets.node_id=sqlc.arg(node_id) AND b04_targets.phase IN ('queued','armed') AND b04_targets.next_attempt<=sqlc.arg(tick);

-- name: B04WakeNode :exec
UPDATE b04_targets SET next_attempt=0 WHERE node_id=? AND b04_targets.phase IN ('queued','armed');

-- name: B04SetRunning :exec
UPDATE tasks SET state='running', revision=revision+1 WHERE id=? AND state='queued';

-- name: B04SetTerminal :execrows
UPDATE tasks SET state=sqlc.arg(state),revision=revision+1
WHERE id=sqlc.arg(id) AND state IN ('queued','running');

-- name: B04FinishTarget :execrows
UPDATE b04_targets SET phase=sqlc.arg(phase)
WHERE b04_targets.task_id=sqlc.arg(task_id) AND b04_targets.node_id=sqlc.arg(node_id) AND phase='armed';

-- name: B04UnconfirmedCount :one
SELECT count(*) FROM b04_targets WHERE task_id=? AND phase!='confirmed';

-- name: B04ArmedCount :one
SELECT count(*) FROM b04_targets WHERE task_id=? AND phase!='queued';

-- name: B04Expiring :many
SELECT t.id FROM tasks t
WHERE t.state IN ('queued','running') AND t.deadline<=sqlc.arg(now)
AND EXISTS (SELECT 1 FROM b04_targets b WHERE b.task_id=t.id)
ORDER BY t.deadline,t.id LIMIT 100;

-- name: B04FencedTasks :many
SELECT DISTINCT t.id FROM tasks t
JOIN b04_targets b ON b.task_id=t.id
JOIN task_nodes tn ON tn.task_id=b.task_id AND tn.node_id=b.node_id
JOIN nodes n ON n.id=tn.node_id
WHERE t.state IN ('queued','running') AND (n.removed_at IS NOT NULL OR n.revocation_epoch!=tn.revocation_epoch)
LIMIT 100;

-- name: B04GetResult :one
SELECT * FROM b04_results WHERE task_id=? AND node_id=? AND sequence=?;

-- name: B04InsertResult :exec
INSERT INTO b04_results(task_id,node_id,sequence,payload_digest,payload,received_at,applied,verification,rollback_state,cleanup_state,error_code,confirmed_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?);

-- name: B04InsertRestriction :exec
INSERT INTO b04_restrictions(task_id,node_id,sequence,identity_id,listener_id,generation,reason,observed_at,epoch,effective_digest)
VALUES (?,?,?,?,?,?,?,?,?,?);

-- name: B04GetState :one
SELECT * FROM b04_node_state WHERE node_id=?;

-- name: B04ObserveHello :exec
INSERT INTO b04_node_state(node_id,reconciliation) VALUES (?,?)
ON CONFLICT(node_id) DO UPDATE SET reconciliation=excluded.reconciliation;

-- name: B04ObserveApplied :exec
INSERT INTO b04_node_state(node_id,applied_revision,artifact_digest,effective_digest,epoch,evidence_at,reconciliation)
VALUES (?,?,?,?,?,?,'matched')
ON CONFLICT(node_id) DO UPDATE SET applied_revision=excluded.applied_revision,artifact_digest=excluded.artifact_digest,
epoch=excluded.epoch,evidence_at=excluded.evidence_at,effective_digest=excluded.effective_digest,evidence_task_id='',evidence_sequence=0,reconciliation='matched'
WHERE b04_node_state.applied_revision<excluded.applied_revision AND b04_node_state.epoch<=excluded.epoch
AND b04_node_state.evidence_at<=excluded.evidence_at;

-- name: B04ObserveRestriction :exec
INSERT INTO b04_node_state(node_id,epoch,evidence_at,effective_digest,evidence_task_id,evidence_sequence) VALUES (?,?,?,?,?,?)
ON CONFLICT(node_id) DO UPDATE SET epoch=excluded.epoch,evidence_at=excluded.evidence_at,effective_digest=excluded.effective_digest,
evidence_task_id=excluded.evidence_task_id,evidence_sequence=excluded.evidence_sequence
WHERE b04_node_state.epoch<=excluded.epoch AND (b04_node_state.evidence_at<excluded.evidence_at
OR (b04_node_state.evidence_at=excluded.evidence_at AND
(b04_node_state.effective_digest='' OR (b04_node_state.evidence_task_id=excluded.evidence_task_id
AND b04_node_state.evidence_sequence<excluded.evidence_sequence))));

-- name: B04GetAction :one
SELECT * FROM b04_actions WHERE scope=? AND key=?;

-- name: B04InsertAction :exec
INSERT INTO b04_actions(scope,key,request_digest,task_id) VALUES (?,?,?,?);

-- name: B04MarkFetched :exec
UPDATE b04_targets SET fetched=1 WHERE task_id=? AND node_id=?;

-- name: B04RemovalTasks :many
SELECT t.id FROM b02_mutations m JOIN tasks t ON t.id=m.task_id
WHERE m.scope='remove:'||m.node_id AND t.id>sqlc.arg(after_id)
ORDER BY t.id LIMIT 32;

-- name: B04RemovalRouteTargets :many
SELECT DISTINCT peer.node_id,peer.listener_id FROM route_hops gone
JOIN routes r ON r.id=gone.route_id AND r.published_revision=gone.revision
JOIN route_hops peer ON peer.route_id=gone.route_id AND peer.revision=gone.revision
WHERE gone.node_id=? AND peer.node_id!=gone.node_id
ORDER BY peer.node_id,peer.listener_id;

-- name: B04LinkRemoval :exec
INSERT INTO b04_removal_links(removal_task_id,node_id,listener_id) VALUES (?,?,?)
ON CONFLICT DO NOTHING;

-- name: B04GetRemovalLinks :many
SELECT * FROM b04_removal_links WHERE removal_task_id=? ORDER BY node_id,listener_id;

-- name: B04SetRemovalChild :exec
UPDATE b04_removal_links SET cleanup_task_id=sqlc.arg(child)
WHERE removal_task_id=sqlc.arg(removal) AND node_id=sqlc.arg(node_id) AND cleanup_task_id IS NULL;

-- name: B04LatestTargetRevision :one
SELECT target_revision FROM b04_artifacts WHERE node_id=? ORDER BY target_revision DESC LIMIT 1;


-- name: B04WaitForResult :exec
UPDATE b04_targets SET next_attempt=sqlc.arg(deadline_tick)
WHERE task_id=sqlc.arg(task_id) AND node_id=sqlc.arg(node_id) AND phase='armed'
AND next_attempt=sqlc.arg(lease);


-- name: B04HasRestrictions :one
SELECT EXISTS (SELECT 1 FROM b04_target_grants WHERE task_id=? AND action!='include');

-- name: B04PeerListenerInUse :one
SELECT EXISTS (
 SELECT 1 FROM route_hops hop JOIN routes r ON r.id=hop.route_id AND r.published_revision=hop.revision
 WHERE hop.listener_id=sqlc.arg(listener_id) AND NOT EXISTS (
  SELECT 1 FROM route_hops gone WHERE gone.route_id=hop.route_id AND gone.revision=hop.revision
  AND gone.node_id=sqlc.arg(removed_node)
 )
);
