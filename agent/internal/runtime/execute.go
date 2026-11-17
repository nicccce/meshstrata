package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func keyFor(c control.Command) control.ArtifactKey {
	return control.ArtifactKey{TaskID: c.TaskID, NodeID: c.NodeID, TargetRevision: c.TargetRevision, TargetDigest: c.TargetDigest}
}

func (r *Runtime) Execute(ctx context.Context, c control.Command) (control.Result, error) {
	if !r.available() {
		return control.Result{}, ErrUnavailable
	}
	switch c.Kind {
	case domain.Publish, domain.Rollback, domain.Revoke, domain.Cleanup:
	default:
		return control.Result{}, control.ErrBinding
	}
	c.Deadline = c.Deadline.UTC()
	m := control.Message{Version: control.Version, Type: "command", MessageID: r.messageID, NodeID: r.o.NodeID,
		SentAt: c.Deadline.Add(-time.Nanosecond), Command: &c}
	if m.Validate(r.o.NodeID) != nil {
		return control.Result{}, control.ErrBinding
	}
	if err := acquire(ctx, r.commandGate); err != nil {
		return control.Result{}, err
	}
	defer release(r.commandGate)
	ctx, done := r.operation(ctx)
	defer done()

	s, _, err := r.snapshotState(ctx)
	if err != nil {
		return control.Result{}, err
	}
	previous, err := admitCommand(s, c, authorization.Clock(s, r.o.Now()))
	if err != nil {
		return control.Result{}, err
	}
	if previous != nil && previous.LastResult != nil {
		return *previous.LastResult, nil
	}
	if previous == nil {
		err = r.save(ctx, func(s *control.DurableState) error {
			now := authorization.Clock(*s, r.o.Now())
			if _, err := admitCommand(*s, c, now); err != nil {
				return err
			}
			s.ClockFloor = now
			s.Executions = append(s.Executions, control.Execution{Command: c})
			return nil
		})
		if err != nil {
			return control.Result{}, err
		}
	}
	limit := c.Deadline.Sub(authorization.Clock(s, r.o.Now()))
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	key := keyFor(c)
	data, err := r.o.State.ReadArtifact(ctx, key)
	if errors.Is(err, os.ErrNotExist) {
		data, err = r.o.Fetch(ctx, key, control.MaxBundleBytes)
		if err != nil {
			return control.Result{}, ErrUnavailable
		}
		if len(data) > control.MaxBundleBytes {
			return r.finishFailure(c, false, "none", domain.InvalidArgument)
		}
	} else if err != nil {
		return control.Result{}, err
	}
	b, err := control.DecodeBundle(data, key, r.o.NodeID)
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
	if err := r.recordBundle(ctx, c, b, key); err != nil {
		if errors.Is(err, control.ErrAuthorization) {
			return r.finishFailure(c, false, "none", domain.RevisionConflict)
		}
		return control.Result{}, err
	}
	s, pending, err := r.snapshotState(ctx)
	if err != nil {
		return control.Result{}, err
	}
	now := authorization.Clock(s, r.o.Now())
	next, err := r.render(ctx, b, authorization.Active(s, b, now))
	if err != nil {
		// New restrictions may already be durable while the prior instance
		// still serves. Reconcile in the independent worker.
		r.signal()
		return r.rollbackFailure(ctx, c, s, false)
	}
	old, err := r.liveRender(ctx, s)
	if err != nil {
		if stopErr := r.stopKernel(); stopErr != nil {
			return control.Result{}, errors.Join(errUncertain, stopErr)
		}
		r.signal()
		return r.rollbackFailure(ctx, c, s, false)
	}
	snap := &control.Snapshot{TaskID: c.TaskID, Revision: c.TargetRevision, ArtifactDigest: c.TargetDigest,
		EffectiveDigest: next.Digest, Epoch: s.RevocationEpoch}
	application, err := r.apply(ctx, snap, old, next, pending)
	if err != nil || (!application.verified && (len(next.Expected) > 0 || len(application.removed) > 0)) {
		if r.o.OnApplyFailure != nil {
			if err == nil {
				err = ErrVerification
			}
			r.o.OnApplyFailure(err)
		}
		return r.rollbackFailure(ctx, c, s, application.attempted)
	}
	out := baseResult(c)
	out.Applied = application.attempted
	if len(b.Tombstones) > 0 {
		out.Cleanup = "pending"
		// Unknown grants have no local binding that can prove cleanup.
		known := make(map[domain.ID]bool, len(s.Grants))
		for _, g := range s.Grants {
			known[g.Listener.Authorization.IdentityID] = true
		}
		complete := true
		for _, tomb := range b.Tombstones {
			if !known[tomb.IdentityID] {
				complete = false
				break
			}
		}
		if complete {
			// Pending known grants were included in the revocation probe.
			// The other known grants already have durable confirmed evidence.
			out.Cleanup = "confirmed"
			application.verified = true
		}
	} else if len(application.removed) > 0 {
		out.Cleanup = "confirmed"
	}
	if application.verified {
		out.Verification = domain.Verified
		out.EffectiveDigest = next.Digest
	}
	if c.Kind == domain.Rollback {
		out.Rollback = "pending"
		if application.verified {
			out.Rollback = "confirmed"
		}
	}
	err = r.save(ctx, func(state *control.DurableState) error {
		now := authorization.Clock(*state, r.o.Now())
		if !now.Before(c.Deadline) || authorization.Due(*state, now) || state.RevocationEpoch != snap.Epoch {
			return errRestricted
		}
		state.ClockFloor = now
		if application.verified {
			state.Checkpoint.Startup = snap
			state.Checkpoint.Verified = snap
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
		return appendRestrictions(state, pending, next.Digest, true)
	})
	if err != nil {
		return r.rollbackFailure(ctx, c, s, application.attempted)
	}
	if len(pending) > 0 {
		r.notify()
	}
	return out, nil
}

func admitCommand(s control.DurableState, c control.Command, now time.Time) (*control.Execution, error) {
	var same *control.Execution
	for i := range s.Executions {
		old := &s.Executions[i]
		if old.Command.TaskID != c.TaskID && old.Command.IdempotencyKey != c.IdempotencyKey {
			continue
		}
		if !reflect.DeepEqual(old.Command, c) {
			return nil, &domain.Problem{Code: domain.IdempotencyConflict}
		}
		same = old
		if old.LastResult != nil {
			return same, nil
		}
	}
	if !now.Before(c.Deadline) {
		return nil, &domain.Problem{Code: domain.TaskExpired}
	}
	if c.RevocationEpoch < s.RevocationEpoch {
		return nil, &domain.Problem{Code: domain.RevisionConflict}
	}
	for _, old := range s.Executions {
		if old.Command.TaskID != c.TaskID && old.Command.TargetRevision >= c.TargetRevision {
			return nil, &domain.Problem{Code: domain.RevisionConflict}
		}
	}
	return same, nil
}

func (r *Runtime) recordBundle(ctx context.Context, c control.Command, b control.Bundle, key control.ArtifactKey) error {
	freshCount := 0
	err := r.save(ctx, func(s *control.DurableState) error {
		now := authorization.Clock(*s, r.o.Now())
		if _, err := admitCommand(*s, c, now); err != nil {
			return err
		}
		if err := authorization.Merge(s, b, key, now); err != nil {
			return err
		}
		history, err := r.o.State.RestrictionResults(ctx)
		if err != nil {
			return err
		}
		fresh, _ := pendingRestrictions(*s, history)
		freshCount = len(fresh)
		if freshCount == 0 {
			return nil
		}
		digest := r.effectiveBefore(*s)
		s.Checkpoint.Verified = nil
		return appendRestrictions(s, fresh, digest, false)
	})
	if err == nil && freshCount > 0 {
		// A controller command can add restrictions, but the pending commit
		// may outlive its transport. Signal the independent reconciliation loop.
		r.signal()
	}
	return err
}

func (r *Runtime) finishFailure(c control.Command, attempted bool, rollback string, code domain.ErrorCode) (control.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out := baseResult(c)
	out.Applied = attempted
	out.Rollback = rollback
	out.Verification = domain.Failed
	if c.Kind == domain.Revoke || c.Kind == domain.Cleanup {
		out.Cleanup = "pending"
	}
	out.Issue = &domain.Problem{Code: code}
	err := r.save(ctx, func(s *control.DurableState) error {
		if rollback == "confirmed" {
			now := authorization.Clock(*s, r.o.Now())
			s.ClockFloor = now
			out.ConfirmedAt = &now
		}
		var err error
		out, err = enqueue(s, out, true)
		return err
	})
	return out, err
}

func (r *Runtime) rollbackFailure(ctx context.Context, c control.Command, before control.DurableState, attempted bool) (control.Result, error) {
	if errors.Is(context.Cause(ctx), errRestricted) {
		r.signal()
		return r.finishFailure(c, attempted, "pending", domain.RevisionConflict)
	}
	// Roll back even when the Controller transport has disconnected. The
	// independent expiry clock can still cancel this operation.
	local, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	local, done := r.operation(local)
	defer done()
	err := r.restore(local, before.Checkpoint.Startup, false, true, attempted)
	if err == nil {
		return r.finishFailure(c, attempted, "confirmed", domain.ErrorCode("AGENT_APPLY_FAILED"))
	}
	if errors.Is(err, errRestricted) || r.lifetime.Err() != nil || errors.Is(context.Cause(local), errRestricted) {
		stopErr := r.stopKernel()
		r.signal()
		if stopErr != nil {
			return control.Result{}, errors.Join(errUncertain, stopErr)
		}
		return r.finishFailure(c, attempted, "pending", domain.ErrorCode("AGENT_APPLY_FAILED"))
	}
	if r.o.OnApplyFailure != nil {
		r.o.OnApplyFailure(fmt.Errorf("Mihomo restore failed: %w", err))
	}
	return r.finishFailure(c, attempted, "failed", domain.ErrorCode("AGENT_APPLY_FAILED"))
}
