package releases

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func (s *Service) actionV2(ctx context.Context, source dbgen.Task, name string, m domain.Mutation) (domain.TaskAccepted, error) {
	var meta dbgen.I03V2Task
	var targets []dbgen.TaskNode
	e := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		meta, e = q.I03Task(ctx, source.ID)
		if e != nil {
			return e
		}
		targets, e = q.B04TaskNodes(ctx, source.ID)
		return e
	})
	if e != nil {
		return domain.TaskAccepted{}, e
	}
	// Cleanup recovery keeps the B08 operation and generation rather than
	// bypassing its fence with a generic new task.
	if meta.EntryOperationID != "" && source.Kind == "cleanup" {
		return domain.TaskAccepted{}, problem(domain.DependencyInUse)
	}
	plan := stage3.PublishPlan{Kind: domain.TaskKind(source.Kind), Subject: control.Pin{ID: domain.ID(source.SubjectID), Revision: domain.Revision(source.TargetRevision), Digest: meta.SubjectDigest}, Deadline: s.options.Now().Add(actionDuration), Action: &stage3.TaskAction{SourceTaskID: domain.ID(source.ID), SourceRevision: m.ExpectedRevision, Kind: name, IdempotencyKey: m.IdempotencyKey}}
	if name == "rollback" {
		plan.Kind = domain.Rollback
	}
	for _, target := range targets {
		snap, e := s.NodeSnapshot(ctx, domain.ID(target.NodeID))
		if e != nil {
			return domain.TaskAccepted{}, e
		}
		var artifact dbgen.B04Artifact
		var deps []string
		e = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
			var e error
			if name == "rollback" {
				artifact, e = q.I03RollbackArtifact(ctx, dbgen.I03RollbackArtifactParams{NodeID: target.NodeID, TargetRevision: target.TargetRevision})
			} else {
				latest, x := q.B04LatestTargetRevision(ctx, target.NodeID)
				if x != nil {
					return x
				}
				if latest != target.TargetRevision {
					return problem(domain.RevisionConflict)
				}
				artifact, e = q.B04GetArtifact(ctx, dbgen.B04GetArtifactParams{TaskID: source.ID, NodeID: target.NodeID})
			}
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			deps, e = q.B04Dependencies(ctx, dbgen.B04DependenciesParams{TaskID: source.ID, NodeID: target.NodeID})
			return e
		})
		if e != nil {
			return domain.TaskAccepted{}, e
		}
		b := emptyV2(domain.ID(target.NodeID), snap.Revision+1, 1, snap.DNS)
		if artifact.ID != "" {
			b, e = s.openV2(ctx, artifact)
			if e != nil {
				return domain.TaskAccepted{}, e
			}
		}
		if snap.Extended != nil {
			b.RevocationEpoch = snap.Extended.RevocationEpoch
			for _, t := range snap.Extended.Tombstones {
				if !slices.ContainsFunc(b.Tombstones, func(x control.Tombstone) bool { return x.IdentityID == t.IdentityID }) {
					b.Tombstones = append(b.Tombstones, t)
				}
			}
			for _, r := range snap.Extended.RetiredLinks {
				if !slices.Contains(b.RetiredLinks, r) {
					b.RetiredLinks = append(b.RetiredLinks, r)
				}
			}
		}
		var grants []dbgen.B04Grant
		e = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
			var e error
			grants, e = q.B04PossibleLiveGrants(ctx, target.NodeID)
			return e
		})
		if e != nil {
			return domain.TaskAccepted{}, e
		}
		keep := []control.ListenerV2{}
		for _, l := range b.Listeners {
			allowed := true
			if l.Entry != nil {
				e = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
					g, e := q.B04GetGrant(ctx, string(l.Entry.IdentityID))
					if e != nil {
						return e
					}
					allowed = !g.RestrictedAt.Valid && g.ExpiresAt > stamp(s.options.Now())
					return nil
				})
				if e != nil {
					return domain.TaskAccepted{}, e
				}
			}
			if allowed {
				keep = append(keep, l)
			}
		}
		b.Listeners = keep
		for _, g := range grants {
			included := slices.ContainsFunc(b.Listeners, func(l control.ListenerV2) bool { return l.Entry != nil && string(l.Entry.IdentityID) == g.IdentityID })
			if !included && !slices.ContainsFunc(b.Tombstones, func(t control.Tombstone) bool { return string(t.IdentityID) == g.IdentityID }) {
				b.Tombstones = append(b.Tombstones, makeTombstone(g, b.RevocationEpoch, s.options.Now()))
			}
		}
		b.TaskID = ""
		b.Kind = plan.Kind
		b.TargetRevision = snap.Revision + 1
		b.DNS = snap.DNS
		slices.SortFunc(b.Tombstones, func(a, b control.Tombstone) int { return strings.Compare(string(a.IdentityID), string(b.IdentityID)) })
		slices.SortFunc(b.RetiredLinks, func(a, b control.InternalRetirement) int {
			return strings.Compare(string(a.Link.ID), string(b.Link.ID))
		})
		n := stage3.NodePlan{NodeID: domain.ID(target.NodeID), ExpectedRevision: snap.Revision, Template: b}
		for _, dep := range deps {
			n.DependsOn = append(n.DependsOn, domain.ID(dep))
		}
		plan.Nodes = append(plan.Nodes, n)
	}
	return s.SubmitV2(ctx, domain.Mutation{ExpectedRevision: plan.Subject.Revision, IdempotencyKey: "action/" + source.ID + "/" + name + "/" + m.IdempotencyKey}, plan)
}
