package releases

import (
	"context"
	"database/sql"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
)

func (s *Service) reconcileRemovals(ctx context.Context) error {
	now := s.options.Now()
	if now.Before(s.nextRemoval) {
		return nil
	}
	s.nextRemoval = now.Add(redelivery)
	var tasks []string
	err := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		tasks, err = q.B04RemovalTasks(ctx, s.removalCursor)
		return err
	})
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if err := s.reconcileRemoval(ctx, task); err != nil {
			return err
		}
		s.removalCursor = task
	}
	if len(tasks) < 32 {
		s.removalCursor = ""
	}
	return nil
}

func (s *Service) reconcileRemoval(ctx context.Context, task string) error {
	intent, err := s.options.RemovalIntent(ctx, domain.ID(task))
	if err != nil {
		return err
	}
	if string(intent.TaskID) != task || !intent.NodeID.Valid() || !intent.Revision.Valid() {
		return problem(domain.RevisionConflict)
	}
	var links []dbgen.B04RemovalLink
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		gone, err := q.GetNode(ctx, string(intent.NodeID))
		if err != nil {
			return err
		}
		if !gone.RemovedAt.Valid {
			return problem(domain.RevisionConflict)
		}
		if err := q.B04MarkRemovedPorts(ctx, gone.ID); err != nil {
			return err
		}
		peers, err := q.B04RemovalRouteTargets(ctx, gone.ID)
		if err != nil {
			return err
		}
		for _, peer := range peers {
			if err := q.B04LinkRemoval(ctx, dbgen.B04LinkRemovalParams{RemovalTaskID: task, NodeID: peer.NodeID, ListenerID: peer.ListenerID}); err != nil {
				return err
			}
		}
		links, err = q.B04GetRemovalLinks(ctx, task)
		return err
	})
	if err != nil {
		return err
	}
	groups := map[string]map[domain.ID]bool{}
	for _, link := range links {
		if link.CleanupTaskID.Valid || link.NodeID == string(intent.NodeID) {
			continue
		}
		if groups[link.NodeID] == nil {
			groups[link.NodeID] = map[domain.ID]bool{}
		}
		groups[link.NodeID][domain.ID(link.ListenerID)] = true
	}
	for node, listeners := range groups {
		if s.options.Connected(domain.ID(node)) {
			if err := s.enqueueRemovalChild(ctx, task, string(intent.NodeID), node, listeners); err != nil && !isProblem(err) {
				return err
			}
		}
	}
	return nil
}

func (s *Service) enqueueRemovalChild(ctx context.Context, task, removed, node string, listeners map[domain.ID]bool) error {
	input := templateInput{remove: listeners}
	err := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		input.node, err = q.GetNode(ctx, node)
		if err != nil {
			return missing(err)
		}
		if input.node.RemovedAt.Valid {
			return problem(domain.NotFound)
		}
		input.artifact, err = q.B04LatestVerifiedArtifact(ctx, node)
		if err != nil {
			return missing(err)
		}
		latest, err := q.B04LatestTargetRevision(ctx, node)
		if err != nil {
			return err
		}
		if latest != input.artifact.TargetRevision {
			return problem(domain.RevisionConflict)
		}
		input.grants, err = q.B04SourceGrants(ctx, dbgen.B04SourceGrantsParams{NodeID: node, TaskID: input.artifact.TaskID})
		return err
	})
	if err != nil {
		return err
	}
	found := false
	for _, g := range input.grants {
		found = found || listeners[domain.ID(g.ListenerID)] && g.OriginTaskID.Valid && !g.ConfirmedAt.Valid
	}
	if !found {
		return problem(domain.DependencyInUse)
	}
	now := s.options.Now()
	target, err := s.recompile(ctx, input, domain.Cleanup, now)
	if err != nil {
		return err
	}
	for _, t := range target.Template.Tombstones {
		for _, g := range input.grants {
			if domain.ID(g.IdentityID) == t.IdentityID && !listeners[domain.ID(g.ListenerID)] &&
				g.ExpiresAt > stamp(now) && !g.RestrictedAt.Valid {
				return problem(domain.DependencyInUse)
			}
		}
	}
	plan := Plan{Kind: domain.Cleanup, Subject: domain.RevisionRef{ID: domain.ID(task), Revision: 1}, Deadline: now.Add(actionDuration), Nodes: []NodePlan{target}}
	key := node
	scope := "b04:removal:" + task
	plan, err = normalizePlan(domain.Mutation{ExpectedRevision: 1, IdempotencyKey: key}, plan)
	if err != nil {
		return err
	}
	prepared, err := s.prepare(ctx, plan)
	if err != nil {
		return err
	}
	hash, err := hashPayload(plan)
	if err != nil {
		return err
	}
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		for listener := range listeners {
			inUse, err := q.B04PeerListenerInUse(ctx, dbgen.B04PeerListenerInUseParams{ListenerID: string(listener), RemovedNode: removed})
			if err != nil {
				return err
			}
			if inUse {
				return problem(domain.DependencyInUse)
			}
		}
		out, err := s.commitPlan(ctx, q, scope, key, hash, prepared)
		if err != nil {
			return err
		}
		return q.B04SetRemovalChild(ctx, dbgen.B04SetRemovalChildParams{NodeID: node, Removal: task, Child: sql.NullString{String: string(out.TaskID), Valid: true}})
	})
	if err == nil {
		s.signal()
	}
	return err
}
