package stage3

import (
	"context"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
)

// RequireActive runs in the same transaction that installs a new head or
// reference. Historical reads remain available after an archive.
func RequireActive(ctx context.Context, q *dbgen.Queries, kind string, id domain.ID) error {
	if id == "" {
		return nil
	}
	archived, err := q.I03CArchived(ctx, dbgen.I03CArchivedParams{Kind: kind, ID: string(id)})
	if err != nil {
		return err
	}
	if archived {
		return &domain.Problem{Code: domain.NotFound}
	}
	return nil
}
