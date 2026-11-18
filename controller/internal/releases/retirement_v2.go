package releases

import (
	"context"
	"slices"
	"strings"

	"github.com/nicccce/meshstrata/controller/internal/routes"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

// RetireInternal submits a permanent internal tombstone only after B06 has
// retired its final route/entry reference. Port release waits for real cleanup.
func (s *Service) RetireInternal(ctx context.Context, m domain.Mutation, pin control.Pin) (domain.TaskAccepted, error) {
	if !mutationValid(m) || !pin.Valid() || m.ExpectedRevision != pin.Revision {
		return domain.TaskAccepted{}, problem(domain.InvalidArgument)
	}
	hash, e := retirementHash(m, pin)
	if e != nil {
		return domain.TaskAccepted{}, e
	}
	var replay domain.TaskAccepted
	e = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		replay, err = previousAction(ctx, q, "i03:retire:"+string(pin.ID), m.IdempotencyKey, hash)
		return err
	})
	if e != nil || replay.TaskID != "" {
		return replay, e
	}
	var row dbgen.B06InternalLink
	e = s.repo.db.Read(ctx, func(q *dbgen.Queries) error { var e error; row, e = q.B06LinkByID(ctx, string(pin.ID)); return e })
	if e != nil {
		return domain.TaskAccepted{}, e
	}
	if !pin.Valid() || row.SuffixDigest != pin.Digest || row.Revision != int64(pin.Revision) || row.State != "retiring" {
		return domain.TaskAccepted{}, problem(domain.DependencyInUse)
	}
	snap, e := s.NodeSnapshot(ctx, domain.ID(row.NodeID))
	if e != nil {
		return domain.TaskAccepted{}, e
	}
	if snap.Extended == nil {
		return domain.TaskAccepted{}, problem(domain.NotImplemented)
	}
	b := *snap.Extended
	b.TaskID = ""
	b.Kind = domain.Cleanup
	b.TargetRevision = snap.Revision + 1
	b.Listeners = slices.DeleteFunc(slices.Clone(b.Listeners), func(l control.ListenerV2) bool { return l.Internal != nil && *l.Internal == pin })
	b.RetiredLinks = append(slices.Clone(b.RetiredLinks), control.InternalRetirement{Link: pin, Epoch: b.RevocationEpoch})
	slices.SortFunc(b.RetiredLinks, func(a, b control.InternalRetirement) int {
		return strings.Compare(string(a.Link.ID), string(b.Link.ID))
	})
	return s.SubmitV2(ctx, m, stage3.PublishPlan{InternalRetirement: true, Kind: domain.Cleanup, Subject: pin, Deadline: s.options.Now().Add(actionDuration), Nodes: []stage3.NodePlan{{NodeID: domain.ID(row.NodeID), ExpectedRevision: snap.Revision, Template: b}}})
}

func retirementHash(m domain.Mutation, pin control.Pin) (string, error) {
	return hashPayload(struct {
		Mutation domain.Mutation
		Link     control.Pin
	}{m, pin})
}

func (s *Service) settleV2(ctx context.Context) error {
	var entries []string
	var links []dbgen.I03RetiredTasksRow
	e := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		entries, e = q.I03SettledCleanupWork(ctx)
		if e != nil {
			return e
		}
		links, e = q.I03RetiredTasks(ctx)
		return e
	})
	if e != nil {
		return e
	}
	for _, id := range entries {
		if e = s.SettleEntryCleanup(ctx, domain.ID(id)); e != nil {
			return e
		}
	}
	for _, l := range links {
		if e = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
			return routes.ConfirmInternalCleanup(ctx, q, control.Pin{ID: domain.ID(l.ID), Revision: domain.Revision(l.Revision), Digest: l.SuffixDigest}, domain.ID(l.TaskID), s.options.Now())
		}); e != nil {
			return e
		}
	}
	return nil
}
