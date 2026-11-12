package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func testID(kind, suffix string) domain.ID { return domain.ID(kind + "_" + strings.Repeat(suffix, 32)) }
func foundation(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
func execSQL(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func rejectSQL(t *testing.T, s *Store, query string, args ...any) {
	t.Helper()
	if _, err := s.db.Exec(query, args...); err == nil {
		t.Fatalf("constraint accepted %s", query)
	}
}
func fixtureNode(t *testing.T, s *Store, id domain.ID) {
	t.Helper()
	err := dbgen.New(s.db).CreateNode(context.Background(), dbgen.CreateNodeParams{ID: string(id), Name: "Test", ConnectionMode: "direct", Role: "node", ControlAddress: "controller.example.invalid:9443", UpdatedAt: "2026-11-12T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
}
func assertCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	var p *domain.Problem
	if !errors.As(err, &p) || p.Code != code {
		t.Fatalf("got %v, want %s", err, code)
	}
}

func TestUpgrade001PreservesInstanceAndData(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(ctx, db, fstest.MapFS{"migrations/001_initial.sql": {Data: body}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO app_metadata(key,value) VALUES ('instance_id','legacy-instance'),('preserve','yes')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	s, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.InstanceID != "legacy-instance" {
		t.Fatal("instance changed on upgrade")
	}
	var value string
	if err := s.db.QueryRow(`SELECT value FROM app_metadata WHERE key='preserve'`).Scan(&value); err != nil || value != "yes" {
		t.Fatal("existing data lost", err)
	}
	if err := s.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := s.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign key violation")
	}
}
func TestRevisionAndTaskLedger(t *testing.T) {
	ctx := context.Background()
	s := foundation(t)
	node := testID("node", "1")
	other := testID("node", "2")
	fixtureNode(t, s, node)
	fixtureNode(t, s, other)
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	if got, err := s.RenameNode(ctx, node, 1, "Renamed", now); err != nil || got != 2 {
		t.Fatal(got, err)
	}
	_, err := s.RenameNode(ctx, node, 1, "Stale", now)
	assertCode(t, err, domain.RevisionConflict)
	_, err = s.RenameNode(ctx, testID("node", "3"), 1, "Missing", now)
	assertCode(t, err, domain.NotFound)
	hash := strings.Repeat("a", 64)
	request := TaskRequest{Kind: domain.Publish, Subject: domain.RevisionRef{ID: testID("route", "1"), Revision: 2}, Deadline: now.Add(time.Minute), Targets: []TaskTarget{{NodeID: node, Revision: 2, Digest: hash, RevocationEpoch: 1, DependsOn: []domain.ID{other}}, {NodeID: other, Revision: 3, Digest: hash, RevocationEpoch: 1}}}
	id, err := s.EnqueueTask(ctx, "admin:publish", "key", request, now)
	if err != nil {
		t.Fatal(err)
	}
	// Reverse input order without changing meaning; replay after deadline still returns original task.
	request.Targets[0], request.Targets[1] = request.Targets[1], request.Targets[0]
	again, err := s.EnqueueTask(ctx, "admin:publish", "key", request, now.Add(time.Hour))
	if err != nil || again != id {
		t.Fatal(again, err)
	}
	request.Subject.Revision = 3
	_, err = s.EnqueueTask(ctx, "admin:publish", "key", request, now)
	assertCode(t, err, domain.IdempotencyConflict)
	request.Subject.Revision = 2
	_, err = s.EnqueueTask(ctx, "admin:publish", "expired", request, now.Add(time.Hour))
	assertCode(t, err, domain.TaskExpired)
	cyclic := request
	cyclic.Targets = append([]TaskTarget(nil), request.Targets...)
	cyclic.Targets[0].DependsOn = []domain.ID{node}
	_, err = s.EnqueueTask(ctx, "admin:publish", "cycle", cyclic, now)
	assertCode(t, err, domain.InvalidArgument)
	missing := request
	missing.Targets = []TaskTarget{{NodeID: testID("node", "f"), Revision: 1, Digest: hash, RevocationEpoch: 1}}
	if _, err = s.EnqueueTask(ctx, "admin:publish", "missing-node", missing, now); err == nil {
		t.Fatal("missing FK accepted")
	}
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM tasks`).Scan(&count); err != nil || count != 1 {
		t.Fatal("partial task persisted", count, err)
	}
	result := control.Result{TaskID: id, NodeResult: domain.NodeResult{NodeID: node, TargetRevision: 2, TargetDigest: hash, Applied: true, Verification: domain.Verified, Rollback: "none", Cleanup: "none", ConfirmedAt: &now}, Sequence: 1}
	if changed, err := s.RecordResult(ctx, node, result); err != nil || !changed {
		t.Fatal(changed, err)
	}
	if changed, err := s.RecordResult(ctx, node, result); err != nil || changed {
		t.Fatal("replay changed result", err)
	}
	result.Applied = false
	result.Verification = domain.Failed
	_, err = s.RecordResult(ctx, node, result)
	assertCode(t, err, domain.IdempotencyConflict)
	result.TargetRevision = 1
	_, err = s.RecordResult(ctx, node, result)
	assertCode(t, err, domain.RevisionConflict)
	result.TargetRevision = 2
	_, err = s.RecordResult(ctx, other, result)
	assertCode(t, err, domain.InvalidArgument)
	// Storing evidence must never advance the task's release state by itself.
	task, err := dbgen.New(s.db).GetTask(ctx, string(id))
	if err != nil || task.State != "queued" {
		t.Fatal(task, err)
	}
}
func TestRelationsIsolationAndConfirmedCleanup(t *testing.T) {
	s := foundation(t)
	node := testID("node", "1")
	fixtureNode(t, s, node)
	hash := strings.Repeat("a", 64)
	execSQL(t, s, `INSERT INTO credentials(id,purpose,key_id,ciphertext,created_at) VALUES ('cred1','proxy_entry','test',X'01','now'),('cred2','proxy_entry','test',X'02','now')`)
	execSQL(t, s, `INSERT INTO node_dns_revisions(node_id,revision,outbound,digest) VALUES (?,1,'DIRECT',?)`, node, hash)
	execSQL(t, s, `INSERT INTO listeners(id,node_id,name,revision,protocol,listen_address,listen_port,outer_transport,credential_ref,config_digest,dns_revision,updated_at) VALUES ('listener1',?,'Entry',1,'ss2022','127.0.0.1',22001,'tcp','cred1',?,1,'now')`, node, hash)
	rejectSQL(t, s, `DELETE FROM credentials WHERE id='cred1'`)
	execSQL(t, s, `INSERT INTO routes(id,name,revision,updated_at) VALUES ('route1','Route',1,'now')`)
	execSQL(t, s, `INSERT INTO route_revisions(route_id,revision,entry_listener_id,terminal_node_id,digest) VALUES ('route1',1,'listener1',?,?)`, node, hash)
	rejectSQL(t, s, `UPDATE routes SET published_revision=99 WHERE id='route1'`)
	execSQL(t, s, `INSERT INTO rule_profiles(id,name,revision,updated_at) VALUES ('profile1','Default',1,'now')`)
	execSQL(t, s, `INSERT INTO rule_profile_revisions(profile_id,revision,fallback_target,source_text) VALUES ('profile1',1,'REJECT','')`)
	execSQL(t, s, `INSERT INTO subscriptions(id,name,revision,profile_id,profile_revision,expires_at,updated_at) VALUES ('sub1','Laptop',1,'profile1',1,'later','now'),('sub2','Phone',1,'profile1',1,'later','now')`)
	execSQL(t, s, `INSERT INTO subscription_entries(id,subscription_id,route_id,position,listener_id,expires_at) VALUES ('entry1','sub1','route1',0,'listener1','later')`)
	rejectSQL(t, s, `INSERT INTO subscription_entries(id,subscription_id,route_id,position,listener_id,expires_at) VALUES ('entry2','sub2','route1',0,'listener1','later')`)
	execSQL(t, s, `INSERT INTO entry_identities(id,entry_id,generation,credential_ref,expires_at,revoked_at) VALUES ('identity1','entry1',1,'cred1','earlier','now')`)
	rejectSQL(t, s, `UPDATE entry_identities SET revoked_at=NULL WHERE id='identity1'`)
	// A fresh identity generation preserves the old tombstone and the stable entry association.
	execSQL(t, s, `INSERT INTO entry_identities(id,entry_id,generation,credential_ref,expires_at) VALUES ('identity2','entry1',2,'cred2','later')`)
	rejectSQL(t, s, `INSERT INTO entry_identities(id,entry_id,generation,credential_ref,expires_at) VALUES ('identity3','entry1',3,'cred2','later')`)
	execSQL(t, s, `UPDATE subscription_entries SET revoked_at='now' WHERE id='entry1'`)
	rejectSQL(t, s, `UPDATE subscription_entries SET revoked_at=NULL WHERE id='entry1'`)
	execSQL(t, s, `INSERT INTO listener_references(id,listener_id,entry_id) VALUES ('ref1','listener1','entry1')`)
	execSQL(t, s, `INSERT INTO port_reservations(id,node_id,listener_id,purpose,address,transport,port,state) VALUES ('port1',?,'listener1','listener','127.0.0.1','tcp',22001,'pending_cleanup')`, node)
	rejectSQL(t, s, `INSERT INTO port_reservations(id,node_id,purpose,address,transport,port) VALUES ('port2',?,'probe','127.0.0.1','tcp',22001)`, node)
	rejectSQL(t, s, `UPDATE port_reservations SET state='released',cleanup_confirmed_at='now' WHERE id='port1'`)
	q := dbgen.New(s.db)
	ctx := context.Background()
	for i, want := range []int64{1, 0} {
		n, err := q.ReleaseListenerReference(ctx, dbgen.ReleaseListenerReferenceParams{ID: "ref1", ReleasedAt: sql.NullString{String: "now", Valid: true}})
		if err != nil || n != want {
			t.Fatal(i, n, err)
		}
	}
	if n, err := q.CountListenerReferences(ctx, "listener1"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	release := dbgen.ReleaseListenerPortParams{ID: "port1", CleanupConfirmedAt: sql.NullString{String: "now", Valid: true}}
	if n, err := q.ReleaseListenerPort(ctx, release); err != nil || n != 0 {
		t.Fatal("released before node confirmation", n, err)
	}
	execSQL(t, s, `UPDATE listeners SET cleanup_state='confirmed' WHERE id='listener1'`)
	for _, want := range []int64{1, 0} {
		n, err := q.ReleaseListenerPort(ctx, release)
		if err != nil || n != want {
			t.Fatal(n, err)
		}
	}
	// Port reuse becomes legal only after the confirmed release transaction.
	execSQL(t, s, `INSERT INTO port_reservations(id,node_id,purpose,address,transport,port) VALUES ('port2',?,'probe','127.0.0.1','tcp',22001)`, node)
	var success, failure int
	if err := s.db.QueryRow(`SELECT success_cache_seconds,failure_cache_seconds FROM settings`).Scan(&success, &failure); err != nil || success != 345600 || failure != 300 {
		t.Fatal(success, failure, err)
	}
}
