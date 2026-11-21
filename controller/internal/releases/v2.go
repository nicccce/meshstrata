package releases

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/controller/internal/subscriptions"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

// V2Session is supplied by a trusted, authenticated control-channel adapter.
// The callback is a nonblocking in-memory read; SessionID must change on every
// reconnect. Runtime=true alone is insufficient. No HTTP input supplies it.
type V2Session struct {
	SessionID      string
	RecoveryFormat int
}

func (s *Service) v2Session(node domain.ID) string {
	if s.options.V2Session == nil || s.options.V2Deliver == nil || !s.options.Connected(node) {
		return ""
	}
	c := s.options.V2Session(node)
	if c.RecoveryFormat != 2 {
		return ""
	}
	return c.SessionID
}

// A recovering peer may replay its durable outbox before becoming ready for
// commands. Only the authenticated adapter's current session may do so.
func (s *Service) v2ResultSession(ctx context.Context, node domain.ID) string {
	id := control.SessionFromContext(ctx)
	if id == "" {
		return s.v2Session(node)
	}
	if s.options.V2Session == nil || s.options.V2Deliver == nil {
		return ""
	}
	c := s.options.V2Session(node)
	if c.RecoveryFormat != 2 || c.SessionID != string(id) {
		return ""
	}
	return c.SessionID
}

type preparedV2 struct {
	node        stage3.NodePlan
	artifact    dbgen.I03InsertV2ArtifactParams
	snap        stage3.NodeSnapshot
	session     string
	fences      []dbgen.I03PutLinkFenceParams
	grantHashes []string
	refIDs      []domain.ID
}

func (s *Service) SubmitV2(ctx context.Context, m domain.Mutation, p stage3.PublishPlan) (domain.TaskAccepted, error) {
	var out domain.TaskAccepted
	// Capture the caller's sealed candidate before normalization for task hashing.
	entryDigest, digestErr := control.ComponentDigest(p)
	if digestErr != nil {
		return out, digestErr
	}
	if !mutationValid(m) || !p.Subject.Valid() || m.ExpectedRevision != p.Subject.Revision || len(p.Nodes) == 0 || len(p.Nodes) > 100 || p.Deadline.IsZero() {
		return out, problem(domain.InvalidArgument)
	}
	if p.Kind != domain.Publish && p.Kind != domain.Cleanup && p.Kind != domain.Revoke && p.Kind != domain.Rollback {
		return out, problem(domain.NotImplemented)
	}
	if p.InternalRetirement && (p.Kind != domain.Cleanup || p.Action != nil || p.EntryOperationID != "" || len(p.Nodes) != 1) {
		return out, problem(domain.InvalidArgument)
	}
	p.Nodes = slices.Clone(p.Nodes)
	edges := map[domain.ID][]domain.ID{}
	for i := range p.Nodes {
		n := &p.Nodes[i]
		if !n.NodeID.Valid() || !n.ExpectedRevision.Valid() || !(n.ExpectedRevision + 1).Valid() || n.Template.NodeID != n.NodeID || n.Template.TargetRevision != n.ExpectedRevision+1 {
			return out, problem(domain.InvalidArgument)
		}
		if _, ok := edges[n.NodeID]; ok {
			return out, problem(domain.InvalidArgument)
		}
		n.Template.TaskID = ""
		n.Template.Kind = p.Kind
		n.DependsOn = slices.Clone(n.DependsOn)
		slices.Sort(n.DependsOn)
		for j, d := range n.DependsOn {
			if d == n.NodeID || j > 0 && d == n.DependsOn[j-1] {
				return out, problem(domain.InvalidArgument)
			}
		}
		edges[n.NodeID] = n.DependsOn
	}
	if !acyclic(edges) {
		return out, problem(domain.InvalidArgument)
	}
	slices.SortFunc(p.Nodes, func(a, b stage3.NodePlan) int {
		if a.NodeID < b.NodeID {
			return -1
		}
		if a.NodeID > b.NodeID {
			return 1
		}
		return 0
	})
	hash, err := hashPayload(struct {
		Mutation domain.Mutation
		Plan     stage3.PublishPlan
	}{m, p})
	if err != nil {
		return out, err
	}
	scope := "i03:v2:submit"
	err = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		old, e := lookupTask(ctx, q, scope, m.IdempotencyKey, hash)
		out.TaskID = domain.ID(old.ID)
		return e
	})
	if err == nil {
		return out, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.TaskAccepted{}, err
	}
	out = domain.TaskAccepted{}
	if !p.Deadline.After(s.options.Now()) {
		return out, problem(domain.TaskExpired)
	}
	task, err := s.newID("task")
	if err != nil {
		return out, err
	}
	prepared := make([]preparedV2, len(p.Nodes))
	byNode := map[domain.ID]int{}
	for i, n := range p.Nodes {
		session := s.v2Session(n.NodeID)
		if session == "" {
			return out, problem(domain.NotImplemented)
		}
		snap, e := s.NodeSnapshot(ctx, n.NodeID)
		if e != nil {
			return out, e
		}
		if snap.Revision != n.ExpectedRevision {
			return out, conflict(int64(snap.Revision))
		}
		// A V1 baseline needs an explicit migration/rotation flow, including its
		// permanent restrictions. Capability alone cannot authorize that switch.
		if snap.Legacy != nil {
			permit := switchPermit(ctx)
			if permit == nil || permit.Node != n.NodeID || permit.Source != snap.Artifact || len(p.Nodes) != 1 || p.Kind != domain.Cleanup || p.Action != nil || p.InternalRetirement || p.EntryOperationID != "" || len(n.Template.Listeners) != 0 || len(n.Template.Terminal) != 0 || len(n.Template.RetiredLinks) != 0 || !slices.Equal(n.Template.Tombstones, snap.Legacy.Tombstones) {
				return out, problem(domain.NotImplemented)
			}
		}
		if snap.Extended != nil {
			for _, old := range snap.Extended.Tombstones {
				if !slices.Contains(n.Template.Tombstones, old) {
					return out, problem(domain.GrantOriginUncertain)
				}
			}
			for _, old := range snap.Extended.RetiredLinks {
				if !slices.Contains(n.Template.RetiredLinks, old) {
					return out, problem(domain.GrantOriginUncertain)
				}
			}
		}
		for _, l := range n.Template.Listeners {
			if l.Internal == nil {
				continue
			}
			var a dbgen.B04Artifact
			e := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
				o, e := q.I03Origin(ctx, string(l.Internal.ID))
				if e != nil {
					return e
				}
				a, e = q.B04GetArtifact(ctx, dbgen.B04GetArtifactParams{TaskID: o.TaskID, NodeID: o.NodeID})
				return e
			})
			if errors.Is(e, sql.ErrNoRows) {
				continue
			}
			if e != nil {
				return out, e
			}
			previous, e := s.openV2(ctx, a)
			if e != nil {
				return out, e
			}
			found := false
			for _, old := range previous.Listeners {
				if old.Internal != nil && *old.Internal == *l.Internal {
					if control.SameInternalLink(old, l) != nil {
						return out, problem(domain.GrantOriginUncertain)
					}
					found = true
				}
			}
			if !found {
				return out, problem(domain.GrantOriginUncertain)
			}
		}
		n.Template.TaskID = task
		data, key, e := control.EncodeBundleV2(n.Template)
		if e != nil {
			return out, problem(domain.InvalidArgument)
		}
		if e = n.Template.CheckEgressLoops(snap.Loops); e != nil {
			clear(data)
			return out, problem(domain.InvalidArgument)
		}
		id, e := s.newID("artifact")
		if e != nil {
			clear(data)
			return out, e
		}
		sealed, e := s.cipher.Seal(ctx, binding(id), data)
		clear(data)
		if e != nil {
			return out, e
		}
		t := preparedV2{node: n, snap: snap, session: session, artifact: dbgen.I03InsertV2ArtifactParams{ID: string(id), TaskID: string(task), NodeID: string(n.NodeID), TargetRevision: int64(key.TargetRevision), TargetDigest: key.TargetDigest, KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext}}
		for _, l := range n.Template.Listeners {
			h, e := hashPayload(l)
			if e != nil {
				return out, e
			}
			t.grantHashes = append(t.grantHashes, h)
			id, e := s.newID("reference")
			if e != nil {
				return out, e
			}
			t.refIDs = append(t.refIDs, id)
		}
		prepared[i] = t
		byNode[n.NodeID] = i
	}
	// Validate concrete credentials/tails against a real receiving bundle. A
	// receiver in this task is fenced by the B04 DAG; a shared receiver is pinned
	// to its currently confirmed artifact, never the latest submitted version.
	for i := range prepared {
		t := &prepared[i]
		for _, l := range t.node.Template.Listeners {
			if l.Forward.Next == nil {
				continue
			}
			receiver := l.Tail.Hops[1].NodeID
			var bundle *control.BundleV2
			var key control.ArtifactKey
			var epoch domain.Revision
			if j, ok := byNode[receiver]; ok {
				r := prepared[j]
				if !slices.Contains(t.node.DependsOn, receiver) {
					return out, problem(domain.DependencyInUse)
				}
				bundle = &r.node.Template
				key = control.ArtifactKey{TargetRevision: domain.Revision(r.artifact.TargetRevision), TargetDigest: r.artifact.TargetDigest}
				epoch = bundle.RevocationEpoch
			} else {
				snap, e := s.NodeSnapshot(ctx, receiver)
				if e != nil {
					return out, e
				}
				if snap.BaseState != "confirmed" || snap.Extended == nil {
					return out, problem(domain.GrantOriginUncertain)
				}
				bundle = snap.Extended
				key = snap.Artifact
				epoch = bundle.RevocationEpoch
			}
			found := false
			for _, r := range bundle.Listeners {
				if r.Internal != nil && *r.Internal == l.Forward.Next.Link && control.CheckLinkPair(l, r) == nil {
					found = true
				}
			}
			if !found {
				return out, problem(domain.GrantOriginUncertain)
			}
			fence := dbgen.I03PutLinkFenceParams{TaskID: string(task), NodeID: string(t.node.NodeID), ReceiverID: string(receiver), LinkID: string(l.Forward.Next.Link.ID), TargetRevision: int64(key.TargetRevision), ArtifactDigest: key.TargetDigest, Epoch: int64(epoch)}
			shared := false
			for _, prior := range t.fences {
				if prior.LinkID == fence.LinkID {
					if prior != fence {
						return out, problem(domain.GrantOriginUncertain)
					}
					shared = true
				}
			}
			if !shared {
				t.fences = append(t.fences, fence)
			}
		}
	}
	var actionHash string
	var retireHash string
	if p.InternalRetirement {
		retireHash, err = retirementHash(m, p.Subject)
		if err != nil {
			return out, err
		}
	}
	if p.Action != nil {
		actionHash, err = hashPayload(domain.Mutation{ExpectedRevision: p.Action.SourceRevision, IdempotencyKey: p.Action.IdempotencyKey})
		if err != nil {
			return out, err
		}
	}
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		if p.InternalRetirement {
			replay, e := previousAction(ctx, q, "i03:retire:"+string(p.Subject.ID), m.IdempotencyKey, retireHash)
			if e != nil || replay.TaskID != "" {
				out = replay
				return e
			}
		}
		if p.Action != nil {
			replay, e := previousAction(ctx, q, "b04:"+p.Action.Kind+":"+string(p.Action.SourceTaskID), p.Action.IdempotencyKey, actionHash)
			if e != nil || replay.TaskID != "" {
				out = replay
				return e
			}
		}
		old, e := lookupTask(ctx, q, scope, m.IdempotencyKey, hash)
		if e == nil {
			out.TaskID = domain.ID(old.ID)
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if p.EntryOperationID != "" && p.Action == nil {
			fence, e := q.I03CEntryPlanFence(ctx, dbgen.I03CEntryPlanFenceParams{OperationID: string(p.EntryOperationID), Kind: string(p.Kind)})
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e == nil && fence != "" && fence != entryDigest {
				return problem(domain.RevisionConflict)
			}
		}
		now := s.options.Now()
		if !p.Deadline.After(now) {
			return problem(domain.TaskExpired)
		}
		blocked, e := q.I03OperationBlocked(ctx, string(p.EntryOperationID))
		if e != nil {
			return e
		}
		if blocked && p.Kind == domain.Publish {
			return problem(domain.GrantOriginUncertain)
		}
		if p.Action != nil {
			source, e := q.GetTask(ctx, string(p.Action.SourceTaskID))
			if e != nil {
				return e
			}
			if e = checkAction(ctx, q, source, p.Action.Kind, domain.Mutation{ExpectedRevision: p.Action.SourceRevision, IdempotencyKey: m.IdempotencyKey}, now); e != nil {
				return e
			}
			meta, e := q.I03Task(ctx, source.ID)
			if e != nil {
				return e
			}
			if source.SubjectID != string(p.Subject.ID) || source.TargetRevision != int64(p.Subject.Revision) || meta.SubjectDigest != p.Subject.Digest {
				return problem(domain.RevisionConflict)
			}
			if p.Action.Kind == "rollback" {
				for _, t := range prepared {
					for _, tomb := range t.node.Template.Tombstones {
						g, e := q.B04GetGrant(ctx, string(tomb.IdentityID))
						if e != nil {
							return e
						}
						if !g.RestrictedAt.Valid {
							if e = subscriptions.AuthorizeTaskRollback(ctx, q, tomb.IdentityID, now); e != nil {
								return e
							}
						}
					}
				}
			}
		}
		var change stage3.EntryChange
		if p.EntryOperationID != "" && p.Action == nil {
			change, e = subscriptions.CheckDispatch(ctx, q, p.EntryOperationID, p.Kind, p.Subject, now)
			if e != nil {
				return e
			}
		} else if switchPermit(ctx) != nil {
			if e = checkLegacySwitch(ctx, q, switchPermit(ctx)); e != nil {
				return e
			}
		} else if p.Action == nil {
			ok, e := q.I03PublicationSubject(ctx, dbgen.I03PublicationSubjectParams{RouteID: string(p.Subject.ID), Revision: int64(p.Subject.Revision), DraftDigest: p.Subject.Digest})
			if e != nil {
				return e
			}
			if !ok {
				if p.Kind != domain.Cleanup {
					return problem(domain.RevisionConflict)
				}
				link, e := q.B06LinkByID(ctx, string(p.Subject.ID))
				if e != nil {
					return e
				}
				if link.State != "retiring" || link.SuffixDigest != p.Subject.Digest || link.Revision != int64(p.Subject.Revision) {
					return problem(domain.RevisionConflict)
				}
			}
		}
		if e = q.InsertTask(ctx, dbgen.InsertTaskParams{ID: string(task), Kind: string(p.Kind), SubjectID: string(p.Subject.ID), TargetRevision: int64(p.Subject.Revision), RequestDigest: hash, IdempotencyScope: scope, IdempotencyKey: m.IdempotencyKey, Deadline: stamp(p.Deadline), CreatedAt: stamp(now)}); e != nil {
			return e
		}
		if e = q.I03PutTask(ctx, dbgen.I03PutTaskParams{TaskID: string(task), SubjectDigest: p.Subject.Digest, EntryOperationID: string(p.EntryOperationID)}); e != nil {
			return e
		}
		if permit := switchPermit(ctx); permit != nil {
			if e = q.B04InsertAction(ctx, dbgen.B04InsertActionParams{Scope: "i03:legacy-switch:" + string(permit.Node), Key: permit.Key, RequestDigest: permit.RequestDigest, TaskID: string(task)}); e != nil {
				return e
			}
		}
		if p.InternalRetirement {
			if e = q.B04InsertAction(ctx, dbgen.B04InsertActionParams{Scope: "i03:retire:" + string(p.Subject.ID), Key: m.IdempotencyKey, RequestDigest: retireHash, TaskID: string(task)}); e != nil {
				return e
			}
		}
		if p.Action != nil {
			if e = q.I03PutAction(ctx, dbgen.I03PutActionParams{TaskID: string(task), SourceTaskID: string(p.Action.SourceTaskID), SourceRevision: int64(p.Action.SourceRevision), Action: p.Action.Kind}); e != nil {
				return e
			}
			if e = q.B04InsertAction(ctx, dbgen.B04InsertActionParams{Scope: "b04:" + p.Action.Kind + ":" + string(p.Action.SourceTaskID), Key: p.Action.IdempotencyKey, RequestDigest: actionHash, TaskID: string(task)}); e != nil {
				return e
			}
		}
		matched := p.EntryOperationID == "" || p.Action != nil
		for _, t := range prepared {
			if s.v2Session(t.node.NodeID) != t.session {
				return problem(domain.NotImplemented)
			}
			n, e := q.GetNode(ctx, string(t.node.NodeID))
			if e != nil {
				return e
			}
			if n.RemovedAt.Valid || n.Revision != int64(t.node.ExpectedRevision) || n.RevocationEpoch != int64(t.node.Template.RevocationEpoch) {
				return conflict(n.Revision)
			}
			inv, e := q.I03Inventory(ctx, n.ID)
			if e != nil {
				return e
			}
			if inv.NodeRevision != n.Revision || inv.ExpiresAt <= stamp(now) || inv.DnsDigest != t.node.Template.DNS.Pin.Digest {
				return problem(domain.RevisionConflict)
			}
			if e = checkV2Base(ctx, q, t); e != nil {
				return e
			}
			if e = checkV2Policy(ctx, q, t.node.Template, n.Revision, now); e != nil {
				return e
			}
			if _, e = q.B04BumpNode(ctx, dbgen.B04BumpNodeParams{NodeID: n.ID, Expected: n.Revision, Epoch: n.RevocationEpoch, Now: stamp(now)}); e != nil {
				return e
			}
			if e = q.InsertTaskNode(ctx, dbgen.InsertTaskNodeParams{TaskID: string(task), NodeID: n.ID, TargetRevision: t.artifact.TargetRevision, TargetDigest: t.artifact.TargetDigest, RevocationEpoch: n.RevocationEpoch}); e != nil {
				return e
			}
			if e = q.B04InsertTarget(ctx, dbgen.B04InsertTargetParams{TaskID: string(task), NodeID: n.ID}); e != nil {
				return e
			}
			if e = s.insertV2Resources(ctx, q, t, now); e != nil {
				return e
			}
			if p.EntryOperationID != "" {
				yes, e := matchChange(ctx, q, t, change)
				if e != nil {
					return e
				}
				matched = matched || yes
			}
			if e = q.I03InsertV2Artifact(ctx, t.artifact); e != nil {
				return e
			}
		}
		if p.Kind == domain.Cleanup && p.EntryOperationID != "" {
			i, e := q.B08CleanupIntent(ctx, string(p.EntryOperationID))
			if e != nil {
				return e
			}
			prior, e := q.I03CleanupAttempt(ctx, i.CleanupID)
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if e = q.I03PutCleanupAttempt(ctx, dbgen.I03PutCleanupAttemptParams{OperationID: i.CleanupID, Generation: i.CleanupGeneration, TaskID: string(task), PriorTaskID: nullableIf(prior.TaskID)}); e != nil {
				return e
			}
		}
		if !matched {
			return problem(domain.GrantOriginUncertain)
		}
		for _, t := range prepared {
			for _, dep := range t.node.DependsOn {
				if e = q.InsertTaskDependency(ctx, dbgen.InsertTaskDependencyParams{TaskID: string(task), NodeID: string(t.node.NodeID), RequiresNodeID: string(dep)}); e != nil {
					return e
				}
			}
			for _, f := range t.fences {
				if e = q.I03PutLinkFence(ctx, f); e != nil {
					return e
				}
			}
		}
		out.TaskID = task
		return nil
	})
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	s.signal()
	return out, nil
}

func checkV2Base(ctx context.Context, q *dbgen.Queries, t preparedV2) error {
	if t.snap.BaseState == "confirmed" {
		state, e := q.B04GetState(ctx, string(t.node.NodeID))
		if e != nil {
			return e
		}
		confirmedRestriction, e := confirmedRestrictionBase(ctx, q, state)
		if e != nil {
			return e
		}
		if state.Reconciliation != "matched" || state.AppliedRevision != int64(t.snap.Artifact.TargetRevision) || state.ArtifactDigest != t.snap.Artifact.TargetDigest || state.EffectiveDigest != t.snap.EffectiveDigest || !confirmedRestriction {
			return problem(domain.GrantOriginUncertain)
		}
	}
	return nil
}
func checkV2Policy(ctx context.Context, q *dbgen.Queries, b control.BundleV2, rev int64, now time.Time) error {
	for _, p := range b.Terminal {
		for _, t := range p.Spec.Targets {
			if t.Spec.Type == "node_default" && !bUsesUDP(b, p.Pin) {
				continue
			}
			row, e := q.B06EgressEvidence(ctx, dbgen.B06EgressEvidenceParams{NodeID: string(b.NodeID), NodeRevision: rev, TargetDigest: t.Pin.Digest, DnsDigest: p.Spec.DNS.Digest})
			if e != nil {
				return missing(e)
			}
			observation, e := q.I03Observation(ctx, dbgen.I03ObservationParams{NodeID: string(b.NodeID), NodeRevision: rev, TargetDigest: t.Pin.Digest, DnsDigest: p.Spec.DNS.Digest})
			if e != nil {
				return missing(e)
			}
			if observation.ExpiresAt != row.ExpiresAt {
				return problem(domain.RevisionConflict)
			}
			if row.ExpiresAt <= stamp(now) || bUsesUDP(b, p.Pin) && row.BusinessUdp != 1 {
				return problem(domain.DependencyInUse)
			}
		}
	}
	return nil
}
func bUsesUDP(b control.BundleV2, p control.Pin) bool {
	for _, l := range b.Listeners {
		if l.Tail.Egress == p && l.Tail.BusinessUDP {
			return true
		}
	}
	return false
}
func (s *Service) insertV2Resources(ctx context.Context, q *dbgen.Queries, t preparedV2, now time.Time) error {
	b := t.node.Template
	task := t.artifact.TaskID
	node := t.artifact.NodeID
	for i, l := range b.Listeners {
		row, e := q.B06InternalListener(ctx, string(l.Socket.ListenerID))
		if e != nil {
			return missing(e)
		}
		if row.NodeID != node || row.Protocol != string(l.Tail.Hops[0].Endpoint.Protocol) || row.ListenAddress != l.Socket.Address || row.ListenPort != int64(l.Socket.Port) || row.OuterTransport != l.Socket.Transport || row.CredentialRef != string(l.Socket.Credential.Ref) || row.CleanupState != "none" {
			return problem(domain.GrantOriginUncertain)
		}
		var identity sql.NullString
		if l.Entry != nil {
			a := l.Entry
			if !a.ExpiresAt.After(now) {
				return problem(domain.TaskExpired)
			}
			ok, e := q.I03EntryAuthorized(ctx, dbgen.I03EntryAuthorizedParams{IdentityID: string(a.IdentityID), Now: stamp(now)})
			if e != nil {
				return e
			}
			if !ok {
				return problem(domain.RevisionConflict)
			}
			if e = storeGrant(ctx, q, dbgen.B04InsertGrantParams{IdentityID: string(a.IdentityID), EntryID: string(a.EntryID), ListenerID: row.ID, NodeID: node, Generation: int64(a.Generation), ExpiresAt: stamp(a.ExpiresAt), GrantDigest: t.grantHashes[i]}); e != nil {
				return e
			}
			if e = q.B04InsertTargetGrant(ctx, dbgen.B04InsertTargetGrantParams{TaskID: task, NodeID: node, IdentityID: string(a.IdentityID), Action: "include"}); e != nil {
				return e
			}
			identity = nullable(string(a.IdentityID))
		} else {
			link, e := q.B06LinkByID(ctx, string(l.Internal.ID))
			if e != nil {
				return e
			}
			if link.State != "active" || link.SuffixDigest != l.Internal.Digest || link.ListenerID != row.ID {
				return problem(domain.GrantOriginUncertain)
			}
			if e = q.I03PutTargetLink(ctx, dbgen.I03PutTargetLinkParams{TaskID: task, NodeID: node, LinkID: link.ID, Action: "include"}); e != nil {
				return e
			}
		}
		if e = q.B04InsertReference(ctx, dbgen.B04InsertReferenceParams{ID: string(t.refIDs[i]), TaskID: task, NodeID: node, Kind: "listener", ResourceID: row.ID, ResourceRevision: 1, IdentityID: identity}); e != nil {
			return e
		}
	}
	for _, tomb := range b.Tombstones {
		grant, e := q.B04GetGrant(ctx, string(tomb.IdentityID))
		if e != nil {
			return missing(e)
		}
		if grant.NodeID != node || grant.Generation != int64(tomb.Generation) || tomb.Reason == "expired" && (stamp(tomb.At) < grant.ExpiresAt || stamp(now) < grant.ExpiresAt) {
			return problem(domain.GrantOriginUncertain)
		}
		action := "revoke"
		if tomb.Reason == "expired" {
			action = "expire"
		}
		if e = q.B04InsertTargetGrant(ctx, dbgen.B04InsertTargetGrantParams{TaskID: task, NodeID: node, IdentityID: grant.IdentityID, Action: action}); e != nil {
			return e
		}
		if e = markPending(ctx, q, node, grant.IdentityID, task, stamp(now)); e != nil {
			return e
		}
	}
	for _, r := range b.RetiredLinks {
		link, e := q.B06LinkByID(ctx, string(r.Link.ID))
		if e != nil {
			return e
		}
		if link.State == "active" || link.SuffixDigest != r.Link.Digest || link.NodeID != node {
			return problem(domain.DependencyInUse)
		}
		if e = q.I03PutTargetLink(ctx, dbgen.I03PutTargetLinkParams{TaskID: task, NodeID: node, LinkID: link.ID, Action: "retire"}); e != nil {
			return e
		}
	}
	lives, e := q.I03LiveLinks(ctx, node)
	if e != nil {
		return e
	}
	for _, l := range lives {
		found := false
		for _, r := range b.Listeners {
			found = found || r.Internal != nil && string(r.Internal.ID) == l.LinkID
		}
		for _, r := range b.RetiredLinks {
			found = found || string(r.Link.ID) == l.LinkID
		}
		if !found {
			return problem(domain.DependencyInUse)
		}
	}
	return checkTargetGrants(ctx, q, task, node, now)
}
func matchChange(ctx context.Context, q *dbgen.Queries, t preparedV2, c stage3.EntryChange) (bool, error) {
	if c.Lease.ListenerID == "" {
		return false, problem(domain.GrantOriginUncertain)
	}
	origin, e := q.B06IngressByIdentity(ctx, string(c.Entry.Authorization.IdentityID))
	if e != nil {
		return false, e
	}
	lease, e := q.I03Lease(ctx, origin.OperationID)
	if e != nil {
		return false, e
	}
	if lease.ListenerID != string(c.Lease.ListenerID) || lease.ActualPort != int64(c.Lease.Endpoint.Port) || lease.ReleasedAt.Valid || c.Kind == domain.Publish && lease.BlockedAt.Valid {
		return false, problem(domain.GrantOriginUncertain)
	}
	b := t.node.Template
	if b.RevocationEpoch != c.Epoch {
		return false, problem(domain.RevisionConflict)
	}
	for _, l := range b.Listeners {
		if l.Entry != nil && l.Entry.IdentityID == c.Entry.Authorization.IdentityID {
			return c.Kind == domain.Publish && l.Socket.ListenerID == c.Lease.ListenerID && l.Socket.Credential.Ref == c.Entry.CredentialRef && l.Socket.Port == c.Lease.Endpoint.Port && *l.Entry == c.Entry.Authorization, nil
		}
	}
	for _, r := range b.Tombstones {
		if r.IdentityID == c.Entry.Authorization.IdentityID && r.Generation == c.Entry.Authorization.Generation {
			return c.Kind != domain.Publish, nil
		}
	}
	return false, nil
}

func (s *Service) ReleaseState(ctx context.Context, id domain.ID) (stage3.ReleaseState, error) {
	var out stage3.ReleaseState
	err := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		meta, e := q.I03Task(ctx, string(id))
		if e != nil {
			return missing(e)
		}
		task, e := q.GetTask(ctx, string(id))
		if e != nil {
			return e
		}
		dto, e := taskDTO(ctx, q, task)
		if e != nil {
			return e
		}
		out = stage3.ReleaseState{TaskID: id, Subject: control.Pin{ID: dto.Subject.ID, Revision: dto.Subject.Revision, Digest: meta.SubjectDigest}, EntryOperationID: domain.ID(meta.EntryOperationID), State: task.State, Nodes: dto.Nodes}
		if task.State == "succeeded" {
			out.State = "published"
		}
		return nil
	})
	return out, err
}

func (s *Service) openV2(ctx context.Context, a dbgen.B04Artifact) (control.BundleV2, error) {
	if a.Format != 2 {
		return control.BundleV2{}, problem(domain.NotImplemented)
	}
	data, e := s.openBytes(ctx, a)
	if e != nil {
		return control.BundleV2{}, e
	}
	defer clear(data)
	return control.DecodeBundleV2(data, artifactKey(a), domain.ID(a.NodeID))
}

// checkV2Target is used at queue arm and both sides of artifact decryption. It
// does not decrypt or call a business service while holding the database lock.
func (s *Service) checkV2Target(ctx context.Context, q *dbgen.Queries, task, node string, now time.Time) error {
	a, e := q.B04GetArtifact(ctx, dbgen.B04GetArtifactParams{TaskID: task, NodeID: node})
	if e != nil {
		return e
	}
	if a.Format == 1 {
		return nil
	}
	session := s.v2Session(domain.ID(node))
	if a.Format != 2 || session == "" || (control.SessionFromContext(ctx) != "" && string(control.SessionFromContext(ctx)) != session) {
		return problem(domain.NotImplemented)
	}
	meta, e := q.I03Task(ctx, task)
	if e != nil {
		return e
	}
	t, e := q.GetTask(ctx, task)
	if e != nil {
		return e
	}
	if meta.EntryOperationID != "" {
		c, e := subscriptions.CheckDispatch(ctx, q, domain.ID(meta.EntryOperationID), domain.TaskKind(t.Kind), control.Pin{ID: domain.ID(t.SubjectID), Revision: domain.Revision(t.TargetRevision), Digest: meta.SubjectDigest}, now)
		if e != nil {
			return e
		}
		origin, e := q.B06IngressByIdentity(ctx, string(c.Entry.Authorization.IdentityID))
		if e != nil {
			return e
		}
		lease, e := q.I03Lease(ctx, origin.OperationID)
		if e != nil {
			return e
		}
		if lease.ReleasedAt.Valid || t.Kind == "publish" && lease.BlockedAt.Valid {
			return problem(domain.GrantOriginUncertain)
		}
	}
	grants, e := q.B04TargetGrants(ctx, dbgen.B04TargetGrantsParams{TaskID: task, NodeID: node})
	if e != nil {
		return e
	}
	for _, g := range grants {
		if g.Action == "include" {
			ok, e := q.I03EntryAuthorized(ctx, dbgen.I03EntryAuthorizedParams{IdentityID: g.IdentityID, Now: stamp(now)})
			if e != nil {
				return e
			}
			if !ok {
				return problem(domain.RevisionConflict)
			}
		}
	}
	links, e := q.I03TargetLinks(ctx, dbgen.I03TargetLinksParams{TaskID: task, NodeID: node})
	if e != nil {
		return e
	}
	for _, l := range links {
		if l.Action != "include" {
			continue
		}
		if l.State != "active" {
			return problem(domain.GrantOriginUncertain)
		}
		origin, e := q.I03Origin(ctx, l.ID)
		if errors.Is(e, sql.ErrNoRows) {
			continue
		}
		if e != nil {
			return e
		}
		if origin.TaskID != task {
			target, e := q.B04GetTarget(ctx, dbgen.B04GetTargetParams{TaskID: origin.TaskID, NodeID: node})
			if e != nil {
				return e
			}
			if target.Phase != "confirmed" {
				return problem(domain.GrantOriginUncertain)
			}
		}
	}
	fences, e := q.I03LinkFences(ctx, dbgen.I03LinkFencesParams{TaskID: task, NodeID: node})
	if e != nil {
		return e
	}
	for _, f := range fences {
		n, e := q.GetNode(ctx, f.ReceiverID)
		if e != nil {
			return e
		}
		st, e := q.B04GetState(ctx, n.ID)
		if e != nil {
			return missing(e)
		}
		link, e := q.B06LinkByID(ctx, f.LinkID)
		if e != nil {
			return e
		}
		confirmedRestriction, e := confirmedRestrictionBase(ctx, q, st)
		if e != nil {
			return e
		}
		if n.RemovedAt.Valid || n.RevocationEpoch != f.Epoch || st.Epoch != f.Epoch || st.Reconciliation != "matched" || st.AppliedRevision != f.TargetRevision || st.ArtifactDigest != f.ArtifactDigest || !confirmedRestriction || link.State != "active" {
			return problem(domain.GrantOriginUncertain)
		}
	}
	return nil
}

func nullableIf(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
