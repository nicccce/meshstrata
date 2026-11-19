package subscriptions

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type work struct {
	intent dbgen.B08Intent
	pin    control.Pin
	kind   domain.TaskKind
	mode   string
}

// PendingIntents is the worker's bounded, repeatable discovery read. Reset
// after to empty at the end of a sweep so delayed/still-pending jobs are visited.
func (s *Service) PendingIntents(ctx context.Context, after domain.ID, limit int) ([]domain.ID, error) {
	if after != "" && !after.Valid() || limit < 1 || limit > 128 {
		return nil, invalid("")
	}
	if err := s.tick(ctx); err != nil {
		return nil, localError(ctx, err)
	}
	out := []domain.ID{}
	err := s.o.DB.Read(ctx, func(q *dbgen.Queries) error {
		now, err := dbNow(ctx, q)
		if err != nil {
			return err
		}
		ids, err := q.B08Work(ctx, dbgen.B08WorkParams{AfterID: string(after), PageSize: int64(limit), Now: stamp(now)})
		for _, id := range ids {
			out = append(out, domain.ID(id))
		}
		return err
	})
	return out, localError(ctx, err)
}

func checkedSubject(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent) (control.Pin, error) {
	r, err := q.B08Revision(ctx, dbgen.B08RevisionParams{SubscriptionID: i.SubscriptionID, Revision: i.SubjectRevision})
	p := subjectPin(r)
	if err == nil && !p.Valid() {
		err = problem(unavailable)
	}
	return p, err
}

func selected(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent) (bool, error) {
	head, err := q.B08Head(ctx, i.SubscriptionID)
	if err != nil || head.DesiredRevision != i.SubjectRevision {
		return false, err
	}
	entries, err := q.B08RevisionEntries(ctx, dbgen.B08RevisionEntriesParams{SubscriptionID: i.SubscriptionID, Revision: head.DesiredRevision})
	for _, e := range entries {
		if e.ID == i.ID {
			return true, err
		}
	}
	return false, err
}

func currentIdentity(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent) (bool, error) {
	if alive, err := routeNodesAvailable(ctx, q, i); err != nil || !alive {
		return false, err
	}
	e, err := q.B08LegacyEntryByRoute(ctx, dbgen.B08LegacyEntryByRouteParams{SubscriptionID: i.SubscriptionID, RouteID: i.RouteID})
	if err != nil {
		return false, err
	}
	if e.RevokedAt.Valid || e.ID != i.EntryKey || e.ListenerID != i.ListenerID.String || e.ExpiresAt != i.ExpiresAt {
		return false, nil
	}
	v, err := q.B08LatestIdentity(ctx, e.ID)
	if err != nil {
		return false, err
	}
	return v.ID == i.IdentityKey && v.Generation == i.Generation && v.CredentialRef == i.CredentialRef &&
		v.ExpiresAt == i.ExpiresAt && !v.RevokedAt.Valid, nil
}

// Removal revokes downloads/late reservation immediately, before a worker has
// persisted the corresponding cleanup intent. Use the immutable captured route,
// rather than a mutable route head or an inventory advertisement.
func routeNodesAvailable(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent) (bool, error) {
	var route stage3.PublishedRoute
	if json.Unmarshal([]byte(i.RouteJson), &route) != nil || route.Pin != routePin(i) || len(route.Tail.Hops) == 0 || len(route.Tail.Hops) > 8 {
		return false, problem(unavailable)
	}
	for _, hop := range route.Tail.Hops {
		n, err := q.GetNode(ctx, string(hop.NodeID))
		if err != nil {
			return false, err
		}
		if n.RemovedAt.Valid {
			return false, nil
		}
	}
	return true, nil
}

// nextWork may advance only within its own short transaction. Every external
// call happens after commit. A state moving forward never relies on RAM.
func (s *Service) nextWork(ctx context.Context, id string) (w work, err error) {
	err = s.o.DB.Write(ctx, func(q *dbgen.Queries) error {
		now, err := dbNow(ctx, q)
		if err != nil {
			return err
		}
		i, err := q.B08Intent(ctx, id)
		if err != nil {
			return err
		}
		sub, err := q.B08Subscription(ctx, i.SubscriptionID)
		if err != nil {
			return err
		}
		at := stamp(now)
		if !i.RevokedAt.Valid {
			active := eligible(sub, now) && i.ExpiresAt > at
			if active {
				active, err = routeNodesAvailable(ctx, q, i)
				if err != nil {
					return err
				}
			}
			if active && i.State != "confirmed" {
				active, err = selected(ctx, q, i)
				if err != nil {
					return err
				}
			}
			if !active {
				if err := retire(ctx, q, i, at); err != nil {
					return err
				}
				i, err = q.B08Intent(ctx, id)
				if err != nil {
					return err
				}
			}
		}
		w.intent = i
		if i.RevokedAt.Valid {
			if i.CleanupState != "pending" {
				return nil
			}
			// No 002 identity until lease CAS commits. An interrupted
			// reservation needs proof from the I03 reservation ledger.
			if !i.IdentityID.Valid || !i.DispatchEpoch.Valid {
				return nil
			}
			w.pin, err = checkedSubject(ctx, q, i)
			if err != nil {
				return err
			}
			w.kind = domain.Cleanup
			if i.CleanupTaskID.Valid {
				w.mode = "observe"
				return nil
			}
			if !i.CleanupEpoch.Valid {
				l, err := q.B08Listener(ctx, i.ListenerID.String)
				if err != nil {
					return err
				}
				epoch, err := q.B08NodeEpoch(ctx, l.NodeID)
				if err != nil {
					return err
				}
				i.CleanupEpoch, i.CleanupDeadline = revision(epoch), nullable(stamp(now.Add(releaseWindow)))
				if !i.CleanupEpoch.Valid {
					return problem(unavailable)
				}
				if err := affected(q.B08BeginCleanup(ctx, dbgen.B08BeginCleanupParams{
					ID: id, Epoch: i.CleanupEpoch, Deadline: i.CleanupDeadline,
				})); err != nil {
					return err
				}
				w.intent = i
			}
			// Even after a deadline, an exact retry may retrieve a task whose
			// accepted response was lost. I03 must replay before deadline checks
			// and reject creating a late task.
			w.mode = "apply"
			return nil
		}
		if i.State == "confirmed" || i.State == "failed" {
			return nil
		}
		w.pin, err = checkedSubject(ctx, q, i)
		if err != nil {
			return err
		}
		w.kind = domain.Publish
		if i.State == "dispatching" && i.TaskID.Valid {
			w.mode = "observe"
			return nil
		}
		if i.State != "dispatching" && i.Deadline <= at {
			return failPublish(ctx, q, i, at)
		}
		switch i.State {
		case "prepared":
			if i.ReserveStarted == 0 {
				if err := affected(q.B08BeginReserve(ctx, id)); err != nil {
					return err
				}
				w.intent.ReserveStarted = 1
			}
			w.mode = "reserve"
		case "leased":
			if err := commitLease(ctx, q, i); err != nil {
				return err
			}
			w.mode = "repeat"
		case "committed", "dispatching":
			current, err := currentIdentity(ctx, q, i)
			if err != nil {
				return err
			}
			if !current {
				return problem(domain.RevisionConflict)
			}
			if i.State == "committed" {
				l, err := q.B08Listener(ctx, i.ListenerID.String)
				if err != nil {
					return err
				}
				ep, err := q.B08NodeEpoch(ctx, l.NodeID)
				if err != nil {
					return err
				}
				w.intent.DispatchEpoch = revision(ep)
				if !w.intent.DispatchEpoch.Valid {
					return problem(unavailable)
				}
				if err := affected(q.B08BeginDispatch(ctx, dbgen.B08BeginDispatchParams{ID: id, Epoch: w.intent.DispatchEpoch})); err != nil {
					return err
				}
			}
			w.mode = "apply"
		default:
			return problem(unavailable)
		}
		return nil
	})
	return
}

func reservation(i dbgen.B08Intent) (r stage3.EntryReservation, err error) {
	r = stage3.EntryReservation{OperationID: domain.ID(i.ID), SubscriptionID: domain.ID(i.SubscriptionID),
		Route: routePin(i), CredentialRef: domain.ID(i.CredentialRef), Authorization: control.BundleAuthorization{
			EntryID: domain.ID(i.EntryKey), IdentityID: domain.ID(i.IdentityKey), Generation: domain.Revision(i.Generation),
		},
	}
	r.Authorization.ExpiresAt, err = parse(i.ExpiresAt)
	if err != nil {
		return r, err
	}
	r.Deadline, err = parse(i.Deadline)
	return
}

func leaseFor(i dbgen.B08Intent) (l stage3.IngressLease, err error) {
	l = stage3.IngressLease{ReservationID: domain.ID(i.ReservationID.String), ListenerID: domain.ID(i.ListenerID.String), Route: routePin(i)}
	if !l.ListenerID.Valid() || !l.ReservationID.Valid() || !i.EndpointJson.Valid {
		return l, problem(unavailable)
	}
	err = json.Unmarshal([]byte(i.EndpointJson.String), &l.Endpoint)
	return
}

func changeFor(w work) (change stage3.EntryChange, err error) {
	i := w.intent
	e, err := reservation(i)
	if err != nil {
		return change, err
	}
	l, err := leaseFor(i)
	if err != nil {
		return change, err
	}
	epoch := i.DispatchEpoch.Int64
	if w.kind == domain.Cleanup {
		e.OperationID, epoch = domain.ID(i.CleanupID), i.CleanupEpoch.Int64
		e.Deadline, err = parse(i.CleanupDeadline.String)
		if err != nil {
			return change, err
		}
		e.Authorization.RevokedAt, err = stringTime(i.RevokedAt)
		if err != nil {
			return change, err
		}
	}
	return stage3.EntryChange{RecoveryGeneration: i.CleanupGeneration, Kind: w.kind, Subject: w.pin, Entry: e, Lease: l, Epoch: domain.Revision(epoch),
		Mutation: domain.Mutation{ExpectedRevision: w.pin.Revision, IdempotencyKey: string(e.OperationID) + "/" + strconv.FormatInt(i.CleanupGeneration, 10)}}, nil
}

func (s *Service) storeLease(ctx context.Context, i dbgen.B08Intent, route stage3.PublishedRoute, l stage3.IngressLease) error {
	want := route.Tail.Hops[0].Endpoint
	want.Port = l.Endpoint.Port
	if !l.ReservationID.Valid() || !l.ListenerID.Valid() || l.Route != route.Pin || !l.Endpoint.Valid() || want != l.Endpoint {
		return problem(domain.GrantOriginUncertain)
	}
	b, err := jsonValue(l.Endpoint)
	if err != nil {
		return err
	}
	return s.o.DB.Write(ctx, func(q *dbgen.Queries) error {
		current, err := q.B08Intent(ctx, i.ID)
		if err != nil {
			return err
		}
		if current.ListenerID.Valid {
			old, err := leaseFor(current)
			if err != nil || old != l {
				return problem(domain.GrantOriginUncertain)
			}
			return nil
		}
		// Preserve a late lease after configuration CAS loss/deletion. It is
		// evidence for the compensator and never creates an entry in this step.
		node := route.Tail.Hops[0].NodeID
		row, err := q.B08Listener(ctx, string(l.ListenerID))
		if err != nil {
			return err
		}
		if row.NodeID != string(node) || row.CredentialRef != i.CredentialRef ||
			row.ListenPort != int64(l.Endpoint.Port) || row.Protocol != string(l.Endpoint.Protocol) {
			return problem(domain.GrantOriginUncertain)
		}
		return affected(q.B08StoreLease(ctx, dbgen.B08StoreLeaseParams{ID: i.ID, ReservationID: nullable(string(l.ReservationID)),
			ListenerID: nullable(string(l.ListenerID)), EndpointJson: nullable(b)}))
	})
}

func commitLease(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent) error {
	row, err := q.B08Listener(ctx, i.ListenerID.String)
	if err != nil {
		return err
	}
	epoch, err := q.B08NodeEpoch(ctx, row.NodeID)
	if err != nil {
		return err
	}
	old, err := q.B08LegacyEntryByRoute(ctx, dbgen.B08LegacyEntryByRouteParams{SubscriptionID: i.SubscriptionID, RouteID: i.RouteID})
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := q.B08InsertEntry(ctx, dbgen.B08InsertEntryParams{ID: i.EntryKey, SubscriptionID: i.SubscriptionID,
			RouteID: i.RouteID, Position: i.LegacyPosition, ListenerID: row.ID, ExpiresAt: i.ExpiresAt, RevocationEpoch: epoch}); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		if old.ID != i.EntryKey || old.RevokedAt.Valid {
			return problem(domain.RevisionConflict)
		}
		previous, err := q.B08LatestIdentity(ctx, old.ID)
		if err != nil {
			return err
		}
		if previous.Generation >= i.Generation || !previous.RevokedAt.Valid || previous.ID == i.IdentityKey {
			return problem(domain.RevisionConflict)
		}
		if err := affected(q.B08MoveEntryGeneration(ctx, dbgen.B08MoveEntryGenerationParams{ListenerID: row.ID,
			ExpiresAt: i.ExpiresAt, Epoch: epoch, ID: old.ID, SubscriptionID: i.SubscriptionID})); err != nil {
			return err
		}
	}
	if err := q.B08InsertIdentity(ctx, dbgen.B08InsertIdentityParams{ID: i.IdentityKey, EntryID: i.EntryKey,
		Generation: i.Generation, CredentialRef: i.CredentialRef, ExpiresAt: i.ExpiresAt}); err != nil {
		return err
	}
	if err := q.B08InsertReference(ctx, dbgen.B08InsertReferenceParams{ID: i.ID, ListenerID: row.ID, EntryID: nullable(i.EntryKey)}); err != nil {
		return err
	}
	return affected(q.B08CommitEntry(ctx, i.ID))
}

func (s *Service) storeTask(ctx context.Context, w work, task domain.ID) error {
	if !task.Valid() {
		return problem(domain.GrantOriginUncertain)
	}
	return s.o.DB.Write(ctx, func(q *dbgen.Queries) error {
		i, err := q.B08Intent(ctx, w.intent.ID)
		if err != nil {
			return err
		}
		old := i.TaskID
		if w.kind == domain.Cleanup {
			old = i.CleanupTaskID
		}
		if old.Valid {
			if old.String != string(task) {
				return problem(domain.GrantOriginUncertain)
			}
			return nil
		}
		if w.kind == domain.Cleanup {
			return affected(q.B08StoreCleanupTask(ctx, dbgen.B08StoreCleanupTaskParams{ID: i.ID, TaskID: nullable(string(task))}))
		}
		return affected(q.B08StoreTask(ctx, dbgen.B08StoreTaskParams{ID: i.ID, TaskID: nullable(string(task))}))
	})
}

func (s *Service) acceptRelease(ctx context.Context, w work, task domain.ID, state stage3.ReleaseState) error {
	op := w.intent.ID
	if w.kind == domain.Cleanup {
		op = w.intent.CleanupID
	}
	if state.TaskID != task || state.Subject != w.pin || state.EntryOperationID != domain.ID(op) {
		return problem(domain.GrantOriginUncertain)
	}
	switch state.State {
	case "queued", "running", "rollback_pending":
		return nil
	case "published", "failed", "cancelled", "timed_out", "rolled_back", "rollback_failed":
	default:
		return problem(unavailable)
	}
	if w.kind == domain.Cleanup && state.State != "published" {
		if recovery, ok := s.o.Publisher.(interface {
			RecoverEntryCleanup(context.Context, domain.ID, domain.ID) error
		}); ok {
			return recovery.RecoverEntryCleanup(ctx, domain.ID(w.intent.CleanupID), task)
		}
	}
	if err := s.tick(ctx); err != nil {
		return err
	}
	return s.o.DB.Write(ctx, func(q *dbgen.Queries) error {
		now, err := dbNow(ctx, q)
		if err != nil {
			return err
		}
		i, err := q.B08Intent(ctx, w.intent.ID)
		if err != nil {
			return err
		}
		if w.kind == domain.Cleanup {
			if i.CleanupTaskID.String != string(task) || i.CleanupState != "pending" || state.State != "published" {
				return nil
			}
			listener, err := q.B08Listener(ctx, i.ListenerID.String)
			if err != nil {
				return err
			}
			confirmed := false
			for _, n := range state.Nodes {
				if n.NodeID == domain.ID(listener.NodeID) && n.Cleanup == "confirmed" {
					confirmed = true
				}
			}
			if !confirmed {
				return nil
			}
			if err := affected(q.B08ConfirmCleanup(ctx, dbgen.B08ConfirmCleanupParams{ID: i.ID, TaskID: nullable(string(task))})); err != nil {
				return err
			}
			if err := affected(q.B08ReleaseReference(ctx, dbgen.B08ReleaseReferenceParams{ID: i.ID, At: nullable(stamp(now))})); err != nil {
				return err
			}
			if _, err := q.B08ConfirmEntryCleanup(ctx, i.EntryKey); err != nil {
				return err
			}
			return revokeCredential(ctx, q, i, stamp(now))
		}
		if i.RevokedAt.Valid || i.State != "dispatching" || i.TaskID.String != string(task) {
			return nil // a late or duplicate acknowledgment has no authority
		}
		sub, err := q.B08Subscription(ctx, i.SubscriptionID)
		if err != nil {
			return err
		}
		cur, err := selected(ctx, q, i)
		if err != nil {
			return err
		}
		if !cur || !eligible(sub, now) || i.ExpiresAt <= stamp(now) {
			return retire(ctx, q, i, stamp(now))
		}
		cur, err = currentIdentity(ctx, q, i)
		if err != nil {
			return err
		}
		if !cur {
			return problem(domain.RevisionConflict)
		}
		if state.State != "published" {
			return failPublish(ctx, q, i, stamp(now))
		}
		if err := affected(q.B08SetPublishState(ctx, dbgen.B08SetPublishStateParams{ID: i.ID, Expected: "dispatching", Next: "confirmed"})); err != nil {
			return err
		}
		return confirmRevision(ctx, q, i.SubscriptionID, i.SubjectRevision, stamp(now))
	})
}

func failPublish(ctx context.Context, q *dbgen.Queries, i dbgen.B08Intent, now string) error {
	if err := affected(q.B08SetPublishState(ctx, dbgen.B08SetPublishStateParams{ID: i.ID, Expected: i.State, Next: "failed"})); err != nil {
		return err
	}
	// Retire the failed candidate, not any still-valid previous snapshot.
	return retire(ctx, q, i, now)
}

func confirmRevision(ctx context.Context, q *dbgen.Queries, sub string, rev int64, at string) error {
	entries, err := q.B08RevisionEntries(ctx, dbgen.B08RevisionEntriesParams{SubscriptionID: sub, Revision: rev})
	if err != nil {
		return err
	}
	if len(entries) == 0 || len(entries) > stage3.MaxSubscriptionEntries {
		return problem(unavailable)
	}
	for _, i := range entries {
		if i.State != "confirmed" || i.RevokedAt.Valid || i.ExpiresAt <= at {
			return nil
		}
	}
	for _, i := range entries {
		active, err := currentIdentity(ctx, q, i)
		if err != nil {
			return err
		}
		if !active {
			return problem(domain.RevisionConflict)
		}
	}
	_, err = q.B08ConfirmDesired(ctx, dbgen.B08ConfirmDesiredParams{SubscriptionID: sub, Revision: revision(rev), Now: at})
	return err
}

func (s *Service) Reconcile(ctx context.Context, id domain.ID) error {
	if !id.Valid() {
		return problem(domain.NotFound)
	}
	// At most reserve, install FK, dispatch, observe, then return. Any uncertain
	// external result returns here; the next sweep resumes the same operation.
	for step := 0; step < 6; step++ {
		if err := s.tick(ctx); err != nil {
			return localError(ctx, err)
		}
		w, err := s.nextWork(ctx, string(id))
		if err != nil {
			return localError(ctx, err)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if w.intent.RevokedAt.Valid && w.intent.CleanupState == "pending" && !w.intent.CleanupTaskID.Valid {
			if recovery, ok := s.o.Publisher.(interface {
				CompensateUnarmed(context.Context, domain.ID) error
			}); ok {
				err := recovery.CompensateUnarmed(ctx, domain.ID(w.intent.ID))
				if err == nil {
					continue
				}
				var p *domain.Problem
				if !errors.As(err, &p) || p.Code != domain.GrantOriginUncertain || w.mode == "" {
					return err
				}
			}
		}
		switch w.mode {
		case "":
			if w.intent.RevokedAt.Valid && w.intent.CleanupState == "pending" && (!w.intent.IdentityID.Valid || !w.intent.DispatchEpoch.Valid) {
				if recovery, ok := s.o.Publisher.(interface {
					CompensateUnarmed(context.Context, domain.ID) error
				}); ok {
					return recovery.CompensateUnarmed(ctx, domain.ID(w.intent.ID))
				}
			}
			return nil
		case "repeat":
			continue
		case "reserve":
			route, err := rowRoute(w.intent)
			if err != nil {
				return localError(ctx, err)
			}
			r, err := reservation(w.intent)
			if err != nil {
				return localError(ctx, err)
			}
			lease, err := s.o.Routes.ReserveIngress(ctx, r)
			if err != nil {
				return safe(ctx, err)
			}
			if err := s.storeLease(ctx, w.intent, route, lease); err != nil {
				return localError(ctx, err)
			}
		case "apply":
			change, err := changeFor(w)
			if err != nil {
				return localError(ctx, err)
			}
			if _, err := rowRoute(w.intent); err != nil {
				return localError(ctx, err)
			}
			task, err := s.o.Routes.ApplyEntry(ctx, change)
			if err != nil {
				return safe(ctx, err)
			}
			if err := s.storeTask(ctx, w, task.TaskID); err != nil {
				return localError(ctx, err)
			}
		case "observe":
			task := domain.ID(w.intent.TaskID.String)
			if w.kind == domain.Cleanup {
				task = domain.ID(w.intent.CleanupTaskID.String)
			}
			result, err := s.o.Publisher.ReleaseState(ctx, task)
			if err != nil {
				return safe(ctx, err)
			}
			if err := s.acceptRelease(ctx, w, task, result); err != nil {
				return localError(ctx, err)
			}
			if w.kind == domain.Cleanup && result.State == "published" {
				if settle, ok := s.o.Publisher.(interface {
					SettleEntryCleanup(context.Context, domain.ID) error
				}); ok {
					return settle.SettleEntryCleanup(ctx, id)
				}
			}
			return nil
		}
	}
	return problem(unavailable)
}
