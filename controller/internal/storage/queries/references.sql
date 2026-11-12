-- B04 owns Listener references and port reservations after B00.
-- name: ReleaseListenerReference :execrows
UPDATE listener_references SET released_at = ? WHERE id = ? AND released_at IS NULL;

-- name: CountListenerReferences :one
SELECT count(*) FROM listener_references WHERE listener_id = ? AND released_at IS NULL;

-- name: ReleaseListenerPort :execrows
UPDATE port_reservations SET state = 'released', cleanup_confirmed_at = ?
WHERE port_reservations.id = ? AND port_reservations.listener_id IS NOT NULL AND port_reservations.state = 'pending_cleanup'
AND EXISTS (SELECT 1 FROM listeners WHERE listeners.id = port_reservations.listener_id AND cleanup_state = 'confirmed')
AND NOT EXISTS (SELECT 1 FROM listener_references WHERE listener_references.listener_id = port_reservations.listener_id AND released_at IS NULL);
