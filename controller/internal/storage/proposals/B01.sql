-- Slot 003. Singleton state survives administrator deletion and prevents bootstrap reuse.
CREATE TABLE b01_auth_state (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    initialized INTEGER NOT NULL DEFAULT 0 CHECK (initialized IN (0,1))
);
INSERT INTO b01_auth_state(id) VALUES (1);
CREATE TABLE b01_administrator (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    username TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE b01_sessions (
    token_hash BLOB PRIMARY KEY NOT NULL CHECK (length(token_hash) = 32),
    admin_id INTEGER NOT NULL REFERENCES b01_administrator(id),
    admin_revision INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL CHECK (expires_at > created_at)
);
CREATE INDEX b01_session_expiry ON b01_sessions(expires_at);
-- Fixed server-wide buckets: no usernames, tokens or untrusted proxy/IP keys.
CREATE TABLE b01_auth_limits (
    bucket TEXT PRIMARY KEY CHECK (bucket IN ('initialize','login','password')),
    window_start INTEGER NOT NULL,
    attempts INTEGER NOT NULL CHECK (attempts > 0)
);
CREATE TRIGGER b01_bootstrap_no_reset BEFORE UPDATE ON b01_auth_state
WHEN OLD.initialized = 1 AND NEW.initialized != 1
BEGIN SELECT RAISE(ABORT, 'bootstrap already consumed'); END;
CREATE TRIGGER b01_bootstrap_no_delete BEFORE DELETE ON b01_auth_state
BEGIN SELECT RAISE(ABORT, 'bootstrap state is permanent'); END;
CREATE TRIGGER b01_credential_no_resurrection BEFORE UPDATE ON credentials
WHEN OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS NULL OR NEW.key_id != OLD.key_id OR NEW.ciphertext != OLD.ciphertext)
BEGIN SELECT RAISE(ABORT, 'revoked credential is immutable'); END;
