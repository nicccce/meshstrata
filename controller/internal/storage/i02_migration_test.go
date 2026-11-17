package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

func TestI02UpgradePreservesStageOneRelationships(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "controller.db"))
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
	exec("PRAGMA foreign_keys=1")
	list, err := listMigrations(migrations)
	if err != nil {
		t.Fatal(err)
	}
	stageOne := fstest.MapFS{}
	stageTwo := fstest.MapFS{}
	for _, migration := range list {
		if migration.version > 7 {
			break
		}
		content, err := migrations.ReadFile(migration.name)
		if err != nil {
			t.Fatal(err)
		}
		stageTwo[migration.name] = &fstest.MapFile{Data: content}
		if migration.version <= 5 {
			stageOne[migration.name] = &fstest.MapFile{Data: content}
		}
	}
	if err := migrate(ctx, db, stageOne); err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("a", 64)
	const node = "node_11111111111111111111111111111111"
	exec("INSERT INTO app_metadata(key,value) VALUES ('instance_id','stage-one')")
	exec("INSERT INTO nodes(id,name,connection_mode,role,control_address,updated_at) VALUES (?,'Stage One','direct','node','localhost:9443','now')", node)
	exec("INSERT INTO b02_node_certificates(fingerprint,node_id,certificate,csr_hash,not_after) VALUES (?,?,x'A10123',?,'later')", hash, node, hash)
	exec("INSERT INTO credentials(id,purpose,key_id,ciphertext,created_at) VALUES ('cred1','proxy_entry','key1',x'B20145','now')")
	exec("INSERT INTO node_dns_revisions(node_id,revision,outbound,digest) VALUES (?,1,'DIRECT',?)", node, hash)
	exec(`INSERT INTO listeners(id,node_id,name,revision,protocol,listen_address,listen_port,
        outer_transport,credential_ref,config_digest,dns_revision,updated_at)
        VALUES ('listener1',?,'Entry',1,'ss2022','127.0.0.1',22001,'tcp','cred1',?,1,'now')`, node, hash)
	exec("INSERT INTO routes(id,name,revision,updated_at) VALUES ('route1','Route',1,'now')")
	exec("INSERT INTO route_revisions(route_id,revision,entry_listener_id,terminal_node_id,digest) VALUES ('route1',1,'listener1',?,?)", node, hash)
	exec("INSERT INTO listener_references(id,listener_id,route_id,route_revision) VALUES ('ref1','listener1','route1',1)")
	exec(`INSERT INTO port_reservations(id,node_id,listener_id,purpose,address,transport,port,state)
       VALUES ('legacy1',?,'listener1','listener','127.0.0.1','tcp',22001,'pending_cleanup')`, node)

	// Break 007 after 006 has executed. Neither migration should take effect.
	broken := fstest.MapFS{}
	for name, entry := range stageTwo {
		broken[name] = entry
	}
	broken["migrations/007_b05.sql"] = &fstest.MapFile{Data: []byte("SELECT unknown_column FROM missing_table;")}
	if err := migrate(ctx, db, broken); err == nil {
		t.Fatal("broken batch committed")
	}
	count := func(sql string, want int) {
		t.Helper()
		var got int
		if err := db.QueryRowContext(ctx, sql).Scan(&got); err != nil || got != want {
			t.Fatalf("count=%d, want %d, err=%v", got, want, err)
		}
	}
	count("SELECT count(*) FROM schema_migrations", 5)
	count("SELECT count(*) FROM sqlite_master WHERE name LIKE 'b04_%'", 0)
	if err := migrate(ctx, db, stageTwo); err != nil {
		t.Fatal(err)
	}
	count("SELECT count(*) FROM schema_migrations", 7)
	count("SELECT count(*) FROM app_metadata WHERE key='instance_id' AND value='stage-one'", 1)
	count("SELECT count(*) FROM pragma_foreign_key_check", 0)
	count(`SELECT count(*) FROM port_reservations p
        JOIN listener_references r ON r.listener_id=p.listener_id
        JOIN listeners l ON l.id=p.listener_id
        JOIN credentials c ON l.credential_ref=c.id
        JOIN b02_node_certificates n ON p.node_id=n.node_id
        WHERE p.state='pending_cleanup' AND r.id='ref1' AND r.released_at IS NULL
        AND c.key_id='key1' AND hex(c.ciphertext)='B20145' AND hex(n.certificate)='A10123'`, 1)

	exec(`INSERT INTO b04_grants(identity_id,entry_id,listener_id,node_id,generation,expires_at,grant_digest)
          VALUES ('identity2','entry2','listener2',?,1,'later',?)`, node, hash)
	exec(`INSERT INTO b04_ports(id,node_id,listener_id,identity_id,address,port,transport,state)
          VALUES ('modern2',?,'listener2','identity2','0.0.0.0',22002,'tcp','pending_cleanup')`, node)
	if _, err := db.Exec(`INSERT INTO port_reservations(id,node_id,purpose,address,transport,port)
         VALUES ('legacy2',?,'probe','127.0.0.1','tcp',22002)`, node); err == nil {
		t.Fatal("legacy writer bypassed a pending wildcard reservation")
	}
	if _, err := db.Exec(`UPDATE b04_ports SET state='released',confirmed_at='now' WHERE id='modern2'`); err == nil {
		t.Fatal("format-1 reservation released without grant confirmation")
	}
}
