package runtime

import (
	"context"
	"errors"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

// executeV2 runs under the same admitted-command gate and cancellation scope as
// V1. The durable format is upgraded only after saving the exact bound source.
func (r *Runtime) executeV2(ctx context.Context, c control.Command, data []byte) (control.Result, error) {
	key := keyFor(c)
	b, err := control.DecodeBundleV2(data, key, r.o.NodeID)
	if err != nil || b.BindCommand(c) != nil {
		return r.finishFailure(c, false, "none", domain.InvalidArgument)
	}
	if err := r.o.State.SaveArtifact(ctx, key, data); err != nil {
		return control.Result{}, err
	}
	if err := acquire(ctx, r.kernelGate); err != nil {
		return control.Result{}, err
	}
	defer release(r.kernelGate)
	err = r.save(ctx, func(s *control.DurableState) error {
		now := authorization.Clock(*s, r.o.Now())
		if _, err := admitCommand(*s, c, now); err != nil {
			return err
		}
		if err := r.checkV2PolicyOrigins(ctx, b, *s); err != nil {
			return err
		}
		if err := authorization.MergeV2(s, b, key, now); err != nil {
			return err
		}
		history, err := r.o.State.RestrictionResults(ctx)
		if err != nil {
			return err
		}
		fresh, _ := pendingRestrictions(*s, history)
		if len(fresh) == 0 {
			return nil
		}
		s.Checkpoint.Verified = nil
		return appendRestrictions(s, fresh, r.effectiveBefore(*s), false)
	})
	if err != nil {
		if errors.Is(err, control.ErrAuthorization) {
			return r.finishFailure(c, false, "none", domain.RevisionConflict)
		}
		return control.Result{}, err
	}
	r.signal() // durable restrictions remain effective even if transport disappears
	s, pending, err := r.snapshotState(ctx)
	if err != nil {
		return control.Result{}, err
	}
	next, err := r.renderV2(ctx, b, authorization.ActiveV2(s, b, authorization.Clock(s, r.o.Now())))
	if err != nil {
		return r.rollbackFailure(ctx, c, s, false)
	}
	old, err := r.liveRender(ctx, s)
	if err != nil {
		if stopErr := r.stopKernel(); stopErr != nil {
			return control.Result{}, errors.Join(errUncertain, stopErr)
		}
		return r.rollbackFailure(ctx, c, s, false)
	}
	snap := &control.Snapshot{Format: 2, TaskID: c.TaskID, Revision: c.TargetRevision, ArtifactDigest: c.TargetDigest, EffectiveDigest: next.Digest, Epoch: s.RevocationEpoch}
	app, err := r.apply(ctx, snap, old, next, pending)
	if err != nil || !app.verified && (len(next.Expected) > 0 || len(app.removed) > 0) {
		if r.o.OnApplyFailure != nil {
			r.o.OnApplyFailure(errors.Join(ErrVerification, err))
		}
		return r.rollbackFailure(ctx, c, s, app.attempted)
	}
	// The exact empty owned socket set is sufficient for an empty node. Unknown
	// cleanup targets still cannot be confirmed without their original binding.
	if len(next.Expected) == 0 && len(app.removed) == 0 {
		app.verified = true
	}
	out := baseResult(c)
	out.Applied = app.attempted
	if len(b.Tombstones) > 0 || len(b.RetiredLinks) > 0 || len(app.removed) > 0 {
		out.Cleanup = "pending"
		complete := true
		for _, t := range b.Tombstones {
			found := false
			for _, g := range s.BindingsV2 {
				found = found || g.Listener.Entry != nil && g.Listener.Entry.IdentityID == t.IdentityID && g.Listener.Entry.Generation == t.Generation
			}
			for _, g := range s.Grants {
				found = found || g.Listener.Authorization.IdentityID == t.IdentityID && g.Listener.Authorization.Generation == t.Generation
			}
			complete = complete && found
		}
		for _, t := range b.RetiredLinks {
			found := false
			for _, g := range s.BindingsV2 {
				found = found || g.Listener.Internal != nil && *g.Listener.Internal == t.Link
			}
			complete = complete && found
		}
		if complete && app.verified {
			out.Cleanup = "confirmed"
		}
	}
	if app.verified {
		out.Verification, out.EffectiveDigest = domain.Verified, next.Digest
	}
	if c.Kind == domain.Rollback {
		out.Rollback = "pending"
		if app.verified {
			out.Rollback = "confirmed"
		}
	}
	err = r.save(ctx, func(state *control.DurableState) error {
		now := authorization.Clock(*state, r.o.Now())
		if !now.Before(c.Deadline) || authorization.Due(*state, now) || state.RevocationEpoch != snap.Epoch {
			return errRestricted
		}
		state.ClockFloor = now
		if app.verified {
			state.Checkpoint.Startup, state.Checkpoint.Verified = snap, snap
			if s.Checkpoint.Startup != nil && s.Checkpoint.Startup.ArtifactDigest != snap.ArtifactDigest {
				state.Checkpoint.PreviousGood = s.Checkpoint.Startup
			}
		}
		if out.Verification == domain.Verified || out.Cleanup == "confirmed" || out.Rollback == "confirmed" {
			out.ConfirmedAt = &now
		}
		var err error
		out, err = enqueue(state, out, true)
		if err != nil {
			return err
		}
		return appendRestrictions(state, pending, next.Digest, app.verified)
	})
	if err != nil {
		return r.rollbackFailure(ctx, c, s, app.attempted)
	}
	if len(pending) > 0 {
		r.notify()
	}
	return out, nil
}
