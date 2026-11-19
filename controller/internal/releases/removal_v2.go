package releases

import (
	"context"
	"database/sql"
	"errors"

	"github.com/nicccce/meshstrata/controller/internal/routes"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/controller/internal/subscriptions"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func (s *Service) prepareV2Removal(ctx context.Context, q *dbgen.Queries, node string) error {
	now := s.options.Now()
	if err := q.I03MarkRemovedLegacyPorts(ctx, node); err != nil {
		return err
	}
	// A historical confirmation is retained, but a route through a removed node
	// cannot remain a current head or authorize another use of its credentials.
	if err := q.I03InvalidateRemovedHeads(ctx, node); err != nil {
		return err
	}
	intents, err := q.I03RemovalIntents(ctx, node)
	if err != nil {
		return err
	}
	for _, i := range intents {
		if err := subscriptions.RevokeRemovedNodeIntent(ctx, q, domain.ID(node), domain.ID(i.ID), now); err != nil {
			return err
		}
		if err := q.I03FenceOperation(ctx, dbgen.I03FenceOperationParams{OperationID: i.ID, BlockedAt: stamp(now)}); err != nil {
			return err
		}
		if i.ListenerID.Valid {
			if err := q.B06SetInternalListenerPending(ctx, i.ListenerID.String); err != nil {
				return err
			}
			if err := q.B06SetInternalPortsPending(ctx, i.ListenerID); err != nil {
				return err
			}
			if err := q.B04PendingPorts(ctx, i.IdentityKey); err != nil {
				return err
			}
		}
	}
	publications, err := q.I03RemovalPublications(ctx, node)
	if err != nil {
		return err
	}
	for _, p := range publications {
		if p.TaskID.Valid {
			if err := finishTask(ctx, q, p.TaskID.String, domain.Cancelled, now); err != nil {
				return err
			}
		}
		if p.State == "preparing" || p.State == "prepared" || p.State == "queued" || p.State == "running" || p.State == "rollback_pending" || p.State == "rollback_failed" {
			if _, err := q.B06SetPublicationState(ctx, dbgen.B06SetPublicationStateParams{NewState: "failed", ID: p.ID, OldState: p.State}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) reconcileV2Removal(ctx context.Context, task, node string) error {
	var links []dbgen.B06InternalLink
	err := s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		// Entry references remain until the owning ingress proves exact cleanup.
		// Only then may the invalid publication surrender its route references.
		operations, err := q.I03RemovalReleasableReferences(ctx, node)
		if err != nil {
			return err
		}
		for _, op := range operations {
			if _, err := routes.RetireInternalReference(ctx, q, domain.ID(op), s.options.Now()); err != nil {
				return err
			}
		}
		links, err = q.I03RemovalLinks(ctx, node)
		if err != nil {
			return err
		}
		for _, l := range links {
			if err := q.B04LinkRemoval(ctx, dbgen.B04LinkRemovalParams{RemovalTaskID: task, NodeID: l.NodeID, ListenerID: l.ListenerID}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, l := range links {
		if l.State != "retiring" || l.NodeID == node || s.v2Session(domain.ID(l.NodeID)) == "" {
			continue
		}
		var origin dbgen.I03InternalOrigin
		err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error { var e error; origin, e = q.I03Origin(ctx, l.ID); return e })
		if errors.Is(err, sql.ErrNoRows) {
			continue
		} // no invented origin/receipt for a never-fetched link
		if err != nil {
			return err
		}
		if origin.NodeID != l.NodeID {
			return problem(domain.GrantOriginUncertain)
		}
		pin := control.Pin{ID: domain.ID(l.ID), Revision: domain.Revision(l.Revision), Digest: l.SuffixDigest}
		out, err := s.RetireInternal(ctx, domain.Mutation{ExpectedRevision: pin.Revision, IdempotencyKey: "removal:" + task + ":" + l.ID}, pin)
		if err != nil {
			if isProblem(err) {
				continue
			} // offline/stale inventory/uncertain recovery stay pending
			return err
		}
		if err := s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
			return q.I03SetRemovalChild(ctx, dbgen.I03SetRemovalChildParams{Removal: task, NodeID: l.NodeID, ListenerID: l.ListenerID, Child: sql.NullString{String: string(out.TaskID), Valid: true}})
		}); err != nil {
			return err
		}
	}
	return nil
}
