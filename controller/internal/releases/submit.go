package releases

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"sort"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

const artifactPurpose = secretstore.Purpose("release_bundle")

// Plan is a trusted internal compiler input. Each template is the complete
// intended node configuration. No management endpoint decodes Plan.
type Plan struct {
	Kind     domain.TaskKind    `json:"kind"`
	Subject  domain.RevisionRef `json:"subject"`
	Deadline time.Time          `json:"deadline"`
	Nodes    []NodePlan         `json:"nodes"`
}
type NodePlan struct {
	NodeID           domain.ID       `json:"node_id"`
	ExpectedRevision domain.Revision `json:"expected_revision"`
	Template         control.Bundle  `json:"template"`
	DependsOn        []domain.ID     `json:"depends_on"`
}
type preparedTarget struct {
	plan     NodePlan
	artifact dbgen.B04InsertArtifactParams
	grants   []dbgen.B04InsertGrantParams
	refs     []dbgen.B04InsertReferenceParams
	portRows []dbgen.B04InsertPortParams
}
type preparedPlan struct {
	plan    Plan
	task    domain.ID
	targets []preparedTarget
}

const submitScope = "b04:submit"

func (s *Service) Submit(ctx context.Context, mutation domain.Mutation, plan Plan) (domain.TaskAccepted, error) {
	plan, err := normalizePlan(mutation, plan)
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	hash, err := hashPayload(plan)
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	var existing dbgen.Task
	err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		existing, e = lookupTask(ctx, q, submitScope, mutation.IdempotencyKey, hash)
		return e
	})
	switch {
	case err == nil:
		return domain.TaskAccepted{TaskID: domain.ID(existing.ID)}, nil
	case !errors.Is(err, sql.ErrNoRows):
		return domain.TaskAccepted{}, err
	}
	if !plan.Deadline.After(s.options.Now()) {
		return domain.TaskAccepted{}, problem(domain.TaskExpired)
	}
	prepared, err := s.prepare(ctx, plan)
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	var out domain.TaskAccepted
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		var e error
		out, e = s.commitPlan(ctx, q, submitScope, mutation.IdempotencyKey, hash, prepared)
		return e
	})
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	s.signal()
	return out, nil
}

func normalizePlan(m domain.Mutation, plan Plan) (Plan, error) {
	if !mutationValid(m) || !plan.Subject.ID.Valid() || !plan.Subject.Revision.Valid() ||
		plan.Deadline.IsZero() || plan.Deadline.Year() < 1 || plan.Deadline.Year() > 9999 ||
		len(plan.Nodes) == 0 || len(plan.Nodes) > 100 {
		return Plan{}, problem(domain.InvalidArgument)
	}
	if m.ExpectedRevision != plan.Subject.Revision {
		return Plan{}, conflict(int64(plan.Subject.Revision))
	}
	switch plan.Kind {
	case domain.Publish, domain.Rollback, domain.Revoke, domain.Cleanup:
	default:
		return Plan{}, problem(domain.NotImplemented)
	}
	plan.Deadline = plan.Deadline.UTC()
	plan.Nodes = slices.Clone(plan.Nodes)
	edges := make(map[domain.ID][]domain.ID, len(plan.Nodes))
	for i := range plan.Nodes {
		n := &plan.Nodes[i]
		if _, exists := edges[n.NodeID]; exists || !n.NodeID.Valid() || !n.ExpectedRevision.Valid() || !(n.ExpectedRevision + 1).Valid() {
			return Plan{}, problem(domain.InvalidArgument)
		}
		if n.Template.NodeID != n.NodeID || n.Template.TargetRevision != n.ExpectedRevision+1 {
			return Plan{}, conflict(int64(n.ExpectedRevision))
		}
		if len(n.Template.Listeners) == 0 && (plan.Kind == domain.Publish || len(n.Template.Tombstones) == 0) {
			return Plan{}, problem(domain.NotImplemented)
		}
		n.Template.TaskID = ""
		n.Template.Kind = plan.Kind
		n.DependsOn = append([]domain.ID{}, n.DependsOn...)
		slices.Sort(n.DependsOn)
		for j, dep := range n.DependsOn {
			if dep == n.NodeID || j > 0 && dep == n.DependsOn[j-1] {
				return Plan{}, problem(domain.InvalidArgument)
			}
		}
		edges[n.NodeID] = n.DependsOn
	}
	if !acyclic(edges) {
		return Plan{}, problem(domain.InvalidArgument)
	}
	sort.Slice(plan.Nodes, func(i, j int) bool { return plan.Nodes[i].NodeID < plan.Nodes[j].NodeID })
	return plan, nil
}

func acyclic(edges map[domain.ID][]domain.ID) bool {
	visiting, done := map[domain.ID]bool{}, map[domain.ID]bool{}
	var visit func(domain.ID) bool
	visit = func(id domain.ID) bool {
		children, exists := edges[id]
		if !exists || visiting[id] {
			return false
		}
		if done[id] {
			return true
		}
		visiting[id] = true
		for _, child := range children {
			if !visit(child) {
				return false
			}
		}
		visiting[id], done[id] = false, true
		return true
	}
	for id := range edges {
		if !visit(id) {
			return false
		}
	}
	return true
}

func (s *Service) prepare(ctx context.Context, plan Plan) (preparedPlan, error) {
	task, err := s.newID("task")
	if err != nil {
		return preparedPlan{}, err
	}
	prepared := preparedPlan{plan: plan, task: task}
	for _, node := range plan.Nodes {
		target, err := s.prepareTarget(ctx, task, node)
		if err != nil {
			return preparedPlan{}, err
		}
		prepared.targets = append(prepared.targets, target)
	}
	return prepared, nil
}

func (s *Service) prepareTarget(ctx context.Context, task domain.ID, node NodePlan) (preparedTarget, error) {
	node.Template.TaskID = task
	data, key, err := control.EncodeBundle(node.Template)
	if err != nil {
		return preparedTarget{}, problem(domain.InvalidArgument)
	}
	defer clear(data)
	id, err := s.newID("artifact")
	if err != nil {
		return preparedTarget{}, err
	}
	sealed, err := s.cipher.Seal(ctx, binding(id), data)
	if err != nil || sealed.KeyID == "" || len(sealed.Ciphertext) == 0 {
		return preparedTarget{}, errors.New("release encryption unavailable")
	}
	target := preparedTarget{plan: node, artifact: dbgen.B04InsertArtifactParams{
		ID: string(id), TaskID: string(task), NodeID: string(node.NodeID), TargetRevision: int64(key.TargetRevision),
		TargetDigest: key.TargetDigest, KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext,
	}}
	if err := s.extractReferences(&target); err != nil {
		return preparedTarget{}, err
	}
	return target, nil
}

func binding(id domain.ID) secretstore.Binding {
	return secretstore.Binding{Ref: id, Purpose: artifactPurpose}
}

func lookupTask(ctx context.Context, q *dbgen.Queries, scope, key, hash string) (dbgen.Task, error) {
	old, err := q.GetTaskByKey(ctx, dbgen.GetTaskByKeyParams{IdempotencyScope: scope, IdempotencyKey: key})
	if err == nil && old.RequestDigest != hash {
		return dbgen.Task{}, problem(domain.IdempotencyConflict)
	}
	return old, err
}

func (s *Service) commitPlan(ctx context.Context, q *dbgen.Queries, scope, key, hash string, prepared preparedPlan) (domain.TaskAccepted, error) {
	existing, err := lookupTask(ctx, q, scope, key, hash)
	if err == nil {
		return domain.TaskAccepted{TaskID: domain.ID(existing.ID)}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.TaskAccepted{}, err
	}
	now := s.options.Now().UTC()
	plan := prepared.plan
	if !plan.Deadline.After(now) {
		return domain.TaskAccepted{}, problem(domain.TaskExpired)
	}
	task := string(prepared.task)
	if err := q.InsertTask(ctx, dbgen.InsertTaskParams{ID: task, Kind: string(plan.Kind), SubjectID: string(plan.Subject.ID),
		TargetRevision: int64(plan.Subject.Revision), RequestDigest: hash, IdempotencyScope: scope, IdempotencyKey: key,
		Deadline: stamp(plan.Deadline), CreatedAt: stamp(now)}); err != nil {
		return domain.TaskAccepted{}, err
	}
	for _, target := range prepared.targets {
		if err := insertTarget(ctx, q, target, now); err != nil {
			return domain.TaskAccepted{}, err
		}
	}
	for _, target := range prepared.targets {
		for _, dep := range target.plan.DependsOn {
			if err := q.InsertTaskDependency(ctx, dbgen.InsertTaskDependencyParams{TaskID: task, NodeID: target.artifact.NodeID, RequiresNodeID: string(dep)}); err != nil {
				return domain.TaskAccepted{}, err
			}
		}
	}
	return domain.TaskAccepted{TaskID: prepared.task}, nil
}

func insertTarget(ctx context.Context, q *dbgen.Queries, t preparedTarget, now time.Time) error {
	a, b := t.artifact, t.plan.Template
	node, err := q.GetNode(ctx, a.NodeID)
	if err != nil {
		return missing(err)
	}
	if node.RemovedAt.Valid {
		return problem(domain.NotFound)
	}
	if node.Revision != int64(t.plan.ExpectedRevision) || node.RevocationEpoch != int64(b.RevocationEpoch) {
		return conflict(node.Revision)
	}
	n, err := q.B04BumpNode(ctx, dbgen.B04BumpNodeParams{NodeID: a.NodeID, Expected: node.Revision, Epoch: node.RevocationEpoch, Now: stamp(now)})
	if err != nil {
		return err
	}
	if n != 1 {
		return conflict(node.Revision)
	}
	if err := q.InsertTaskNode(ctx, dbgen.InsertTaskNodeParams{TaskID: a.TaskID, NodeID: a.NodeID, TargetRevision: a.TargetRevision, TargetDigest: a.TargetDigest, RevocationEpoch: int64(b.RevocationEpoch)}); err != nil {
		return err
	}
	if err := q.B04InsertTarget(ctx, dbgen.B04InsertTargetParams{TaskID: a.TaskID, NodeID: a.NodeID}); err != nil {
		return err
	}
	if err := insertResources(ctx, q, t, now); err != nil {
		return err
	}
	if err := checkTargetGrants(ctx, q, a.TaskID, a.NodeID, now); err != nil {
		return err
	}
	return q.B04InsertArtifact(ctx, a)
}
