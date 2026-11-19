package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type restriction struct {
	grant control.Grant
	tomb  control.Tombstone
	v2    *control.BindingV2
}

func (v restriction) origin() control.ArtifactKey {
	if v.v2 != nil {
		return v.v2.Origin
	}
	return v.grant.Origin
}
func (v restriction) listener() control.BundleListener {
	if v.v2 != nil {
		return transportV2(v.v2.Origin.NodeID, v.v2.Listener)
	}
	return v.grant.Listener
}

func pendingRestrictions(s control.DurableState, history map[domain.ID]control.Result) (fresh, pending []restriction) {
	tombs := make(map[domain.ID]control.Tombstone, len(s.Tombstones))
	for _, t := range s.Tombstones {
		tombs[t.IdentityID] = t
	}
	for _, g := range s.Grants {
		id := g.Listener.Authorization.IdentityID
		t, ok := tombs[id]
		if !ok {
			continue
		}
		last := history[id]
		observed := false
		for _, e := range last.Restrictions {
			if e.IdentityID == id && e.Reason == t.Reason {
				observed = true
				break
			}
		}
		item := restriction{grant: g, tomb: t}
		if !observed {
			fresh = append(fresh, item)
		}
		if !observed || last.Cleanup != "confirmed" {
			pending = append(pending, item)
		}
	}
	for _, g := range s.BindingsV2 {
		l := g.Listener
		if l.Entry == nil {
			for _, r := range s.RetiredLinks {
				if r.Link != *l.Internal {
					continue
				}
				confirmed := false
				for _, c := range s.LinkCleanupsV2 {
					confirmed = confirmed || c.Link == r.Link
				}
				if !confirmed {
					pending = append(pending, restriction{v2: &g})
				}
			}
			continue
		}
		t, ok := tombs[l.Entry.IdentityID]
		if !ok {
			continue
		}
		last := history[t.IdentityID]
		observed := false
		for _, e := range last.Restrictions {
			observed = observed || e.IdentityID == t.IdentityID && e.Reason == t.Reason
		}
		item := restriction{v2: &g, tomb: t}
		if !observed {
			fresh = append(fresh, item)
		}
		if !observed || last.Cleanup != "confirmed" {
			pending = append(pending, item)
		}
	}
	return fresh, pending
}

func baseResult(c control.Command) control.Result {
	return control.Result{TaskID: c.TaskID, NodeResult: domain.NodeResult{
		NodeID: c.NodeID, TargetRevision: c.TargetRevision, TargetDigest: c.TargetDigest,
		Verification: domain.Incomplete, Rollback: "none", Cleanup: "none",
	}}
}

func execution(s *control.DurableState, id domain.ID) *control.Execution {
	for i := range s.Executions {
		if s.Executions[i].Command.TaskID == id {
			return &s.Executions[i]
		}
	}
	return nil
}

func enqueue(s *control.DurableState, out control.Result, commandResult bool) (control.Result, error) {
	x := execution(s, out.TaskID)
	if x == nil || x.Sequence >= int64(domain.MaxRevision) {
		return control.Result{}, errUncertain
	}
	x.Sequence++
	out.Sequence = x.Sequence
	s.Outbox = append(s.Outbox, out)
	if commandResult {
		x.LastResult = &out
	}
	return out, nil
}

func appendRestrictions(s *control.DurableState, items []restriction, digest string, confirmed bool) error {
	if len(items) == 0 {
		return nil
	}
	groups := make(map[domain.ID][]restriction)
	var order []domain.ID
	for _, item := range items {
		if item.v2 != nil && item.v2.Listener.Internal != nil {
			if confirmed {
				s.LinkCleanupsV2 = append(s.LinkCleanupsV2, control.LinkCleanupV2{Link: *item.v2.Listener.Internal, EffectiveDigest: digest, ConfirmedAt: s.ClockFloor})
			}
			continue
		}
		id := item.origin().TaskID
		if len(groups[id]) == 0 {
			order = append(order, id)
		}
		groups[id] = append(groups[id], item)
	}
	for _, id := range order {
		x := execution(s, id)
		if x == nil {
			return errUncertain
		}
		for offset := 0; offset < len(groups[id]); offset += 32 {
			out := baseResult(x.Command)
			out.Cleanup = "pending"
			if confirmed {
				out.Cleanup = "confirmed"
				t := s.ClockFloor
				out.ConfirmedAt = &t
			}
			for _, item := range groups[id][offset:min(offset+32, len(groups[id]))] {
				out.Restrictions = append(out.Restrictions, control.Restriction{
					IdentityID: item.tomb.IdentityID, ListenerID: item.listener().ListenerID, Generation: item.tomb.Generation,
					Reason: item.tomb.Reason, ObservedAt: s.ClockFloor, Epoch: s.RevocationEpoch, EffectiveDigest: digest,
				})
			}
			if _, err := enqueue(s, out, false); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runtime) notify() {
	if r.o.OnPending != nil {
		select {
		case r.pendingNotification <- struct{}{}:
		default:
		}
	}
}

func (r *Runtime) expire(ctx context.Context) (bool, bool, error) {
	r.stateMu.Lock()
	changed, pending, err := r.expireLocked(ctx)
	if changed {
		r.urgent.Store(true)
		r.interrupt(errRestricted)
	}
	r.stateMu.Unlock()
	if changed && err == nil {
		r.notify()
	}
	return changed, pending, err
}

func (r *Runtime) expireLocked(ctx context.Context) (bool, bool, error) {
	s, err := r.o.State.Load(ctx)
	if err != nil {
		return false, false, err
	}
	now := authorization.Clock(s, r.o.Now())
	restrict := authorization.Due(s, now)
	hasActive := !authorization.NextExpiry(s).IsZero()
	if restrict {
		authorization.Expire(&s, now)
	}
	history, err := r.o.State.RestrictionResults(ctx)
	if err != nil {
		return false, false, err
	}
	fresh, pending := pendingRestrictions(s, history)
	if !restrict && len(fresh) == 0 && (!hasActive || now.Sub(s.ClockFloor) < time.Second) {
		return false, len(pending) > 0, nil
	}
	s.ClockFloor = now
	if len(fresh) > 0 {
		digest := r.effectiveBefore(s)
		s.Checkpoint.Verified = nil
		if err := appendRestrictions(&s, fresh, digest, false); err != nil {
			return true, true, err
		}
	}
	err = r.o.State.Commit(ctx, s.Revision, s)
	return restrict || len(fresh) > 0, len(pending) > 0, err
}

func (r *Runtime) effectiveBefore(s control.DurableState) string {
	for _, snap := range []*control.Snapshot{s.Checkpoint.Applied, s.Checkpoint.Verified, s.Checkpoint.Startup} {
		if snap != nil {
			return snap.EffectiveDigest
		}
	}
	return r.empty.Digest
}

func (r *Runtime) snapshotState(ctx context.Context) (control.DurableState, []restriction, error) {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	s, err := r.o.State.Load(ctx)
	if err != nil {
		return s, nil, err
	}
	history, err := r.o.State.RestrictionResults(ctx)
	if err != nil {
		return s, nil, err
	}
	_, pending := pendingRestrictions(s, history)
	return s, pending, nil
}

func (r *Runtime) save(ctx context.Context, change func(*control.DurableState) error) error {
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := r.o.State.Load(ctx)
	if err != nil {
		return errors.Join(errUncertain, err)
	}
	if err := change(&s); err != nil {
		return err
	}
	if err := r.o.State.Commit(ctx, s.Revision, s); err != nil {
		return errors.Join(errUncertain, err)
	}
	return nil
}

func (r *Runtime) confirmRestrictions(ctx context.Context, snap *control.Snapshot, prior *control.Snapshot, pending []restriction, digest string, verified bool) error {
	if err := r.save(ctx, func(s *control.DurableState) error {
		now := authorization.Clock(*s, r.o.Now())
		if authorization.Due(*s, now) {
			return errRestricted
		}
		s.ClockFloor = now
		if verified {
			s.Checkpoint.Verified, s.Checkpoint.Startup = snap, snap
			if prior != nil {
				s.Checkpoint.PreviousGood = prior
			}
		}
		return appendRestrictions(s, pending, digest, true)
	}); err != nil {
		return err
	}
	return nil
}
