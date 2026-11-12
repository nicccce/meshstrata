package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestI01UpgradeEachMergedMigrationAndAtomicFailure(t *testing.T) {
	ctx := context.Background()
	db, e := sql.Open("sqlite", filepath.Join(t.TempDir(), "upgrade.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.Exec("PRAGMA foreign_keys=ON")
	list, e := listMigrations(migrations)
	if e != nil {
		t.Fatal(e)
	}
	source := fstest.MapFS{}
	for i, m := range list {
		body, _ := migrations.ReadFile(m.name)
		source[m.name] = &fstest.MapFile{Data: body}
		if i < 2 {
			if e = migrate(ctx, db, source); e != nil {
				t.Fatal(e)
			}
			continue
		}
		if i == 2 {
			if _, e = db.Exec(`INSERT INTO app_metadata(key,value) VALUES('instance_id','i01-existing'); INSERT INTO nodes(id,name,connection_mode,role,control_address,updated_at) VALUES('node_11111111111111111111111111111111','Existing','direct','node','localhost:9443','2026-11-12T00:00:00Z')`); e != nil {
				t.Fatal(e)
			}
		}
		// Exercise each real pending migration then force failure. All of that
		// migration, including its version marker, must roll back.
		broken := fstest.MapFS{}
		for k, v := range source {
			broken[k] = v
		}
		broken[m.name] = &fstest.MapFile{Data: append(append([]byte{}, body...), []byte("\nINVALID SQL;")...)}
		if e = migrate(ctx, db, broken); e == nil {
			t.Fatal("broken migration committed")
		}
		var count int
		db.QueryRow("SELECT count(*) FROM schema_migrations").Scan(&count)
		if count != i {
			t.Fatal("partial migration", count, i)
		}
		if e = migrate(ctx, db, source); e != nil {
			t.Fatal(m.name, e)
		}
		var id, name string
		if e = db.QueryRow("SELECT value FROM app_metadata WHERE key='instance_id'").Scan(&id); e != nil || id != "i01-existing" {
			t.Fatal("instance changed", e)
		}
		if e = db.QueryRow("SELECT name FROM nodes").Scan(&name); e != nil || name != "Existing" {
			t.Fatal("data lost", e)
		}
	}
	// Combined foreign-key and permanent bootstrap/revocation constraints.
	if _, e = db.Exec("UPDATE b01_auth_state SET initialized=1"); e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec("UPDATE b01_auth_state SET initialized=0"); e == nil {
		t.Fatal("bootstrap reset")
	}
	if _, e = db.Exec("INSERT INTO b02_management_ca VALUES(1,x'01','missing')"); e == nil {
		t.Fatal("orphan CA accepted")
	}
	var count int
	db.QueryRow("SELECT count(*) FROM pragma_foreign_key_check").Scan(&count)
	if count != 0 {
		t.Fatal("foreign key check failed")
	}
}
