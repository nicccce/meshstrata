-- B02 / slot 004: management identities, single-use enrollment and write replay.
CREATE TABLE b02_management_ca (
 singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
 certificate BLOB NOT NULL,
 credential_ref TEXT NOT NULL UNIQUE REFERENCES credentials(id)
);
CREATE TABLE b02_node_enrollment (
 node_id TEXT PRIMARY KEY REFERENCES nodes(id),
 relay_node_id TEXT REFERENCES nodes(id),
 generation INTEGER NOT NULL DEFAULT 0 CHECK(generation >= 0)
);
CREATE TABLE b02_tokens (
 id TEXT PRIMARY KEY REFERENCES credentials(id),
 node_id TEXT NOT NULL REFERENCES nodes(id),
 generation INTEGER NOT NULL CHECK(generation > 0),
 token_hash TEXT NOT NULL UNIQUE CHECK(length(token_hash)=64),
 expires_at TEXT NOT NULL,
 revoked_at TEXT,
 csr_hash TEXT,
 certificate BLOB,
 UNIQUE(node_id,generation),
 CHECK ((csr_hash IS NULL AND certificate IS NULL) OR (csr_hash IS NOT NULL AND certificate IS NOT NULL))
);
CREATE TABLE b02_node_certificates (
 fingerprint TEXT PRIMARY KEY CHECK(length(fingerprint)=64),
 node_id TEXT NOT NULL REFERENCES nodes(id),
 certificate BLOB NOT NULL,
 csr_hash TEXT NOT NULL,
 not_after TEXT NOT NULL,
 revoked_at TEXT,
 renewed_to TEXT REFERENCES b02_node_certificates(fingerprint)
);
CREATE INDEX b02_cert_node ON b02_node_certificates(node_id);
CREATE TABLE b02_mutations (
 scope TEXT NOT NULL,
 idempotency_key TEXT NOT NULL,
 request_digest TEXT NOT NULL,
 node_id TEXT NOT NULL REFERENCES nodes(id),
 revision INTEGER NOT NULL,
 token_ref TEXT REFERENCES b02_tokens(id),
 task_id TEXT UNIQUE REFERENCES tasks(id),
 PRIMARY KEY(scope,idempotency_key)
);
