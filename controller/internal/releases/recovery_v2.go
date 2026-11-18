package releases

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/routes"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/controller/internal/subscriptions"
	"github.com/nicccce/meshstrata/internal/domain"
)

// RecoverEntryCleanup advances a persisted generation only after the exact old
// task is terminal. The logical operation, subject, identity and grant Origin
// remain unchanged. It never interprets unknown origin as never armed.
func (s *Service) RecoverEntryCleanup(ctx context.Context, op, task domain.ID) error {
	return s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		i, e := q.B08CleanupIntent(ctx, string(op))
		if e != nil {
			return e
		}
		if i.CleanupState != "pending" || !i.RevokedAt.Valid || i.CleanupTaskID.String != string(task) {
			return nil
		}
		old, e := q.GetTask(ctx, string(task))
		if e != nil {
			return e
		}
		meta, e := q.I03Task(ctx, old.ID)
		if e != nil {
			return e
		}
		if active(old.State) || old.State == "succeeded" || meta.EntryOperationID != string(op) || old.Kind != "cleanup" {
			return problem(domain.RevisionConflict)
		}
		grant, e := q.B04GetGrant(ctx, i.IdentityKey)
		if e != nil {
			return e
		}
		if !grant.OriginTaskID.Valid || grant.ListenerID != i.ListenerID.String || grant.Generation != i.Generation || grant.EntryID != i.EntryKey {
			return problem(domain.GrantOriginUncertain)
		}
		origin, e := q.B04GetTarget(ctx, dbgen.B04GetTargetParams{TaskID: grant.OriginTaskID.String, NodeID: grant.NodeID})
		if e != nil {
			return e
		}
		if origin.Fetched != 1 {
			return problem(domain.GrantOriginUncertain)
		}
		n, e := q.GetNode(ctx, grant.NodeID)
		if e != nil {
			return e
		}
		if n.RemovedAt.Valid {
			return problem(domain.NotFound)
		}

		_, e = q.I03RecoverCleanup(ctx, dbgen.I03RecoverCleanupParams{CleanupEpoch: sql.NullInt64{Int64: n.RevocationEpoch, Valid: true}, CleanupDeadline: nullable(stamp(s.options.Now().Add(5 * time.Minute))), CleanupID: string(op), CleanupTaskID: nullable(old.ID), CleanupGeneration: i.CleanupGeneration})
		return e
	})
}

// CompensateUnarmed seals late reserve/arm in the same transaction that checks
// every possible task and settles the lease. A deadline is never proof.
func (s *Service) CompensateUnarmed(ctx context.Context, op domain.ID) error {
	var uncertain bool
	err := s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		i, e := q.B08Intent(ctx, string(op))
		if e != nil {
			return e
		}
		if !i.RevokedAt.Valid || i.CleanupState != "pending" {
			return nil
		}
		now := s.options.Now()
		if e = q.I03FenceOperation(ctx, dbgen.I03FenceOperationParams{OperationID: string(op), BlockedAt: stamp(now)}); e != nil {
			return e
		}
		lease, e := q.I03Lease(ctx, string(op))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil {
			if e = q.I03BlockLease(ctx, dbgen.I03BlockLeaseParams{OperationID: string(op), BlockedAt: nullable(stamp(now))}); e != nil {
				return e
			}
		}
		grant, e := q.B04GetGrant(ctx, i.IdentityKey)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil {
			unarmed, e := q.I03UnarmedIdentity(ctx, i.IdentityKey)
			if e != nil {
				return e
			}
			if !unarmed || grant.OriginTaskID.Valid {
				// Keep the late-arm fence even when existing runtime authority
				// needs a real cleanup receipt. Unknown origin never frees ports.
				uncertain = true
				return nil
			}
			tasks, e := q.I03IdentityTasks(ctx, i.IdentityKey)
			if e != nil {
				return e
			}
			for _, task := range tasks {
				if e = finishTask(ctx, q, task, domain.Cancelled, now); e != nil {
					return e
				}
			}
			if e = confirmCleanup(ctx, q, grant.NodeID, i.IdentityKey, string(op), stamp(now)); e != nil {
				return e
			}
		}
		if e = subscriptions.ConfirmUnarmed(ctx, q, op, domain.ID(lease.ListenerID), now); e != nil {
			return e
		}
		if e = routes.DropEntryLinkReferences(ctx, q, domain.ID(i.IdentityKey), now); e != nil {
			return e
		}
		if lease.ListenerID != "" {
			if e = q.B06SetInternalListenerPending(ctx, lease.ListenerID); e != nil {
				return e
			}
			if e = q.B06SetInternalPortsPending(ctx, nullable(lease.ListenerID)); e != nil {
				return e
			}
			if e = q.B06ConfirmInternalListener(ctx, lease.ListenerID); e != nil {
				return e
			}
			if e = q.B06ReleaseInternalPorts(ctx, dbgen.B06ReleaseInternalPortsParams{ListenerID: nullable(lease.ListenerID), At: nullable(stamp(now))}); e != nil {
				return e
			}
			return q.I03ReleaseLease(ctx, dbgen.I03ReleaseLeaseParams{OperationID: string(op), ReleasedAt: nullable(stamp(now))})
		}
		return nil
	})
	if err == nil && uncertain {
		return problem(domain.GrantOriginUncertain)
	}
	return err
}

// SettleEntryCleanup runs after B08 has persisted the exact cleanup receipt and
// released its reference. Retrying it is safe after crashes between modules.
func (s *Service) SettleEntryCleanup(ctx context.Context, op domain.ID) error {
	return s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		i, e := q.B08Intent(ctx, string(op))
		if e != nil {
			return e
		}
		if i.CleanupState != "confirmed" {
			return problem(domain.DependencyInUse)
		}
		grant, e := q.B04GetGrant(ctx, i.IdentityKey)
		if e != nil {
			return e
		}
		if !grant.ConfirmedAt.Valid {
			return problem(domain.GrantOriginUncertain)
		}
		now := s.options.Now()
		if e = routes.DropEntryLinkReferences(ctx, q, domain.ID(i.IdentityKey), now); e != nil {
			return e
		}
		if e = q.I03BlockLease(ctx, dbgen.I03BlockLeaseParams{OperationID: i.ID, BlockedAt: nullable(stamp(now))}); e != nil {
			return e
		}
		if e = q.B06SetInternalListenerPending(ctx, grant.ListenerID); e != nil {
			return e
		}
		if e = q.B06SetInternalPortsPending(ctx, nullable(grant.ListenerID)); e != nil {
			return e
		}
		if e = q.B06ConfirmInternalListener(ctx, grant.ListenerID); e != nil {
			return e
		}
		if e = q.B06ReleaseInternalPorts(ctx, dbgen.B06ReleaseInternalPortsParams{ListenerID: nullable(grant.ListenerID), At: nullable(stamp(now))}); e != nil {
			return e
		}
		return q.I03ReleaseLease(ctx, dbgen.I03ReleaseLeaseParams{OperationID: i.ID, ReleasedAt: nullable(stamp(now))})
	})
}
