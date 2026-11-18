package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestB06UpgradePreservesLegacyRouteCredentialAndCA(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "b06.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	count := func(query string, want int, args ...any) {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil || n != want {
			t.Fatalf("count=%d want=%d err=%v", n, want, err)
		}
	}
	exec("PRAGMA foreign_keys=ON")
	files, err := listMigrations(migrations)
	if err != nil {
		t.Fatal(err)
	}
	old, up := fstest.MapFS{}, fstest.MapFS{}
	var migration8 string
	for _, f := range files {
		if f.version > 8 {
			break
		}
		b, err := migrations.ReadFile(f.name)
		if err != nil {
			t.Fatal(err)
		}
		up[f.name] = &fstest.MapFile{Data: b}
		if f.version <= 7 {
			old[f.name] = up[f.name]
		} else {
			migration8 = f.name
		}
	}
	if migration8 == "" {
		t.Fatal("008 not installed in the isolated copy")
	}
	if err = migrate(ctx, db, old); err != nil {
		t.Fatal(err)
	}
	const node = "node_00000000000000000000000000000001"
	hash := strings.Repeat("a", 64)
	exec("INSERT INTO nodes(id,name,connection_mode,role,control_address,updated_at) VALUES (?,'node','direct','node','example.invalid:9443','now')", node)
	exec("INSERT INTO credentials(id,purpose,key_id,ciphertext,created_at) VALUES ('edge','proxy_entry','k',x'AB','now'),('ca','management_ca','k',x'CD','now')")
	exec("INSERT INTO node_dns_revisions(node_id,revision,outbound,digest) VALUES (?,1,'DIRECT',?)", node, hash)
	exec("INSERT INTO listeners(id,node_id,name,revision,protocol,listen_address,listen_port,outer_transport,credential_ref,config_digest,dns_revision,updated_at) VALUES ('listener',?,'legacy',1,'ss2022','0.0.0.0',23000,'tcp','edge',?,1,'now')", node, hash)
	exec("INSERT INTO routes(id,name,revision,updated_at) VALUES ('legacy','legacy',1,'now')")
	exec("INSERT INTO route_revisions(route_id,revision,entry_listener_id,terminal_node_id,digest) VALUES ('legacy',1,'listener',?,?)", node, hash)
	exec("UPDATE routes SET published_revision=1 WHERE id='legacy'")
	// A legacy CA stays attached to the original credentials table.
	exec("INSERT INTO b02_management_ca(singleton,certificate,credential_ref) VALUES (1,x'CA','ca')")
	good := up[migration8]
	invalid := append([]byte{}, good.Data...)
	invalid = append(invalid, []byte("\nINSERT INTO absent_table VALUES(1);")...)
	up[migration8] = &fstest.MapFile{Data: invalid}
	if err = migrate(ctx, db, up); err == nil {
		t.Fatal("broken 008 applied")
	}
	count("SELECT count(*) FROM schema_migrations", 7)
	count("SELECT count(*) FROM sqlite_master WHERE name LIKE 'b06_%'", 0)
	up[migration8] = good
	if err = migrate(ctx, db, up); err != nil {
		t.Fatal(err)
	}
	count("SELECT count(*) FROM schema_migrations", 8)
	count("SELECT count(*) FROM pragma_foreign_key_check", 0)
	count(`SELECT count(*) FROM routes r JOIN route_revisions rv ON rv.route_id=r.id AND rv.revision=r.published_revision
 JOIN listeners l ON l.id=rv.entry_listener_id JOIN credentials c ON c.id=l.credential_ref
 WHERE r.id='legacy' AND hex(c.ciphertext)='AB'`, 1)
	count("SELECT count(*) FROM b02_management_ca s JOIN credentials c ON c.id=s.credential_ref WHERE c.id='ca' AND hex(c.ciphertext)='CD'", 1)
	count("SELECT count(*) FROM b06_route_heads", 0)
	if _, err = db.Exec("INSERT INTO credentials(id,purpose,key_id,ciphertext,created_at) VALUES ('misplaced','egress_proxy','k',x'00','now')"); err == nil {
		t.Fatal("002 credential allowlist expanded")
	}
	if _, err = db.Exec("INSERT INTO b06_route_versions VALUES ('missing route',1,'bad',?,'policy',1,?,0,0)", hash, hash); err == nil {
		t.Fatal("route FK bypassed")
	}
	if _, err = db.Exec("INSERT INTO b06_egress_credentials VALUES ('bad','missing target',1,'k',x'00')"); err == nil {
		t.Fatal("egress FK bypassed")
	}
}
