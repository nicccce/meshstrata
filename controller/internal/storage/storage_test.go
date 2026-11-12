package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

func TestPersistentInstanceAndPragmas(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	id := store.InstanceID
	if id == "" {
		t.Fatal("empty instance ID")
	}
	for query, want := range map[string]string{"PRAGMA journal_mode": "wal", "PRAGMA foreign_keys": "1", "PRAGMA busy_timeout": "5000"} {
		var got string
		if err := store.db.QueryRow(query).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("%s = %s; want %s", query, got, want)
		}
	}
	if err := store.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(ctx); err == nil {
		t.Fatal("closed database is ready")
	}
	store, err = Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if store.InstanceID != id {
		t.Fatal("instance ID changed after restart")
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	list, err := listMigrations(migrations)
	if err != nil {
		t.Fatal(err)
	}
	if count != len(list) {
		t.Fatalf("duplicate migrations: %d", count)
	}
	if _, err := store.db.Exec(`DELETE FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	if err := store.Ready(ctx); err == nil {
		t.Fatal("missing migration history is ready")
	}
}

func TestMigrationFailureRollsBack(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "failure.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source := fstest.MapFS{
		"migrations/001_first.sql":  {Data: []byte(`CREATE TABLE first (id INTEGER);`)},
		"migrations/002_broken.sql": {Data: []byte(`CREATE TABLE second (id INTEGER); INVALID SQL;`)},
	}
	if err := migrate(ctx, db, source); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("partial migration persisted: %d tables", count)
	}
}

func TestRejectNewerSchemaAndCorruptDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO schema_migrations(version) SELECT max(version)+1 FROM schema_migrations`); err != nil {
		t.Fatal(err)
	}
	store.Close()
	if next, err := Open(ctx, dir); err == nil {
		next.Close()
		t.Fatal("newer schema accepted")
	}
	badDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(badDir, "controller.db"), []byte("not sqlite"), 0o600); err != nil {
		t.Fatal(err)
	}
	if next, err := Open(ctx, badDir); err == nil {
		next.Close()
		t.Fatal("corrupt database accepted")
	}
}
