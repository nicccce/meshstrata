package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	db         *sql.DB
	InstanceID string
}

func Open(ctx context.Context, stateDir string) (*Store, error) {
	path := filepath.Join(stateDir, "controller.db")
	uri := url.URL{Scheme: "file", Path: path}
	q := uri.Query()
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	uri.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("connect database: %w", err)
	}
	if err := migrate(ctx, db, migrations); err != nil {
		db.Close()
		return nil, err
	}
	queries := dbgen.New(db)
	if err := queries.CreateMetadata(ctx, dbgen.CreateMetadataParams{Key: "instance_id", Value: rand.Text()}); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize instance metadata: %w", err)
	}
	store.InstanceID, err = queries.GetMetadata(ctx, "instance_id")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("read instance metadata: %w", err)
	}
	return store, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Ready(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return err
	}
	return checkVersions(ctx, s.db, migrations)
}

type migration struct {
	version int
	name    string
}

func listMigrations(source fs.FS) ([]migration, error) {
	files, err := fs.Glob(source, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	var result []migration
	for _, name := range files {
		prefix, _, ok := strings.Cut(filepath.Base(name), "_")
		version, err := strconv.Atoi(prefix)
		if !ok || err != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration filename: %s", name)
		}
		result = append(result, migration{version: version, name: name})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].version < result[j].version })
	for i, m := range result {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions must start at 1 and be contiguous: %s", m.name)
		}
	}
	return result, nil
}

func migrate(ctx context.Context, db *sql.DB, source fs.FS) error {
	list, err := listMigrations(source)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migrations: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return fmt.Errorf("create migration history: %w", err)
	}
	var applied int
	// Validate all recorded versions, including a database from a newer binary.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations`).Scan(&applied); err != nil {
		return err
	}
	for i := 1; i <= applied; i++ {
		var found int
		if i > len(list) {
			return fmt.Errorf("database schema is newer than this binary")
		}
		if err := tx.QueryRowContext(ctx, `SELECT version FROM schema_migrations WHERE version = ?`, i).Scan(&found); err != nil {
			return fmt.Errorf("invalid migration history: %w", err)
		}
	}
	for _, m := range list[applied:] {
		body, err := fs.ReadFile(source, m.name)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply migration %s: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, m.version); err != nil {
			return fmt.Errorf("record migration %s: %w", m.name, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

func checkVersions(ctx context.Context, db *sql.DB, source fs.FS) error {
	list, err := listMigrations(source)
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	defer rows.Close()
	i := 0
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return err
		}
		if i >= len(list) || version != list[i].version {
			return fmt.Errorf("migration history does not match this binary")
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if i != len(list) {
		return fmt.Errorf("database migrations are incomplete")
	}
	return nil
}
