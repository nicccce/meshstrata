-- name: B04InsertGrant :exec
INSERT INTO b04_grants(identity_id,entry_id,listener_id,node_id,generation,expires_at,grant_digest)
VALUES (?,?,?,?,?,?,?) ON CONFLICT(identity_id) DO NOTHING;

-- name: B04GetGrant :one
SELECT * FROM b04_grants WHERE identity_id=?;

-- name: B04InsertTargetGrant :exec
INSERT INTO b04_target_grants(task_id,node_id,identity_id,action) VALUES (?,?,?,?);

-- name: B04TargetGrants :many
SELECT g.*,tg.action FROM b04_target_grants tg JOIN b04_grants g USING(identity_id)
WHERE tg.task_id=? AND tg.node_id=? ORDER BY identity_id;

-- name: B04PinGrantOrigins :exec
UPDATE b04_grants SET origin_task_id=sqlc.arg(task_id) WHERE origin_task_id IS NULL AND b04_grants.node_id=sqlc.arg(node_id)
AND identity_id IN (SELECT tg.identity_id FROM b04_target_grants tg WHERE tg.task_id=sqlc.arg(task_id) AND tg.node_id=sqlc.arg(node_id) AND tg.action='include');

-- name: B04MarkRestricted :exec
UPDATE b04_grants SET restricted_at=coalesce(restricted_at,sqlc.arg(at)) WHERE identity_id=sqlc.arg(identity_id);

-- name: B04ConfirmGrant :exec
UPDATE b04_grants SET restricted_at=coalesce(restricted_at,sqlc.arg(at)),confirmed_at=coalesce(confirmed_at,sqlc.arg(at))
WHERE identity_id=sqlc.arg(identity_id);

-- name: B04InsertReference :exec
INSERT INTO b04_references(id,task_id,node_id,kind,resource_id,resource_revision,identity_id) VALUES (?,?,?,?,?,?,?);

-- name: B04RetireReference :execrows
UPDATE b04_references SET released_at=sqlc.arg(at) WHERE id=sqlc.arg(id) AND released_at IS NULL;

-- name: B04LogReferenceRelease :exec
INSERT INTO b04_reference_releases(reference_id,operation_id,released_at) VALUES (?,?,?) ON CONFLICT DO NOTHING;

-- name: B04IdentityReferences :many
SELECT id FROM b04_references WHERE node_id=? AND identity_id=? AND released_at IS NULL;

-- name: B04UnsentReferences :many
SELECT r.id FROM b04_references r JOIN b04_targets b USING(task_id,node_id)
WHERE r.task_id=? AND r.released_at IS NULL AND b.phase='queued';

-- name: B04SupersededReferences :many
SELECT r.id FROM b04_references r JOIN task_nodes tn USING(task_id,node_id)
LEFT JOIN b04_grants g ON g.identity_id=r.identity_id
WHERE r.node_id=sqlc.arg(node_id) AND tn.target_revision<sqlc.arg(revision) AND r.released_at IS NULL
AND (r.identity_id IS NULL OR g.confirmed_at IS NOT NULL OR EXISTS (
 SELECT 1 FROM b04_target_grants ng WHERE ng.task_id=sqlc.arg(new_task) AND ng.node_id=r.node_id
 AND ng.identity_id=r.identity_id AND ng.action='include'
));

-- name: B04PortCandidates :many
SELECT modern.address,modern.transport,modern.listener_id,modern.identity_id FROM b04_ports modern
WHERE modern.node_id=sqlc.arg(node_id) AND modern.port=sqlc.arg(port) AND modern.state!='released'
UNION ALL
SELECT legacy.address,legacy.transport,coalesce(legacy.listener_id,''),'' FROM port_reservations legacy
WHERE legacy.node_id=sqlc.arg(node_id) AND legacy.port=sqlc.arg(port) AND legacy.state!='released';

-- name: B04InsertPort :exec
INSERT INTO b04_ports(id,node_id,listener_id,identity_id,address,port,transport) VALUES (?,?,?,?,?,?,?);

-- name: B04PendingPorts :exec
UPDATE b04_ports SET state='pending_cleanup' WHERE identity_id=? AND state='reserved';

-- name: B04SweepPorts :exec
UPDATE b04_ports SET state='released',confirmed_at=sqlc.arg(at) WHERE b04_ports.node_id=sqlc.arg(node_id) AND state='pending_cleanup'
AND EXISTS (SELECT 1 FROM b04_grants g WHERE g.identity_id=b04_ports.identity_id AND g.confirmed_at IS NOT NULL)
AND NOT EXISTS (SELECT 1 FROM b04_references r WHERE r.node_id=b04_ports.node_id AND r.kind='listener'
AND r.resource_id=b04_ports.listener_id AND r.released_at IS NULL)
AND NOT EXISTS (SELECT 1 FROM listener_references lr WHERE lr.listener_id=b04_ports.listener_id AND lr.released_at IS NULL);

-- name: B04LatestVerifiedArtifact :one
SELECT a.* FROM b04_artifacts a JOIN b04_targets b ON b.task_id=a.task_id AND b.node_id=a.node_id JOIN task_nodes tn ON tn.task_id=a.task_id AND tn.node_id=a.node_id
WHERE a.node_id=? AND b.phase='confirmed' AND tn.verification='verified'
ORDER BY a.target_revision DESC LIMIT 1;

-- name: B04PossibleLiveGrants :many
SELECT g.* FROM b04_grants g WHERE g.node_id=? AND g.origin_task_id IS NOT NULL AND g.confirmed_at IS NULL;

-- name: B04RevokeAuthorized :one
SELECT EXISTS (SELECT 1 FROM b04_target_grants tg
JOIN b04_targets b ON b.task_id=tg.task_id AND b.node_id=tg.node_id
JOIN task_nodes tn ON tn.task_id=tg.task_id AND tn.node_id=tg.node_id
WHERE tg.identity_id=sqlc.arg(identity_id) AND tg.node_id=sqlc.arg(node_id) AND tg.action='revoke' AND b.fetched=1
AND tn.revocation_epoch<=sqlc.arg(epoch));


-- name: B04SourceGrants :many
SELECT g.* FROM b04_grants g WHERE g.node_id=sqlc.arg(node_id)
AND ((g.origin_task_id IS NOT NULL AND g.confirmed_at IS NULL) OR EXISTS (
 SELECT 1 FROM b04_target_grants tg WHERE tg.task_id=sqlc.arg(task_id)
 AND tg.node_id=g.node_id AND tg.identity_id=g.identity_id
));


-- name: B04NewestGeneration :one
SELECT generation FROM b04_grants WHERE entry_id=? ORDER BY generation DESC LIMIT 1;

-- name: B04MarkRemovedPorts :exec
UPDATE b04_ports SET state='pending_cleanup' WHERE node_id=? AND state='reserved';

-- name: B04HoldUnusedPorts :exec
UPDATE b04_ports SET state='pending_cleanup'
WHERE state='reserved' AND EXISTS (
 SELECT 1 FROM b04_target_grants tg WHERE tg.task_id=sqlc.arg(task_id) AND tg.node_id=b04_ports.node_id
 AND tg.identity_id=b04_ports.identity_id
)
AND NOT EXISTS (
 SELECT 1 FROM b04_references r WHERE r.node_id=b04_ports.node_id AND r.kind='listener'
 AND r.resource_id=b04_ports.listener_id AND r.released_at IS NULL
)
AND NOT EXISTS (SELECT 1 FROM listener_references lr WHERE lr.listener_id=b04_ports.listener_id AND lr.released_at IS NULL);

-- name: B04ReusePort :exec
UPDATE b04_ports SET state='reserved' WHERE node_id=? AND port=? AND address=? AND transport=? AND identity_id=? AND state='pending_cleanup';
