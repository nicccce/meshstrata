package storage

import (
	"context"
	"database/sql"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
)

// Database is injected into B01/B02 repositories, never HTTP handlers. It reuses
// the one Store connection pool and generated queries; no raw SQL handle escapes.
// Queries are scoped to the callback and MUST NOT be retained or used by a goroutine.
// Callbacks must be short, context-aware and non-nested; no crypto, signing, network
// or calls back into Store (its pool has one connection). Prepare those beforehand
// and CAS/revalidate state in Write. Repository maps sql.ErrNoRows/constraints to
// domain errors; all other failures are internal and must be redacted by handlers.
type Database interface {
	Read(context.Context, func(*dbgen.Queries) error) error
	Write(context.Context, func(*dbgen.Queries) error) error
}

var _ Database = (*Store)(nil)

func (s *Store) Read(ctx context.Context, fn func(*dbgen.Queries) error) error {
	return s.withQueries(ctx, &sql.TxOptions{ReadOnly: true}, fn)
}

// Write commits only after fn succeeds. Callback/commit/cancellation errors are
// returned without retries; callback errors remain compatible with errors.Is.
func (s *Store) Write(ctx context.Context, fn func(*dbgen.Queries) error) error {
	return s.withQueries(ctx, nil, fn)
}

func (s *Store) withQueries(ctx context.Context, opts *sql.TxOptions, fn func(*dbgen.Queries) error) error {
	tx, err := s.db.BeginTx(ctx, opts)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(dbgen.New(tx)); err != nil {
		return err
	}
	return tx.Commit()
}
