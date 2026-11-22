package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

func validReservation(in stage3.EntryReservation) bool {
	a := in.Authorization
	return in.OperationID.Valid() && in.SubscriptionID.Valid() && in.Route.Valid() && in.CredentialRef.Valid() &&
		a.EntryID.Valid() && a.IdentityID.Valid() && a.Generation.Valid() && utc(a.ExpiresAt) && utc(in.Deadline)
}
func (s *Service) ReserveIngress(ctx context.Context, in stage3.EntryReservation) (stage3.IngressLease, error) {
	out, err := s.reserveIngress(ctx, in)
	if err = safe(ctx, err); err != nil {
		return stage3.IngressLease{}, err
	}
	return out, nil
}
func (s *Service) reserveIngress(ctx context.Context, in stage3.EntryReservation) (stage3.IngressLease, error) {
	none := stage3.IngressLease{}
	if !validReservation(in) || in.Authorization.RevokedAt != nil {
		return none, problem(domain.InvalidArgument, "/entry")
	}
	digest, err := control.ComponentDigest(in)
	if err != nil {
		return none, err
	}
	data, _ := json.Marshal(in)
	var row dbgen.B06Ingress
	load := func(q *dbgen.Queries) error {
		var e error
		row, e = q.B06Ingress(ctx, string(in.OperationID))
		return e
	}
	err = s.opts.DB.Read(ctx, load)
	if err == nil {
		if row.RequestDigest != digest {
			return none, problem(domain.IdempotencyConflict, "/entry")
		}
		if len(row.Lease) != 0 {
			if e := json.Unmarshal(row.Lease, &none); e != nil {
				return stage3.IngressLease{}, e
			}
			return none, nil
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return none, err
	}
	if !in.Deadline.After(s.now()) || !in.Authorization.ExpiresAt.After(s.now()) {
		return none, problem(domain.TaskExpired, "/entry")
	}
	route, err := s.PublishedRoute(ctx, in.Route)
	if err != nil {
		return none, err
	}
	h := route.Tail.Hops[0]
	ni, err := s.node(ctx, draftHop(h), route.Tail.BusinessUDP, false, 0)
	if err != nil {
		return none, err
	}
	keys := make([]string, 0, len(route.Tail.Hops)-1)
	for i := 1; i < len(route.Tail.Hops); i++ {
		key, err := suffix(route.Tail, i).Key()
		if err != nil {
			return none, err
		}
		keys = append(keys, key)
	}

	if row.OperationID == "" {
		err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
			e := load(q)
			if e == nil {
				if row.RequestDigest != digest {
					return problem(domain.IdempotencyConflict, "/entry")
				}
				return nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			_, e = q.B06IngressByIdentity(ctx, string(in.Authorization.IdentityID))
			if e == nil {
				return problem(domain.IdempotencyConflict, "/entry/identity_id")
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			_, e = q.B06IngressByCredential(ctx, string(in.CredentialRef))
			if e == nil {
				return problem(domain.IdempotencyConflict, "/entry/credential")
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			_, e = q.B01GetCredential(ctx, dbgen.B01GetCredentialParams{ID: string(in.CredentialRef), Purpose: string(secretstore.ProxyEntry)})
			if e != nil {
				return e
			}
			e = q.B06InsertIngress(ctx, dbgen.B06InsertIngressParams{OperationID: string(in.OperationID), RequestDigest: digest, Reservation: data,
				IdentityID: string(in.Authorization.IdentityID), CredentialRef: string(in.CredentialRef), RouteID: string(in.Route.ID), Revision: int64(in.Route.Revision)})
			if e != nil {
				return e
			}
			for _, key := range keys {
				link, e := q.B06LinkBySuffix(ctx, key)
				if e != nil {
					return e
				}
				if link.State != "active" {
					return problem(domain.DependencyInUse, "/entry/link")
				}
				if e = q.B06AttachEntryReference(ctx, dbgen.B06AttachEntryReferenceParams{IdentityID: string(in.Authorization.IdentityID), LinkID: link.ID}); e != nil {
					return e
				}
			}
			return nil
		})
		if err != nil {
			return none, err
		}
	}
	lease, err := s.opts.Infra.ReserveIngress(ctx, in, route)
	if err != nil {
		return none, dependency(ctx, err, "/entry/lease")
	}
	endpoint := h.Endpoint
	endpoint.Port = lease.Endpoint.Port
	if !lease.ListenerID.Valid() || !lease.ReservationID.Valid() || lease.Route != in.Route || lease.Endpoint != endpoint || !lease.Endpoint.Valid() {
		return none, problem(domain.GrantOriginUncertain, "/entry/lease")
	}
	spec := ni.sock.Spec
	if lease.Endpoint.Port != spec.RequestedPort && (ni.sock.PoolFirst == 0 || lease.Endpoint.Port < ni.sock.PoolFirst || lease.Endpoint.Port > ni.sock.PoolLast) {
		return none, problem(domain.InvalidArgument, "/entry/lease/port")
	}
	blob, _ := json.Marshal(lease)
	err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		if e := load(q); e != nil {
			return e
		}
		if row.RequestDigest != digest {
			return problem(domain.IdempotencyConflict, "/entry")
		}
		if len(row.Lease) != 0 {
			if string(row.Lease) != string(blob) {
				return problem(domain.GrantOriginUncertain, "/entry/lease")
			}
			return nil
		}
		l, e := q.B06InternalListener(ctx, string(lease.ListenerID))
		if e != nil {
			return e
		}
		if l.NodeID != string(h.NodeID) || l.CredentialRef != string(in.CredentialRef) || l.ListenPort != int64(lease.Endpoint.Port) || l.Protocol != string(h.Endpoint.Protocol) ||
			l.ListenAddress != spec.Address || l.OuterTransport != spec.Transport || l.DnsRevision != int64(h.DNS.Revision) || l.CleanupState != "none" {
			return problem(domain.GrantOriginUncertain, "/entry/lease")
		}
		_, e = q.B06BindIngress(ctx, dbgen.B06BindIngressParams{OperationID: string(in.OperationID), Lease: blob, ListenerID: sql.NullString{String: string(lease.ListenerID), Valid: true}})
		return e
	})
	return lease, err
}

func draftHop(h control.HopSpec) stage3.HopDraft {
	return stage3.HopDraft{NodeID: h.NodeID, NodeConfig: h.NodeConfig, DNS: h.DNS, Endpoint: h.Endpoint, DialIP: h.DialIP}
}
func (s *Service) ApplyEntry(ctx context.Context, c stage3.EntryChange) (domain.TaskAccepted, error) {
	out, err := s.applyEntry(ctx, c)
	if err = safe(ctx, err); err != nil {
		return domain.TaskAccepted{}, err
	}
	return out, nil
}
func (s *Service) applyEntry(ctx context.Context, c stage3.EntryChange) (domain.TaskAccepted, error) {
	none := domain.TaskAccepted{}
	if !validReservation(c.Entry) || !c.Subject.Valid() || c.Subject.ID != c.Entry.SubscriptionID || !c.Mutation.ExpectedRevision.Valid() ||
		!validKey(c.Mutation.IdempotencyKey) || !c.Epoch.Valid() || (c.Kind != domain.Publish && c.Kind != domain.Revoke && c.Kind != domain.Cleanup) {
		return none, problem(domain.InvalidArgument, "/entry")
	}
	hash, err := control.ComponentDigest(c)
	if err != nil {
		return none, err
	}
	key := dbgen.B06EntryPlanParams{Kind: string(c.Kind), IdemKey: c.Mutation.IdempotencyKey}
	var row dbgen.B06EntryPlan
	var origin dbgen.B06Ingress
	err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		row, e = q.B06EntryPlan(ctx, key)
		if e == nil && row.RequestDigest != hash {
			return problem(domain.IdempotencyConflict, "/entry")
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		origin, e = q.B06IngressByIdentity(ctx, string(c.Entry.Authorization.IdentityID))
		return e
	})
	if err != nil {
		return none, err
	}
	if row.TaskID.Valid {
		return domain.TaskAccepted{TaskID: domain.ID(row.TaskID.String)}, nil
	}
	var reserved stage3.EntryReservation
	var got stage3.IngressLease
	if len(origin.Lease) == 0 || json.Unmarshal(origin.Reservation, &reserved) != nil || json.Unmarshal(origin.Lease, &got) != nil || got != c.Lease ||
		reserved.SubscriptionID != c.Entry.SubscriptionID || reserved.Route != c.Entry.Route || reserved.CredentialRef != c.Entry.CredentialRef ||
		reserved.Authorization.IdentityID != c.Entry.Authorization.IdentityID || reserved.Authorization.EntryID != c.Entry.Authorization.EntryID ||
		reserved.Authorization.Generation != c.Entry.Authorization.Generation || !reserved.Authorization.ExpiresAt.Equal(c.Entry.Authorization.ExpiresAt) {
		return none, problem(domain.GrantOriginUncertain, "/entry")
	}
	if c.Kind == domain.Publish && (c.Entry.OperationID != reserved.OperationID || !c.Entry.Deadline.Equal(reserved.Deadline) || c.Entry.Authorization.RevokedAt != nil) {
		return none, problem(domain.GrantOriginUncertain, "/entry")
	}
	if at := c.Entry.Authorization.RevokedAt; at != nil && (!utc(*at) || at.After(s.now())) {
		return none, problem(domain.InvalidArgument, "/entry/revoked_at")
	}
	var plan stage3.PublishPlan
	if row.OperationID == "" {
		if !c.Entry.Deadline.After(s.now()) || c.Kind == domain.Publish && !reserved.Authorization.ExpiresAt.After(s.now()) {
			return none, problem(domain.TaskExpired, "/entry")
		}
		route, err := s.PublishedRoute(ctx, reserved.Route)
		if err != nil {
			return none, err
		}
		var refs []domain.ID
		plan, refs, err = s.compileEntry(ctx, c, route)
		if err != nil {
			return none, err
		}
		ref := derived("entry_plan", c.Entry.OperationID, string(c.Kind)+"/"+strconv.FormatInt(c.RecoveryGeneration, 10))
		sealed, err := s.sealPlan(ctx, ref, plan)
		if err != nil {
			return none, err
		}
		err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
			var e error
			row, e = q.B06EntryPlan(ctx, key)
			if e == nil {
				if row.RequestDigest != hash {
					return problem(domain.IdempotencyConflict, "/entry")
				}
				return nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			_, e = q.B06EntryOperationPlan(ctx, dbgen.B06EntryOperationPlanParams{OperationID: string(c.Entry.OperationID), Kind: string(c.Kind), RecoveryGeneration: c.RecoveryGeneration})
			if e == nil {
				return problem(domain.IdempotencyConflict, "/entry")
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			for _, id := range refs {
				if e = q.B06AttachEntryReference(ctx, dbgen.B06AttachEntryReferenceParams{IdentityID: origin.IdentityID, LinkID: string(id)}); e != nil {
					return e
				}
			}
			if e = q.B06InsertEntryPlan(ctx, dbgen.B06InsertEntryPlanParams{OperationID: string(c.Entry.OperationID), OriginOperationID: origin.OperationID, Kind: key.Kind, IdemKey: key.IdemKey,
				RecoveryGeneration: c.RecoveryGeneration, RequestDigest: hash, SecretRef: string(ref), KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext}); e != nil {
				return e
			}
			row, e = q.B06EntryPlan(ctx, key)
			return e
		})
		if err != nil {
			return none, err
		}
	}
	if row.TaskID.Valid {
		return domain.TaskAccepted{TaskID: domain.ID(row.TaskID.String)}, nil
	}
	if err = s.openPlan(ctx, row.SecretRef, row.KeyID, row.Ciphertext, &plan); err != nil {
		return none, err
	}
	if plan.Subject != c.Subject || plan.EntryOperationID != c.Entry.OperationID || plan.Kind != c.Kind || !plan.Deadline.Equal(c.Entry.Deadline) {
		return none, problem(domain.GrantOriginUncertain, "/entry/plan")
	}
	digest, err := control.ComponentDigest(plan)
	if err != nil {
		return none, err
	}
	if err = s.seedEntryPlan(ctx, row, digest); err != nil {
		return none, err
	}
	taskKey := string(derived("entry_task", c.Entry.OperationID, string(c.Kind)+"/"+strconv.FormatInt(c.RecoveryGeneration, 10)))
	out, err := s.opts.Publisher.SubmitV2(ctx, domain.Mutation{ExpectedRevision: c.Mutation.ExpectedRevision, IdempotencyKey: taskKey}, plan)
	if err != nil {
		// Exact replay above must precede replanning: a lost accepted reply is
		// authoritative even if its old node baseline has since advanced.
		if codeIs(err, domain.RevisionConflict) {
			return none, s.rebaseEntryPlan(ctx, c, row, digest, taskKey)
		}
		return none, dependency(ctx, err, "/entry/task")
	}
	if !out.TaskID.Valid() {
		return none, problem(domain.GrantOriginUncertain, "/entry/task")
	}
	err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		current, err := q.B06EntryPlan(ctx, key)
		if err != nil {
			return err
		}
		if current.TaskID.Valid {
			if current.TaskID.String != string(out.TaskID) {
				return problem(domain.GrantOriginUncertain, "/entry/task")
			}
			return nil
		}
		_, err = q.B06BindEntryTask(ctx, dbgen.B06BindEntryTaskParams{Kind: key.Kind, IdemKey: key.IdemKey, TaskID: sql.NullString{String: string(out.TaskID), Valid: true}})
		return err
	})
	return out, err
}

func rememberEntryPlan(ctx context.Context, q *dbgen.Queries, row dbgen.B06EntryPlan, digest string) error {
	return q.I03CRememberEntryPlan(ctx, dbgen.I03CRememberEntryPlanParams{Kind: row.Kind, IdemKey: row.IdemKey, Digest: digest, SecretRef: row.SecretRef, KeyID: row.KeyID, Ciphertext: row.Ciphertext})
}

func (s *Service) seedEntryPlan(ctx context.Context, row dbgen.B06EntryPlan, digest string) error {
	return s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		current, e := q.B06EntryPlan(ctx, dbgen.B06EntryPlanParams{Kind: row.Kind, IdemKey: row.IdemKey})
		if e != nil {
			return e
		}
		if current.SecretRef != row.SecretRef || current.PlanDigest != "" && current.PlanDigest != digest {
			return problem(domain.RevisionConflict, "/entry/plan")
		}
		if e = rememberEntryPlan(ctx, q, current, digest); e != nil {
			return e
		}
		_, e = q.I03CSeedEntryPlanDigest(ctx, dbgen.I03CSeedEntryPlanDigestParams{Kind: row.Kind, IdemKey: row.IdemKey, PlanDigest: digest})
		return e
	})
}

func (s *Service) rebaseEntryPlan(ctx context.Context, c stage3.EntryChange, old dbgen.B06EntryPlan, oldDigest, taskKey string) error {
	route, e := s.PublishedRoute(ctx, c.Entry.Route)
	if e != nil {
		return e
	}
	plan, refs, e := s.compileEntry(ctx, c, route)
	if e != nil {
		return e
	}
	digest, e := control.ComponentDigest(plan)
	if e != nil {
		return e
	}
	if digest == oldDigest {
		return problem(domain.RevisionConflict, "/entry/plan")
	}
	ref := derived("entry_plan", c.Entry.OperationID, string(c.Kind)+"/"+digest)
	sealed, e := s.sealPlan(ctx, ref, plan)
	if e != nil {
		return e
	}
	e = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		// This CAS excludes any accepted task in the same SQLite transaction.
		// SubmitV2 also checks the digest here, fencing a delayed old submitter.
		n, e := q.I03CRebaseEntryPlan(ctx, dbgen.I03CRebaseEntryPlanParams{Kind: old.Kind, IdemKey: old.IdemKey, OldDigest: oldDigest, NewDigest: digest, SecretRef: string(ref), KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext, TaskKey: taskKey})
		if e != nil {
			return e
		}
		if n != 1 {
			return problem(domain.RevisionConflict, "/entry/plan")
		}
		for _, id := range refs {
			if e = q.B06AttachEntryReference(ctx, dbgen.B06AttachEntryReferenceParams{IdentityID: string(c.Entry.Authorization.IdentityID), LinkID: string(id)}); e != nil {
				return e
			}
		}
		old.SecretRef, old.KeyID, old.Ciphertext = string(ref), sealed.KeyID, sealed.Ciphertext
		return rememberEntryPlan(ctx, q, old, digest)
	})
	if e != nil {
		return e
	}
	// Resume from the durable candidate on the next bounded worker attempt.
	return problem(domain.RevisionConflict, "/entry/plan")
}

func (s *Service) compileEntry(ctx context.Context, c stage3.EntryChange, route stage3.PublishedRoute) (stage3.PublishPlan, []domain.ID, error) {
	none := stage3.PublishPlan{}
	tail := route.Tail
	h := tail.Hops[0]
	var in nodeInput
	var err error
	if c.Kind == domain.Publish {
		in, err = s.node(ctx, draftHop(h), tail.BusinessUDP, false, 0)
	} else {
		in, err = s.removalNode(ctx, h.NodeID)
	}
	if err != nil {
		return none, nil, err
	}
	checkTail := suffix(tail, 0)
	checkTail.Hops[0].Endpoint.Port = c.Lease.Endpoint.Port
	b := in.base
	b.Kind = c.Kind
	b.TargetRevision = in.snapshot.Revision + 1
	if c.Epoch < b.RevocationEpoch {
		return none, nil, problem(domain.RevisionConflict, "/entry/epoch")
	}
	b.RevocationEpoch = c.Epoch
	auth := c.Entry.Authorization
	var all []stage3.NodePlan
	var refs []domain.ID
	if c.Kind == domain.Publish {
		if c.Lease.Endpoint.Port != in.sock.Spec.RequestedPort && (in.sock.PoolFirst == 0 || c.Lease.Endpoint.Port < in.sock.PoolFirst || c.Lease.Endpoint.Port > in.sock.PoolLast) {
			return none, nil, problem(domain.InvalidArgument, "/entry/lease/port")
		}
		for _, x := range b.Tombstones {
			if x.IdentityID == auth.IdentityID {
				return none, nil, problem(domain.GrantOriginUncertain, "/entry")
			}
		}
		cred, err := s.credential(ctx, h.Endpoint, c.Entry.CredentialRef, h.NodeID, secretstore.ProxyEntry)
		if err != nil {
			return none, nil, err
		}
		l := control.ListenerV2{Socket: socket(in.sock.Spec, c.Lease.ListenerID, c.Lease.Endpoint.Port, cred), Tail: tail, Entry: &auth}
		var next *control.ListenerV2
		for i := len(tail.Hops) - 1; i >= 1; i-- {
			hop := tail.Hops[i]
			fixed, err := s.node(ctx, draftHop(hop), tail.BusinessUDP, false, i)
			if err != nil {
				return none, nil, err
			}
			want := suffix(tail, i)
			key, _ := want.Key()
			var match *control.ListenerV2
			var row dbgen.B06InternalLink
			err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error { var e error; row, e = q.B06LinkBySuffix(ctx, key); return e })
			if err != nil {
				return none, nil, err
			}
			if row.State != "active" {
				return none, nil, problem(domain.DependencyInUse, "/entry/link")
			}
			for _, l := range fixed.base.Listeners {
				expected := control.Pin{ID: domain.ID(row.ID), Revision: domain.Revision(row.Revision), Digest: key}
				if l.Internal != nil && *l.Internal == expected && string(l.Socket.ListenerID) == row.ListenerID && string(l.Socket.Credential.Ref) == row.CredentialRef {
					lc := l
					match = &lc
					break
				}
			}
			if match == nil || next != nil && control.CheckLinkPair(*match, *next) != nil {
				return none, nil, problem(domain.GrantOriginUncertain, "/entry/link")
			}
			if err = s.confirmReceiver(ctx, fixed); err != nil {
				return none, nil, err
			}
			refs = append(refs, match.Internal.ID)
			np := stage3.NodePlan{NodeID: hop.NodeID, ExpectedRevision: fixed.snapshot.Revision, Template: fixed.base, DependsOn: []domain.ID{}}
			np.Template.TargetRevision++
			if next != nil {
				np.DependsOn = append(np.DependsOn, tail.Hops[i+1].NodeID)
			}
			if err = s.checkBundle(ctx, np.Template, fixed, checkTail, s.now()); err != nil {
				return none, nil, err
			}
			all = append(all, np)
			next = match
		}
		if err = forward(&l, next); err != nil {
			return none, nil, err
		}
		if next == nil {
			d := stage3.RouteDraft{Hops: []stage3.HopDraft{draftHop(h)}, Egress: tail.Egress, EgressIsDefault: route.EgressIsDefault, BusinessUDP: tail.BusinessUDP}
			term, err := s.policy(ctx, d, in)
			if err != nil {
				return none, nil, err
			}
			if err = addPolicy(&b, term); err != nil {
				return none, nil, err
			}
		}
		same := false
		for _, e := range b.Listeners {
			if e.Socket.ListenerID != c.Lease.ListenerID && (e.Entry == nil || e.Entry.IdentityID != auth.IdentityID) {
				continue
			}
			if control.SameV2Grant(e, l) != nil {
				return none, nil, problem(domain.GrantOriginUncertain, "/entry")
			}
			same = true
		}
		if !same {
			b.Listeners = append(b.Listeners, l)
		}
	} else {
		found := false
		fresh := make([]control.ListenerV2, 0, len(b.Listeners))
		for _, l := range b.Listeners {
			if l.Socket.ListenerID != c.Lease.ListenerID && (l.Entry == nil || l.Entry.IdentityID != auth.IdentityID) {
				fresh = append(fresh, l)
				continue
			}
			if l.Socket.ListenerID != c.Lease.ListenerID || l.Entry == nil || l.Entry.IdentityID != auth.IdentityID || l.Entry.EntryID != auth.EntryID || l.Entry.Generation != auth.Generation ||
				!l.Entry.ExpiresAt.Equal(auth.ExpiresAt) || l.Socket.Credential.Ref != c.Entry.CredentialRef || l.Socket.Port != c.Lease.Endpoint.Port {
				return none, nil, problem(domain.GrantOriginUncertain, "/entry")
			}

			key, _ := l.Tail.Key()
			want, _ := tail.Key()
			if key != want {
				return none, nil, problem(domain.GrantOriginUncertain, "/entry/route")
			}
			found = true
		}
		tombstoned := false
		for _, t := range b.Tombstones {
			if t.IdentityID != auth.IdentityID {
				continue
			}
			if t.Generation != auth.Generation || t.Epoch > c.Epoch {
				return none, nil, problem(domain.GrantOriginUncertain, "/entry")
			}
			tombstoned = true
		}
		if !found && !tombstoned {
			return none, nil, problem(domain.GrantOriginUncertain, "/entry")
		}
		if !tombstoned {
			reason := "revoked"
			at := s.now()
			if auth.RevokedAt != nil {
				at = *auth.RevokedAt
			} else if !auth.ExpiresAt.After(at) {
				at = auth.ExpiresAt
				reason = "expired"
			} else {
				return none, nil, problem(domain.GrantOriginUncertain, "/entry/revoked_at")
			}
			b.Tombstones = append(b.Tombstones, control.Tombstone{IdentityID: auth.IdentityID, Generation: auth.Generation, Epoch: c.Epoch, At: at, Reason: reason})
		}
		b.Listeners = fresh
		b.KeepListenerPolicies()
	}
	sortBundle(&b)
	if err = s.checkBundle(ctx, b, in, checkTail, s.now()); err != nil {
		return none, nil, err
	}
	deps := []domain.ID{}
	if len(all) > 0 {
		deps = append(deps, tail.Hops[1].NodeID)
	}
	all = append(all, stage3.NodePlan{NodeID: h.NodeID, ExpectedRevision: in.snapshot.Revision, Template: b, DependsOn: deps})
	return stage3.PublishPlan{Kind: c.Kind, Subject: c.Subject, Deadline: c.Entry.Deadline, Nodes: all, EntryOperationID: c.Entry.OperationID}, refs, nil
}

// DropEntryLinkReferences participates in the I03 transaction that has
// confirmed cleanup (or proved the entry never armed) and excluded rollback.
func DropEntryLinkReferences(ctx context.Context, q *dbgen.Queries, identity domain.ID, now time.Time) error {
	if !identity.Valid() {
		return problem(domain.InvalidArgument, "/entry")
	}
	links, err := q.B06ActiveEntryLinkIDs(ctx, string(identity))
	if err != nil {
		return err
	}
	if err = q.B06DropEntryReferences(ctx, dbgen.B06DropEntryReferencesParams{At: sql.NullString{String: timeText(now), Valid: true}, IdentityID: string(identity)}); err != nil {
		return err
	}
	for _, id := range links {
		row, err := q.B06LinkByID(ctx, id)
		if err != nil {
			return err
		}
		if _, err = retireIfUnreferenced(ctx, q, id, row.ListenerID); err != nil {
			return err
		}
	}
	return nil

}
