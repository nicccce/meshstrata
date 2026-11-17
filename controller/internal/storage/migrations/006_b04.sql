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
CREATE TRIGGER b04_artifact_binding_valid BEFORE INSERT ON b04_artifacts
WHEN NOT EXISTS (SELECT 1 FROM task_nodes n WHERE n.task_id=NEW.task_id AND n.node_id=NEW.node_id
                 AND n.target_revision=NEW.target_revision AND n.target_digest=NEW.target_digest)
BEGIN SELECT RAISE(ABORT,'invalid release artifact binding'); END;
CREATE TRIGGER b04_artifact_binding_immutable BEFORE UPDATE OF id, task_id, node_id, target_revision, target_digest ON b04_artifacts
BEGIN SELECT RAISE(ABORT,'immutable release artifact binding'); END;
CREATE TABLE b04_results (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    sequence INTEGER NOT NULL CHECK (sequence BETWEEN 1 AND 9007199254740991),
    payload_digest TEXT NOT NULL CHECK (length(payload_digest)=64),
    payload BLOB NOT NULL CHECK (length(payload) BETWEEN 1 AND 1048576),
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
    UNIQUE(identity_id,node_id),
    FOREIGN KEY (origin_task_id,node_id) REFERENCES b04_targets(task_id,node_id)
);
CREATE TRIGGER b04_grant_binding_immutable BEFORE UPDATE OF identity_id,entry_id,listener_id,node_id,generation,expires_at,grant_digest ON b04_grants
BEGIN SELECT RAISE(ABORT,'immutable release grant binding'); END;
CREATE TRIGGER b04_grant_origin_immutable BEFORE UPDATE OF origin_task_id ON b04_grants
WHEN OLD.origin_task_id IS NOT NULL AND OLD.origin_task_id IS NOT NEW.origin_task_id
BEGIN SELECT RAISE(ABORT,'immutable release grant origin'); END;
CREATE INDEX b04_grant_listener ON b04_grants(node_id,listener_id);
CREATE TABLE b04_target_grants (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    identity_id TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('include','expire','revoke')),
    PRIMARY KEY (task_id,node_id,identity_id),
    FOREIGN KEY (identity_id,node_id) REFERENCES b04_grants(identity_id,node_id),
    FOREIGN KEY (task_id,node_id) REFERENCES b04_targets(task_id,node_id)
);
CREATE TABLE b04_restrictions (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    identity_id TEXT NOT NULL,
    listener_id TEXT NOT NULL,
    generation INTEGER NOT NULL,
    reason TEXT NOT NULL CHECK (reason IN ('expired','revoked')),
    observed_at TEXT NOT NULL,
    epoch INTEGER NOT NULL,
    effective_digest TEXT NOT NULL CHECK (length(effective_digest)=64),
    PRIMARY KEY (task_id,node_id,sequence,identity_id),
    FOREIGN KEY (identity_id,node_id) REFERENCES b04_grants(identity_id,node_id),
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
    identity_id TEXT,
    released_at TEXT,
    FOREIGN KEY (identity_id,node_id) REFERENCES b04_grants(identity_id,node_id),
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
    identity_id TEXT NOT NULL,
    address TEXT NOT NULL,
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    transport TEXT NOT NULL CHECK (transport IN ('tcp','udp')),
    state TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','pending_cleanup','released')),
    confirmed_at TEXT,
    CHECK (state != 'released' OR confirmed_at IS NOT NULL),
    FOREIGN KEY (identity_id,node_id) REFERENCES b04_grants(identity_id,node_id)
);
CREATE UNIQUE INDEX b04_live_port ON b04_ports(node_id,port,transport,address) WHERE state != 'released';
-- Cross-ledger guard for legacy writers. Go allocation also parses and
-- compares binary IP addresses across both ledgers in one transaction.
CREATE TRIGGER b04_guard_legacy_port_insert BEFORE INSERT ON port_reservations
WHEN NEW.state!='released' AND EXISTS (
    SELECT 1 FROM b04_ports b WHERE b.node_id=NEW.node_id AND b.port=NEW.port
    AND b.transport=NEW.transport AND b.state!='released'
    AND (b.address=lower(NEW.address) OR b.address='::' OR NEW.address='::'
         OR (instr(b.address,':')=0 AND instr(NEW.address,':')=0
             AND (b.address='0.0.0.0' OR NEW.address='0.0.0.0')))
)
BEGIN SELECT RAISE(ABORT,'active format-1 port reservation'); END;
CREATE TRIGGER b04_guard_legacy_port_update BEFORE UPDATE OF node_id,address,transport,port,state ON port_reservations
WHEN NEW.state!='released' AND EXISTS (
    SELECT 1 FROM b04_ports b WHERE b.node_id=NEW.node_id AND b.port=NEW.port
    AND b.transport=NEW.transport AND b.state!='released'
    AND (b.address=lower(NEW.address) OR b.address='::' OR NEW.address='::'
         OR (instr(b.address,':')=0 AND instr(NEW.address,':')=0
             AND (b.address='0.0.0.0' OR NEW.address='0.0.0.0')))
)
BEGIN SELECT RAISE(ABORT,'active format-1 port reservation'); END;
CREATE TRIGGER b04_guard_port_release BEFORE UPDATE OF state ON b04_ports
WHEN NEW.state='released' AND (OLD.state!='pending_cleanup' OR NOT EXISTS (
    SELECT 1 FROM b04_grants g WHERE g.identity_id=OLD.identity_id AND g.node_id=OLD.node_id
    AND g.listener_id=OLD.listener_id AND g.confirmed_at IS NOT NULL
) OR EXISTS (
    SELECT 1 FROM b04_references r WHERE r.node_id=OLD.node_id AND r.kind='listener'
    AND r.resource_id=OLD.listener_id AND r.released_at IS NULL
) OR EXISTS (
    SELECT 1 FROM listener_references r WHERE r.listener_id=OLD.listener_id AND r.released_at IS NULL
))
BEGIN SELECT RAISE(ABORT,'format-1 port cleanup unconfirmed or referenced'); END;
CREATE TRIGGER b04_guard_port_resurrection BEFORE UPDATE ON b04_ports
WHEN OLD.state='released'
BEGIN SELECT RAISE(ABORT,'released format-1 port reservation is immutable'); END;
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
