package checkpoint

import (
	"encoding/hex"
	"reflect"
	"sort"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func validateMessage(node domain.ID, kind string, result *control.Result, receipt *control.Receipt) error {
	return (control.Message{Version: control.Version, Type: kind, MessageID: "checkpoint_00000000000000000000000000000000", NodeID: node,
		SentAt: time.Unix(1, 0), Result: result, Receipt: receipt}).Validate(node)
}

type resultKey struct {
	task     domain.ID
	sequence int64
}

func validState(s control.DurableState, node domain.ID) error {
	if s.Checkpoint.NodeID != node || (s.Revision != 0 && !s.Revision.Valid()) || !s.RevocationEpoch.Valid() || s.ClockFloor.IsZero() {
		return ErrState
	}
	for _, snap := range []*control.Snapshot{s.Checkpoint.Candidate, s.Checkpoint.Applied, s.Checkpoint.Verified, s.Checkpoint.Startup, s.Checkpoint.PreviousGood} {
		if snap != nil && (snap.Validate() != nil || snap.Epoch > s.RevocationEpoch) {
			return ErrState
		}
	}
	tombs := make(map[domain.ID]domain.Revision, len(s.Tombstones))
	for _, t := range s.Tombstones {
		if !t.IdentityID.Valid() || !t.Generation.Valid() || !t.Epoch.Valid() || t.Epoch > s.RevocationEpoch || t.At.IsZero() ||
			(t.Reason != "revoked" && t.Reason != "expired") || tombs[t.IdentityID] != 0 {
			return ErrState
		}
		tombs[t.IdentityID] = t.Generation
	}
	grants := make(map[domain.ID]control.Grant, len(s.Grants))
	for _, g := range s.Grants {
		a := g.Listener.Authorization
		_, exists := grants[a.IdentityID]
		if exists || !a.IdentityID.Valid() || !a.EntryID.Valid() || !a.Generation.Valid() || a.ExpiresAt.IsZero() ||
			(tombs[a.IdentityID] != 0 && tombs[a.IdentityID] != a.Generation) ||
			!g.Listener.ListenerID.Valid() || g.Listener.NodeID != node || g.Listener.Credential.Audience != node || g.Origin.NodeID != node ||
			!validTarget(g.Origin.TaskID, g.Origin.TargetRevision, g.Origin.TargetDigest) {
			return ErrState
		}
		grants[a.IdentityID] = g
	}
	executions := make(map[domain.ID]control.Execution, len(s.Executions))
	keys := make(map[string]bool, len(s.Executions))
	for _, ex := range s.Executions {
		c := ex.Command
		_, exists := executions[c.TaskID]
		if exists || c.NodeID != node || !validTarget(c.TaskID, c.TargetRevision, c.TargetDigest) ||
			c.IdempotencyKey == "" || keys[c.IdempotencyKey] || ex.Sequence < 0 || ex.Sequence > int64(domain.MaxRevision) {
			return ErrState
		}
		if r := ex.LastResult; r != nil && (r.Sequence > ex.Sequence || len(r.Restrictions) != 0 ||
			!matches(c, *r) || validateMessage(node, "result", r, nil) != nil) {
			return ErrState
		}
		executions[c.TaskID] = ex
		keys[c.IdempotencyKey] = true
	}
	for _, grant := range s.Grants {
		c := executions[grant.Origin.TaskID].Command
		if c.NodeID != node || c.TargetRevision != grant.Origin.TargetRevision || c.TargetDigest != grant.Origin.TargetDigest {
			return ErrState
		}
	}
	seen := make(map[resultKey]bool, len(s.Outbox))
	for i := range s.Outbox {
		r := s.Outbox[i]
		ex := executions[r.TaskID]
		key := resultKey{r.TaskID, r.Sequence}
		if seen[key] || !matches(ex.Command, r) || r.Sequence > ex.Sequence || validateMessage(node, "result", &r, nil) != nil {
			return ErrState
		}
		seen[key] = true
		for _, e := range r.Restrictions {
			g := grants[e.IdentityID]
			if g.Origin.TaskID != r.TaskID || g.Listener.ListenerID != e.ListenerID || tombs[e.IdentityID] != e.Generation ||
				e.Epoch > s.RevocationEpoch || e.ObservedAt.After(s.ClockFloor) ||
				(r.Cleanup == "confirmed" && r.ConfirmedAt.Before(e.ObservedAt)) {
				return ErrState
			}
		}
	}
	return nil
}

func validTarget(task domain.ID, revision domain.Revision, digest string) bool {
	d, err := hex.DecodeString(digest)
	return task.Valid() && revision.Valid() && err == nil && len(d) == 32 && hex.EncodeToString(d) == digest
}

func matches(c control.Command, r control.Result) bool {
	return c.TaskID == r.TaskID && c.NodeID == r.NodeID && c.TargetRevision == r.TargetRevision && c.TargetDigest == r.TargetDigest
}

func mergeState(old control.DurableState, next *control.DurableState) error {
	if old.ClockFloor.After(next.ClockFloor) {
		next.ClockFloor = old.ClockFloor
	}
	next.RevocationEpoch = max(old.RevocationEpoch, next.RevocationEpoch)
	grants := make(map[domain.ID]control.Grant, len(old.Grants))
	for _, g := range old.Grants {
		grants[g.Listener.Authorization.IdentityID] = g
	}
	for _, g := range next.Grants {
		id := g.Listener.Authorization.IdentityID
		if prev, ok := grants[id]; ok {
			if control.CheckGrant(prev, g.Listener) != nil {
				return ErrState
			}
			continue
		}
		grants[id] = g
	}
	next.Grants = nil
	for _, g := range grants {
		next.Grants = append(next.Grants, g)
	}
	sort.Slice(next.Grants, func(i, j int) bool {
		return next.Grants[i].Listener.Authorization.IdentityID < next.Grants[j].Listener.Authorization.IdentityID
	})
	tombs := make(map[domain.ID]control.Tombstone)
	for _, list := range [][]control.Tombstone{old.Tombstones, next.Tombstones} {
		for _, t := range list {
			if prev, ok := tombs[t.IdentityID]; ok {
				if prev.Generation != t.Generation {
					return ErrState
				}
				t.Epoch = max(t.Epoch, prev.Epoch)
				if prev.At.Before(t.At) {
					t.At = prev.At
				}
				if prev.Reason == "revoked" {
					t.Reason = prev.Reason
				}
			}
			tombs[t.IdentityID] = t
		}
	}
	next.Tombstones = nil
	for _, t := range tombs {
		next.Tombstones = append(next.Tombstones, t)
	}
	sort.Slice(next.Tombstones, func(i, j int) bool { return next.Tombstones[i].IdentityID < next.Tombstones[j].IdentityID })
	return mergeExecutions(old, next)
}

func mergeExecutions(old control.DurableState, next *control.DurableState) error {
	oldExec := make(map[domain.ID]control.Execution, len(old.Executions))
	for _, e := range old.Executions {
		oldExec[e.Command.TaskID] = e
	}
	oldOut := make(map[resultKey]control.Result, len(old.Outbox))
	for _, r := range old.Outbox {
		oldOut[resultKey{r.TaskID, r.Sequence}] = r
	}
	added := map[domain.ID]int64{}
	var appended []control.Result
	for _, r := range next.Outbox {
		key := resultKey{r.TaskID, r.Sequence}
		if prev, ok := oldOut[key]; ok {
			if !reflect.DeepEqual(r, prev) {
				return ErrState
			}
			continue
		}
		added[r.TaskID]++
		if r.Sequence != oldExec[r.TaskID].Sequence+added[r.TaskID] {
			return ErrState
		}
		appended = append(appended, r)
	}
	for i, e := range next.Executions {
		prev, exists := oldExec[e.Command.TaskID]
		if exists && !reflect.DeepEqual(prev.Command, e.Command) {
			return ErrState
		}
		if e.Sequence != prev.Sequence+added[e.Command.TaskID] {
			return ErrState
		}
		if prev.LastResult != nil {
			if e.LastResult != nil && !reflect.DeepEqual(e.LastResult, prev.LastResult) {
				return ErrState
			}
			next.Executions[i].LastResult = prev.LastResult
		} else if e.LastResult != nil {
			found := false
			for _, r := range appended {
				if reflect.DeepEqual(&r, e.LastResult) {
					found = true
					break
				}
			}
			if !found {
				return ErrState
			}
		}
		delete(oldExec, e.Command.TaskID)
	}
	for _, prev := range oldExec {
		next.Executions = append(next.Executions, prev)
	}
	next.Outbox = append(old.Outbox, appended...)
	return nil
}
