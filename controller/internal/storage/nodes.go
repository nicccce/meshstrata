package storage

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
	"strings"
	"time"
)

// RenameNode demonstrates the required compare-and-swap transaction boundary.
// No handler exposes it until B01/B02 authentication is installed.
func (s *Store) RenameNode(ctx context.Context, id domain.ID, expected domain.Revision, name string, now time.Time) (domain.Revision, error) {
	if !id.Valid() || !expected.Valid() || strings.TrimSpace(name) == "" || len(name) > 128 || now.IsZero() {
		return 0, &domain.Problem{Code: domain.InvalidArgument}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	q := dbgen.New(tx)
	n, err := q.RenameNode(ctx, dbgen.RenameNodeParams{ID: string(id), Revision: int64(expected), Name: name, UpdatedAt: stamp(now)})
	if err != nil {
		return 0, err
	}
	if n == 0 {
		current, err := q.GetNode(ctx, string(id))
		if errors.Is(err, sql.ErrNoRows) {
			return 0, &domain.Problem{Code: domain.NotFound}
		}
		if err != nil {
			return 0, err
		}
		revision := domain.Revision(current.Revision)
		return 0, &domain.Problem{Code: domain.RevisionConflict, CurrentRevision: &revision}
	}
	return expected + 1, tx.Commit()
}
