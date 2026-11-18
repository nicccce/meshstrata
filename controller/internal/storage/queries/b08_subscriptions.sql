-- name: B08AdvanceClock :exec
UPDATE b08_clock SET floor = max(floor, sqlc.arg(now)) WHERE singleton = 1;

-- name: B08Clock :one
SELECT floor FROM b08_clock WHERE singleton = 1;

-- name: B08Request :one
SELECT digest, result_json FROM b08_requests WHERE scope = ? AND operation = ? AND idempotency_key = ?;

-- name: B08RecordRequest :exec
INSERT INTO b08_requests (scope, operation, idempotency_key, digest, result_json) VALUES (?, ?, ?, ?, ?);

-- name: B08Subscription :one
SELECT * FROM subscriptions WHERE id = ?;

-- name: B08InsertSubscription :exec
INSERT INTO subscriptions (id, name, revision, profile_id, profile_revision, expires_at, all_routes_group, group_by_exit, client_mode, updated_at)
VALUES (?, ?, 1, ?, ?, ?, ?, ?, ?, ?);

-- name: B08ChangeConfig :execrows
UPDATE subscriptions SET revision = revision + 1, name = sqlc.arg(name), profile_id = sqlc.arg(profile_id),
    profile_revision = sqlc.arg(profile_revision), expires_at = sqlc.arg(expires_at), all_routes_group = sqlc.arg(all_routes_group),
    group_by_exit = sqlc.arg(group_by_exit), client_mode = sqlc.arg(client_mode), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL AND revision = sqlc.arg(expected);

-- name: B08InstallToken :execrows
UPDATE subscriptions SET download_token_hash = sqlc.arg(hash), token_version = sqlc.arg(version),
    download_token_credential_ref = sqlc.arg(ref)
WHERE id = sqlc.arg(id) AND revision = 1 AND revoked_at IS NULL AND download_token_hash IS NULL;

-- name: B08RotateToken :execrows
UPDATE subscriptions SET download_token_hash = sqlc.arg(hash), token_version = token_version + 1,
    download_token_credential_ref = sqlc.arg(ref), revision = revision + 1, updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND revision = sqlc.arg(expected) AND token_version = sqlc.arg(expected_token) AND revoked_at IS NULL;

-- name: B08RevokeSubscription :execrows
UPDATE subscriptions SET revoked_at = sqlc.arg(at), download_token_hash = NULL, download_token_credential_ref = NULL,
    revision = revision + 1, updated_at = sqlc.arg(at)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL AND revision = sqlc.arg(expected);

-- name: B08InsertToken :exec
INSERT INTO subscription_download_credentials (id, subscription_id, token_version, key_id, ciphertext) VALUES (?, ?, ?, ?, ?);

-- name: B08RevokeToken :execrows
UPDATE subscription_download_credentials SET revoked_at = sqlc.arg(at)
WHERE id = sqlc.arg(id) AND subscription_id = sqlc.arg(subscription_id)
  AND token_version = sqlc.arg(version) AND revoked_at IS NULL;

-- name: B08TokenBySubscription :one
SELECT c.*, s.download_token_hash FROM subscriptions s
JOIN subscription_download_credentials c ON c.id = s.download_token_credential_ref
    AND c.subscription_id = s.id AND c.token_version = s.token_version
WHERE s.id = ? AND c.revoked_at IS NULL AND s.revoked_at IS NULL;

-- name: B08SubscriptionByHash :one
SELECT s.* FROM subscriptions s JOIN subscription_download_credentials c
    ON c.id = s.download_token_credential_ref AND c.subscription_id = s.id AND c.token_version = s.token_version
WHERE s.download_token_hash = ? AND c.revoked_at IS NULL AND s.revoked_at IS NULL;

-- name: B08InsertRevision :exec
INSERT INTO b08_revisions (subscription_id, revision, digest, name, expires_at, profile_id, profile_revision, profile_digest, preferences_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: B08Revision :one
SELECT * FROM b08_revisions WHERE subscription_id = ? AND revision = ?;

-- name: B08Head :one
SELECT * FROM b08_heads WHERE subscription_id = ?;

-- name: B08InsertHead :exec
INSERT INTO b08_heads (subscription_id, desired_revision) VALUES (?, ?);

-- name: B08AdvanceDesired :execrows
UPDATE b08_heads SET desired_revision = sqlc.arg(next) WHERE b08_heads.subscription_id = sqlc.arg(subscription_id)
  AND desired_revision = sqlc.arg(expected);

-- name: B08ConfirmDesired :execrows
UPDATE b08_heads SET confirmed_revision = sqlc.arg(revision) WHERE b08_heads.subscription_id = sqlc.arg(subscription_id)
  AND desired_revision = sqlc.arg(revision)
  AND (confirmed_revision IS NULL OR confirmed_revision < sqlc.arg(revision))
  AND NOT EXISTS (
    SELECT 1 FROM b08_revision_entries v JOIN b08_intents i ON i.id = v.intent_id
    WHERE v.subscription_id = sqlc.arg(subscription_id) AND v.revision = sqlc.arg(revision)
      AND (i.state != 'confirmed' OR i.revoked_at IS NOT NULL OR i.expires_at <= sqlc.arg(now))
  );

-- name: B08InsertRevisionEntry :exec
INSERT INTO b08_revision_entries (subscription_id, revision, position, route_id, intent_id) VALUES (?, ?, ?, ?, ?);

-- name: B08RevisionEntries :many
SELECT i.* FROM b08_revision_entries v JOIN b08_intents i ON i.id = v.intent_id
WHERE v.subscription_id = ? AND v.revision = ? ORDER BY v.position;

-- name: B08InsertIntent :exec
INSERT INTO b08_intents
(id, subscription_id, subject_revision, entry_key, identity_key, generation, legacy_position, route_id, route_revision, route_digest,
 route_json, credential_ref, expires_at, deadline, cleanup_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: B08Intent :one
SELECT * FROM b08_intents WHERE id = ?;

-- name: B08Work :many
SELECT id FROM b08_intents WHERE id > sqlc.arg(after_id) AND (
    state IN ('prepared', 'leased', 'committed', 'dispatching') OR cleanup_state = 'pending'
    OR (revoked_at IS NULL AND expires_at <= sqlc.arg(now))
) ORDER BY id LIMIT sqlc.arg(page_size);

-- name: B08AllLiveIntents :many
SELECT * FROM b08_intents WHERE subscription_id = ? AND revoked_at IS NULL ORDER BY id;

-- name: B08CleanupPending :one
SELECT EXISTS (SELECT 1 FROM b08_intents WHERE subscription_id = ? AND cleanup_state = 'pending');

-- name: B08StoreLease :execrows
UPDATE b08_intents SET reservation_id = sqlc.arg(reservation_id), listener_id = sqlc.arg(listener_id),
    endpoint_json = sqlc.arg(endpoint_json), state = CASE WHEN state = 'prepared' THEN 'leased' ELSE state END
WHERE id = sqlc.arg(id) AND listener_id IS NULL;

-- name: B08CommitEntry :execrows
UPDATE b08_intents SET entry_id = entry_key, identity_id = identity_key, state = 'committed'
WHERE id = ? AND state = 'leased' AND revoked_at IS NULL;

-- name: B08BeginDispatch :execrows
UPDATE b08_intents SET state = 'dispatching', dispatch_epoch = sqlc.arg(epoch)
WHERE id = sqlc.arg(id) AND state = 'committed' AND revoked_at IS NULL;

-- name: B08StoreTask :execrows
UPDATE b08_intents SET task_id = sqlc.arg(task_id) WHERE id = sqlc.arg(id) AND task_id IS NULL;

-- name: B08SetPublishState :execrows
UPDATE b08_intents SET state = sqlc.arg(next)
WHERE id = sqlc.arg(id) AND revoked_at IS NULL AND state = sqlc.arg(expected);

-- name: B08RetireIntent :execrows
UPDATE b08_intents SET state = CASE WHEN state = 'failed' THEN 'failed' ELSE 'retired' END, revoked_at = sqlc.arg(at), cleanup_state = 'pending'
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: B08BeginCleanup :execrows
UPDATE b08_intents SET cleanup_epoch = sqlc.arg(epoch), cleanup_deadline = sqlc.arg(deadline)
WHERE id = sqlc.arg(id) AND cleanup_state = 'pending' AND cleanup_epoch IS NULL;

-- name: B08StoreCleanupTask :execrows
UPDATE b08_intents SET cleanup_task_id = sqlc.arg(task_id)
WHERE id = sqlc.arg(id) AND cleanup_state = 'pending' AND cleanup_task_id IS NULL;

-- name: B08ConfirmCleanup :execrows
UPDATE b08_intents SET cleanup_state = 'confirmed'
WHERE id = sqlc.arg(id) AND cleanup_state = 'pending' AND cleanup_task_id = sqlc.arg(task_id);

-- name: B08Listener :one
SELECT * FROM listeners WHERE id = ?;

-- name: B08NodeEpoch :one
SELECT revocation_epoch FROM nodes WHERE id = ?;

-- name: B08LegacyEntryByRoute :one
SELECT * FROM subscription_entries WHERE subscription_id = ? AND route_id = ? AND revoked_at IS NULL;

-- name: B08LegacyEntries :many
SELECT * FROM subscription_entries WHERE subscription_id = ? ORDER BY position;

-- name: B08InsertEntry :exec
INSERT INTO subscription_entries (id, subscription_id, route_id, position, listener_id, expires_at, revocation_epoch)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: B08MoveEntryGeneration :execrows
UPDATE subscription_entries SET listener_id = sqlc.arg(listener_id), expires_at = sqlc.arg(expires_at),
    revocation_epoch = max(revocation_epoch, sqlc.arg(epoch)), cleanup_state = 'none'
WHERE id = sqlc.arg(id) AND subscription_id = sqlc.arg(subscription_id) AND revoked_at IS NULL;

-- name: B08InsertIdentity :exec
INSERT INTO entry_identities (id, entry_id, generation, credential_ref, expires_at) VALUES (?, ?, ?, ?, ?);

-- name: B08Identity :one
SELECT * FROM entry_identities WHERE id = ?;

-- name: B08RetireIdentity :execrows
UPDATE entry_identities SET revoked_at = sqlc.arg(at) WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: B08RemoveEntry :execrows
UPDATE subscription_entries SET revoked_at = sqlc.arg(at), cleanup_state = 'pending'
WHERE id = sqlc.arg(id) AND revoked_at IS NULL;

-- name: B08RetireLegacyIdentities :exec
UPDATE entry_identities SET revoked_at = sqlc.arg(at) WHERE entry_id = sqlc.arg(entry_id) AND revoked_at IS NULL;

-- name: B08ConfirmEntryCleanup :execrows
UPDATE subscription_entries SET cleanup_state = 'confirmed'
WHERE subscription_entries.id = sqlc.arg(id) AND revoked_at IS NOT NULL AND cleanup_state = 'pending'
  AND NOT EXISTS (SELECT 1 FROM b08_intents i WHERE i.entry_key = subscription_entries.id AND i.cleanup_state = 'pending');

-- name: B08InsertReference :exec
INSERT INTO listener_references (id, listener_id, entry_id) VALUES (?, ?, ?);

-- name: B08ReleaseReference :execrows
UPDATE listener_references SET released_at = sqlc.arg(at) WHERE id = sqlc.arg(id) AND released_at IS NULL;

-- name: B08BeginReserve :execrows
UPDATE b08_intents SET reserve_started = 1 WHERE id = ? AND state = 'prepared' AND revoked_at IS NULL AND reserve_started = 0;

-- name: B08ConfirmNoReserve :execrows
UPDATE b08_intents SET cleanup_state = 'confirmed'
WHERE id = ? AND cleanup_state = 'pending' AND reserve_started = 0 AND listener_id IS NULL;

-- name: B08LatestIdentity :one
SELECT * FROM entry_identities WHERE entry_id = ? ORDER BY generation DESC LIMIT 1;

-- name: B08CleanupIntent :one
SELECT * FROM b08_intents WHERE cleanup_id = ?;

-- name: B08ConfirmUnarmed :execrows
UPDATE b08_intents SET cleanup_state = 'confirmed'
WHERE id = ? AND cleanup_state = 'pending' AND revoked_at IS NOT NULL AND task_id IS NULL AND cleanup_task_id IS NULL;

-- name: B08RoutePinInUse :one
SELECT EXISTS (SELECT 1 FROM b08_intents
 WHERE route_id = ? AND route_revision = ? AND route_digest = ?
 AND (revoked_at IS NULL OR cleanup_state = 'pending'));
