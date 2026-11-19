package releases

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type switchContextKey struct{}
type legacySwitch struct {
	Node          domain.ID
	Source        control.ArtifactKey
	RequestDigest string
	Key           string
}

func switchPermit(ctx context.Context) *legacySwitch {
	p, _ := ctx.Value(switchContextKey{}).(*legacySwitch)
	return p
}

func migrationHash(node domain.ID, m domain.Mutation) (string, error) {
	return hashPayload(struct {
		Node     domain.ID
		Mutation domain.Mutation
	}{node, m})
}

// DrainLegacyNode explicitly retires every V1 identity using its original V1
// artifact and grants. It does not rewrite that artifact or claim a V2 baseline.
// Completion and real cleanup receipts are required before SwitchNodeV2.
func (s *Service) DrainLegacyNode(ctx context.Context, node domain.ID, m domain.Mutation) (domain.TaskAccepted, error) {
	var out domain.TaskAccepted
	if !node.Valid() || !mutationValid(m) {
		return out, problem(domain.InvalidArgument)
	}
	hash, err := migrationHash(node, m)
	if err != nil {
		return out, err
	}
	scope := "i03:legacy-drain:" + string(node)
	err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		out, e = previousAction(ctx, q, scope, m.IdempotencyKey, hash)
		return e
	})
	if err != nil || out.TaskID != "" {
		return out, err
	}
	if s.v2Session(node) == "" {
		return out, problem(domain.NotImplemented)
	}
	snap, err := s.NodeSnapshot(ctx, node)
	if err != nil {
		return out, err
	}
	if snap.Legacy == nil || snap.Revision != m.ExpectedRevision {
		return out, problem(domain.RevisionConflict)
	}
	input := templateInput{remove: map[domain.ID]bool{}}
	for _, l := range snap.Legacy.Listeners {
		input.remove[l.ListenerID] = true
	}
	err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		input.node, e = q.GetNode(ctx, string(node))
		if e != nil {
			return e
		}
		input.artifact, e = q.B04GetArtifact(ctx, dbgen.B04GetArtifactParams{TaskID: string(snap.Artifact.TaskID), NodeID: string(node)})
		if e != nil {
			return e
		}
		input.grants, e = q.B04SourceGrants(ctx, dbgen.B04SourceGrantsParams{NodeID: string(node), TaskID: input.artifact.TaskID})
		return e
	})
	if err != nil {
		return out, err
	}
	target, err := s.recompile(ctx, input, domain.Cleanup, s.options.Now())
	if err != nil {
		return out, err
	}
	plan := Plan{Kind: domain.Cleanup, Subject: domain.RevisionRef{ID: node, Revision: m.ExpectedRevision}, Deadline: s.options.Now().Add(actionDuration), Nodes: []NodePlan{target}}
	plan, err = normalizePlan(m, plan)
	if err != nil {
		return out, err
	}
	prepared, err := s.prepare(ctx, plan)
	if err != nil {
		return out, err
	}
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		var e error
		out, e = previousAction(ctx, q, scope, m.IdempotencyKey, hash)
		if e != nil || out.TaskID != "" {
			return e
		}
		out, e = s.commitPlan(ctx, q, scope, m.IdempotencyKey, hash, prepared)
		if e != nil {
			return e
		}
		return q.B04InsertAction(ctx, dbgen.B04InsertActionParams{Scope: scope, Key: m.IdempotencyKey, RequestDigest: hash, TaskID: string(out.TaskID)})
	})
	if err == nil {
		s.signal()
	}
	return out, err
}

// SwitchNodeV2 creates an explicit empty V2 checkpoint after every old V1 grant
// has confirmed cleanup. Renewed entries subsequently use fresh identities and
// credentials through the ordinary B08 flow. No existing credential is projected
// into a different routing model, and all permanent V1 tombstones are retained.
func (s *Service) SwitchNodeV2(ctx context.Context, node domain.ID, m domain.Mutation) (domain.TaskAccepted, error) {
	var out domain.TaskAccepted
	if !node.Valid() || !mutationValid(m) {
		return out, problem(domain.InvalidArgument)
	}
	hash, err := migrationHash(node, m)
	if err != nil {
		return out, err
	}
	err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		out, e = previousAction(ctx, q, "i03:legacy-switch:"+string(node), m.IdempotencyKey, hash)
		return e
	})
	if err != nil || out.TaskID != "" {
		return out, err
	}
	snap, err := s.NodeSnapshot(ctx, node)
	if err != nil {
		return out, err
	}
	if snap.Legacy == nil || snap.Revision != m.ExpectedRevision || len(snap.Legacy.Listeners) != 0 {
		return out, problem(domain.DependencyInUse)
	}
	permit := &legacySwitch{Node: node, Source: snap.Artifact, RequestDigest: hash, Key: m.IdempotencyKey}
	if err := s.repo.db.Read(ctx, func(q *dbgen.Queries) error { return checkLegacySwitch(ctx, q, permit) }); err != nil {
		return out, err
	}
	digest, err := control.ComponentDigest(snap.Artifact)
	if err != nil {
		return out, err
	}
	b := emptyV2(node, snap.Revision+1, snap.Legacy.RevocationEpoch, snap.DNS)
	b.Tombstones = slices.Clone(snap.Legacy.Tombstones)
	plan := stage3.PublishPlan{Kind: domain.Cleanup, Subject: control.Pin{ID: node, Revision: m.ExpectedRevision, Digest: digest}, Deadline: s.options.Now().Add(actionDuration), Nodes: []stage3.NodePlan{{NodeID: node, ExpectedRevision: snap.Revision, Template: b}}}
	return s.SubmitV2(context.WithValue(ctx, switchContextKey{}, permit), m, plan)
}

func checkLegacySwitch(ctx context.Context, q *dbgen.Queries, p *legacySwitch) error {
	a, err := q.I03ConfirmedArtifact(ctx, string(p.Node))
	if err != nil {
		return err
	}
	if a.Format != 1 || artifactKey(a) != p.Source {
		return problem(domain.RevisionConflict)
	}
	grants, err := q.B04SourceGrants(ctx, dbgen.B04SourceGrantsParams{NodeID: a.NodeID, TaskID: a.TaskID})
	if err != nil {
		return err
	}
	for _, g := range grants {
		if !g.ConfirmedAt.Valid {
			return problem(domain.GrantOriginUncertain)
		}
	}
	live, err := q.B04PossibleLiveGrants(ctx, a.NodeID)
	if err != nil {
		return err
	}
	if len(live) != 0 {
		return problem(domain.GrantOriginUncertain)
	}
	links, err := q.I03LiveLinks(ctx, a.NodeID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if len(links) != 0 {
		return problem(domain.DependencyInUse)
	}
	return nil
}
