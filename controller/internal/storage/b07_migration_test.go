package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestB07UpgradePreserves002ProfileAndReference(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "b07.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	run := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(q string, want int) {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil || n != want {
			t.Fatalf("count %d, want %d: %v", n, want, err)
		}
	}
	run("PRAGMA foreign_keys = ON")
	history, err := listMigrations(migrations)
	if err != nil {
		t.Fatal(err)
	}
	one, next := fstest.MapFS{}, fstest.MapFS{}
	for _, m := range history {
		if m.version > 9 {
			break
		}
		raw, err := migrations.ReadFile(m.name)
		if err != nil {
			t.Fatal(err)
		}
		next[m.name] = &fstest.MapFile{Data: raw}
		if m.version <= 7 {
			one[m.name] = next[m.name]
		}
	}
	if err := migrate(ctx, db, one); err != nil {
		t.Fatal(err)
	}
	run(`INSERT INTO domain_sets(id,identifier,name,revision,updated_at) VALUES ('s','original','Old',1,'now');
		INSERT INTO domain_set_revisions(domain_set_id,revision,target_group) VALUES ('s',1,'REJECT');
		INSERT INTO domain_set_members(domain_set_id,revision,domain) VALUES ('s',1,'legacy.example');
		INSERT INTO rule_profiles(id,name,revision,updated_at) VALUES ('p','Old',1,'now');
		INSERT INTO rule_profile_revisions(profile_id,revision,fallback_target,source_text) VALUES ('p',1,'REJECT','MATCH,REJECT');
		INSERT INTO rule_lines(profile_id,revision,position,type,value,target) VALUES ('p',1,0,'RULE-SET','s','REJECT');
		INSERT INTO rule_domain_sets(profile_id,profile_revision,domain_set_id,domain_set_revision) VALUES ('p',1,'s',1);
		INSERT INTO subscriptions(id,name,revision,profile_id,profile_revision,expires_at,updated_at) VALUES ('sub','Old',1,'p',1,'later','now');`)
	if err := migrate(ctx, db, next); err != nil {
		t.Fatal(err)
	}
	count("SELECT count(*) FROM schema_migrations", 9)
	count("SELECT count(*) FROM pragma_foreign_key_check", 0)
	count("SELECT count(*) FROM domain_set_members WHERE domain='legacy.example' AND position IS NULL", 1)
	count("SELECT count(*) FROM rule_lines WHERE profile_id='p' AND source_line IS NULL", 1)
	count("SELECT count(*) FROM subscriptions s JOIN rule_profile_revisions p USING (profile_id) WHERE s.profile_revision=p.revision", 1)
	count("SELECT count(*) FROM b07_profile_pins", 0)

	run("INSERT INTO domain_set_revisions(domain_set_id,revision,target_group) VALUES ('s',2,'')")
	run("INSERT INTO rule_profile_revisions(profile_id,revision,fallback_target,source_text) VALUES ('p',2,'REJECT','MATCH,REJECT')")
	digest := strings.Repeat("a", 64)
	run("INSERT INTO b07_domain_pins VALUES ('s',1,?)", digest)
	run("INSERT INTO b07_domain_pins VALUES ('s',2,?)", digest)
	run("INSERT INTO b07_profile_pins VALUES ('p',2,?,1,1)", digest)
	run("INSERT INTO rule_domain_sets VALUES ('p',2,'s',2)")
	if _, err := db.ExecContext(ctx, "INSERT INTO b07_profile_set_pins VALUES ('p',2,'s',2,?,0)", strings.Repeat("f", 64)); err == nil {
		t.Fatal("bad digest accepted by FK")
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO b07_profile_set_pins VALUES ('p',2,'s',1,?,0)", digest); err == nil {
		t.Fatal("pinned revision diverged from the referenced 002 revision")
	}
	run("INSERT INTO b07_profile_set_pins VALUES ('p',2,'s',2,?,0)", digest)
	if _, err := db.ExecContext(ctx, "DELETE FROM b07_domain_pins WHERE domain_set_id='s' AND revision=2"); err == nil {
		t.Fatal("referenced version deleted")
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM rule_profile_revisions WHERE profile_id='p' AND revision=1"); err == nil {
		t.Fatal("002 relationship disappeared")
	}
	count("SELECT count(*) FROM pragma_foreign_key_check", 0)
}
