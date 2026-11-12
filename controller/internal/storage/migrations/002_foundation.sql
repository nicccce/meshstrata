-- B00 relational foundation. 001 is immutable. UTC timestamps use RFC3339 in services.
-- No plaintext secrets: B01 implements authenticated encryption and external key handling.
CREATE TABLE credentials (
    id TEXT PRIMARY KEY NOT NULL,
    purpose TEXT NOT NULL CHECK (purpose IN ('proxy_entry','internal_proxy','dns_api','location_api','management_ca','tunnel','certificate')),
    key_id TEXT NOT NULL,
    ciphertext BLOB NOT NULL CHECK (length(ciphertext) > 0),
    created_at TEXT NOT NULL,
    revoked_at TEXT
);
CREATE TABLE nodes (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991),
    connection_mode TEXT NOT NULL CHECK (connection_mode IN ('direct','reverse_tunnel')),
    role TEXT NOT NULL CHECK (role IN ('node','both')),
    control_address TEXT NOT NULL,
    proxy_address TEXT,
    observed_source_ip TEXT,
    control_state TEXT NOT NULL DEFAULT 'unknown' CHECK (control_state IN ('unknown','online','offline')),
    tunnel_state TEXT NOT NULL DEFAULT 'unknown' CHECK (tunnel_state IN ('unknown','online','offline')),
    verification TEXT NOT NULL DEFAULT 'not_checked' CHECK (verification IN ('not_checked','verified','failed','incomplete')),
    country TEXT,
    city TEXT,
    manual_location INTEGER NOT NULL DEFAULT 0 CHECK (manual_location IN (0,1)),
    target_dns_revision INTEGER NOT NULL DEFAULT 1 CHECK (target_dns_revision > 0),
    revocation_epoch INTEGER NOT NULL DEFAULT 1 CHECK (revocation_epoch > 0),
    last_confirmed_at TEXT,
    removed_at TEXT,
    updated_at TEXT NOT NULL
);
CREATE TABLE node_protocol_defaults (
    node_id TEXT NOT NULL REFERENCES nodes(id),
    protocol TEXT NOT NULL CHECK (protocol IN ('ss2022','trojan','vless','hysteria2')),
    enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
    listen_address TEXT NOT NULL,
    preferred_port INTEGER CHECK (preferred_port BETWEEN 1 AND 65535),
    outer_transport TEXT NOT NULL CHECK (outer_transport IN ('tcp','udp','tcp_udp')),
    PRIMARY KEY (node_id, protocol)
);
CREATE TABLE node_port_pools (
    node_id TEXT NOT NULL REFERENCES nodes(id),
    protocol TEXT NOT NULL CHECK (protocol IN ('all','ss2022','trojan','vless','hysteria2','relay')),
    start_port INTEGER NOT NULL CHECK (start_port BETWEEN 1 AND 65535),
    end_port INTEGER NOT NULL CHECK (end_port BETWEEN start_port AND 65535),
    PRIMARY KEY (node_id, protocol, start_port)
);
CREATE TABLE node_dns_revisions (
    node_id TEXT NOT NULL REFERENCES nodes(id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    outbound TEXT NOT NULL,
    digest TEXT NOT NULL CHECK (length(digest) = 64),
    PRIMARY KEY (node_id, revision)
);
CREATE TABLE node_dns_upstreams (
    node_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('target','bootstrap')),
    position INTEGER NOT NULL CHECK (position >= 0),
    address TEXT NOT NULL,
    PRIMARY KEY (node_id, revision, kind, position),
    FOREIGN KEY (node_id, revision) REFERENCES node_dns_revisions(node_id, revision)
);
CREATE TABLE egress_targets (
    id TEXT PRIMARY KEY NOT NULL,
    node_id TEXT NOT NULL REFERENCES nodes(id),
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    updated_at TEXT NOT NULL,
    UNIQUE (id, node_id)
);
CREATE TABLE egress_target_revisions (
    target_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    node_id TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('node_default','interface','http','socks5')),
    interface_name TEXT,
    host TEXT,
    port INTEGER CHECK (port BETWEEN 1 AND 65535),
    credential_ref TEXT REFERENCES credentials(id),
    business_udp TEXT NOT NULL DEFAULT 'not_checked' CHECK (business_udp IN ('not_checked','verified','failed','incomplete')),
    PRIMARY KEY (target_id, revision),
    UNIQUE (target_id, revision, node_id),
    FOREIGN KEY (target_id, node_id) REFERENCES egress_targets(id, node_id),
    CHECK ((type IN ('http','socks5') AND host IS NOT NULL AND port IS NOT NULL) OR (type = 'interface' AND interface_name IS NOT NULL) OR type = 'node_default')
);
CREATE TABLE egress_policies (
    id TEXT PRIMARY KEY NOT NULL,
    terminal_node_id TEXT NOT NULL REFERENCES nodes(id),
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    updated_at TEXT NOT NULL,
    UNIQUE (id, terminal_node_id)
);
CREATE TABLE egress_policy_revisions (
    policy_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    node_id TEXT NOT NULL,
    digest TEXT NOT NULL CHECK (length(digest) = 64),
    PRIMARY KEY (policy_id, revision),
    UNIQUE (policy_id, revision, node_id),
    FOREIGN KEY (policy_id, node_id) REFERENCES egress_policies(id, terminal_node_id)
);
CREATE TABLE egress_rules (
    policy_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    node_id TEXT NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    destination TEXT NOT NULL,
    target_id TEXT,
    target_revision INTEGER,
    reject_traffic INTEGER NOT NULL DEFAULT 0 CHECK (reject_traffic IN (0,1)),
    no_resolve INTEGER NOT NULL DEFAULT 0 CHECK (no_resolve IN (0,1)),
    PRIMARY KEY (policy_id, revision, position),
    FOREIGN KEY (policy_id, revision, node_id) REFERENCES egress_policy_revisions(policy_id, revision, node_id),
    FOREIGN KEY (target_id, target_revision, node_id) REFERENCES egress_target_revisions(target_id, revision, node_id),
    CHECK ((reject_traffic = 1 AND target_id IS NULL AND target_revision IS NULL) OR (reject_traffic = 0 AND target_id IS NOT NULL AND target_revision IS NOT NULL))
);
CREATE UNIQUE INDEX egress_single_match ON egress_rules(policy_id, revision) WHERE destination = 'MATCH';
-- B06 validates exactly one final MATCH and CIDRs before a revision becomes publishable.
CREATE TABLE listeners (
    id TEXT PRIMARY KEY NOT NULL,
    node_id TEXT NOT NULL REFERENCES nodes(id),
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    protocol TEXT NOT NULL CHECK (protocol IN ('ss2022','trojan','vless','hysteria2')),
    listen_address TEXT NOT NULL,
    listen_port INTEGER NOT NULL CHECK (listen_port BETWEEN 1 AND 65535),
    outer_transport TEXT NOT NULL CHECK (outer_transport IN ('tcp','udp','tcp_udp')),
    business_udp TEXT NOT NULL DEFAULT 'not_checked' CHECK (business_udp IN ('not_checked','verified','failed','incomplete')),
    credential_ref TEXT NOT NULL REFERENCES credentials(id),
    config_digest TEXT NOT NULL CHECK (length(config_digest) = 64),
    dns_revision INTEGER NOT NULL,
    cleanup_state TEXT NOT NULL DEFAULT 'none' CHECK (cleanup_state IN ('none','pending','confirmed')),
    updated_at TEXT NOT NULL,
    UNIQUE (id, node_id),
    FOREIGN KEY (node_id, dns_revision) REFERENCES node_dns_revisions(node_id, revision)
);
-- A tcp_udp listener reserves TWO rows. Wildcard/dual-stack conflicts additionally require B04 runtime checks.
CREATE TABLE port_reservations (
    id TEXT PRIMARY KEY NOT NULL,
    node_id TEXT NOT NULL REFERENCES nodes(id),
    listener_id TEXT,
    purpose TEXT NOT NULL CHECK (purpose IN ('listener','relay','probe')),
    address TEXT NOT NULL,
    transport TEXT NOT NULL CHECK (transport IN ('tcp','udp')),
    port INTEGER NOT NULL CHECK (port BETWEEN 1 AND 65535),
    state TEXT NOT NULL DEFAULT 'reserved' CHECK (state IN ('reserved','pending_cleanup','released')),
    cleanup_confirmed_at TEXT,
    FOREIGN KEY (listener_id, node_id) REFERENCES listeners(id, node_id),
    CHECK (purpose != 'listener' OR listener_id IS NOT NULL),
    CHECK (state != 'released' OR cleanup_confirmed_at IS NOT NULL)
);
CREATE UNIQUE INDEX port_allocation ON port_reservations(node_id, address, transport, port) WHERE state != 'released';
CREATE TABLE reverse_tunnels (
    id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    relay_node_id TEXT NOT NULL REFERENCES nodes(id),
    remote_node_id TEXT NOT NULL REFERENCES nodes(id),
    target_listener_id TEXT NOT NULL,
    mapping_reservation_id TEXT NOT NULL REFERENCES port_reservations(id),
    relay_endpoint TEXT NOT NULL,
    credential_ref TEXT NOT NULL REFERENCES credentials(id),
    transport TEXT NOT NULL DEFAULT 'tcp_tls' CHECK (transport = 'tcp_tls'),
    relay_confirmed_at TEXT,
    remote_confirmed_at TEXT,
    PRIMARY KEY (id, revision),
    FOREIGN KEY (target_listener_id, remote_node_id) REFERENCES listeners(id, node_id),
    CHECK (relay_node_id != remote_node_id)
);
CREATE TABLE routes (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    published_revision INTEGER,
    state TEXT NOT NULL DEFAULT 'draft' CHECK (state IN ('draft','validating','applying','verifying','published','failed','rolling_back','rolled_back','rollback_pending','rollback_failed')),
    updated_at TEXT NOT NULL,
    FOREIGN KEY (id, published_revision) REFERENCES route_revisions(route_id, revision) DEFERRABLE INITIALLY DEFERRED
);
CREATE TABLE route_revisions (
    route_id TEXT NOT NULL REFERENCES routes(id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    entry_listener_id TEXT NOT NULL REFERENCES listeners(id),
    terminal_node_id TEXT NOT NULL REFERENCES nodes(id),
    policy_id TEXT,
    policy_revision INTEGER,
    business_udp INTEGER NOT NULL DEFAULT 0 CHECK (business_udp IN (0,1)),
    digest TEXT NOT NULL CHECK (length(digest) = 64),
    PRIMARY KEY (route_id, revision),
    FOREIGN KEY (policy_id, policy_revision, terminal_node_id) REFERENCES egress_policy_revisions(policy_id, revision, node_id),
    CHECK ((policy_id IS NULL) = (policy_revision IS NULL))
);
CREATE TABLE route_hops (
    route_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    node_id TEXT NOT NULL REFERENCES nodes(id),
    listener_id TEXT NOT NULL,
    tunnel_id TEXT,
    tunnel_revision INTEGER,
    PRIMARY KEY (route_id, revision, position),
    UNIQUE (route_id, revision, node_id),
    FOREIGN KEY (route_id, revision) REFERENCES route_revisions(route_id, revision),
    FOREIGN KEY (listener_id, node_id) REFERENCES listeners(id, node_id),
    FOREIGN KEY (tunnel_id, tunnel_revision) REFERENCES reverse_tunnels(id, revision),
    CHECK ((tunnel_id IS NULL) = (tunnel_revision IS NULL))
);
CREATE TABLE domain_sets (
    id TEXT PRIMARY KEY NOT NULL,
    identifier TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    updated_at TEXT NOT NULL
);
CREATE TABLE domain_set_revisions (
    domain_set_id TEXT NOT NULL REFERENCES domain_sets(id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    target_group TEXT NOT NULL,
    PRIMARY KEY (domain_set_id, revision)
);
CREATE TABLE domain_set_members (
    domain_set_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    domain TEXT NOT NULL,
    PRIMARY KEY (domain_set_id, revision, domain),
    FOREIGN KEY (domain_set_id, revision) REFERENCES domain_set_revisions(domain_set_id, revision)
);
CREATE TABLE rule_profiles (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL CHECK (revision > 0),
    updated_at TEXT NOT NULL
);
CREATE TABLE rule_profile_revisions (
    profile_id TEXT NOT NULL REFERENCES rule_profiles(id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    fallback_target TEXT NOT NULL,
    source_text TEXT NOT NULL,
    PRIMARY KEY (profile_id, revision)
);
CREATE TABLE rule_lines (
    profile_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    type TEXT NOT NULL CHECK (type != 'MATCH'),
    value TEXT NOT NULL,
    target TEXT NOT NULL,
    options TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (profile_id, revision, position),
    FOREIGN KEY (profile_id, revision) REFERENCES rule_profile_revisions(profile_id, revision)
);
CREATE TABLE rule_domain_sets (
    profile_id TEXT NOT NULL,
    profile_revision INTEGER NOT NULL,
    domain_set_id TEXT NOT NULL,
    domain_set_revision INTEGER NOT NULL,
    PRIMARY KEY (profile_id, profile_revision, domain_set_id),
    FOREIGN KEY (profile_id, profile_revision) REFERENCES rule_profile_revisions(profile_id, revision),
    FOREIGN KEY (domain_set_id, domain_set_revision) REFERENCES domain_set_revisions(domain_set_id, revision)
);
CREATE TABLE subscriptions (
    id TEXT PRIMARY KEY NOT NULL,
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    profile_id TEXT NOT NULL,
    profile_revision INTEGER NOT NULL,
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    download_token_hash BLOB UNIQUE,
    token_version INTEGER NOT NULL DEFAULT 1 CHECK (token_version > 0),
    all_routes_group INTEGER NOT NULL DEFAULT 1 CHECK (all_routes_group IN (0,1)),
    group_by_exit INTEGER NOT NULL DEFAULT 1 CHECK (group_by_exit IN (0,1)),
    client_mode TEXT NOT NULL DEFAULT 'tun' CHECK (client_mode IN ('tun','system_proxy')),
    updated_at TEXT NOT NULL,
    FOREIGN KEY (profile_id, profile_revision) REFERENCES rule_profile_revisions(profile_id, revision)
);
CREATE TABLE subscription_entries (
    id TEXT PRIMARY KEY NOT NULL,
    subscription_id TEXT NOT NULL REFERENCES subscriptions(id),
    route_id TEXT NOT NULL REFERENCES routes(id),
    position INTEGER NOT NULL CHECK (position >= 0),
    listener_id TEXT NOT NULL REFERENCES listeners(id),
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    revocation_epoch INTEGER NOT NULL DEFAULT 1 CHECK (revocation_epoch > 0),
    cleanup_state TEXT NOT NULL DEFAULT 'none' CHECK (cleanup_state IN ('none','pending','confirmed')),
    UNIQUE (id, subscription_id),
    UNIQUE (subscription_id, route_id),
    UNIQUE (subscription_id, position)
);
-- Stable entry association and replaceable authentication generations are separate.
-- Renewing an already expired identity creates a new generation; old tombstones remain.
CREATE TABLE entry_identities (
    id TEXT PRIMARY KEY NOT NULL,
    entry_id TEXT NOT NULL REFERENCES subscription_entries(id),
    generation INTEGER NOT NULL CHECK (generation BETWEEN 1 AND 9007199254740991),
    credential_ref TEXT NOT NULL UNIQUE REFERENCES credentials(id),
    expires_at TEXT NOT NULL,
    revoked_at TEXT,
    UNIQUE (entry_id, generation)
);
CREATE TRIGGER identity_no_resurrection BEFORE UPDATE ON entry_identities
WHEN OLD.revoked_at IS NOT NULL AND NEW.revoked_at IS NULL
BEGIN SELECT RAISE(ABORT, 'revoked identity cannot be reactivated'); END;
-- Default deny sharing for every entry protocol until B03 proves independent revocation.
CREATE UNIQUE INDEX isolated_entry_listener ON subscription_entries(listener_id);
CREATE TRIGGER entry_no_resurrection BEFORE UPDATE ON subscription_entries
WHEN OLD.revoked_at IS NOT NULL AND (NEW.revoked_at IS NULL OR NEW.revocation_epoch < OLD.revocation_epoch)
BEGIN SELECT RAISE(ABORT, 'revoked entry cannot be reactivated'); END;
CREATE TABLE subscription_groups (
    id TEXT PRIMARY KEY NOT NULL,
    subscription_id TEXT NOT NULL REFERENCES subscriptions(id),
    name TEXT NOT NULL,
    selected_entry_id TEXT NOT NULL,
    UNIQUE (id, subscription_id),
    UNIQUE (subscription_id, name),
    FOREIGN KEY (selected_entry_id, subscription_id) REFERENCES subscription_entries(id, subscription_id)
);
CREATE TABLE subscription_group_members (
    group_id TEXT NOT NULL,
    subscription_id TEXT NOT NULL,
    entry_id TEXT NOT NULL,
    position INTEGER NOT NULL CHECK (position >= 0),
    PRIMARY KEY (group_id, entry_id),
    UNIQUE (group_id, position),
    FOREIGN KEY (group_id, subscription_id) REFERENCES subscription_groups(id, subscription_id),
    FOREIGN KEY (entry_id, subscription_id) REFERENCES subscription_entries(id, subscription_id)
);
CREATE TABLE listener_references (
    id TEXT PRIMARY KEY NOT NULL,
    listener_id TEXT NOT NULL REFERENCES listeners(id),
    route_id TEXT,
    route_revision INTEGER,
    entry_id TEXT REFERENCES subscription_entries(id),
    released_at TEXT,
    FOREIGN KEY (route_id, route_revision) REFERENCES route_revisions(route_id, revision),
    CHECK ((entry_id IS NOT NULL AND route_id IS NULL AND route_revision IS NULL) OR (entry_id IS NULL AND route_id IS NOT NULL AND route_revision IS NOT NULL))
);
CREATE UNIQUE INDEX route_listener_reference ON listener_references(listener_id, route_id, route_revision) WHERE route_id IS NOT NULL;
CREATE UNIQUE INDEX entry_listener_reference ON listener_references(listener_id, entry_id) WHERE entry_id IS NOT NULL;
CREATE VIEW listener_reference_counts AS SELECT listener_id, count(*) AS reference_count FROM listener_references WHERE released_at IS NULL GROUP BY listener_id;
CREATE TRIGGER no_early_listener_port_release BEFORE UPDATE OF state ON port_reservations
WHEN NEW.state = 'released' AND NEW.listener_id IS NOT NULL AND (
    EXISTS (SELECT 1 FROM listener_references WHERE listener_id = NEW.listener_id AND released_at IS NULL)
    OR NOT EXISTS (SELECT 1 FROM listeners WHERE id = NEW.listener_id AND cleanup_state = 'confirmed')
)
BEGIN SELECT RAISE(ABORT, 'listener cleanup not confirmed or still referenced'); END;
CREATE TABLE tunnel_references (
    tunnel_id TEXT NOT NULL,
    tunnel_revision INTEGER NOT NULL,
    route_id TEXT NOT NULL,
    route_revision INTEGER NOT NULL,
    released_at TEXT,
    PRIMARY KEY (tunnel_id, tunnel_revision, route_id, route_revision),
    FOREIGN KEY (tunnel_id, tunnel_revision) REFERENCES reverse_tunnels(id, revision),
    FOREIGN KEY (route_id, route_revision) REFERENCES route_revisions(route_id, revision)
);
CREATE TABLE tasks (
    id TEXT PRIMARY KEY NOT NULL,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision BETWEEN 1 AND 9007199254740991),
    kind TEXT NOT NULL CHECK (kind IN ('publish','rollback','revoke','cleanup','query_status','test_protocol','measure','check_egress','query_usage','read_logs')),
    subject_id TEXT NOT NULL,
    target_revision INTEGER NOT NULL CHECK (target_revision > 0),
    request_digest TEXT NOT NULL CHECK (length(request_digest) = 64),
    idempotency_scope TEXT NOT NULL,
    idempotency_key TEXT NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    state TEXT NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','succeeded','failed','cancelled','timed_out')),
    deadline TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (idempotency_scope, idempotency_key)
);
CREATE TABLE task_nodes (
    task_id TEXT NOT NULL REFERENCES tasks(id),
    node_id TEXT NOT NULL REFERENCES nodes(id),
    target_revision INTEGER NOT NULL CHECK (target_revision > 0),
    target_digest TEXT NOT NULL CHECK (length(target_digest) = 64),
    revocation_epoch INTEGER NOT NULL CHECK (revocation_epoch > 0),
    applied INTEGER NOT NULL DEFAULT 0 CHECK (applied IN (0,1)),
    verification TEXT NOT NULL DEFAULT 'not_checked' CHECK (verification IN ('not_checked','verified','failed','incomplete')),
    rollback_state TEXT NOT NULL DEFAULT 'none' CHECK (rollback_state IN ('none','pending','confirmed','failed')),
    cleanup_state TEXT NOT NULL DEFAULT 'none' CHECK (cleanup_state IN ('none','pending','confirmed')),
    result_sequence INTEGER NOT NULL DEFAULT 0 CHECK (result_sequence >= 0),
    result_digest TEXT,
    error_code TEXT,
    confirmed_at TEXT,
    PRIMARY KEY (task_id, node_id),
    CHECK (verification != 'verified' OR (applied = 1 AND confirmed_at IS NOT NULL AND error_code IS NULL)),
    CHECK (rollback_state != 'confirmed' OR confirmed_at IS NOT NULL),
    CHECK (cleanup_state != 'confirmed' OR confirmed_at IS NOT NULL)
);
CREATE TABLE task_dependencies (
    task_id TEXT NOT NULL,
    node_id TEXT NOT NULL,
    requires_node_id TEXT NOT NULL,
    PRIMARY KEY (task_id, node_id, requires_node_id),
    FOREIGN KEY (task_id, node_id) REFERENCES task_nodes(task_id, node_id),
    FOREIGN KEY (task_id, requires_node_id) REFERENCES task_nodes(task_id, node_id),
    CHECK (node_id != requires_node_id)
);
CREATE TABLE measurements (
    id TEXT PRIMARY KEY NOT NULL,
    cache_key TEXT NOT NULL,
    task_id TEXT REFERENCES tasks(id),
    node_id TEXT REFERENCES nodes(id),
    route_id TEXT REFERENCES routes(id),
    revision INTEGER NOT NULL CHECK (revision > 0),
    endpoint_revision INTEGER NOT NULL CHECK (endpoint_revision > 0),
    scope TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('network_rtt','browser_http','path_cost')),
    mode TEXT NOT NULL CHECK (mode IN ('network_rtt','browser_estimate','partial')),
    method TEXT NOT NULL,
    transport TEXT NOT NULL,
    http_protocol TEXT,
    environment TEXT NOT NULL,
    path_verified INTEGER NOT NULL DEFAULT 0 CHECK (path_verified IN (0,1)),
    value_ms REAL CHECK (value_ms >= 0),
    samples INTEGER NOT NULL CHECK (samples >= 0),
    status TEXT NOT NULL CHECK (status IN ('observed','failed','incomplete','not_checked')),
    started_at TEXT NOT NULL,
    observed_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    CHECK (status != 'failed' OR value_ms IS NULL),
    CHECK (mode != 'network_rtt' OR kind = 'network_rtt')
);
CREATE INDEX measurement_cache ON measurements(cache_key, expires_at);
CREATE TABLE events (
    id TEXT PRIMARY KEY NOT NULL,
    kind TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id TEXT NOT NULL,
    task_id TEXT REFERENCES tasks(id),
    error_code TEXT,
    occurred_at TEXT NOT NULL
);
CREATE INDEX event_history ON events(occurred_at, id);
CREATE TABLE settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    success_cache_seconds INTEGER NOT NULL DEFAULT 345600 CHECK (success_cache_seconds > 0),
    failure_cache_seconds INTEGER NOT NULL DEFAULT 300 CHECK (failure_cache_seconds > 0),
    default_port_start INTEGER NOT NULL DEFAULT 20000 CHECK (default_port_start BETWEEN 1 AND 65535),
    default_port_end INTEGER NOT NULL DEFAULT 29999 CHECK (default_port_end BETWEEN default_port_start AND 65535),
    location_provider TEXT NOT NULL DEFAULT 'ipinfo' CHECK (location_provider = 'ipinfo'),
    location_credential_ref TEXT REFERENCES credentials(id),
    certificate_provider TEXT NOT NULL DEFAULT 'cloudflare' CHECK (certificate_provider IN ('cloudflare','import')),
    dns_credential_ref TEXT REFERENCES credentials(id),
    log_level TEXT NOT NULL DEFAULT 'warn' CHECK (log_level IN ('error','warn','info','debug')),
    log_file_bytes INTEGER NOT NULL DEFAULT 5242880 CHECK (log_file_bytes > 0),
    log_files INTEGER NOT NULL DEFAULT 4 CHECK (log_files > 0),
    log_total_bytes INTEGER NOT NULL DEFAULT 20971520 CHECK (log_total_bytes > 0),
    log_retention_seconds INTEGER NOT NULL DEFAULT 604800 CHECK (log_retention_seconds > 0)
);
INSERT INTO settings(id) VALUES (1);

-- Tombstones and released reservations cannot be overwritten by a replayed snapshot.
CREATE TRIGGER reference_no_resurrection BEFORE UPDATE ON listener_references
WHEN OLD.released_at IS NOT NULL AND NEW.released_at IS NULL
BEGIN SELECT RAISE(ABORT, 'released reference cannot be reactivated'); END;
CREATE TRIGGER reservation_no_resurrection BEFORE UPDATE ON port_reservations
WHEN OLD.state = 'released' AND NEW.state != 'released'
BEGIN SELECT RAISE(ABORT, 'released reservation cannot be reactivated'); END;
CREATE TRIGGER no_insert_released_port BEFORE INSERT ON port_reservations
WHEN NEW.state = 'released'
BEGIN SELECT RAISE(ABORT, 'new reservation must await cleanup'); END;
