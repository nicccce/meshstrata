-- B06 (008): format-2 snapshots. Do not modify the installed format-1 heads.
CREATE TABLE b06_mutations (
    operation TEXT NOT NULL,
    idem_key TEXT NOT NULL,
    request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
    result BLOB NOT NULL,
    PRIMARY KEY(operation,idem_key)
);

CREATE TABLE b06_route_versions (
    route_id TEXT NOT NULL REFERENCES routes(id),
    revision INTEGER NOT NULL CHECK(revision > 0),
    name TEXT NOT NULL,
    draft_digest TEXT NOT NULL CHECK(length(draft_digest)=64),
    egress_id TEXT NOT NULL,
    egress_revision INTEGER NOT NULL CHECK(egress_revision > 0),
    egress_digest TEXT NOT NULL CHECK(length(egress_digest)=64),
    node_default INTEGER NOT NULL CHECK(node_default IN (0,1)),
    business_udp INTEGER NOT NULL CHECK(business_udp IN (0,1)),
    PRIMARY KEY(route_id,revision)
);
CREATE TABLE b06_route_hops (
    route_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    position INTEGER NOT NULL CHECK(position BETWEEN 0 AND 7),
    node_id TEXT NOT NULL REFERENCES nodes(id),
    node_config BLOB NOT NULL,
    dns BLOB NOT NULL,
    endpoint BLOB NOT NULL,
    dial_ip TEXT NOT NULL,
    PRIMARY KEY(route_id,revision,position),
    UNIQUE(route_id,revision,node_id),
    FOREIGN KEY(route_id,revision) REFERENCES b06_route_versions(route_id,revision)
);
CREATE TABLE b06_publications (
    id TEXT PRIMARY KEY NOT NULL,
    route_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    idem_key TEXT NOT NULL UNIQUE,
    deadline TEXT NOT NULL,
    task_id TEXT UNIQUE REFERENCES tasks(id),
    failure BLOB,
    state TEXT NOT NULL DEFAULT 'preparing'
        CHECK(state IN ('preparing','prepared','queued','running','published','failed','cancelled','timed_out','rolled_back','rollback_pending','rollback_failed')),
    FOREIGN KEY(route_id,revision) REFERENCES b06_route_versions(route_id,revision)
);
CREATE UNIQUE INDEX b06_active_publication ON b06_publications(route_id)
WHERE state IN ('preparing','prepared','queued','running','rollback_pending','rollback_failed');
CREATE TABLE b06_publication_nodes (
    publication_id TEXT NOT NULL REFERENCES b06_publications(id),
    position INTEGER NOT NULL CHECK(position BETWEEN 0 AND 7),
    node_id TEXT NOT NULL REFERENCES nodes(id),
    target_revision INTEGER NOT NULL CHECK(target_revision > 0),
    secret_ref TEXT NOT NULL UNIQUE,
    key_id TEXT NOT NULL,
    ciphertext BLOB NOT NULL CHECK(length(ciphertext)>0),
    PRIMARY KEY(publication_id,position),
    UNIQUE(publication_id,node_id)
);
CREATE TABLE b06_published_versions (
    route_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    publication_id TEXT NOT NULL UNIQUE REFERENCES b06_publications(id),
    route_digest TEXT NOT NULL CHECK(length(route_digest)=64),
    confirmed INTEGER NOT NULL DEFAULT 0 CHECK(confirmed IN (0,1)),
    PRIMARY KEY(route_id,revision),
    FOREIGN KEY(route_id,revision) REFERENCES b06_route_versions(route_id,revision)
);
CREATE TABLE b06_published_hops (
    route_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    position INTEGER NOT NULL,
    port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
    PRIMARY KEY(route_id,revision,position),
    FOREIGN KEY(route_id,revision) REFERENCES b06_published_versions(route_id,revision),
    FOREIGN KEY(route_id,revision,position) REFERENCES b06_route_hops(route_id,revision,position)
);
CREATE TABLE b06_route_heads (
    route_id TEXT PRIMARY KEY NOT NULL,
    revision INTEGER NOT NULL,
    FOREIGN KEY(route_id,revision) REFERENCES b06_published_versions(route_id,revision)
);

CREATE TABLE b06_egress_objects (
    id TEXT PRIMARY KEY NOT NULL,
    node_id TEXT NOT NULL REFERENCES nodes(id),
    kind TEXT NOT NULL CHECK(kind IN ('target','policy')),
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK(revision > 0)
);
CREATE TABLE b06_egress_versions (
    id TEXT NOT NULL REFERENCES b06_egress_objects(id),
    revision INTEGER NOT NULL CHECK(revision > 0),
    digest TEXT NOT NULL CHECK(length(digest)=64),
    spec BLOB NOT NULL,
    PRIMARY KEY(id,revision)
);
CREATE TABLE b06_egress_credentials (
    id TEXT PRIMARY KEY NOT NULL,
    target_id TEXT NOT NULL,
    origin_revision INTEGER NOT NULL,
    key_id TEXT NOT NULL,
    ciphertext BLOB NOT NULL CHECK(length(ciphertext)>0),
    FOREIGN KEY(target_id,origin_revision) REFERENCES b06_egress_versions(id,revision)
);
-- Provisioned only from trusted Controller/Agent evidence. Store topology
-- evidence separately from the pinned policy payload so it can expire.
CREATE TABLE b06_egress_evidence (
    node_id TEXT NOT NULL REFERENCES nodes(id),
    node_revision INTEGER NOT NULL CHECK(node_revision > 0),
    target_digest TEXT NOT NULL CHECK(length(target_digest)=64),
    dns_digest TEXT NOT NULL CHECK(length(dns_digest)=64),
    expires_at TEXT NOT NULL,
    business_udp INTEGER NOT NULL CHECK(business_udp IN (0,1)),
    PRIMARY KEY(node_id,node_revision,target_digest,dns_digest)
);

CREATE TABLE b06_link_candidates (
    operation_id TEXT PRIMARY KEY NOT NULL,
    publication_id TEXT NOT NULL REFERENCES b06_publications(id),
    position INTEGER NOT NULL CHECK(position BETWEEN 1 AND 7),
    suffix_digest TEXT NOT NULL CHECK(length(suffix_digest)=64),
    proposed_link TEXT NOT NULL UNIQUE,
    credential_ref TEXT NOT NULL UNIQUE,
    key_id TEXT NOT NULL,
    ciphertext BLOB NOT NULL CHECK(length(ciphertext)>0),
    UNIQUE(publication_id,position,suffix_digest),
    UNIQUE(operation_id,publication_id,position)
);
CREATE TABLE b06_internal_links (
    id TEXT PRIMARY KEY NOT NULL,
    revision INTEGER NOT NULL CHECK(revision=1),
    suffix_digest TEXT NOT NULL CHECK(length(suffix_digest)=64),
    tail BLOB NOT NULL,
    node_id TEXT NOT NULL REFERENCES nodes(id),
    listener_id TEXT NOT NULL UNIQUE,
    credential_ref TEXT NOT NULL UNIQUE REFERENCES credentials(id),
    state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','retiring','released')),
    cleanup_task_id TEXT UNIQUE REFERENCES tasks(id),
    CHECK((state='released') = (cleanup_task_id IS NOT NULL)),
    FOREIGN KEY(listener_id,node_id) REFERENCES listeners(id,node_id)
);
CREATE UNIQUE INDEX b06_link_suffix ON b06_internal_links(suffix_digest) WHERE state != 'released';
CREATE TABLE b06_link_references (
    operation_id TEXT PRIMARY KEY NOT NULL,
    publication_id TEXT NOT NULL,
    position INTEGER NOT NULL,
    link_id TEXT NOT NULL REFERENCES b06_internal_links(id),
    reservation_id TEXT NOT NULL UNIQUE,
    request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
    released_at TEXT,
    UNIQUE(publication_id,position),
    FOREIGN KEY(operation_id,publication_id,position) REFERENCES b06_link_candidates(operation_id,publication_id,position)
);
CREATE TRIGGER b06_link_reference_no_resurrection BEFORE UPDATE ON b06_link_references
WHEN OLD.released_at IS NOT NULL AND NEW.released_at IS NULL
BEGIN SELECT RAISE(ABORT,'released internal reference'); END;
CREATE TRIGGER b06_link_state_guard BEFORE UPDATE OF state ON b06_internal_links
WHEN (OLD.state='released' AND NEW.state!='released')
OR (OLD.state='retiring' AND NEW.state='active')
OR (NEW.state!='active' AND EXISTS (SELECT 1 FROM b06_link_references r WHERE r.link_id=OLD.id AND r.released_at IS NULL))
BEGIN SELECT RAISE(ABORT,'invalid internal link transition'); END;

-- B08 owns the intent, not this projection. Store only the immutable binding
-- received at reservation and the lease actually materialized by I03.
CREATE TABLE b06_ingress (
    operation_id TEXT PRIMARY KEY NOT NULL,
    request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
    reservation BLOB NOT NULL,
    lease BLOB,
    listener_id TEXT UNIQUE REFERENCES listeners(id),
    credential_ref TEXT NOT NULL UNIQUE REFERENCES credentials(id),
    identity_id TEXT NOT NULL UNIQUE,
    route_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    FOREIGN KEY(route_id,revision) REFERENCES b06_published_versions(route_id,revision)
);
CREATE TABLE b06_entry_plans (
    operation_id TEXT NOT NULL,
    origin_operation_id TEXT NOT NULL REFERENCES b06_ingress(operation_id),
    kind TEXT NOT NULL CHECK(kind IN ('publish','revoke','cleanup')),
    idem_key TEXT NOT NULL,
    request_digest TEXT NOT NULL CHECK(length(request_digest)=64),
    secret_ref TEXT NOT NULL UNIQUE,
    key_id TEXT NOT NULL,
    ciphertext BLOB NOT NULL CHECK(length(ciphertext)>0),
    task_id TEXT UNIQUE REFERENCES tasks(id),
    PRIMARY KEY(kind,idem_key),
    UNIQUE(operation_id,kind)
);
CREATE TABLE b06_entry_link_references (
    identity_id TEXT NOT NULL REFERENCES b06_ingress(identity_id),
    link_id TEXT NOT NULL REFERENCES b06_internal_links(id),
    released_at TEXT,
    PRIMARY KEY(identity_id,link_id)
);
CREATE TRIGGER b06_link_entry_guard BEFORE UPDATE OF state ON b06_internal_links
WHEN NEW.state!='active' AND EXISTS (SELECT 1 FROM b06_entry_link_references WHERE link_id=OLD.id AND released_at IS NULL)
BEGIN SELECT RAISE(ABORT,'active entry link reference'); END;
CREATE TRIGGER b06_no_reference_after_retirement BEFORE INSERT ON b06_entry_link_references
WHEN NOT EXISTS(SELECT 1 FROM b06_internal_links WHERE id=NEW.link_id AND state='active')
BEGIN SELECT RAISE(ABORT,'retired entry link'); END;
CREATE TRIGGER b06_no_early_internal_port_release BEFORE UPDATE OF state ON port_reservations
WHEN NEW.state='released' AND EXISTS(SELECT 1 FROM b06_internal_links
    WHERE listener_id=NEW.listener_id AND state!='released')
BEGIN SELECT RAISE(ABORT,'internal cleanup not confirmed'); END;

CREATE TABLE b06_policy_targets (
    policy_id TEXT NOT NULL,
    policy_revision INTEGER NOT NULL,
    target_id TEXT NOT NULL,
    target_revision INTEGER NOT NULL,
    PRIMARY KEY(policy_id,policy_revision,target_id),
    FOREIGN KEY(policy_id,policy_revision) REFERENCES b06_egress_versions(id,revision),
    FOREIGN KEY(target_id,target_revision) REFERENCES b06_egress_versions(id,revision)
);
