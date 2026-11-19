-- name: I03RemovalIntents :many
SELECT i.* FROM b08_intents i
WHERE EXISTS (SELECT 1 FROM b06_route_hops h WHERE h.route_id=i.route_id AND h.revision=i.route_revision AND h.node_id=?)
ORDER BY i.id;

-- name: I03InvalidateRemovedHeads :exec
DELETE FROM b06_route_heads WHERE EXISTS
(SELECT 1 FROM b06_route_hops h WHERE h.route_id=b06_route_heads.route_id AND h.revision=b06_route_heads.revision AND h.node_id=?);

-- name: I03RemovalPublications :many
SELECT p.* FROM b06_publications p WHERE EXISTS
(SELECT 1 FROM b06_route_hops h WHERE h.route_id=p.route_id AND h.revision=p.revision AND h.node_id=?) ORDER BY p.id;

-- name: I03RemovalReleasableReferences :many
SELECT r.operation_id FROM b06_link_references r JOIN b06_publications p ON p.id=r.publication_id
WHERE r.released_at IS NULL AND EXISTS
(SELECT 1 FROM b06_route_hops h WHERE h.route_id=p.route_id AND h.revision=p.revision AND h.node_id=?)
AND NOT EXISTS (SELECT 1 FROM b08_intents i WHERE i.route_id=p.route_id AND i.route_revision=p.revision AND i.cleanup_state!='confirmed')
ORDER BY r.operation_id;

-- name: I03RemovalLinks :many
SELECT DISTINCT l.* FROM b06_internal_links l JOIN b06_link_references r ON r.link_id=l.id
JOIN b06_publications p ON p.id=r.publication_id WHERE EXISTS
(SELECT 1 FROM b06_route_hops h WHERE h.route_id=p.route_id AND h.revision=p.revision AND h.node_id=?) ORDER BY l.id;

-- name: I03ModernRemovalListener :one
SELECT EXISTS(SELECT 1 FROM b06_internal_links l WHERE l.listener_id=?) AS modern;

-- name: I03SetRemovalChild :exec
UPDATE b04_removal_links SET cleanup_task_id=sqlc.arg(child)
WHERE removal_task_id=sqlc.arg(removal) AND node_id=sqlc.arg(node_id) AND listener_id=sqlc.arg(listener_id)
AND cleanup_task_id IS NULL;

-- name: I03MarkRemovedLegacyPorts :exec
UPDATE port_reservations SET state='pending_cleanup' WHERE node_id=? AND state='reserved';
