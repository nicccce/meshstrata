package releases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

const validationID domain.ID = "msg_00000000000000000000000000000000"

func resultMessage(node domain.ID, r *control.Result, now time.Time) control.Message {
	return control.Message{Version: control.Version, Type: "result", MessageID: validationID, NodeID: node, SentAt: now, Result: r}
}

func validateMessage(m control.Message, node domain.ID) error {
	if m.Validate(node) != nil {
		return problem(domain.InvalidArgument)
	}
	data, err := json.Marshal(m)
	if err != nil || len(data) > control.MaxMessageBytes {
		return problem(domain.InvalidArgument)
	}
	return nil
}

func (s *Service) RecordResult(ctx context.Context, node domain.ID, r control.Result) (bool, error) {
	now := s.options.Now()
	if err := validateMessage(resultMessage(node, &r, now), node); err != nil {
		return false, err
	}
	payload, err := json.Marshal(r)
	if err != nil {
		return false, problem(domain.InvalidArgument)
	}
	defer clear(payload)
	hash := control.DigestBytes(payload)
	var written bool
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		var err error
		written, err = s.record(ctx, q, r, payload, hash, s.options.Now())
		return err
	})
	if err != nil {
		return false, err
	}
	if written {
		s.signal()
	}
	return written, nil
}

func (s *Service) record(ctx context.Context, q *dbgen.Queries, r control.Result, payload []byte, hash string, now time.Time) (bool, error) {
	node, err := q.GetNode(ctx, string(r.NodeID))
	if err != nil {
		return false, missing(err)
	}
	if node.RemovedAt.Valid {
		return false, problem(domain.Forbidden)
	}
	id := string(r.TaskID)
	target, err := q.B04GetTarget(ctx, dbgen.B04GetTargetParams{TaskID: id, NodeID: node.ID})
	if err != nil {
		return false, missing(err)
	}
	tn, err := q.GetTaskNode(ctx, dbgen.GetTaskNodeParams{TaskID: id, NodeID: node.ID})
	if err != nil {
		return false, err
	}
	if tn.TargetRevision != int64(r.TargetRevision) || tn.TargetDigest != r.TargetDigest {
		return false, problem(domain.RevisionConflict)
	}
	previous, err := q.B04GetResult(ctx, dbgen.B04GetResultParams{TaskID: id, NodeID: node.ID, Sequence: r.Sequence})
	if err == nil {
		if previous.PayloadDigest != hash {
			return false, problem(domain.IdempotencyConflict)
		}
		return false, nil
	}
	if !legitimatelyAbsent(err) {
		return false, err
	}
	a, err := q.B04GetArtifact(ctx, dbgen.B04GetArtifactParams{TaskID: id, NodeID: node.ID})
	if err != nil {
		return false, err
	}
	if a.Format != 1 && a.Format != 2 {
		return false, problem(domain.NotImplemented)
	}
	if a.Format == 2 && s.v2Session(domain.ID(node.ID)) == "" {
		return false, problem(domain.NotImplemented)
	}
	if target.Phase == "queued" || target.Fetched == 0 && (r.Applied || r.Cleanup == "confirmed" || len(r.Restrictions) > 0) {
		return false, problem(domain.RevisionConflict)
	}
	at := stamp(now)
	app := int64(0)
	if r.Applied {
		app = 1
	}
	var errorCode sql.NullString
	if r.Issue != nil {
		errorCode = nullable(string(safeIssue(r.Issue.Code)))
	}
	err = q.B04InsertResult(ctx, dbgen.B04InsertResultParams{TaskID: id, NodeID: node.ID, Sequence: r.Sequence, PayloadDigest: hash, Payload: payload, ReceivedAt: at,
		Applied: app, Verification: string(r.Verification), RollbackState: r.Rollback, CleanupState: r.Cleanup, ErrorCode: errorCode, ConfirmedAt: nullTime(r.ConfirmedAt)})
	if err != nil {
		return false, err
	}
	if len(r.Restrictions) > 0 {
		for _, restriction := range r.Restrictions {
			if err := recordRestriction(ctx, q, r, restriction, tn.RevocationEpoch, node.RevocationEpoch); err != nil {
				return false, err
			}
		}
		return true, nil
	}
	task, err := q.GetTask(ctx, id)
	if err != nil {
		return false, err
	}
	if !active(task.State) || target.Phase != "armed" || tn.ResultSequence >= r.Sequence {
		return true, nil
	}
	if task.Deadline <= at {
		return true, finishTask(ctx, q, id, domain.TimedOut, now)
	}
	if tn.RevocationEpoch != node.RevocationEpoch {
		return true, finishTask(ctx, q, id, domain.TaskFailed, now)
	}
	update := dbgen.RecordTaskResultParams{TaskID: id, NodeID: node.ID, TargetRevision: tn.TargetRevision, TargetDigest: tn.TargetDigest,
		Applied: app, Verification: string(r.Verification), RollbackState: r.Rollback, CleanupState: r.Cleanup, ResultSequence: r.Sequence, ResultSequence_2: r.Sequence,
		ResultDigest: nullable(hash), ErrorCode: errorCode, ConfirmedAt: nullTime(r.ConfirmedAt)}
	if _, err := q.RecordTaskResult(ctx, update); err != nil {
		return false, err
	}
	explicitRollback := task.Kind == string(domain.Rollback) && r.Rollback == "confirmed" && r.Verification == domain.Verified
	// Preserve the last confirmed artifact as history. An unverified
	// mutation cannot leave its node eligible as a verified prerequisite.
	if r.Rollback != "confirmed" && (r.Rollback != "none" ||
		r.Applied && (r.Verification != domain.Verified || r.Cleanup == "pending")) {
		if err := q.B04ObserveHello(ctx, dbgen.B04ObserveHelloParams{NodeID: node.ID, Reconciliation: "pending"}); err != nil {
			return false, err
		}
	}
	if r.Verification == domain.Failed || r.Issue != nil || r.Rollback != "none" && !explicitRollback {
		if _, err := q.B04FinishTarget(ctx, dbgen.B04FinishTargetParams{TaskID: id, NodeID: node.ID, Phase: "failed"}); err != nil {
			return false, err
		}
		return true, finishTask(ctx, q, id, domain.TaskFailed, now)
	}
	grants, err := q.B04TargetGrants(ctx, dbgen.B04TargetGrantsParams{TaskID: id, NodeID: node.ID})
	if err != nil {
		return false, err
	}
	included, removed := 0, 0
	for _, g := range grants {
		if g.Action == "include" {
			included++
			if g.RestrictedAt.Valid || g.ExpiresAt <= at {
				return true, finishTask(ctx, q, id, domain.TaskFailed, now)
			}
		} else if !g.ConfirmedAt.Valid {
			removed++
		}
	}
	cleanup := task.Kind == string(domain.Cleanup) || task.Kind == string(domain.Revoke) || task.Kind == string(domain.Rollback)
	if a.Format == 2 && removed > 0 {
		cleanup = true
	}
	verified := r.Verification == domain.Verified && r.Applied
	if r.Cleanup == "confirmed" && cleanup && (r.Rollback == "none" || explicitRollback) && (included == 0 || verified) {
		for _, g := range grants {
			if g.Action == "include" || g.ConfirmedAt.Valid {
				continue
			}
			if err := confirmCleanup(ctx, q, node.ID, g.IdentityID, id, stamp(*r.ConfirmedAt)); err != nil {
				return false, err
			}
		}
		removed = 0
	}
	if a.Format == 2 {
		links, e := q.I03TargetLinks(ctx, dbgen.I03TargetLinksParams{TaskID: id, NodeID: node.ID})
		if e != nil {
			return false, e
		}
		for _, l := range links {
			if l.Action == "retire" && (r.Cleanup != "confirmed" || !r.Applied) {
				return true, nil
			}
		}
	}
	emptyCleanup := included == 0 && len(grants) > 0 && cleanup && r.Cleanup == "confirmed" && r.Rollback == "none"
	if removed > 0 || (!verified && !emptyCleanup) {
		return true, nil
	}
	if r.Applied {
		if err := q.B04ObserveApplied(ctx, dbgen.B04ObserveAppliedParams{NodeID: node.ID, AppliedRevision: tn.TargetRevision,
			ArtifactDigest: tn.TargetDigest, EffectiveDigest: r.EffectiveDigest, Epoch: tn.RevocationEpoch, EvidenceAt: stamp(*r.ConfirmedAt)}); err != nil {
			return false, err
		}
	}
	refs, err := q.B04SupersededReferences(ctx, dbgen.B04SupersededReferencesParams{NodeID: node.ID, Revision: tn.TargetRevision, NewTask: id})
	if err != nil {
		return false, err
	}
	if err := retireReferences(ctx, q, refs, id, at); err != nil {
		return false, err
	}
	if err := q.B04SweepPorts(ctx, dbgen.B04SweepPortsParams{NodeID: node.ID, At: nullable(at)}); err != nil {
		return false, err
	}
	if _, err := q.B04FinishTarget(ctx, dbgen.B04FinishTargetParams{TaskID: id, NodeID: node.ID, Phase: "confirmed"}); err != nil {
		return false, err
	}
	n, err := q.B04UnconfirmedCount(ctx, id)
	if err != nil || n > 0 {
		return err == nil, err
	}
	return true, finishTask(ctx, q, id, domain.Succeeded, now)
}

func recordRestriction(ctx context.Context, q *dbgen.Queries, r control.Result, e control.Restriction, originEpoch, nodeEpoch int64) error {
	grant, err := q.B04GetGrant(ctx, string(e.IdentityID))
	if err != nil {
		return missing(err)
	}
	if grant.NodeID != string(r.NodeID) || grant.ListenerID != string(e.ListenerID) || grant.Generation != int64(e.Generation) ||
		!grant.OriginTaskID.Valid || grant.OriginTaskID.String != string(r.TaskID) ||
		int64(e.Epoch) < originEpoch || int64(e.Epoch) > nodeEpoch {
		return problem(domain.RevisionConflict)
	}
	observed := stamp(e.ObservedAt)
	if e.Reason == "expired" && observed < grant.ExpiresAt {
		return problem(domain.RevisionConflict)
	}
	if e.Reason == "revoked" {
		allowed, err := q.B04RevokeAuthorized(ctx, dbgen.B04RevokeAuthorizedParams{IdentityID: grant.IdentityID, NodeID: grant.NodeID, Epoch: int64(e.Epoch)})
		if err != nil {
			return err
		}
		if !allowed {
			return problem(domain.RevisionConflict)
		}
	}
	if r.Cleanup == "confirmed" && (r.Issue != nil || r.ConfirmedAt.Before(e.ObservedAt)) {
		return problem(domain.InvalidArgument)
	}
	err = q.B04InsertRestriction(ctx, dbgen.B04InsertRestrictionParams{TaskID: string(r.TaskID), NodeID: grant.NodeID, Sequence: r.Sequence,
		IdentityID: grant.IdentityID, ListenerID: grant.ListenerID, Generation: grant.Generation, Reason: e.Reason,
		ObservedAt: observed, Epoch: int64(e.Epoch), EffectiveDigest: e.EffectiveDigest})
	if err != nil {
		return err
	}
	if err := markPending(ctx, q, grant.NodeID, grant.IdentityID, string(r.TaskID)+"/"+strconv.FormatInt(r.Sequence, 10), observed); err != nil {
		return err
	}
	if err := q.B04ObserveRestriction(ctx, dbgen.B04ObserveRestrictionParams{NodeID: grant.NodeID, Epoch: int64(e.Epoch),
		EvidenceAt: observed, EffectiveDigest: e.EffectiveDigest, EvidenceTaskID: string(r.TaskID), EvidenceSequence: r.Sequence}); err != nil {
		return err
	}
	if r.Cleanup == "confirmed" {
		return confirmCleanup(ctx, q, grant.NodeID, grant.IdentityID, string(r.TaskID)+"/"+strconv.FormatInt(r.Sequence, 10), stamp(*r.ConfirmedAt))
	}
	return nil
}

func safeIssue(code domain.ErrorCode) domain.ErrorCode {
	switch code {
	case domain.InvalidArgument, domain.Unauthenticated, domain.Forbidden, domain.NotFound, domain.RevisionConflict,
		domain.IdempotencyConflict, domain.PortInUse, domain.DependencyInUse, domain.NotImplemented, domain.TaskExpired:
		return code
	default:
		return "RELEASE_FAILED"
	}
}

func (s *Service) Hello(ctx context.Context, node domain.ID, h control.Hello) error {
	if err := validateMessage(control.Message{Version: control.Version, Type: "hello", MessageID: validationID, NodeID: node, SentAt: s.options.Now(), Hello: &h}, node); err != nil {
		return err
	}
	hashes := make([]string, len(h.PendingResults))
	for i, r := range h.PendingResults {
		hash, err := hashPayload(r)
		if err != nil {
			return err
		}
		hashes[i] = hash
	}
	status := "pending"
	// B02 reports a newly registered peer as unready until this Hello returns.
	// Subsequent Hello pages on the same peer must not reset the delivery wait.
	initial := !s.options.Connected(node)
	err := s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		n, err := q.GetNode(ctx, string(node))
		if err != nil {
			return missing(err)
		}
		if n.RemovedAt.Valid {
			return problem(domain.Forbidden)
		}
		// B02 persists every page entry and sends its receipt before calling
		// Recovery. Check that boundary without relying on Hello as evidence.
		for i, r := range h.PendingResults {
			old, err := q.B04GetResult(ctx, dbgen.B04GetResultParams{TaskID: string(r.TaskID), NodeID: n.ID, Sequence: r.Sequence})
			if err != nil {
				return missing(err)
			}
			if old.PayloadDigest != hashes[i] {
				return problem(domain.IdempotencyConflict)
			}
		}
		state, err := q.B04GetState(ctx, n.ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		switch {
		case int64(h.RevocationEpoch) > n.RevocationEpoch:
			status = "conflict"
		case h.AppliedRevision > 0:
			artifact, e := q.B04ArtifactAtRevision(ctx, dbgen.B04ArtifactAtRevisionParams{NodeID: n.ID, TargetRevision: int64(h.AppliedRevision)})
			if e != nil && !legitimatelyAbsent(e) {
				return e
			}
			if e == nil {
				target, err := q.B04GetTarget(ctx, dbgen.B04GetTargetParams{TaskID: artifact.TaskID, NodeID: n.ID})
				if err != nil {
					return err
				}
				if target.Fetched != 0 && artifact.TargetDigest == h.AppliedDigest {
					if int64(h.AppliedRevision) == state.AppliedRevision && int64(h.RevocationEpoch) == n.RevocationEpoch {
						status = "matched"
					}
					break
				}
			}
			status = "conflict"
		case state.AppliedRevision == 0 && int64(h.RevocationEpoch) == n.RevocationEpoch:
			status = "matched"
		}
		if err := q.B04ObserveHello(ctx, dbgen.B04ObserveHelloParams{NodeID: n.ID, Reconciliation: status}); err != nil {
			return err
		}
		if status == "conflict" || !initial {
			return nil
		}
		return q.B04WakeNode(ctx, n.ID)
	})
	if err != nil {
		return err
	}
	if status == "conflict" {
		return problem(domain.RevisionConflict)
	}
	s.signal()
	return nil
}
