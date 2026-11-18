-- 009: extend the 002 rule catalog without rebuilding existing foreign keys.
-- Null side columns are legacy rows and cannot be used as B07 pinned snapshots.
ALTER TABLE domain_set_members ADD COLUMN position INTEGER CHECK (position >= 0);
CREATE UNIQUE INDEX b07_domain_member_position ON domain_set_members(domain_set_id, revision, position);
ALTER TABLE rule_lines ADD COLUMN source_line INTEGER CHECK (source_line > 0);
ALTER TABLE rule_lines ADD COLUMN source_column INTEGER CHECK (source_column > 0);

CREATE TABLE b07_domain_pins (
    domain_set_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    digest TEXT NOT NULL CHECK (length(digest) = 64),
    PRIMARY KEY (domain_set_id, revision),
    UNIQUE (domain_set_id, revision, digest),
    FOREIGN KEY (domain_set_id, revision) REFERENCES domain_set_revisions(domain_set_id, revision)
);
CREATE TABLE b07_profile_pins (
    profile_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    digest TEXT NOT NULL CHECK (length(digest) = 64),
    match_line INTEGER NOT NULL CHECK (match_line > 0),
    match_column INTEGER NOT NULL CHECK (match_column > 0),
    PRIMARY KEY (profile_id, revision),
    UNIQUE (profile_id, revision, digest),
    FOREIGN KEY (profile_id, revision) REFERENCES rule_profile_revisions(profile_id, revision)
);
-- 002 stores the exact revision reference. This companion FK also requires the
-- exact B07 digest; the position preserves ProfileSpec.Sets in the hash.
CREATE UNIQUE INDEX b07_legacy_set_reference_revision
    ON rule_domain_sets(profile_id, profile_revision, domain_set_id, domain_set_revision);
CREATE TABLE b07_profile_set_pins (
    profile_id TEXT NOT NULL,
    profile_revision INTEGER NOT NULL,
    domain_set_id TEXT NOT NULL,
    domain_set_revision INTEGER NOT NULL,
    digest TEXT NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (profile_id, profile_revision, position),
    UNIQUE (profile_id, profile_revision, domain_set_id),
    FOREIGN KEY (profile_id, profile_revision) REFERENCES b07_profile_pins(profile_id, revision),
    FOREIGN KEY (profile_id, profile_revision, domain_set_id, domain_set_revision)
        REFERENCES rule_domain_sets(profile_id, profile_revision, domain_set_id, domain_set_revision),
    FOREIGN KEY (domain_set_id, domain_set_revision, digest)
        REFERENCES b07_domain_pins(domain_set_id, revision, digest)
);
CREATE TABLE b07_mutations (
    scope TEXT NOT NULL CHECK (scope = 'controller_admin'),
    operation TEXT NOT NULL CHECK (operation IN ('domain_set.save','profile.save')),
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
    result_id TEXT NOT NULL,
    result_revision INTEGER NOT NULL CHECK (result_revision > 0),
    result_digest TEXT NOT NULL CHECK (length(result_digest) = 64),
    PRIMARY KEY (scope, operation, idempotency_key)
);
