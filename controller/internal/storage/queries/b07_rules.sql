-- name: B07GetMutation :one
SELECT request_digest, result_id, result_revision, result_digest FROM b07_mutations
WHERE scope = ? AND operation = ? AND idempotency_key = ?;

-- name: B07InsertMutation :exec
INSERT INTO b07_mutations (scope, operation, idempotency_key, request_digest, result_id, result_revision, result_digest)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: B07GetDomainHead :one
SELECT revision FROM domain_sets WHERE id = ?;

-- name: B07InsertDomainHead :exec
INSERT INTO domain_sets (id, identifier, name, revision, updated_at) VALUES (?, ?, ?, ?, ?);

-- name: B07UpdateDomainHead :execrows
UPDATE domain_sets SET name = ?, revision = ?, updated_at = ? WHERE id = ? AND revision = sqlc.arg(expected_revision);

-- name: B07InsertDomainRevision :exec
-- Set target is selected per rule in B07; 002 target_group is a legacy column.
INSERT INTO domain_set_revisions (domain_set_id, revision, target_group) VALUES (?, ?, '');

-- name: B07InsertDomainMember :exec
INSERT INTO domain_set_members (domain_set_id, revision, domain, position) VALUES (?, ?, ?, ?);

-- name: B07InsertDomainPin :exec
INSERT INTO b07_domain_pins (domain_set_id, revision, digest) VALUES (?, ?, ?);

-- name: B07GetDomainPin :one
SELECT digest FROM b07_domain_pins WHERE domain_set_id = ? AND revision = ?;

-- name: B07ListDomainMembers :many
SELECT domain, position FROM domain_set_members WHERE domain_set_id = ? AND revision = ? ORDER BY position LIMIT 4097;

-- name: B07GetProfileHead :one
SELECT revision FROM rule_profiles WHERE id = ?;

-- name: B07InsertProfileHead :exec
INSERT INTO rule_profiles (id, name, revision, updated_at) VALUES (?, ?, ?, ?);

-- name: B07UpdateProfileHead :execrows
UPDATE rule_profiles SET name = ?, revision = ?, updated_at = ? WHERE id = ? AND revision = sqlc.arg(expected_revision);

-- name: B07InsertProfileRevision :exec
INSERT INTO rule_profile_revisions (profile_id, revision, fallback_target, source_text) VALUES (?, ?, ?, ?);

-- name: B07InsertRuleLine :exec
INSERT INTO rule_lines (profile_id, revision, position, type, value, target, options, source_line, source_column)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: B07InsertLegacySetRef :exec
INSERT INTO rule_domain_sets (profile_id, profile_revision, domain_set_id, domain_set_revision) VALUES (?, ?, ?, ?);

-- name: B07InsertProfileSetPin :exec
INSERT INTO b07_profile_set_pins (profile_id, profile_revision, domain_set_id, domain_set_revision, digest, position)
VALUES (?, ?, ?, ?, ?, ?);

-- name: B07InsertProfilePin :exec
INSERT INTO b07_profile_pins (profile_id, revision, digest, match_line, match_column) VALUES (?, ?, ?, ?, ?);

-- name: B07GetProfilePin :one
SELECT b07_profile_pins.digest, b07_profile_pins.match_line, b07_profile_pins.match_column, rule_profile_revisions.fallback_target
FROM b07_profile_pins JOIN rule_profile_revisions USING (profile_id, revision)
WHERE b07_profile_pins.profile_id = ? AND b07_profile_pins.revision = ?;

-- name: B07ListRuleLines :many
SELECT position, type, value, target, options, source_line, source_column FROM rule_lines
WHERE profile_id = ? AND revision = ? ORDER BY position LIMIT 8193;

-- name: B07ListProfileSetPins :many
SELECT p.domain_set_id, p.domain_set_revision, p.digest, p.position FROM b07_profile_set_pins AS p
JOIN rule_domain_sets AS r USING (profile_id, profile_revision, domain_set_id, domain_set_revision)
WHERE p.profile_id = ? AND p.profile_revision = ? ORDER BY p.position LIMIT 129;
