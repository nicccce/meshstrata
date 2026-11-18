package subscriptions

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

// CheckReservation is for the I03 gateway's reservation transaction, after
// replaying any already reserved operation with exactly the same input.
func CheckReservation(ctx context.Context, q *dbgen.Queries, input stage3.EntryReservation, now time.Time) error {
	i, err := q.B08Intent(ctx, string(input.OperationID))
	if err != nil {
		return safe(ctx, err)
	}
	want, err := reservation(i)
	if err != nil || !equalReservation(input, want) || i.ReserveStarted != 1 ||
		i.State != "prepared" || i.RevokedAt.Valid {
		return problem(domain.RevisionConflict)
	}
	return checkPublishAuthorization(ctx, q, i, now)
}

func equalReservation(a, b stage3.EntryReservation) bool {
	ar, br := a.Authorization.RevokedAt, b.Authorization.RevokedAt
	return (ar == nil && br == nil || ar != nil && br != nil && ar.Equal(*br)) &&
		a.Authorization.EntryID == b.Authorization.EntryID && a.Authorization.IdentityID == b.Authorization.IdentityID &&
		a.Authorization.Generation == b.Authorization.Generation && a.Authorization.ExpiresAt.Equal(b.Authorization.ExpiresAt) &&
		a.OperationID == b.OperationID && a.SubscriptionID == b.SubscriptionID && a.Route == b.Route &&
		a.CredentialRef == b.CredentialRef && a.Deadline.Equal(b.Deadline)
}

func checkPublishAuthorization(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent, now time.Time) error {
	floor, err := dbNow(ctx, q)
	if err != nil {
		return safe(ctx, err)
	}
	if now.Before(floor) {
		now = floor
	}
	sub, err := q.B08Subscription(ctx, i.SubscriptionID)
	if err != nil {
		return safe(ctx, err)
	}
	yes, err := selected(ctx, q, i)
	if err != nil {
		return safe(ctx, err)
	}
	if !yes || i.RevokedAt.Valid || !eligible(sub, now) {
		return problem(domain.RevisionConflict)
	}
	if i.ExpiresAt <= stamp(now) || i.Deadline <= stamp(now) {
		return problem(domain.TaskExpired)
	}
	return nil
}

// CheckDispatch is the B08 part of I03's transaction fence before accepting a
// new task or arming an existing task. I03 compares the returned binding with
// the plan's exact grant/tombstone and its own lease, epoch and origin ledger.
// Accepted task replay is resolved by operation ID before authorizing new work.
func CheckDispatch(ctx context.Context, q *dbgen.Queries, op domain.ID, kind domain.TaskKind, subject control.Pin, now time.Time) (stage3.EntryChange, error) {
	var i dbgen.B08Intent
	var err error
	switch kind {
	case domain.Publish:
		i, err = q.B08Intent(ctx, string(op))
	case domain.Cleanup:
		i, err = q.B08CleanupIntent(ctx, string(op))
	default:
		return stage3.EntryChange{}, problem(domain.InvalidArgument)
	}
	if err != nil {
		return stage3.EntryChange{}, safe(ctx, err)
	}
	p, err := checkedSubject(ctx, q, i)
	if err != nil || p != subject {
		return stage3.EntryChange{}, problem(domain.RevisionConflict)
	}
	if kind == domain.Publish {
		if i.State != "dispatching" || !i.DispatchEpoch.Valid {
			return stage3.EntryChange{}, problem(domain.RevisionConflict)
		}
		if err := checkPublishAuthorization(ctx, q, i, now); err != nil {
			return stage3.EntryChange{}, err
		}
		current, err := currentIdentity(ctx, q, i)
		if err != nil {
			return stage3.EntryChange{}, safe(ctx, err)
		}
		if !current {
			return stage3.EntryChange{}, problem(domain.RevisionConflict)
		}
	} else {
		if !i.RevokedAt.Valid || i.CleanupState != "pending" || !i.CleanupEpoch.Valid || !i.IdentityID.Valid {
			return stage3.EntryChange{}, problem(domain.RevisionConflict)
		}
		floor, err := dbNow(ctx, q)
		if err != nil {
			return stage3.EntryChange{}, safe(ctx, err)
		}
		if now.Before(floor) {
			now = floor
		}
		if i.CleanupDeadline.String <= stamp(now) {
			return stage3.EntryChange{}, problem(domain.TaskExpired)
		}
		old, err := q.B08Identity(ctx, i.IdentityID.String)
		if err != nil || !old.RevokedAt.Valid || old.ID != i.IdentityKey || old.EntryID != i.EntryKey ||
			old.Generation != i.Generation || old.CredentialRef != i.CredentialRef || old.ExpiresAt != i.ExpiresAt {
			return stage3.EntryChange{}, problem(domain.GrantOriginUncertain)
		}
	}
	out, err := changeFor(work{intent: i, pin: p, kind: kind})
	return out, localError(ctx, err)
}

// ConfirmUnarmed completes B08 compensation inside the I03 gateway's short
// transaction, after the gateway atomically proved that the operation didn't
// arm, fenced any late reserve, and settled its own lease/port records. An
// absent listener in B08 alone (or an error/timeout) is never such evidence.
// No B08 port changes are made here.
func ConfirmUnarmed(ctx context.Context, q *dbgen.Queries, op, listener domain.ID, observedAt time.Time) error {
	i, err := q.B08Intent(ctx, string(op))
	if err != nil {
		return safe(ctx, err)
	}
	if i.CleanupState == "confirmed" {
		return nil
	}
	if !validTime(observedAt) || i.CleanupTaskID.Valid || !i.RevokedAt.Valid ||
		i.ListenerID.Valid && i.ListenerID.String != string(listener) {
		return problem(domain.GrantOriginUncertain)
	}
	if listener != "" {
		row, err := q.B08Listener(ctx, string(listener))
		if err != nil || row.CredentialRef != i.CredentialRef {
			return problem(domain.GrantOriginUncertain)
		}
	}
	g, err := q.B04GetGrant(ctx, i.IdentityKey)
	if !errors.Is(err, sql.ErrNoRows) && (err != nil || g.OriginTaskID.Valid || !g.ConfirmedAt.Valid) {
		return problem(domain.GrantOriginUncertain)
	}
	if err := affected(q.B08ConfirmUnarmed(ctx, i.ID)); err != nil {
		return localError(ctx, err)
	}
	at := stamp(observedAt)
	if i.EntryID.Valid {
		if err := affected(q.B08ReleaseReference(ctx, dbgen.B08ReleaseReferenceParams{ID: i.ID, At: nullable(at)})); err != nil {
			return localError(ctx, err)
		}
	}
	if _, err := q.B08ConfirmEntryCleanup(ctx, i.EntryKey); err != nil {
		return localError(ctx, err)
	}
	return localError(ctx, revokeCredential(ctx, q, i, at))
}

// AuthorizeTaskRollback is called only by the authenticated B04 action gateway
// in the same transaction that commits the explicit rollback task. It makes a
// removed grant unavailable for download immediately; its history is retained.
func AuthorizeTaskRollback(ctx context.Context, q *dbgen.Queries, identity domain.ID, now time.Time) error {
	i, err := q.I03IntentByIdentity(ctx, string(identity))
	if err != nil {
		return err
	}
	return retire(ctx, q, i, stamp(now))
}
