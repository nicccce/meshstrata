package routes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/egress"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

func TestEgressPinsFailClosedAndCiphertext(t *testing.T) {
	f := setup(t, 1)
	h := f.hops[0]
	target := control.EgressTarget{NodeID: h.NodeID, Type: "socks5", Host: "proxy.example.invalid", DialIP: "198.51.100.100", Port: 1080}
	const pwd = "private-test-value-123"
	m := domain.Mutation{IdempotencyKey: "external-egress"}
	targetIn := stage3.EgressTargetInput{Name: "proxy", Spec: target, Auth: &stage3.ProxyAuth{Username: "fixture-user", Password: pwd}}
	tp, err := f.e.SaveTarget(f.ctx, m, targetIn)
	testOK(t, err)
	again, err := f.e.SaveTarget(f.ctx, m, targetIn)
	testOK(t, err)
	if again != tp {
		t.Fatal("auth replay changed pin")
	}
	changed := targetIn
	changed.Auth = &stage3.ProxyAuth{Username: targetIn.Auth.Username, Password: "wrong-test-value"}
	_, err = f.e.SaveTarget(f.ctx, m, changed)
	testCode(t, err, domain.IdempotencyConflict)
	// Get the server-owned ref through the private adapter; it is never a password.
	a, err := egress.OpenAuth(f.ctx, f.r, f.cipher, tp, h.NodeID)
	testOK(t, err)
	if a.Password != pwd || !a.Ref.Valid() {
		t.Fatal("bad private credential")
	}
	target.CredentialRef = a.Ref
	testOK(t, f.r.DB.Read(f.ctx, func(q *dbgen.Queries) error {
		row, e := q.B06EgressCredential(f.ctx, dbgen.B06EgressCredentialParams{ID: string(a.Ref), TargetID: string(tp.ID)})
		if e != nil {
			return e
		}
		if bytes.Contains(row.Ciphertext, []byte(pwd)) {
			t.Fatal("credential stored in plaintext")
		}
		return nil
	}))
	var sealed secretstore.Sealed
	testOK(t, f.r.DB.Read(f.ctx, func(q *dbgen.Queries) error {
		row, e := q.B06EgressCredential(f.ctx, dbgen.B06EgressCredentialParams{ID: string(a.Ref), TargetID: string(tp.ID)})
		sealed = secretstore.Sealed{KeyID: row.KeyID, Ciphertext: row.Ciphertext}
		return e
	}))
	if plain, err := f.cipher.Open(f.ctx, secretstore.Binding{Ref: a.Ref, Purpose: secretstore.InternalProxy}, sealed); err == nil || plain != nil {
		t.Fatal("cross-purpose secret retrieval")
	}
	retained, err := f.e.SaveTarget(f.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "keep"}, stage3.EgressTargetInput{ID: tp.ID, Name: "renamed", Spec: target})
	testOK(t, err)
	if retained.Revision != 2 || retained.Digest != tp.Digest {
		t.Fatal("owned credential did not retain fixed content")
	}
	hijack := target
	hijack.Host = "unapproved.example.invalid"
	_, err = f.e.SaveTarget(f.ctx, domain.Mutation{ExpectedRevision: 2, IdempotencyKey: "hijack"}, stage3.EgressTargetInput{ID: tp.ID, Name: "change host", Spec: hijack})
	testCode(t, err, domain.InvalidArgument)
	_, err = f.e.SaveTarget(f.ctx, domain.Mutation{IdempotencyKey: "wrong owner"}, stage3.EgressTargetInput{Name: "wrong owner", Spec: target})
	testCode(t, err, domain.NotFound)
	policyIn := stage3.EgressDraft{Name: "custom", Spec: control.EgressSpec{NodeID: h.NodeID, DNS: h.DNS,
		Targets: []control.PinnedEgressTarget{{Pin: tp, Spec: target}},
		Rules:   []control.EgressRule{{Destination: "203.0.113.4"}, {Destination: "203.0.113.0/24", Target: &tp}}}}
	p, err := f.e.SavePolicy(f.ctx, domain.Mutation{IdempotencyKey: "custom"}, policyIn)
	testOK(t, err)
	forged := p
	forged.Digest = strings.Repeat("f", 64)
	_, err = f.e.Policy(f.ctx, forged)
	testCode(t, err, domain.NotFound)
	spec, err := f.e.Policy(f.ctx, p)
	testOK(t, err)
	data, _ := json.Marshal(spec)
	if bytes.Contains(data, []byte(pwd)) || bytes.Contains(data, []byte("fixture-user")) {
		t.Fatal("policy projected plaintext")
	}
	if len(spec.Rules) != 3 || spec.Rules[0].Destination != "203.0.113.4/32" || egress.Select(spec, netip.MustParseAddr("203.0.113.4")) != nil ||
		egress.Select(spec, netip.MustParseAddr("192.0.2.50")) != nil {
		t.Fatal("first reject/custom default lost")
	}
	if pick := egress.Select(spec, netip.MustParseAddr("203.0.113.5")); pick == nil || *pick != tp {
		t.Fatal("expected pinned proxy")
	}
	spec.Rules[1].Target.Digest = ""
	original, err := f.e.Policy(f.ctx, p)
	testOK(t, err)
	if *original.Rules[1].Target != tp {
		t.Fatal("policy result mutated persisted pin")
	}
	route, err := f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: "external-route"}, stage3.RouteDraft{Name: "with external proxy", Hops: f.hops, Egress: p})
	testOK(t, err)
	expectReview := func(code domain.ErrorCode, path string) {
		t.Helper()
		r, e := f.svc.ValidateDraft(f.ctx, route)
		testOK(t, e)
		if len(r.Blocking) != 1 || r.Blocking[0].Code != code || r.Blocking[0].Path != path {
			t.Fatalf("review: %+v", r)
		}
	}
	expectReview(domain.NotFound, "/egress/evidence")
	evidence := func(until time.Time) {
		t.Helper()
		testOK(t, f.r.DB.Write(f.ctx, func(q *dbgen.Queries) error {
			return q.B06PutEgressEvidence(f.ctx, dbgen.B06PutEgressEvidenceParams{NodeID: string(h.NodeID), NodeRevision: 1, TargetDigest: tp.Digest, DnsDigest: h.DNS.Digest, ExpiresAt: timeText(until)})
		}))
	}
	evidence(f.g.now.Add(-time.Minute))
	expectReview(domain.TaskExpired, "/egress/evidence")
	evidence(f.g.now.Add(time.Hour))
	snap := f.g.snapshots[h.NodeID]
	empty := snap
	empty.Loops = control.LoopInventory{}
	f.g.snapshots[h.NodeID] = empty
	expectReview(domain.NotFound, "/egress/inventory")
	loop := snap
	loop.Loops.ProtectedEndpoints = []netip.AddrPort{netip.MustParseAddrPort("198.51.100.100:1080")}
	f.g.snapshots[h.NodeID] = loop
	expectReview(domain.DependencyInUse, "/egress/loop")
	f.g.snapshots[h.NodeID] = snap
	task := f.submit(t, route, "external publish")
	plan := f.g.plans[task]
	if len(plan.Nodes[0].Template.Terminal) != 1 || plan.Nodes[0].Template.Terminal[0].Auth[0].Password != pwd {
		t.Fatal("private artifact auth missing")
	}
	testOK(t, f.r.DB.Read(context.Background(), func(q *dbgen.Queries) error {
		row, e := q.B06TaskPublication(f.ctx, sql.NullString{String: string(task), Valid: true})
		if e != nil {
			return e
		}
		nodes, e := q.B06PublicationNodes(f.ctx, row.ID)
		if e != nil {
			return e
		}
		if len(nodes) != 1 || bytes.Contains(nodes[0].Ciphertext, []byte(pwd)) {
			t.Fatal("sealed plan leaked auth")
		}
		return nil
	}))
}

func TestDraftProtocolPinsAndUDP(t *testing.T) {
	f := setup(t, 1)
	h := f.hops[0]
	p := f.defaultPolicy(t, h, "udp")
	d := stage3.RouteDraft{Name: "udp draft", Hops: f.hops, Egress: p, EgressIsDefault: true, BusinessUDP: true}
	pin, err := f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: "udp"}, d)
	testOK(t, err)
	r, err := f.svc.ValidateDraft(f.ctx, pin)
	testOK(t, err)
	if len(r.Blocking) != 1 || r.Blocking[0].Path != "/hops/0/endpoint/protocol" || r.Blocking[0].Code != domain.DependencyInUse {
		t.Fatalf("unverified business UDP was not rejected: %+v", r)
	}
	bad := d
	bad.Hops = append([]stage3.HopDraft(nil), d.Hops...)
	bad.Hops = append(bad.Hops, bad.Hops[0])
	_, err = f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: "repeat-node"}, bad)
	testCode(t, err, domain.InvalidArgument)
	d.BusinessUDP = false
	d.Hops = append([]stage3.HopDraft(nil), d.Hops...)
	d.Hops[0].NodeConfig.Digest = strings.Repeat("a", 64)
	x, err := f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: "bad-socket"}, d)
	testOK(t, err)
	r, err = f.svc.ValidateDraft(f.ctx, x)
	testOK(t, err)
	if len(r.Blocking) != 1 || r.Blocking[0].Path != "/hops/0/node_config" {
		t.Fatalf("forged socket pin accepted: %+v", r)
	}
}
