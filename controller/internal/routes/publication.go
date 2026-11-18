package routes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

func (s *Service) PublishDraft(ctx context.Context, m domain.Mutation, pin control.Pin, deadline time.Time) (domain.TaskAccepted, error) {
	out, err := s.publishDraft(ctx, m, pin, deadline.UTC())
	if err = safe(ctx, err); err != nil {
		return domain.TaskAccepted{}, err
	}
	return out, nil
}

func (s *Service) publishDraft(ctx context.Context, m domain.Mutation, pin control.Pin, deadline time.Time) (domain.TaskAccepted, error) {
	const op = "routes.draft.publish"
	none := domain.TaskAccepted{}
	if !pin.Valid() || !validKey(m.IdempotencyKey) || !m.ExpectedRevision.Valid() || !utc(deadline) {
		return none, problem(domain.InvalidArgument, "/publication")
	}
	hash, err := control.ComponentDigest(struct {
		Revision domain.Revision
		Pin      control.Pin
		Deadline time.Time
	}{m.ExpectedRevision, pin, deadline})
	if err != nil {
		return none, err
	}
	var id domain.ID
	var found bool
	err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		found, err = replay(ctx, q, op, m.IdempotencyKey, hash, &id)
		return err
	})
	if err != nil {
		return none, err
	}
	if !found {
		if m.ExpectedRevision != pin.Revision {
			return none, problem(domain.InvalidArgument, "/expected_revision")
		}
		if !deadline.After(s.now()) {
			return none, problem(domain.TaskExpired, "/deadline")
		}
		if _, err = s.draft(ctx, pin); err != nil {
			return none, err
		}
		id, err = s.opts.NewID("publication")
		if err != nil || !id.Valid() {
			return none, problem("INTERNAL", "")
		}
		err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
			if found, err := replay(ctx, q, op, m.IdempotencyKey, hash, &id); found || err != nil {
				return err
			}
			current, err := q.B06RouteRevision(ctx, string(pin.ID))
			if err != nil {
				return err
			}
			if current != int64(pin.Revision) {
				return revisionConflict(current)
			}
			head, e := q.B06RouteHead(ctx, string(pin.ID))
			if e == nil && head >= current {
				return problem(domain.DependencyInUse, "/draft")
			}
			if e != nil && !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			_, e = q.B06ActivePublication(ctx, string(pin.ID))
			if e == nil {
				return problem(domain.DependencyInUse, "/publication")
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			if err = q.B06InsertPublication(ctx, dbgen.B06InsertPublicationParams{ID: string(id), RouteID: string(pin.ID), Revision: int64(pin.Revision), IdemKey: m.IdempotencyKey, Deadline: timeText(deadline)}); err != nil {
				return err
			}
			return remember(ctx, q, op, m.IdempotencyKey, hash, id)
		})
		if err != nil {
			return none, err
		}
	}
	return s.resumePublication(ctx, id, pin)
}

// ResumePublication is a worker entry point for a durable operation that may
// not have received a task ID yet. PublishDraft with the same key also resumes.
func (s *Service) ResumePublication(ctx context.Context, id domain.ID) (domain.TaskAccepted, error) {
	if !id.Valid() {
		return domain.TaskAccepted{}, problem(domain.InvalidArgument, "/publication")
	}
	var pin control.Pin
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		pub, err := q.B06Publication(ctx, string(id))
		if err != nil {
			return err
		}
		row, err := q.B06RouteVersion(ctx, dbgen.B06RouteVersionParams{RouteID: pub.RouteID, Revision: pub.Revision})
		pin = control.Pin{ID: domain.ID(row.RouteID), Revision: domain.Revision(row.Revision), Digest: row.DraftDigest}
		return err
	})
	if err = safe(ctx, err); err != nil {
		return domain.TaskAccepted{}, err
	}
	task, err := s.resumePublication(ctx, id, pin)
	if err = safe(ctx, err); err != nil {
		return domain.TaskAccepted{}, err
	}
	return task, nil
}

func (s *Service) resumePublication(ctx context.Context, id domain.ID, subject control.Pin) (domain.TaskAccepted, error) {
	none := domain.TaskAccepted{}
	pub, rows, err := s.loadPublication(ctx, id)
	if err != nil {
		return none, err
	}
	if string(subject.ID) != pub.RouteID || int64(subject.Revision) != pub.Revision {
		return none, problem(domain.NotFound, "/publication")
	}
	if pub.TaskID.Valid {
		return domain.TaskAccepted{TaskID: domain.ID(pub.TaskID.String)}, nil
	}
	deadline, err := time.Parse(time.RFC3339Nano, pub.Deadline)
	if err != nil {
		return none, err
	}
	if pub.State == "preparing" {
		if !deadline.After(s.now()) {
			if err = s.markFailure(ctx, pub.ID, "preparing", "timed_out", problem(domain.TaskExpired, "/deadline")); err != nil {
				return none, err
			}
			return none, problem(domain.TaskExpired, "/deadline")
		}
		plan, route, err := s.compilePublication(ctx, id, subject, deadline)
		if err != nil {
			// No gateway submission has happened at this point. Only deterministic
			// application errors close the intent; transient failures can resume.
			if definitive(err) {
				if e := s.markFailure(ctx, pub.ID, "preparing", "failed", err); e != nil {
					return none, e
				}
			}
			return none, err
		}
		if err = s.preparePublication(ctx, pub, plan, route); err != nil {
			if definitive(err) {
				if e := s.markFailure(ctx, pub.ID, "preparing", "failed", err); e != nil {
					return none, e
				}
			}
			return none, err
		}
		pub, rows, err = s.loadPublication(ctx, id)
		if err != nil {
			return none, err
		}
		if pub.TaskID.Valid {
			return domain.TaskAccepted{TaskID: domain.ID(pub.TaskID.String)}, nil
		}
	}
	if pub.State != "prepared" {
		if len(pub.Failure) != 0 {
			var e domain.Problem
			if err := json.Unmarshal(pub.Failure, &e); err != nil {
				return none, err
			}
			return none, &e
		}
		if pub.State == "timed_out" {
			return none, problem(domain.TaskExpired, "/deadline")
		}
		return none, problem(domain.DependencyInUse, "/publication")
	}
	plan, err := s.restore(ctx, pub, subject, deadline, rows)
	if err != nil {
		return none, err
	}
	// Gateway first looks up the existing task by this stable operation key.
	// This is necessary even after expiry: a prior Submit may have committed.
	out, err := s.opts.Publisher.SubmitV2(ctx, domain.Mutation{ExpectedRevision: subject.Revision, IdempotencyKey: string(id)}, plan)
	if err != nil {
		e := dependency(ctx, err, "/publication")
		if definitive(e) {
			newState := "failed"
			if codeIs(e, domain.TaskExpired) {
				newState = "timed_out"
			}
			if markErr := s.markFailure(ctx, pub.ID, "prepared", newState, e); markErr != nil {
				return none, markErr
			}
		}
		return none, e
	}
	if !out.TaskID.Valid() {
		return none, problem(domain.GrantOriginUncertain, "/task")
	}
	err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		row, err := q.B06Publication(ctx, string(id))
		if err != nil {
			return err
		}
		if row.TaskID.Valid {
			if row.TaskID.String != string(out.TaskID) {
				return problem(domain.GrantOriginUncertain, "/task")
			}
			return nil
		}
		n, err := q.B06BindPublicationTask(ctx, dbgen.B06BindPublicationTaskParams{ID: string(id), TaskID: sql.NullString{String: string(out.TaskID), Valid: true}})
		if err == nil && n != 1 {
			return problem(domain.GrantOriginUncertain, "/publication")
		}
		return err
	})
	return out, err
}
func definitive(err error) bool {
	for _, code := range []domain.ErrorCode{domain.InvalidArgument, domain.NotFound, domain.RevisionConflict, domain.PortInUse, domain.DependencyInUse, domain.NotImplemented, domain.TaskExpired} {
		if codeIs(err, code) {
			return true
		}
	}
	return false
}
func (s *Service) markFailure(ctx context.Context, id, old, next string, cause error) error {
	var p *domain.Problem
	if !errors.As(cause, &p) {
		return cause
	}
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		_, err := q.B06FailPublication(ctx, dbgen.B06FailPublicationParams{ID: id, OldState: old, NewState: next, Failure: data})
		return err
	})
}
func (s *Service) loadPublication(ctx context.Context, id domain.ID) (pub dbgen.B06Publication, rows []dbgen.B06PublicationNode, err error) {
	err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		pub, e = q.B06Publication(ctx, string(id))
		if e != nil {
			return e
		}
		if pub.State != "preparing" {
			rows, e = q.B06PublicationNodes(ctx, string(id))
		}
		return e
	})
	return
}

func (s *Service) compilePublication(ctx context.Context, id domain.ID, subject control.Pin, deadline time.Time) (stage3.PublishPlan, stage3.PublishedRoute, error) {
	none, empty := stage3.PublishPlan{}, stage3.PublishedRoute{}
	d, err := s.draft(ctx, subject)
	if err != nil {
		return none, empty, err
	}
	r, issues := s.review(ctx, d)
	if len(issues) > 0 {
		p := problem(issues[0].Code, "")
		for _, i := range issues {
			p.Fields = append(p.Fields, domain.FieldError{Path: i.Path, Code: i.Code})
		}
		return none, empty, p
	}
	// Check the candidate topology before reserving and again after actual allocation.
	candidate, _, err := s.allocate(ctx, r, "", subject, deadline)
	if err != nil {
		// A retry can have its own occupied ports; the durable reservation path
		// below recognizes them. Other static checks still run on the final tail.
		if !codeIs(err, domain.PortInUse) {
			return none, empty, err
		}
	} else {
		b := r.inputs[len(r.inputs)-1].base
		if err = addPolicy(&b, r.terminal); err != nil {
			return none, empty, err
		}
		if err = s.checkBundle(ctx, b, r.inputs[len(r.inputs)-1], candidate, s.now()); err != nil {
			return none, empty, err
		}
	}
	actual, leases, err := s.allocate(ctx, r, id, subject, deadline)
	if err != nil {
		return none, empty, err
	}
	view := stage3.PublishedRoute{Pin: subject, Name: d.Name, Tail: actual, EgressIsDefault: d.EgressIsDefault, Verification: domain.NotChecked}
	view.Pin.Digest, err = stage3.RouteDigest(view)
	if err != nil {
		return none, empty, err
	}
	plan := stage3.PublishPlan{Kind: domain.Publish, Subject: subject, Deadline: deadline, Nodes: make([]stage3.NodePlan, 0, len(r.inputs))}
	var downstream *control.ListenerV2
	for i := len(r.inputs) - 1; i >= 0; i-- {
		in := r.inputs[i]
		b := in.base
		b.TargetRevision = in.snapshot.Revision + 1
		if i == len(r.inputs)-1 {
			if err = addPolicy(&b, r.terminal); err != nil {
				return none, empty, err
			}
		}
		if i > 0 {
			l, err := s.internalListener(ctx, suffix(actual, i), leases[i], in.sock.Spec, downstream)
			if err != nil {
				return none, empty, err
			}
			if err = addInternal(&b, l); err != nil {
				return none, empty, err
			}
			downstream = &l
		}
		sortBundle(&b)
		if err = s.checkBundle(ctx, b, in, actual, s.now()); err != nil {
			return none, empty, err
		}
		np := stage3.NodePlan{NodeID: b.NodeID, ExpectedRevision: in.snapshot.Revision, Template: b, DependsOn: []domain.ID{}}
		if i+1 < len(r.inputs) {
			np.DependsOn = append(np.DependsOn, actual.Hops[i+1].NodeID)
		}
		plan.Nodes = append(plan.Nodes, np)
	}
	return plan, view, nil
}
func (s *Service) credential(ctx context.Context, ep control.ProxyEndpoint, ref, node domain.ID, purpose secretstore.Purpose) (control.PrivateCredential, error) {
	plaintext, err := s.opts.Secrets.Open(ctx, secretstore.Binding{Ref: ref, Purpose: purpose})
	defer clear(plaintext)
	if err != nil || len(plaintext) > 1024 {
		return control.PrivateCredential{}, problem(domain.NotFound, "/credential")
	}
	r := json.NewDecoder(bytes.NewReader(plaintext))
	r.DisallowUnknownFields()
	var p stage3.ProxySecret
	if r.Decode(&p) != nil || r.Decode(new(any)) != io.EOF {
		return control.PrivateCredential{}, problem(domain.NotFound, "/credential")
	}
	canonical, _ := json.Marshal(p)
	defer clear(canonical)
	if !bytes.Equal(canonical, plaintext) {
		return control.PrivateCredential{}, problem(domain.NotFound, "/credential")
	}
	c := control.PrivateCredential{Ref: ref, Audience: node, Password: p.Password, UUID: p.UUID}
	if !control.ValidProxyCredential(ep, c, node) {
		return control.PrivateCredential{}, problem(domain.NotFound, "/credential")
	}
	if ctx.Err() != nil {
		return control.PrivateCredential{}, ctx.Err()
	}
	return c, nil
}
func socket(spec control.SocketSpec, id domain.ID, port uint16, c control.PrivateCredential) control.SocketV2 {
	return control.SocketV2{ConfigPort: spec.RequestedPort, TLSLeafSHA256: spec.TLSLeafSHA256, ListenerID: id, Port: port, Address: spec.Address, Transport: spec.Transport, Credential: c,
		Certificate: spec.Certificate, PrivateKey: spec.PrivateKey, RealityKey: spec.RealityKey, RealityDestination: spec.RealityDestination, ServerNames: spec.ServerNames, ShortIDs: spec.ShortIDs}
}
func forward(l *control.ListenerV2, downstream *control.ListenerV2) error {
	if len(l.Tail.Hops) == 1 {
		p := l.Tail.Egress
		l.Forward.Terminal = &p
		return nil
	}
	if downstream == nil || downstream.Internal == nil {
		return problem(domain.NotFound, "/link")
	}
	c := downstream.Socket.Credential
	c.Audience = l.Tail.Hops[0].NodeID
	l.Forward.Next = &control.NextHop{Link: *downstream.Internal, Credential: c}
	if control.CheckLinkPair(*l, *downstream) != nil {
		return problem(domain.GrantOriginUncertain, "/link")
	}
	return nil
}
func (s *Service) internalListener(ctx context.Context, tail control.RouteTail, lease stage3.LinkLease, spec control.SocketSpec, downstream *control.ListenerV2) (control.ListenerV2, error) {
	h := tail.Hops[0]
	c, err := s.credential(ctx, h.Endpoint, lease.CredentialRef, h.NodeID, secretstore.InternalProxy)
	if err != nil {
		return control.ListenerV2{}, err
	}
	l := control.ListenerV2{Socket: socket(spec, lease.ListenerID, h.Endpoint.Port, c), Tail: tail, Internal: &lease.Link}
	if err = forward(&l, downstream); err != nil {
		return control.ListenerV2{}, err
	}
	return l, nil
}
func addInternal(b *control.BundleV2, l control.ListenerV2) error {
	for _, existing := range b.Listeners {
		if existing.Socket.ListenerID != l.Socket.ListenerID && (existing.Internal == nil || existing.Internal.ID != l.Internal.ID) {
			continue
		}
		if control.SameInternalLink(existing, l) != nil {
			return problem(domain.GrantOriginUncertain, "/link")
		}
		return nil
	}
	b.Listeners = append(b.Listeners, l)
	return nil
}
func sortBundle(b *control.BundleV2) {
	slices.SortFunc(b.Listeners, func(a, b control.ListenerV2) int { return compareID(a.Socket.ListenerID, b.Socket.ListenerID) })
	slices.SortFunc(b.Terminal, func(a, b control.TerminalV2) int {
		if c := compareID(a.Pin.ID, b.Pin.ID); c != 0 {
			return c
		}
		if a.Pin.Revision < b.Pin.Revision {
			return -1
		}
		if a.Pin.Revision > b.Pin.Revision {
			return 1
		}
		return 0
	})
	slices.SortFunc(b.Tombstones, func(a, b control.Tombstone) int { return compareID(a.IdentityID, b.IdentityID) })
}
func compareID(a, b domain.ID) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

func (s *Service) sealPlan(ctx context.Context, ref domain.ID, value any) (secretstore.Sealed, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return secretstore.Sealed{}, err
	}
	defer clear(data)
	if len(data) > control.MaxBundleBytes {
		return secretstore.Sealed{}, problem(domain.InvalidArgument, "/bundle")
	}
	return s.opts.Cipher.Seal(ctx, secretstore.Binding{Ref: ref, Purpose: planPurpose}, data)
}
func (s *Service) openPlan(ctx context.Context, ref string, kid string, data []byte, out any) error {
	p, err := s.opts.Cipher.Open(ctx, secretstore.Binding{Ref: domain.ID(ref), Purpose: planPurpose}, secretstore.Sealed{KeyID: kid, Ciphertext: data})
	defer clear(p)
	if err != nil {
		return problem(domain.NotFound, "/publication/plan")
	}
	return json.Unmarshal(p, out)
}
func (s *Service) preparePublication(ctx context.Context, pub dbgen.B06Publication, plan stage3.PublishPlan, route stage3.PublishedRoute) error {
	rows := make([]dbgen.B06InsertPublicationNodeParams, len(plan.Nodes))
	for i, n := range plan.Nodes {
		ref := derived("plan", domain.ID(pub.ID), fmt.Sprint(i))
		sealed, err := s.sealPlan(ctx, ref, n)
		if err != nil {
			return err
		}
		rows[i] = dbgen.B06InsertPublicationNodeParams{PublicationID: pub.ID, Position: int64(i), NodeID: string(n.NodeID), TargetRevision: int64(n.Template.TargetRevision), SecretRef: string(ref), KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext}
	}
	if !plan.Deadline.After(s.now()) {
		return problem(domain.TaskExpired, "/deadline")
	}
	return s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		row, err := q.B06Publication(ctx, pub.ID)
		if err != nil {
			return err
		}
		if row.State != "preparing" {
			return nil
		}
		rev, err := q.B06RouteRevision(ctx, row.RouteID)
		if err != nil {
			return err
		}
		if rev != row.Revision {
			return revisionConflict(rev)
		}
		for i, n := range plan.Nodes {
			node, err := q.GetNode(ctx, string(n.NodeID))
			if err != nil {
				return err
			}
			if node.RemovedAt.Valid || node.Revision != int64(n.ExpectedRevision) || node.RevocationEpoch != int64(n.Template.RevocationEpoch) || node.TargetDnsRevision != int64(n.Template.DNS.Pin.Revision) {
				return revisionConflict(node.Revision)
			}
			if err = q.B06InsertPublicationNode(ctx, rows[i]); err != nil {
				return err
			}
		}
		if err = q.B06InsertPublishedVersion(ctx, dbgen.B06InsertPublishedVersionParams{RouteID: row.RouteID, Revision: row.Revision, PublicationID: row.ID, RouteDigest: route.Pin.Digest}); err != nil {
			return err
		}
		for i, h := range route.Tail.Hops {
			if err = q.B06StorePublishedHop(ctx, dbgen.B06StorePublishedHopParams{RouteID: row.RouteID, Revision: row.Revision, Position: int64(i), Port: int64(h.Endpoint.Port)}); err != nil {
				return err
			}
		}
		_, err = q.B06SetPublicationState(ctx, dbgen.B06SetPublicationStateParams{ID: row.ID, OldState: "preparing", NewState: "prepared"})
		return err
	})
}
func (s *Service) restore(ctx context.Context, p dbgen.B06Publication, subject control.Pin, deadline time.Time, rows []dbgen.B06PublicationNode) (stage3.PublishPlan, error) {
	plan := stage3.PublishPlan{Kind: domain.Publish, Subject: subject, Deadline: deadline, Nodes: make([]stage3.NodePlan, len(rows))}
	if len(rows) == 0 || len(rows) > control.MaxRouteHops {
		return stage3.PublishPlan{}, problem(domain.NotFound, "/publication/plan")
	}
	for i, r := range rows {
		node := &plan.Nodes[i]
		if err := s.openPlan(ctx, r.SecretRef, r.KeyID, r.Ciphertext, node); err != nil {
			return stage3.PublishPlan{}, err
		}
		if r.PublicationID != p.ID || r.Position != int64(i) || domain.ID(r.NodeID) != node.NodeID || int64(node.Template.TargetRevision) != r.TargetRevision ||
			node.Template.NodeID != node.NodeID || node.Template.TaskID != "" || node.ExpectedRevision+1 != node.Template.TargetRevision || structural(node.Template) != nil {
			return stage3.PublishPlan{}, problem(domain.GrantOriginUncertain, "/publication/plan")
		}
	}
	return plan, nil
}
