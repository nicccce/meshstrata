package releases

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/ports"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type resourceKey struct {
	kind     string
	id       domain.ID
	identity domain.ID
}

func (s *Service) extractReferences(t *preparedTarget) error {
	b := t.plan.Template
	pins := make(map[domain.ID]control.Resource, len(b.Resources))
	tied := make(map[domain.ID]bool)
	for _, r := range b.Resources {
		pins[r.ID] = r
	}
	seen := map[resourceKey]bool{}
	add := func(kind string, id, identity domain.ID, rev domain.Revision) error {
		if pin, ok := pins[id]; ok && pin.Kind == kind {
			rev = pin.Revision
			tied[id] = true
		}
		key := resourceKey{kind, id, identity}
		if seen[key] {
			return nil
		}
		seen[key] = true
		ref, err := s.newID("reference")
		if err != nil {
			return err
		}
		t.refs = append(t.refs, dbgen.B04InsertReferenceParams{ID: string(ref), TaskID: t.artifact.TaskID, NodeID: t.artifact.NodeID,
			Kind: kind, ResourceID: string(id), ResourceRevision: int64(rev), IdentityID: sql.NullString{String: string(identity), Valid: identity != ""}})
		return nil
	}
	for _, listener := range b.Listeners {
		a := listener.Authorization
		hash, err := hashPayload(listener)
		if err != nil {
			return err
		}
		t.grants = append(t.grants, dbgen.B04InsertGrantParams{IdentityID: string(a.IdentityID), EntryID: string(a.EntryID), ListenerID: string(listener.ListenerID),
			NodeID: t.artifact.NodeID, Generation: int64(a.Generation), ExpiresAt: stamp(a.ExpiresAt), GrantDigest: hash})
		for _, ref := range []struct {
			kind string
			id   domain.ID
			rev  domain.Revision
		}{
			{"listener", listener.ListenerID, 1}, {"identity", a.IdentityID, a.Generation}, {"entry", a.EntryID, a.Generation},
			{"credential", listener.Credential.Ref, 1},
		} {
			if err := add(ref.kind, ref.id, a.IdentityID, ref.rev); err != nil {
				return err
			}
		}
		for _, m := range []*control.LocalMaterial{listener.Certificate, listener.PrivateKey, listener.RealityKey} {
			if m != nil {
				if err := add("certificate", m.ID, a.IdentityID, 1); err != nil {
					return err
				}
			}
		}
		for _, transport := range []string{"tcp", "udp"} {
			if listener.Transport != transport && listener.Transport != "tcp_udp" {
				continue
			}
			id, err := s.newID("port")
			if err != nil {
				return err
			}
			t.portRows = append(t.portRows, dbgen.B04InsertPortParams{ID: string(id), NodeID: t.artifact.NodeID, ListenerID: string(listener.ListenerID),
				IdentityID: string(a.IdentityID), Address: listener.Address, Port: int64(listener.Port), Transport: transport})
		}
	}
	for _, r := range b.Resources {
		if !tied[r.ID] {
			if err := add(r.Kind, r.ID, "", r.Revision); err != nil {
				return err
			}
		}
	}
	return nil
}

func insertResources(ctx context.Context, q *dbgen.Queries, t preparedTarget, now time.Time) error {
	a, b := t.artifact, t.plan.Template
	for i, grant := range t.grants {
		if !b.Listeners[i].Authorization.ExpiresAt.After(now) || b.Listeners[i].Authorization.RevokedAt != nil {
			return problem(domain.TaskExpired)
		}
		if err := storeGrant(ctx, q, grant); err != nil {
			return err
		}
		if err := q.B04InsertTargetGrant(ctx, dbgen.B04InsertTargetGrantParams{TaskID: a.TaskID, NodeID: a.NodeID, IdentityID: grant.IdentityID, Action: "include"}); err != nil {
			return err
		}
	}
	for _, tomb := range b.Tombstones {
		grant, err := q.B04GetGrant(ctx, string(tomb.IdentityID))
		if errors.Is(err, sql.ErrNoRows) {
			return problem(domain.RevisionConflict)
		}
		if err != nil {
			return err
		}
		if grant.NodeID != a.NodeID || grant.Generation != int64(tomb.Generation) ||
			tomb.Reason == "expired" && (stamp(tomb.At) < grant.ExpiresAt || stamp(now) < grant.ExpiresAt) ||
			tomb.Reason == "revoked" && b.Kind == domain.Publish && !grant.RestrictedAt.Valid {
			return problem(domain.RevisionConflict)
		}
		action := "expire"
		if tomb.Reason == "revoked" {
			action = "revoke"
		}
		if err := q.B04InsertTargetGrant(ctx, dbgen.B04InsertTargetGrantParams{TaskID: a.TaskID, NodeID: a.NodeID, IdentityID: grant.IdentityID, Action: action}); err != nil {
			return err
		}
		// An explicit trusted restriction is permanent even if delivery later times out.
		if err := markPending(ctx, q, a.NodeID, grant.IdentityID, a.TaskID, stamp(now)); err != nil {
			return err
		}
	}
	for _, ref := range t.refs {
		if err := q.B04InsertReference(ctx, ref); err != nil {
			return err
		}
	}
	for _, port := range t.portRows {
		if err := reservePort(ctx, q, port); err != nil {
			return err
		}
	}
	return nil
}

func storeGrant(ctx context.Context, q *dbgen.Queries, grant dbgen.B04InsertGrantParams) error {
	old, err := q.B04GetGrant(ctx, grant.IdentityID)
	if err == nil {
		if old.GrantDigest != grant.GrantDigest || old.RestrictedAt.Valid {
			return problem(domain.RevisionConflict)
		}
		return nil
	}
	if !legitimatelyAbsent(err) {
		return err
	}
	generation, err := q.B04NewestGeneration(ctx, grant.EntryID)
	if err != nil && !legitimatelyAbsent(err) {
		return err
	}
	if generation >= grant.Generation {
		return problem(domain.RevisionConflict)
	}
	return q.B04InsertGrant(ctx, grant)
}

func reservePort(ctx context.Context, q *dbgen.Queries, p dbgen.B04InsertPortParams) error {
	wanted := ports.Parse(p.Address, p.Port, p.Transport)
	reserved, err := ports.CheckAvailable(ctx, q, p.NodeID, wanted, ports.Owner{Listener: p.ListenerID, Identity: p.IdentityID})
	if err != nil {
		return err
	}
	if reserved {
		// storeGrant has already ruled out reactivation of a restricted grant.
		return q.B04ReusePort(ctx, dbgen.B04ReusePortParams{NodeID: p.NodeID, Port: p.Port,
			Address: p.Address, Transport: p.Transport, IdentityID: p.IdentityID})
	}
	return q.B04InsertPort(ctx, p)
}

func retireReferences(ctx context.Context, q *dbgen.Queries, refs []string, operation, at string) error {
	for _, ref := range refs {
		if err := q.B04LogReferenceRelease(ctx, dbgen.B04LogReferenceReleaseParams{ReferenceID: ref, OperationID: operation, ReleasedAt: at}); err != nil {
			return err
		}
		if _, err := q.B04RetireReference(ctx, dbgen.B04RetireReferenceParams{ID: ref, At: nullable(at)}); err != nil {
			return err
		}
	}
	return nil
}

func markPending(ctx context.Context, q *dbgen.Queries, node, identity, operation, at string) error {
	if err := q.B04MarkRestricted(ctx, dbgen.B04MarkRestrictedParams{IdentityID: identity, At: nullable(at)}); err != nil {
		return err
	}
	if err := q.B04PendingPorts(ctx, identity); err != nil {
		return err
	}
	refs, err := q.B04IdentityReferences(ctx, dbgen.B04IdentityReferencesParams{NodeID: node, IdentityID: nullable(identity)})
	if err != nil {
		return err
	}
	return retireReferences(ctx, q, refs, operation, at)
}

func confirmCleanup(ctx context.Context, q *dbgen.Queries, node, identity, operation, at string) error {
	if err := markPending(ctx, q, node, identity, operation, at); err != nil {
		return err
	}
	if err := q.B04ConfirmGrant(ctx, dbgen.B04ConfirmGrantParams{IdentityID: identity, At: nullable(at)}); err != nil {
		return err
	}
	return q.B04SweepPorts(ctx, dbgen.B04SweepPortsParams{NodeID: node, At: nullable(at)})
}
