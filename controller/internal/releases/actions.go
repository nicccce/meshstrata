package releases

import (
	"context"
	"database/sql"
	"slices"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

const actionDuration = 5 * time.Minute

func (s *Service) action(ctx context.Context, id domain.ID, name string, m domain.Mutation) (domain.TaskAccepted, error) {
	var out domain.TaskAccepted
	if !id.Valid() || !mutationValid(m) {
		return out, problem(domain.InvalidArgument)
	}
	scope := "b04:" + name + ":" + string(id)
	hash, err := hashPayload(m)
	if err != nil {
		return out, err
	}
	var source dbgen.Task
	var inputs []templateInput
	now := s.options.Now().UTC()
	err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		replay, err := previousAction(ctx, q, scope, m.IdempotencyKey, hash)
		if err != nil || replay.TaskID != "" {
			out = replay
			return err
		}
		source, err = q.GetTask(ctx, string(id))
		if err != nil {
			return missing(err)
		}
		if err := checkAction(ctx, q, source, name, m, now); err != nil {
			return err
		}
		if name == "cancel" {
			return nil
		}
		inputs, err = loadSource(ctx, q, source, name)
		return err
	})
	if err != nil || out.TaskID != "" {
		return out, err
	}
	var prepared preparedPlan
	if name != "cancel" {
		kind := domain.TaskKind(source.Kind)
		if name == "rollback" {
			kind = domain.Rollback
		}
		plan := Plan{Kind: kind, Subject: domain.RevisionRef{ID: domain.ID(source.SubjectID), Revision: domain.Revision(source.TargetRevision)},
			Deadline: now.Add(actionDuration)}
		for _, input := range inputs {
			node, err := s.recompile(ctx, input, kind, now)
			if err != nil {
				return out, err
			}
			plan.Nodes = append(plan.Nodes, node)
		}
		plan, err = normalizePlan(domain.Mutation{ExpectedRevision: plan.Subject.Revision, IdempotencyKey: m.IdempotencyKey}, plan)
		if err != nil {
			return out, err
		}
		prepared, err = s.prepare(ctx, plan)
		if err != nil {
			return out, err
		}
	}
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		replay, err := previousAction(ctx, q, scope, m.IdempotencyKey, hash)
		if err != nil || replay.TaskID != "" {
			out = replay
			return err
		}
		fresh, err := q.GetTask(ctx, source.ID)
		if err != nil {
			return missing(err)
		}
		now := s.options.Now()
		if err := checkAction(ctx, q, fresh, name, m, now); err != nil {
			return err
		}
		if name == "cancel" {
			if err := finishTask(ctx, q, source.ID, domain.Cancelled, now); err != nil {
				return err
			}
			out = domain.TaskAccepted{TaskID: id}
		} else {
			out, err = s.commitPlan(ctx, q, scope, m.IdempotencyKey, hash, prepared)
			if err != nil {
				return err
			}
		}
		return q.B04InsertAction(ctx, dbgen.B04InsertActionParams{Scope: scope, Key: m.IdempotencyKey, RequestDigest: hash, TaskID: string(out.TaskID)})
	})
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	s.signal()
	return out, nil
}

func previousAction(ctx context.Context, q *dbgen.Queries, scope, key, hash string) (domain.TaskAccepted, error) {
	row, err := q.B04GetAction(ctx, dbgen.B04GetActionParams{Scope: scope, Key: key})
	if legitimatelyAbsent(err) {
		return domain.TaskAccepted{}, nil
	}
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	if row.RequestDigest != hash {
		return domain.TaskAccepted{}, problem(domain.IdempotencyConflict)
	}
	return domain.TaskAccepted{TaskID: domain.ID(row.TaskID)}, nil
}

func checkAction(ctx context.Context, q *dbgen.Queries, t dbgen.Task, name string, m domain.Mutation, now time.Time) error {
	targets, err := q.B04TaskNodes(ctx, t.ID)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return problem(domain.NotImplemented)
	}
	if _, err := q.B04GetTarget(ctx, dbgen.B04GetTargetParams{TaskID: t.ID, NodeID: targets[0].NodeID}); err != nil {
		if legitimatelyAbsent(err) {
			return problem(domain.NotImplemented)
		}
		return err
	}
	if t.Revision != int64(m.ExpectedRevision) {
		return conflict(t.Revision)
	}
	switch name {
	case "cancel":
		if t.Deadline <= stamp(now) {
			return problem(domain.TaskExpired)
		}
		n, err := q.B04ArmedCount(ctx, t.ID)
		if err != nil {
			return err
		}
		restrictions, err := q.B04HasRestrictions(ctx, t.ID)
		if err != nil {
			return err
		}
		// Revocation intent is durable as soon as it is committed.
		if !active(t.State) || n > 0 || restrictions || t.Kind != string(domain.Publish) && t.Kind != string(domain.Rollback) {
			return conflict(t.Revision)
		}
	case "retry":
		if t.State != string(domain.TaskFailed) && t.State != string(domain.TimedOut) && t.State != string(domain.Cancelled) {
			return conflict(t.Revision)
		}
	case "rollback":
		if t.State != string(domain.Succeeded) || t.Kind != string(domain.Publish) && t.Kind != string(domain.Rollback) {
			return conflict(t.Revision)
		}
	default:
		return problem(domain.NotImplemented)
	}
	return nil
}

type templateInput struct {
	node     dbgen.Node
	artifact dbgen.B04Artifact
	grants   []dbgen.B04Grant
	deps     []domain.ID
	remove   map[domain.ID]bool
}

func loadSource(ctx context.Context, q *dbgen.Queries, source dbgen.Task, action string) ([]templateInput, error) {
	targets, err := q.B04TaskNodes(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	out := make([]templateInput, 0, len(targets))
	for _, t := range targets {
		node, err := q.GetNode(ctx, t.NodeID)
		if err != nil {
			return nil, missing(err)
		}
		if node.RemovedAt.Valid {
			return nil, problem(domain.NotFound)
		}
		if action == "retry" {
			latest, err := q.B04LatestTargetRevision(ctx, node.ID)
			if err != nil {
				return nil, err
			}
			if latest != t.TargetRevision {
				return nil, conflict(node.Revision)
			}
		}
		a, err := q.B04GetArtifact(ctx, dbgen.B04GetArtifactParams{TaskID: source.ID, NodeID: t.NodeID})
		if err != nil {
			return nil, err
		}
		grants, err := q.B04SourceGrants(ctx, dbgen.B04SourceGrantsParams{NodeID: node.ID, TaskID: a.TaskID})
		if err != nil {
			return nil, err
		}
		deps, err := q.B04Dependencies(ctx, dbgen.B04DependenciesParams{TaskID: source.ID, NodeID: t.NodeID})
		if err != nil {
			return nil, err
		}
		in := templateInput{node: node, artifact: a, grants: grants}
		for _, dep := range deps {
			in.deps = append(in.deps, domain.ID(dep))
		}
		out = append(out, in)
	}
	return out, nil
}

func (s *Service) recompile(ctx context.Context, in templateInput, kind domain.TaskKind, now time.Time) (NodePlan, error) {
	b, err := s.openTemplate(ctx, in.artifact)
	if err != nil {
		return NodePlan{}, err
	}
	b.Kind = kind
	b.RevocationEpoch = domain.Revision(in.node.RevocationEpoch)
	b.TargetRevision = domain.Revision(in.node.Revision) + 1

	ledger := make(map[domain.ID]dbgen.B04Grant, len(in.grants))
	for _, grant := range in.grants {
		ledger[domain.ID(grant.IdentityID)] = grant
	}
	tombs := map[domain.ID]control.Tombstone{}
	for _, t := range b.Tombstones {
		t.Epoch = b.RevocationEpoch
		tombs[t.IdentityID] = t
	}
	kept := map[domain.ID]bool{}
	omitted := map[domain.ID]bool{}
	listeners := b.Listeners[:0]
	for _, l := range b.Listeners {
		a := l.Authorization
		g := ledger[a.IdentityID]
		if in.remove[l.ListenerID] || !a.ExpiresAt.After(now) || a.RevokedAt != nil || g.RestrictedAt.Valid {
			omitted[l.ListenerID], omitted[a.IdentityID] = true, true
			if _, known := ledger[a.IdentityID]; known && !g.ConfirmedAt.Valid {
				tombs[a.IdentityID] = makeTombstone(g, b.RevocationEpoch, now)
			}
			continue
		}
		kept[a.IdentityID] = true
		listeners = append(listeners, l)
	}
	b.Listeners = listeners
	for id, g := range ledger {
		if !kept[id] && !g.ConfirmedAt.Valid && g.OriginTaskID.Valid && kind != domain.Publish {
			tombs[id] = makeTombstone(g, b.RevocationEpoch, now)
		}
	}
	b.Tombstones = b.Tombstones[:0]
	for _, t := range tombs {
		b.Tombstones = append(b.Tombstones, t)
	}
	slices.SortFunc(b.Tombstones, func(a, b control.Tombstone) int {
		if a.IdentityID < b.IdentityID {
			return -1
		}
		if a.IdentityID > b.IdentityID {
			return 1
		}
		return 0
	})
	b.Resources = slices.DeleteFunc(b.Resources, func(r control.Resource) bool { return omitted[r.ID] })
	return NodePlan{NodeID: b.NodeID, ExpectedRevision: domain.Revision(in.node.Revision), Template: b, DependsOn: in.deps}, nil
}

func makeTombstone(g dbgen.B04Grant, epoch domain.Revision, now time.Time) control.Tombstone {
	reason := "revoked"
	if g.ExpiresAt <= stamp(now) {
		reason = "expired"
	}
	return control.Tombstone{IdentityID: domain.ID(g.IdentityID), Generation: domain.Revision(g.Generation), Epoch: epoch, At: now, Reason: reason}
}

// resultIssue is the only persisted task issue information exposed to HTTP.
func resultIssue(code sql.NullString) *domain.Problem {
	if code.Valid {
		return &domain.Problem{Code: safeIssue(domain.ErrorCode(code.String))}
	}
	return nil
}
