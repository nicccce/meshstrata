-- Slot 006. The existing tasks/task_nodes remain the management projection.
CREATE TABLE b04_targets (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    phase TEXT NOT NULL DEFAULT 'queued' CHECK (phase IN ('queued','armed','confirmed','failed')),
    next_attempt INTEGER NOT NULL DEFAULT 0,
    fetched INTEGER NOT NULL DEFAULT 0 CHECK (fetched IN (0,1)),
    PRIMARY KEY (task_id,node_id),
    FOREIGN KEY (task_id,node_id) REFERENCES task_nodes(task_id,node_id)
);
CREATE INDEX b04_target_node ON b04_targets(node_id,phase);
CREATE INDEX b04_active_task_deadline ON tasks(deadline,id) WHERE state IN ('queued','running');
CREATE TABLE b04_artifacts (
    id TEXT PRIMARY KEY NOT NULL,
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    target_revision INTEGER NOT NULL,
    target_digest TEXT NOT NULL CHECK (length(target_digest)=64),
    key_id TEXT NOT NULL CHECK (length(key_id)>0),
    ciphertext BLOB NOT NULL CHECK (length(ciphertext)>0),
    UNIQUE (task_id,node_id),
    UNIQUE (node_id,target_revision),
    FOREIGN KEY (task_id,node_id) REFERENCES b04_targets(task_id,node_id)
);
CREATE TRIGGER b04_artifact_binding_immutable BEFORE UPDATE OF id, task_id, node_id, target_revision, target_digest ON b04_artifacts
BEGIN SELECT RAISE(ABORT,'immutable release artifact binding'); END;
CREATE TABLE b04_results (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 9007199254740991),
    payload_digest TEXT NOT NULL CHECK (length(payload_digest)=64),
    received_at TEXT NOT NULL,
    applied INTEGER NOT NULL,
    verification TEXT NOT NULL,
    rollback_state TEXT NOT NULL,
    cleanup_state TEXT NOT NULL,
    error_code TEXT,
    confirmed_at TEXT,
    PRIMARY KEY (task_id,node_id,sequence),
    FOREIGN KEY (task_id,node_id) REFERENCES b04_targets(task_id,node_id)
);
CREATE TABLE b04_grants (
    identity_id TEXT PRIMARY KEY NOT NULL,
    entry_id TEXT NOT NULL,
    listener_id TEXT NOT NULL,
    node_id TEXT NOT NULL REFERENCES nodes(id),
    generation INTEGER NOT NULL,
    expires_at TEXT NOT NULL,
    grant_digest TEXT NOT NULL CHECK (length(grant_digest)=64),
    origin_task_id TEXT,
    restricted_at TEXT,
    confirmed_at TEXT,
    UNIQUE(entry_id,generation),
    FOREIGN KEY (origin_task_id,node_id) REFERENCES b04_targets(task_id,node_id)
);
CREATE INDEX b04_grant_listener ON b04_grants(node_id,listener_id);
CREATE TABLE b04_target_grants (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    identity_id TEXT NOT NULL REFERENCES b04_grants(identity_id),
    action TEXT NOT NULL CHECK (action IN ('include','expire','revoke')),
    PRIMARY KEY (task_id,node_id,identity_id),
    FOREIGN KEY (task_id,node_id) REFERENCES b04_targets(task_id,node_id)
);
CREATE TABLE b04_restrictions (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    identity_id TEXT NOT NULL REFERENCES b04_grants(identity_id),
    listener_id TEXT NOT NULL,
    generation INTEGER NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN ('expired','revoked')),
    observed_at TEXT NOT NULL,
    epoch INTEGER NOT NULL,
    effective_digest TEXT NOT NULL CHECK (length(effective_digest)=64),
    PRIMARY KEY (task_id,node_id,sequence,identity_id),
    FOREIGN KEY (task_id,node_id,sequence) REFERENCES b04_results(task_id,node_id,sequence)
);
CREATE TABLE b04_node_state (
    node_id TEXT PRIMARY KEY REFERENCES nodes(id),
    applied_revision INTEGER NOT NULL DEFAULT 0,
    artifact_digest TEXT NOT NULL DEFAULT '',
    effective_digest TEXT NOT NULL DEFAULT '',
    epoch INTEGER NOT NULL DEFAULT 1,
    evidence_at TEXT NOT NULL DEFAULT '',
    evidence_task_id TEXT NOT NULL DEFAULT '',
    evidence_sequence INTEGER NOT NULL DEFAULT 0,
    -- Reconciliation never promotes an unverified Hello into a result.
    reconciliation TEXT NOT NULL DEFAULT 'pending' CHECK (reconciliation IN ('pending','matched','conflict'))
);
CREATE TABLE b04_references (
    id TEXT PRIMARY KEY NOT NULL,
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    resource_revision INTEGER NOT NULL CHECK (resource_revision>0),
    identity_id TEXT REFERENCES b04_grants(identity_id),
    released_at TEXT,
    FOREIGN KEY (task_id,node_id) REFERENCES b04_targets(task_id,node_id)
);
CREATE INDEX b04_ref_target ON b04_references(task_id,node_id);
CREATE INDEX b04_ref_resource ON b04_references(node_id,kind,resource_id) WHERE released_at IS NULL;
CREATE INDEX b04_ref_identity ON b04_references(node_id,identity_id) WHERE released_at IS NULL;
CREATE TABLE b04_reference_releases (
    reference_id TEXT NOT NULL REFERENCES b04_references(id),
    operation_id TEXT NOT NULL,
    released_at TEXT NOT NULL,
    PRIMARY KEY (reference_id,operation_id)
);
CREATE TABLE b04_ports (
    id TEXT PRIMARY KEY NOT NULL,
    node_id TEXT NOT NULL REFERENCES nodes(id),
    listener_id TEXT NOT NULL,
    identity_id TEXT NOT NULL REFERENCES b04_grants(identity_id),
    address TEXT NOT NULL,
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    transport TEXT NOT NULL CHECK (transport IN ('tcp','udp')),
    state TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','pending_cleanup','released')),
    confirmed_at TEXT,
    CHECK (state != 'released' OR confirmed_at IS NOT NULL)
);
CREATE UNIQUE INDEX b04_live_port ON b04_ports(node_id,port,transport,address) WHERE state != 'released';
CREATE TABLE b04_actions (
    scope TEXT NOT NULL,
    key TEXT NOT NULL,
    request_digest TEXT NOT NULL CHECK (length(request_digest)=64),
    task_id TEXT NOT NULL REFERENCES tasks(id),
    PRIMARY KEY (scope,key)
);
CREATE TABLE b04_removal_links (
    removal_task_id TEXT NOT NULL REFERENCES tasks(id),
    node_id TEXT NOT NULL REFERENCES nodes(id),
    listener_id TEXT NOT NULL,
    cleanup_task_id TEXT REFERENCES tasks(id),
    PRIMARY KEY (removal_task_id,node_id,listener_id)
);
