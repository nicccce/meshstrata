-- I03-C; 001-010 remain byte-for-byte frozen. Only reviewed desired state.
CREATE TABLE i03c_preparations (
 node_id TEXT NOT NULL REFERENCES nodes(id),
 revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
 digest TEXT NOT NULL CHECK(length(digest)=64),
 dns BLOB NOT NULL,
 sockets BLOB NOT NULL,
 PRIMARY KEY(node_id,revision)
);
CREATE TABLE i03c_mutations (
 operation TEXT NOT NULL,
 idem_key TEXT NOT NULL,
 digest TEXT NOT NULL CHECK(length(digest)=64),
 result BLOB NOT NULL,
 PRIMARY KEY(operation,idem_key)
);
CREATE TABLE i03c_archives (
 kind TEXT NOT NULL CHECK(kind IN ('route','target','policy','profile','domain_set')),
 id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision>0),
 archived_at TEXT NOT NULL,
 PRIMARY KEY(kind,id)
);

-- Live dependencies include fixed historical Pins retained by subscriptions.
-- A new catalog head must not hide references to an older reviewed revision.
CREATE VIEW i03c_live_route_versions AS
SELECT v.route_id,v.revision,v.egress_id,v.egress_revision
FROM b06_route_versions v JOIN routes r ON r.id=v.route_id
LEFT JOIN b06_route_heads h ON h.route_id=v.route_id
WHERE (NOT EXISTS(SELECT 1 FROM i03c_archives a WHERE a.kind='route' AND a.id=r.id)
 AND (v.revision=r.revision OR v.revision=h.revision))
 OR EXISTS(SELECT 1 FROM b08_intents i WHERE i.route_id=v.route_id AND i.route_revision=v.revision
 AND (i.revoked_at IS NULL OR i.cleanup_state!='confirmed'));
CREATE VIEW i03c_live_policy_versions AS
SELECT id,revision FROM b06_egress_objects r WHERE kind='policy'
 AND NOT EXISTS(SELECT 1 FROM i03c_archives a WHERE a.kind='policy' AND a.id=r.id)
UNION SELECT egress_id,egress_revision FROM i03c_live_route_versions;
CREATE VIEW i03c_live_profile_versions AS
SELECT id,revision FROM rule_profiles r
WHERE NOT EXISTS(SELECT 1 FROM i03c_archives a WHERE a.kind='profile' AND a.id=r.id)
UNION SELECT v.profile_id,v.profile_revision FROM b08_revisions v
JOIN b08_heads h ON h.subscription_id=v.subscription_id
JOIN subscriptions s ON s.id=v.subscription_id
WHERE s.revoked_at IS NULL AND (v.revision=h.desired_revision OR v.revision=h.confirmed_revision);

-- A sealed but unaccepted plan may lose the node CAS during reconnection.
-- Keep every encrypted candidate and fence late submitters in the task transaction.
ALTER TABLE b06_entry_plans ADD COLUMN plan_digest TEXT NOT NULL DEFAULT '' CHECK(plan_digest='' OR length(plan_digest)=64);
CREATE TABLE i03c_entry_plan_history (
 kind TEXT NOT NULL,
 idem_key TEXT NOT NULL,
 digest TEXT NOT NULL CHECK(length(digest)=64),
 secret_ref TEXT NOT NULL,
 key_id TEXT NOT NULL,
 ciphertext BLOB NOT NULL CHECK(length(ciphertext)>0),
 PRIMARY KEY(kind,idem_key,digest),
 FOREIGN KEY(kind,idem_key) REFERENCES b06_entry_plans(kind,idem_key)
);
CREATE TRIGGER i03c_entry_plan_history_immutable_update BEFORE UPDATE ON i03c_entry_plan_history
BEGIN SELECT RAISE(ABORT,'immutable entry plan history'); END;
CREATE TRIGGER i03c_entry_plan_history_immutable_delete BEFORE DELETE ON i03c_entry_plan_history
BEGIN SELECT RAISE(ABORT,'immutable entry plan history'); END;
