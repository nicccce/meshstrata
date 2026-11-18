-- 010: add B08 state without rewriting legacy credentials or any CA foreign keys.
CREATE TABLE subscription_download_credentials (
    id TEXT PRIMARY KEY NOT NULL,
    subscription_id TEXT NOT NULL REFERENCES subscriptions(id),
    token_version INTEGER NOT NULL CHECK (token_version BETWEEN 1 AND 9007199254740991),
    purpose TEXT NOT NULL DEFAULT 'subscription_download' CHECK (purpose = 'subscription_download'),
    key_id TEXT NOT NULL CHECK (length(key_id) > 0),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) > 0),
    revoked_at TEXT,
    UNIQUE (subscription_id, token_version)
);
ALTER TABLE subscriptions ADD COLUMN download_token_credential_ref TEXT REFERENCES subscription_download_credentials(id);
CREATE UNIQUE INDEX b08_token_reference ON subscriptions(download_token_credential_ref);
CREATE TRIGGER b08_token_pointer_guard BEFORE UPDATE OF download_token_hash, token_version, download_token_credential_ref, revoked_at ON subscriptions
WHEN NEW.download_token_credential_ref IS NOT NULL AND (
    NEW.revoked_at IS NOT NULL OR NEW.download_token_hash IS NULL OR length(NEW.download_token_hash) != 32 OR NOT EXISTS (
        SELECT 1 FROM subscription_download_credentials c
        WHERE c.id = NEW.download_token_credential_ref AND c.subscription_id = NEW.id
          AND c.token_version = NEW.token_version AND c.revoked_at IS NULL
    ) OR (OLD.download_token_credential_ref = NEW.download_token_credential_ref
          AND OLD.download_token_hash IS NOT NEW.download_token_hash)
)
BEGIN SELECT RAISE(ABORT, 'invalid subscription download binding'); END;
CREATE TRIGGER b08_token_insert_guard BEFORE INSERT ON subscriptions
WHEN NEW.download_token_credential_ref IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'install subscription download reference after subscription'); END;
CREATE TRIGGER b08_token_envelope_binding BEFORE UPDATE OF id, subscription_id, token_version, purpose, revoked_at ON subscription_download_credentials
WHEN OLD.id != NEW.id OR OLD.subscription_id != NEW.subscription_id OR OLD.token_version != NEW.token_version
  OR OLD.purpose != NEW.purpose OR (OLD.revoked_at IS NOT NULL AND OLD.revoked_at IS NOT NEW.revoked_at)
  OR (NEW.revoked_at IS NOT NULL AND EXISTS (
      SELECT 1 FROM subscriptions s WHERE s.download_token_credential_ref = OLD.id
  ))
BEGIN SELECT RAISE(ABORT, 'invalid download credential change'); END;
CREATE TRIGGER b08_subscription_no_resurrection BEFORE UPDATE ON subscriptions
WHEN OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS NOT OLD.revoked_at
BEGIN SELECT RAISE(ABORT, 'revoked subscription cannot be reactivated'); END;

CREATE TABLE b08_revisions (
    subscription_id TEXT NOT NULL REFERENCES subscriptions(id),
    revision INTEGER NOT NULL CHECK (revision BETWEEN 1 AND 9007199254740991),
    digest TEXT NOT NULL CHECK (length(digest) = 64),
    name TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    profile_id TEXT NOT NULL,
    profile_revision INTEGER NOT NULL,
    profile_digest TEXT NOT NULL CHECK (length(profile_digest) = 64),
    preferences_json TEXT NOT NULL,
    PRIMARY KEY (subscription_id, revision),
    FOREIGN KEY (profile_id, profile_revision) REFERENCES rule_profile_revisions(profile_id, revision)
);
CREATE TABLE b08_heads (
    subscription_id TEXT PRIMARY KEY NOT NULL REFERENCES subscriptions(id),
    desired_revision INTEGER NOT NULL,
    confirmed_revision INTEGER,
    FOREIGN KEY (subscription_id, desired_revision) REFERENCES b08_revisions(subscription_id, revision),
    FOREIGN KEY (subscription_id, confirmed_revision) REFERENCES b08_revisions(subscription_id, revision)
);

-- Before ReserveIngress only IDs and ciphertext exist. Once a real lease is
-- returned it is recorded, and the later state transition installs the 002 FKs.
CREATE TABLE b08_intents (
    id TEXT PRIMARY KEY NOT NULL,
    subscription_id TEXT NOT NULL,
    subject_revision INTEGER NOT NULL,
    entry_key TEXT NOT NULL,
    identity_key TEXT NOT NULL UNIQUE,
    generation INTEGER NOT NULL CHECK (generation BETWEEN 1 AND 9007199254740991),
    legacy_position INTEGER NOT NULL CHECK (legacy_position >= 0),
    route_id TEXT NOT NULL REFERENCES routes(id),
    route_revision INTEGER NOT NULL CHECK (route_revision BETWEEN 1 AND 9007199254740991),
    route_digest TEXT NOT NULL CHECK (length(route_digest) = 64),
    route_json TEXT NOT NULL,
    credential_ref TEXT NOT NULL UNIQUE REFERENCES credentials(id),
    expires_at TEXT NOT NULL,
    deadline TEXT NOT NULL,
    reserve_started INTEGER NOT NULL DEFAULT 0 CHECK (reserve_started IN (0,1)),
    reservation_id TEXT UNIQUE,
    listener_id TEXT UNIQUE REFERENCES listeners(id),
    endpoint_json TEXT,
    entry_id TEXT,
    identity_id TEXT UNIQUE REFERENCES entry_identities(id),
    dispatch_epoch INTEGER,
    task_id TEXT UNIQUE,
    state TEXT NOT NULL DEFAULT 'prepared' CHECK (state IN ('prepared','leased','committed','dispatching','confirmed','failed','retired')),
    revoked_at TEXT,
    cleanup_id TEXT NOT NULL UNIQUE,
    cleanup_epoch INTEGER,
    cleanup_deadline TEXT,
    cleanup_task_id TEXT UNIQUE,
    cleanup_state TEXT NOT NULL DEFAULT 'none' CHECK (cleanup_state IN ('none','pending','confirmed')),
    FOREIGN KEY (subscription_id, subject_revision) REFERENCES b08_revisions(subscription_id, revision),
    FOREIGN KEY (entry_id, subscription_id) REFERENCES subscription_entries(id, subscription_id),
    UNIQUE (subscription_id, id),
    UNIQUE (subscription_id, entry_key, generation),
    CHECK ((reservation_id IS NULL) = (listener_id IS NULL) AND (listener_id IS NULL) = (endpoint_json IS NULL)),
    CHECK ((entry_id IS NULL) = (identity_id IS NULL)),
    CHECK (entry_id IS NULL OR (entry_id = entry_key AND identity_id = identity_key AND listener_id IS NOT NULL)),
    CHECK ((revoked_at IS NULL) = (cleanup_state = 'none')),
    CHECK ((cleanup_epoch IS NULL) = (cleanup_deadline IS NULL))
);
CREATE INDEX b08_intents_work ON b08_intents(state, cleanup_state, id);
CREATE INDEX b08_entry_generations ON b08_intents(subscription_id, route_id, generation DESC);
CREATE TABLE b08_revision_entries (
    subscription_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    position INTEGER NOT NULL CHECK (position BETWEEN 0 AND 127),
    route_id TEXT NOT NULL REFERENCES routes(id),
    intent_id TEXT NOT NULL,
    PRIMARY KEY (subscription_id, revision, position),
    UNIQUE (subscription_id, revision, route_id),
    FOREIGN KEY (subscription_id, revision) REFERENCES b08_revisions(subscription_id, revision),
    FOREIGN KEY (subscription_id, intent_id) REFERENCES b08_intents(subscription_id, id)
);

-- Single administrator in B01; management HTTP must authenticate before calling.
CREATE TABLE b08_requests (
    scope TEXT NOT NULL,
    operation TEXT NOT NULL,
    idempotency_key TEXT NOT NULL,
    digest TEXT NOT NULL CHECK (length(digest) = 64),
    result_json TEXT NOT NULL,
    PRIMARY KEY (scope, operation, idempotency_key)
);
CREATE TABLE b08_clock (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    floor TEXT NOT NULL
);
INSERT INTO b08_clock(singleton, floor) VALUES (1, '');
