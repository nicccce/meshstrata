package storage

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

func TestI03CUpgradePreservesPopulatedLedgerAndRollsBack(t *testing.T) {
	ctx := context.Background()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, e := db.ExecContext(ctx, query, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec("PRAGMA foreign_keys=ON")
	list, e := listMigrations(migrations)
	if e != nil {
		t.Fatal(e)
	}
	old, all, broken := fstest.MapFS{}, fstest.MapFS{}, fstest.MapFS{}
	for _, m := range list {
		data, e := migrations.ReadFile(m.name)
		if e != nil {
			t.Fatal(e)
		}
		all[m.name] = &fstest.MapFile{Data: data}
		broken[m.name] = all[m.name]
		if m.version < 11 {
			old[m.name] = all[m.name]
		}
		if m.version == 11 {
			broken[m.name] = &fstest.MapFile{Data: append(append([]byte{}, data...), []byte("\nINVALID I03C SQL;")...)}
		}
	}
	if e := migrate(ctx, db, old); e != nil {
		t.Fatal(e)
	}
	hash := strings.Repeat("a", 64)
	exec("INSERT INTO app_metadata(key,value) VALUES ('instance_id','preserved-i03c')")
	exec("INSERT INTO credentials(id,purpose,key_id,ciphertext,created_at) VALUES ('ca','management_ca','old-key',x'004142ff','now'),('proxy','proxy_entry','old-key',x'0099ff','now')")
	exec("INSERT INTO b02_management_ca(singleton,certificate,credential_ref) VALUES (1,x'0102ff','ca')")
	exec("INSERT INTO nodes(id,name,connection_mode,role,control_address,updated_at) VALUES ('node','prior','direct','node','localhost','now')")
	exec("INSERT INTO b02_node_certificates(fingerprint,node_id,certificate,csr_hash,not_after) VALUES (?,'node',x'abcdef',?,'later')", hash, hash)
	exec("INSERT INTO rule_profiles(id,name,revision,updated_at) VALUES ('profile','prior',1,'now')")
	exec("INSERT INTO rule_profile_revisions(profile_id,revision,fallback_target,source_text) VALUES ('profile',1,'REJECT','MATCH,REJECT')")
	exec("INSERT INTO subscriptions(id,name,revision,profile_id,profile_revision,expires_at,download_token_hash,token_version,updated_at) VALUES ('sub','prior',1,'profile',1,'later',?,1,'now')", make([]byte, 32))
	exec("INSERT INTO subscription_download_credentials(id,subscription_id,token_version,key_id,ciphertext) VALUES ('download','sub',1,'old-key',x'ffff00')")
	exec("UPDATE subscriptions SET download_token_credential_ref='download' WHERE id='sub'")
	for _, version := range []int{1, 2} {
		task := fmt.Sprint("task", version)
		identity := fmt.Sprint("identity", version)
		exec("INSERT INTO tasks(id,kind,subject_id,target_revision,request_digest,idempotency_scope,idempotency_key,state,deadline,created_at) VALUES (?,'cleanup','route',1,?,'prior',?,'failed','later','now')", task, hash, task)
		exec("INSERT INTO task_nodes(task_id,node_id,target_revision,target_digest,revocation_epoch) VALUES (?,'node',?,?,2)", task, version+1, hash)
		exec("INSERT INTO b04_targets(task_id,node_id,phase) VALUES (?,'node','failed')", task)
		exec("INSERT INTO b04_artifacts(id,task_id,node_id,target_revision,target_digest,key_id,ciphertext,format) VALUES (?,?,'node',?,?,'old-key',x'000102ff',?)", "artifact"+task, task, version+1, hash, version)
		exec("INSERT INTO b04_grants(identity_id,entry_id,listener_id,node_id,generation,expires_at,grant_digest,origin_task_id,restricted_at) VALUES (?,?,?,'node',1,'later',?,?,'now')", identity, "entry"+identity, "listener"+identity, hash, task)
		if version == 2 {
			exec("INSERT INTO i03_v2_tasks(task_id,subject_digest) VALUES (?,?)", task, hash)
		}
	}
	// Compare every prior table, including encrypted CA/token/artifact bytes and
	// failed task / restricted identity history, after rollback and the upgrade.
	snapshot := func() map[string]string {
		t.Helper()
		rows, e := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' AND name!='schema_migrations' AND name NOT LIKE 'i03c_%' ORDER BY name")
		if e != nil {
			t.Fatal(e)
		}
		names := []string{}
		for rows.Next() {
			var n string
			rows.Scan(&n)
			names = append(names, n)
		}
		rows.Close()
		out := map[string]string{}
		for _, n := range names {
			r, e := db.Query(`SELECT * FROM "` + n + `" ORDER BY rowid`)
			if e != nil {
				t.Fatal(e)
			}
			columns, _ := r.Columns()
			var values []string
			for r.Next() {
				v := make([]any, len(columns))
				ptr := make([]any, len(v))
				for i := range v {
					ptr[i] = &v[i]
				}
				if e := r.Scan(ptr...); e != nil {
					t.Fatal(e)
				}
				values = append(values, fmt.Sprintf("%#v", v))
			}
			if e := r.Err(); e != nil {
				t.Fatal(e)
			}
			r.Close()
			out[n] = strings.Join(values, "\n")
		}
		return out
	}
	before := snapshot()
	if e := migrate(ctx, db, broken); e == nil {
		t.Fatal("broken 011 committed")
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("rollback changed prior data")
	}
	var count int
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name LIKE 'i03c_%'").Scan(&count)
	if count != 0 {
		t.Fatal("partial 011 schema remains")
	}
	if e := migrate(ctx, db, all); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(before, snapshot()) {
		t.Fatal("011 changed prior ledger")
	}
	db.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&count)
	if count != 0 {
		t.Fatal("foreign key damage")
	}
	db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count)
	if count != 11 {
		t.Fatal("011 missing")
	}
	if _, e := db.Exec("UPDATE b04_grants SET generation=2 WHERE identity_id='identity1'"); e == nil {
		t.Fatal("immutable grant guard lost")
	}
	if e := migrate(ctx, db, all); e != nil {
		t.Fatal("restart migration", e)
	}
	// A subscription can retain revision 1 after the profile catalog moves to 2.
	// Its domain-set reference must remain visible to archive checks.
	exec("INSERT INTO domain_sets(id,identifier,name,revision,updated_at) VALUES ('set','set','set',1,'now')")
	exec("INSERT INTO domain_set_revisions(domain_set_id,revision,target_group) VALUES ('set',1,'DIRECT')")
	exec("INSERT INTO b07_domain_pins(domain_set_id,revision,digest) VALUES ('set',1,?)", hash)
	exec("INSERT INTO b07_profile_pins(profile_id,revision,digest,match_line,match_column) VALUES ('profile',1,?,1,1)", hash)
	exec("INSERT INTO rule_domain_sets(profile_id,profile_revision,domain_set_id,domain_set_revision) VALUES ('profile',1,'set',1)")
	exec("INSERT INTO b07_profile_set_pins(profile_id,profile_revision,domain_set_id,domain_set_revision,digest,position) VALUES ('profile',1,'set',1,?,0)", hash)
	exec("INSERT INTO b08_revisions(subscription_id,revision,digest,name,expires_at,profile_id,profile_revision,profile_digest,preferences_json) VALUES ('sub',1,?,'sub','later','profile',1,?,'{}')", hash, hash)
	exec("INSERT INTO b08_heads(subscription_id,desired_revision,confirmed_revision) VALUES ('sub',1,1)")
	exec("INSERT INTO rule_profile_revisions(profile_id,revision,fallback_target,source_text) VALUES ('profile',2,'REJECT','MATCH,REJECT')")
	exec("INSERT INTO b07_profile_pins(profile_id,revision,digest,match_line,match_column) VALUES ('profile',2,?,1,1)", hash)
	exec("UPDATE rule_profiles SET revision=2 WHERE id='profile'")
	if e := db.QueryRow("SELECT count(*) FROM b07_profile_set_pins p JOIN i03c_live_profile_versions v ON v.id=p.profile_id AND v.revision=p.profile_revision WHERE p.domain_set_id='set'").Scan(&count); e != nil || count != 1 {
		t.Fatal("historical live dependency disappeared", count, e)
	}
}
