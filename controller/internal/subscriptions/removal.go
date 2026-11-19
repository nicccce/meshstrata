package subscriptions

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
)

// RevokeRemovedNodeIntent participates in the trusted B02/I03 transaction.
// It revokes authority immediately and keeps each original cleanup operation;
// neither revocation nor an unreachable node is physical cleanup evidence.
func RevokeRemovedNodeIntent(ctx context.Context, q *dbgen.Queries, node, intent domain.ID, now time.Time) error {
	n, err := q.GetNode(ctx, string(node))
	if err != nil {
		return err
	}
	if !n.RemovedAt.Valid {
		return problem(domain.RevisionConflict)
	}
	i, err := q.B08Intent(ctx, string(intent))
	if err != nil {
		return err
	}
	var route stage3.PublishedRoute
	if json.Unmarshal([]byte(i.RouteJson), &route) != nil || route.Pin != routePin(i) {
		return problem(domain.RevisionConflict)
	}
	affected := false
	for _, hop := range route.Tail.Hops {
		affected = affected || hop.NodeID == node
	}
	if !affected {
		return problem(domain.RevisionConflict)
	}
	if err := retire(ctx, q, i, stamp(now)); err != nil {
		return err
	}
	if i.EntryID.Valid {
		if _, err := q.B08RemoveEntry(ctx, dbgen.B08RemoveEntryParams{ID: i.EntryID.String, At: nullable(stamp(now))}); err != nil {
			return err
		}
	}
	return nil
}
