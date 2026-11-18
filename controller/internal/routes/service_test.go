package routes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/egress"
	"github.com/nicccce/meshstrata/controller/internal/ports"
	"github.com/nicccce/meshstrata/controller/internal/secrets"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

func testOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func testCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	if !codeIs(err, code) {
		var p *domain.Problem
		_ = errors.As(err, &p)
		t.Fatalf("expected %s, got %v (%+v)", code, err, pFields(p))
	}
}
func pFields(p *domain.Problem) any {
	if p != nil {
		return p.Fields
	}
	return nil
}
func idAt(prefix string, i int) domain.ID { return domain.ID(fmt.Sprintf("%s_%032x", prefix, i)) }
func pinAt(t *testing.T, kind string, i int, obj any) control.Pin {
	t.Helper()
	p, err := stage3.NewPin(idAt(kind, i), 1, obj)
	testOK(t, err)
	return p
}

type gateway struct {
	db           storage.Database
	now          time.Time
	ids          func(string) (domain.ID, error)
	snapshots    map[domain.ID]stage3.NodeSnapshot
	plans        map[domain.ID]stage3.PublishPlan
	states       map[domain.ID]stage3.ReleaseState
	keys         map[string]domain.ID
	leases       map[domain.ID]stage3.IngressLease
	linkRequests []stage3.LinkReservation
	linkLeases   []stage3.LinkLease
	lostSubmit   bool
	lostLink     bool
}

func (g *gateway) NodeSnapshot(ctx context.Context, id domain.ID) (stage3.NodeSnapshot, error) {
	if ctx.Err() != nil {
		return stage3.NodeSnapshot{}, ctx.Err()
	}
	x, ok := g.snapshots[id]
	if !ok {
		return x, problem(domain.NotFound, "")
	}
	return x, nil
}
func (g *gateway) ReserveLink(ctx context.Context, in stage3.LinkReservation) (out stage3.LinkLease, err error) {
	snap := g.snapshots[in.Tail.Hops[0].NodeID]
	reservation, _ := g.ids("reservation")
	listener, _ := g.ids("listener")
	port, _ := g.ids("port")
	udp, _ := g.ids("port")
	prepared, err := PrepareInternal(in, LinkAllocation{Socket: snap.Sockets[0], ExpectedRevision: snap.Revision, ReservationID: reservation, ListenerID: listener, TCPPortID: port, UDPPortID: udp, Now: g.now})
	if err != nil {
		return out, err
	}
	err = g.db.Write(ctx, func(q *dbgen.Queries) error { var e error; out, e = ReserveInternal(ctx, q, prepared); return e })
	if err == nil {
		g.linkRequests = append(g.linkRequests, in)
		g.linkLeases = append(g.linkLeases, out)
		if g.lostLink {
			g.lostLink = false
			return stage3.LinkLease{}, errors.New("lost link response")
		}
	}
	return
}
func (g *gateway) ReserveIngress(ctx context.Context, in stage3.EntryReservation, route stage3.PublishedRoute) (out stage3.IngressLease, err error) {
	if l, ok := g.leases[in.OperationID]; ok {
		return l, nil
	}
	h := route.Tail.Hops[0]
	snap := g.snapshots[h.NodeID]
	spec := snap.Sockets[0].Spec
	listener, _ := g.ids("listener")
	reservation, _ := g.ids("reservation")
	out = stage3.IngressLease{ReservationID: reservation, ListenerID: listener, Endpoint: h.Endpoint, Route: route.Pin}
	err = g.db.Write(ctx, func(q *dbgen.Queries) error {
		for p := uint16(30100); p < 30105; p++ {
			_, err := ports.CheckAvailable(ctx, q, string(h.NodeID), ports.Parse(spec.Address, int64(p), spec.Transport), ports.Owner{})
			if codeIs(err, domain.PortInUse) {
				continue
			}
			if err != nil {
				return err
			}
			out.Endpoint.Port = p
			err = q.B06InsertInternalListener(ctx, dbgen.B06InsertInternalListenerParams{ID: string(listener), NodeID: string(h.NodeID), Protocol: string(h.Endpoint.Protocol), ListenAddress: spec.Address,
				ListenPort: int64(p), OuterTransport: spec.Transport, CredentialRef: string(in.CredentialRef), ConfigDigest: route.Pin.Digest, DnsRevision: int64(h.DNS.Revision), UpdatedAt: timeText(g.now)})
			if err != nil {
				return err
			}
			return q.B06InsertInternalPort(ctx, dbgen.B06InsertInternalPortParams{ID: string(reservation), NodeID: string(h.NodeID), ListenerID: sql.NullString{String: string(listener), Valid: true}, Address: spec.Address, Transport: spec.Transport, Port: int64(p)})
		}
		return problem(domain.PortInUse, "")
	})
	if err == nil {
		g.leases[in.OperationID] = out
	}
	return
}
func (g *gateway) SubmitV2(ctx context.Context, m domain.Mutation, plan stage3.PublishPlan) (domain.TaskAccepted, error) {
	old := g.keys[m.IdempotencyKey]
	if old != "" {
		a, _ := json.Marshal(g.plans[old])
		b, _ := json.Marshal(plan)
		if !bytes.Equal(a, b) {
			return domain.TaskAccepted{}, problem(domain.IdempotencyConflict, "")
		}
		return domain.TaskAccepted{TaskID: old}, nil
	}
	if !plan.Deadline.After(g.now) {
		return domain.TaskAccepted{}, problem(domain.TaskExpired, "")
	}
	task, _ := g.ids("task")
	digest, err := control.ComponentDigest(plan.Subject)
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	err = g.db.Write(ctx, func(q *dbgen.Queries) error {
		for _, n := range plan.Nodes {
			row, e := q.GetNode(ctx, string(n.NodeID))
			if e != nil {
				return e
			}
			if row.Revision != int64(n.ExpectedRevision) || n.Template.TaskID != "" || n.Template.NodeID != n.NodeID || n.Template.TargetRevision != n.ExpectedRevision+1 {
				return problem(domain.RevisionConflict, "")
			}
		}
		return q.InsertTask(ctx, dbgen.InsertTaskParams{ID: string(task), Kind: string(plan.Kind), SubjectID: string(plan.Subject.ID), TargetRevision: int64(m.ExpectedRevision), RequestDigest: digest,
			IdempotencyScope: "b06-isolated", IdempotencyKey: m.IdempotencyKey, Deadline: timeText(plan.Deadline), CreatedAt: timeText(g.now)})
	})
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	g.keys[m.IdempotencyKey] = task
	g.plans[task] = plan
	g.states[task] = stage3.ReleaseState{TaskID: task, Subject: plan.Subject, EntryOperationID: plan.EntryOperationID, State: "queued"}
	if g.lostSubmit {
		g.lostSubmit = false
		return domain.TaskAccepted{}, errors.New("lost submit response")
	}
	return domain.TaskAccepted{TaskID: task}, nil
}
func (g *gateway) ReleaseState(ctx context.Context, id domain.ID) (stage3.ReleaseState, error) {
	if ctx.Err() != nil {
		return stage3.ReleaseState{}, ctx.Err()
	}
	state, ok := g.states[id]
	if !ok {
		return state, problem(domain.NotFound, "")
	}
	return state, nil
}
func (g *gateway) confirm(t *testing.T, id domain.ID) {
	t.Helper()
	ctx := context.Background()
	p := g.plans[id]
	state := g.states[id]
	state.State = "published"
	at := g.now.Add(time.Second)
	for _, n := range p.Nodes {
		b := n.Template
		b.TaskID = id
		_, key, err := control.EncodeBundleV2(b)
		testOK(t, err)
		testOK(t, g.db.Write(ctx, func(q *dbgen.Queries) error {
			current, e := q.GetNode(ctx, string(n.NodeID))
			if e != nil {
				return e
			}
			count, e := q.B04BumpNode(ctx, dbgen.B04BumpNodeParams{NodeID: string(n.NodeID), Expected: int64(n.ExpectedRevision), Epoch: current.RevocationEpoch, Now: timeText(g.now)})
			if e != nil {
				return e
			}
			if count != 1 || int64(b.RevocationEpoch) != current.RevocationEpoch {
				return errors.New("test fixture node CAS mismatch")
			}
			return q.InsertTaskNode(ctx, dbgen.InsertTaskNodeParams{TaskID: string(id), NodeID: string(n.NodeID), TargetRevision: int64(b.TargetRevision), TargetDigest: key.TargetDigest, RevocationEpoch: int64(b.RevocationEpoch)})
		}))
		snap := g.snapshots[n.NodeID]
		snap.Extended = &b
		snap.Revision = b.TargetRevision
		g.snapshots[n.NodeID] = snap
		state.Nodes = append(state.Nodes, domain.NodeResult{NodeID: n.NodeID, TargetRevision: b.TargetRevision, TargetDigest: key.TargetDigest, Applied: true, ConfirmedAt: &at, Verification: domain.NotChecked})
	}
	g.states[id] = state
}

type fixture struct {
	ctx    context.Context
	svc    *Service
	r      stage3.Runtime
	g      *gateway
	e      *egress.Service
	c      *secrets.Credentials
	cipher secretstore.Cipher
	hops   []stage3.HopDraft
}

func setup(t *testing.T, n int) fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	store, err := storage.Open(ctx, dir)
	testOK(t, err)
	testOK(t, store.Close())
	boot, err := sql.Open("sqlite", filepath.Join(dir, "controller.db"))
	testOK(t, err)
	boot.SetMaxOpenConns(1)
	_, err = boot.Exec("PRAGMA foreign_keys=ON")
	testOK(t, err)
	now := time.Date(2026, 11, 17, 2, 0, 0, 0, time.UTC)
	counter := new(atomic.Int64)
	ids := func(k string) (domain.ID, error) { return idAt(k, int(counter.Add(1))), nil }
	g := &gateway{now: now, ids: ids, snapshots: map[domain.ID]stage3.NodeSnapshot{}, plans: map[domain.ID]stage3.PublishPlan{}, states: map[domain.ID]stage3.ReleaseState{}, keys: map[string]domain.ID{}, leases: map[domain.ID]stage3.IngressLease{}}
	cipher, err := secrets.New(secrets.Options{CurrentKeyID: "local_v1", Keys: map[string][]byte{"local_v1": bytes.Repeat([]byte{11}, 32)}})
	testOK(t, err)
	var hops []stage3.HopDraft
	for i := 1; i <= n; i++ {
		node := idAt("node", i)
		address := fmt.Sprintf("192.0.2.%d", i)
		dnsSpec := control.DNSPolicy{Bootstrap: []string{"192.0.2.120"}, Resolvers: []string{"192.0.2.121"}}
		dns := control.DNSInput{Pin: pinAt(t, "dns", i, dnsSpec), Bootstrap: dnsSpec.Bootstrap, Resolvers: dnsSpec.Resolvers}
		spec := control.SocketSpec{Address: "0.0.0.0", Transport: "tcp", ServerNames: []string{}, ShortIDs: []string{}}
		socketPin := pinAt(t, "socket", i, spec)
		_, err = boot.Exec("INSERT INTO nodes(id,name,connection_mode,role,control_address,updated_at) VALUES (?,?,'direct','node','example.invalid:9443',?)", node, "fixture", timeText(now))
		testOK(t, err)
		_, err = boot.Exec("INSERT INTO node_dns_revisions(node_id,revision,outbound,digest) VALUES (?,1,'DIRECT',?)", node, dns.Pin.Digest)
		testOK(t, err)
		base := &control.BundleV2{Format: 2, Kind: domain.Publish, TaskID: idAt("task", 900+i), NodeID: node, TargetRevision: 1, RevocationEpoch: 1, DNS: dns,
			Listeners: []control.ListenerV2{}, Terminal: []control.TerminalV2{}, Tombstones: []control.Tombstone{}, RetiredLinks: []control.InternalRetirement{}}
		g.snapshots[node] = stage3.NodeSnapshot{Revision: 1, Extended: base, DNS: dns, Sockets: []stage3.PinnedSocketSpec{{Pin: socketPin, Spec: spec, PoolFirst: 30000, PoolLast: 31003}},
			Capabilities: []stage3.ProtocolCapability{{Protocol: domain.SS2022, Available: true}},
			Loops:        control.LoopInventory{LocalAddresses: []netip.Addr{netip.MustParseAddr(address)}, ProtectedEndpoints: []netip.AddrPort{}, ProtectedNetworks: []netip.Prefix{}, ProtectedInterfaces: []string{"protected0"}}}
		hops = append(hops, stage3.HopDraft{NodeID: node, NodeConfig: socketPin, DNS: dns.Pin, DialIP: address, Endpoint: control.ProxyEndpoint{Host: address, Protocol: domain.SS2022, Cipher: "2022-blake3-aes-128-gcm"}})
	}
	testOK(t, boot.Close())
	store, err = storage.Open(ctx, dir)
	testOK(t, err)
	t.Cleanup(func() { _ = store.Close() })
	g.db = store
	r := stage3.Runtime{DB: store, Now: func() time.Time { return g.now }, NewID: ids}
	c, err := secrets.NewCredentials(secrets.NewRepository(store), cipher, secrets.CredentialOptions{Clock: r.Now})
	testOK(t, err)
	e, err := egress.NewService(stage3.EgressOptions{Runtime: r, Cipher: cipher})
	testOK(t, err)
	svc, err := NewService(stage3.RoutesOptions{Runtime: r, Publisher: g, Infra: g, Secrets: c, Cipher: cipher, Egress: e})
	testOK(t, err)
	return fixture{ctx, svc, r, g, e, c, cipher, hops}
}
func (f fixture) defaultPolicy(t *testing.T, h stage3.HopDraft, key string) control.Pin {
	t.Helper()
	spec := control.EgressTarget{NodeID: h.NodeID, Type: "node_default"}
	p, err := f.e.SaveTarget(f.ctx, domain.Mutation{IdempotencyKey: key + "-t"}, stage3.EgressTargetInput{Name: "node default", Spec: spec})
	testOK(t, err)
	e, err := f.e.SavePolicy(f.ctx, domain.Mutation{IdempotencyKey: key + "-p"}, stage3.EgressDraft{Name: "default", Spec: control.EgressSpec{NodeID: h.NodeID, DNS: h.DNS,
		Targets: []control.PinnedEgressTarget{{Pin: p, Spec: spec}}, Rules: []control.EgressRule{{Destination: "MATCH", Target: &p}}}})
	testOK(t, err)
	return e
}
func (f fixture) submit(t *testing.T, p control.Pin, key string) domain.ID {
	t.Helper()
	q, err := f.svc.PublishDraft(f.ctx, domain.Mutation{ExpectedRevision: p.Revision, IdempotencyKey: key}, p, f.g.now.Add(time.Hour))
	testOK(t, err)
	return q.TaskID
}
func (f fixture) publish(t *testing.T, d stage3.RouteDraft, key string) (control.Pin, stage3.PublishedRoute) {
	t.Helper()
	p, err := f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: key + "-d"}, d)
	testOK(t, err)
	task := f.submit(t, p, key+"-publish")
	f.g.confirm(t, task)
	_, err = f.svc.ReconcilePublication(f.ctx, task)
	testOK(t, err)
	r, err := f.svc.PublishedHead(f.ctx, p.ID)
	testOK(t, err)
	return p, r
}

func TestSingleNodePublicationRecoveryAndCAS(t *testing.T) {
	f := setup(t, 1)
	p := f.defaultPolicy(t, f.hops[0], "base")
	d := stage3.RouteDraft{Name: "original", Hops: f.hops, Egress: p, EgressIsDefault: true}
	m := domain.Mutation{IdempotencyKey: "draft"}
	pin, err := f.svc.SaveDraft(f.ctx, m, d)
	testOK(t, err)
	replay, err := f.svc.SaveDraft(f.ctx, m, d)
	testOK(t, err)
	if replay != pin {
		t.Fatal("create replay changed ID")
	}
	bad := d
	bad.Name = "different"
	_, err = f.svc.SaveDraft(f.ctx, m, bad)
	testCode(t, err, domain.IdempotencyConflict)
	fake := pin
	fake.Digest = strings.Repeat("0", 64)
	_, err = f.svc.ValidateDraft(f.ctx, fake)
	testCode(t, err, domain.NotFound)
	review, err := f.svc.ValidateDraft(f.ctx, pin)
	testOK(t, err)
	if len(review.Blocking) != 0 || len(f.g.linkRequests) != 0 {
		t.Fatalf("unexpected review %v", review)
	}
	f.g.lostSubmit = true
	m = domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "publish"}
	deadline := f.g.now.Add(time.Hour)
	_, err = f.svc.PublishDraft(f.ctx, m, pin, deadline)
	testCode(t, err, "INTERNAL")
	if len(f.g.plans) != 1 {
		t.Fatal("gateway did not commit on lost response")
	}
	_, err = f.svc.PublishedHead(f.ctx, pin.ID)
	testCode(t, err, domain.NotFound)
	// A new service represents a crashed caller. It reads the sealed, exact plan.
	f.svc, err = NewService(f.svc.opts)
	testOK(t, err)
	task, err := f.svc.PublishDraft(f.ctx, m, pin, deadline)
	testOK(t, err)
	plan := f.g.plans[task.TaskID]
	b := plan.Nodes[0].Template
	if b.TaskID != "" || len(b.Listeners) != 0 || len(b.Terminal) != 1 || b.Terminal[0].Pin != p || plan.Subject != pin {
		t.Fatal("invalid terminal-only plan")
	}
	state, err := f.svc.ReconcilePublication(f.ctx, task.TaskID)
	testOK(t, err)
	if state.State != "queued" {
		t.Fatal("acceptance advanced route")
	}
	f.g.confirm(t, task.TaskID)
	trueState := f.g.states[task.TaskID]
	altered := trueState
	altered.Subject = fake
	f.g.states[task.TaskID] = altered
	_, err = f.svc.ReconcilePublication(f.ctx, task.TaskID)
	testCode(t, err, domain.GrantOriginUncertain)
	f.g.states[task.TaskID] = trueState
	for i := 0; i < 2; i++ {
		_, err = f.svc.ReconcilePublication(f.ctx, task.TaskID)
		testOK(t, err)
	}
	old, err := f.svc.PublishedHead(f.ctx, pin.ID)
	testOK(t, err)
	if old.Verification != domain.NotChecked || old.Pin == pin || old.Tail.Hops[0].Endpoint.Port != 30000 {
		t.Fatal("incorrect published projection")
	}
	old.Tail.Hops[0].Endpoint.Port++
	fresh, err := f.svc.PublishedRoute(f.ctx, old.Pin)
	testOK(t, err)
	if fresh.Tail.Hops[0].Endpoint.Port != 30000 {
		t.Fatal("mutable snapshot")
	}
	var wg sync.WaitGroup
	type answer struct {
		p   control.Pin
		err error
	}
	answers := make(chan answer, 2)
	for i := 1; i <= 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			update := d
			update.ID = pin.ID
			update.Name = fmt.Sprint("edit", i)
			p, e := f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: update.Name, ExpectedRevision: 1}, update)
			answers <- answer{p, e}
		}(i)
	}
	wg.Wait()
	close(answers)
	var edit control.Pin
	good, conflicts := 0, 0
	for a := range answers {
		if a.err == nil {
			edit = a.p
			good++
		} else {
			testCode(t, a.err, domain.RevisionConflict)
			conflicts++
		}
	}
	if good != 1 || conflicts != 1 {
		t.Fatal("concurrent draft CAS failed")
	}
	task2 := f.submit(t, edit, "publish-2")
	failure := f.g.states[task2]
	failure.State = "rollback_pending"
	f.g.states[task2] = failure
	_, err = f.svc.ReconcilePublication(f.ctx, task2)
	testOK(t, err)
	failure.State = "rolled_back"
	f.g.states[task2] = failure
	_, err = f.svc.ReconcilePublication(f.ctx, task2)
	testOK(t, err)
	head, err := f.svc.PublishedHead(f.ctx, pin.ID)
	testOK(t, err)
	if head.Pin != old.Pin {
		t.Fatal("failed edit advanced confirmed head")
	}
	cancelled, cancel := context.WithCancel(f.ctx)
	cancel()
	_, err = f.svc.PublishedRoute(cancelled, old.Pin)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel returned %v", err)
	}
}

func TestThreeHopSuffixSharingAndIndependentEntry(t *testing.T) {
	f := setup(t, 3)
	p := f.defaultPolicy(t, f.hops[2], "base")
	d := stage3.RouteDraft{Name: "three hops", Hops: f.hops, Egress: p, EgressIsDefault: true}
	pin, err := f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: "first"}, d)
	testOK(t, err)
	f.g.lostLink = true
	m := domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "launch"}
	until := f.g.now.Add(time.Hour)
	_, err = f.svc.PublishDraft(f.ctx, m, pin, until)
	testCode(t, err, "INTERNAL")
	task, err := f.svc.PublishDraft(f.ctx, m, pin, until)
	testOK(t, err)
	if len(f.g.linkLeases) != 3 || f.g.linkLeases[0] != f.g.linkLeases[1] {
		t.Fatal("lost reservation replay allocated again")
	}
	b := f.g.plans[task.TaskID].Nodes
	if len(b) != 3 || b[0].NodeID != f.hops[2].NodeID || len(b[0].Template.Listeners) != 1 || len(b[1].Template.Listeners) != 1 ||
		len(b[2].Template.Listeners) != 0 || b[1].DependsOn[0] != b[0].NodeID || b[2].DependsOn[0] != b[1].NodeID {
		t.Fatal("wrong three node DAG")
	}
	testOK(t, control.CheckLinkPair(b[1].Template.Listeners[0], b[0].Template.Listeners[0]))
	original := f.g.linkRequests[0]
	changed := original
	changed.Deadline = changed.Deadline.Add(time.Second)
	_, err = f.g.ReserveLink(f.ctx, changed)
	testCode(t, err, domain.IdempotencyConflict)
	f.g.confirm(t, task.TaskID)
	_, err = f.svc.ReconcilePublication(f.ctx, task.TaskID)
	testOK(t, err)
	route, err := f.svc.PublishedHead(f.ctx, pin.ID)
	testOK(t, err)
	receiver := f.g.states[task.TaskID]
	tampered := receiver
	tampered.Nodes = append([]domain.NodeResult(nil), receiver.Nodes...)
	tampered.Nodes[0].TargetDigest = strings.Repeat("0", 64)
	f.g.states[task.TaskID] = tampered
	review, err := f.svc.ValidateDraft(f.ctx, pin)
	testOK(t, err)
	if len(review.Blocking) != 1 || review.Blocking[0].Code != domain.GrantOriginUncertain {
		t.Fatal("unconfirmed suffix reused")
	}
	f.g.states[task.TaskID] = receiver

	d.Name = "shared"
	_, second := f.publish(t, d, "shared")
	if route.Tail.Hops[1] != second.Tail.Hops[1] || route.Tail.Hops[2] != second.Tail.Hops[2] ||
		f.g.linkLeases[3].Link != f.g.linkLeases[0].Link || f.g.linkLeases[4].Link != f.g.linkLeases[2].Link {
		t.Fatal("exact confirmed suffix not shared")
	}
	spec, err := f.e.Policy(f.ctx, p)
	testOK(t, err)
	p2, err := f.e.SavePolicy(f.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "new egress"}, stage3.EgressDraft{ID: p.ID, Name: "new", Spec: spec})
	testOK(t, err)
	explicit := d
	explicit.Name = "conflict"
	explicit.Egress = p2
	explicit.Hops = append([]stage3.HopDraft(nil), d.Hops...)
	explicit.Hops[2].Endpoint.Port = route.Tail.Hops[2].Endpoint.Port
	x, err := f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: "conflict"}, explicit)
	testOK(t, err)
	review, err = f.svc.ValidateDraft(f.ctx, x)
	testOK(t, err)
	if len(review.Blocking) != 1 || review.Blocking[0].Code != domain.PortInUse {
		t.Fatalf("expected fixed-port conflict: %+v", review)
	}
	d.Name = "different"
	d.Egress = p2
	poolProbe, err := f.svc.SaveDraft(f.ctx, domain.Mutation{IdempotencyKey: "small pool"}, d)
	testOK(t, err)
	node := f.hops[2].NodeID
	snap := f.g.snapshots[node]
	constrained := snap
	constrained.Sockets = append([]stage3.PinnedSocketSpec(nil), snap.Sockets...)
	constrained.Sockets[0].PoolLast = constrained.Sockets[0].PoolFirst
	f.g.snapshots[node] = constrained
	review, err = f.svc.ValidateDraft(f.ctx, poolProbe)
	testOK(t, err)
	if len(review.Blocking) != 1 || review.Blocking[0].Code != domain.PortInUse {
		t.Fatal("exhausted pool did not fail")
	}
	f.g.snapshots[node] = snap

	_, third := f.publish(t, d, "different")
	if third.Tail.Hops[1].Endpoint.Port != 30001 || third.Tail.Hops[2].Endpoint.Port != 30001 {
		t.Fatal("new egress did not trigger distinct bounded allocation")
	}
	terminal := f.g.snapshots[f.hops[2].NodeID].Extended
	if len(terminal.Terminal) != 2 || len(terminal.Listeners) != 2 {
		t.Fatal("other published state lost")
	}
	makeEntry := func(i int) (stage3.EntryChange, domain.ID) {
		a := idAt("identity", i)
		ref := idAt("credential", i+1000)
		sub := idAt("subscription", i)
		plain, _ := json.Marshal(stage3.ProxySecret{Password: base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("fixture-entry-%02d", i)))})
		testOK(t, f.c.Create(f.ctx, secretstore.Binding{Ref: ref, Purpose: secretstore.ProxyEntry}, plain))
		in := stage3.EntryReservation{OperationID: idAt("entry_op", i), SubscriptionID: sub, Route: route.Pin, Authorization: control.BundleAuthorization{EntryID: idAt("entry", i), IdentityID: a, Generation: 1, ExpiresAt: until}, CredentialRef: ref, Deadline: until}
		lease, err := f.svc.ReserveIngress(f.ctx, in)
		testOK(t, err)
		replay, err := f.svc.ReserveIngress(f.ctx, in)
		testOK(t, err)
		if replay != lease {
			t.Fatal("ingress idempotency failed")
		}
		c := stage3.EntryChange{Kind: domain.Publish, Subject: pinAt(t, "subscription", i, "entry config"), Entry: in, Lease: lease,
			Mutation: domain.Mutation{ExpectedRevision: 1, IdempotencyKey: fmt.Sprint("entry", i)}, Epoch: 1}
		task, err := f.svc.ApplyEntry(f.ctx, c)
		testOK(t, err)
		f.g.confirm(t, task.TaskID)
		return c, task.TaskID
	}
	a, _ := makeEntry(1)
	bEntry, last := makeEntry(2)
	entryPlan := f.g.plans[last]
	entry := entryPlan.Nodes[len(entryPlan.Nodes)-1].Template
	if a.Lease.Endpoint.Port == bEntry.Lease.Endpoint.Port || a.Lease.ListenerID == bEntry.Lease.ListenerID || len(entry.Listeners) != 2 || entryPlan.Subject != bEntry.Subject ||
		entryPlan.EntryOperationID != bEntry.Entry.OperationID || entry.Listeners[0].Socket.Credential.Ref == entry.Listeners[1].Socket.Credential.Ref {
		t.Fatal("entry lease cross-contamination")
	}
	revoked := a
	revoked.Kind = domain.Revoke
	revoked.Mutation.IdempotencyKey = "revoke-first"
	revoked.Entry.OperationID = idAt("revoke_op", 1)
	at := f.g.now
	revoked.Entry.Authorization.RevokedAt = &at
	taskR, err := f.svc.ApplyEntry(f.ctx, revoked)
	testOK(t, err)
	removal := f.g.plans[taskR.TaskID].Nodes[0].Template
	if len(removal.Listeners) != 1 || removal.Listeners[0].Entry.IdentityID != bEntry.Entry.Authorization.IdentityID ||
		len(removal.Tombstones) != 1 || removal.Tombstones[0].IdentityID != a.Entry.Authorization.IdentityID {
		t.Fatal("revoke removed another subscription")
	}
	// The route head and active entries still pin the shared suffix.
	err = f.r.DB.Write(f.ctx, func(q *dbgen.Queries) error {
		_, e := RetireInternalReference(f.ctx, q, original.OperationID, f.g.now)
		return e
	})
	testCode(t, err, domain.DependencyInUse)
}

func TestInternalCleanupWaitsForRealTaskEvidence(t *testing.T) {
	f := setup(t, 2)
	first := f.defaultPolicy(t, f.hops[1], "old policy")
	d := stage3.RouteDraft{Name: "previous", Hops: f.hops, Egress: first, EgressIsDefault: true}
	draft, old := f.publish(t, d, "old version")
	op := f.g.linkRequests[0].OperationID
	link := f.g.linkLeases[0]
	d.ID = draft.ID
	d.Name = "replacement"
	d.Egress = f.defaultPolicy(t, f.hops[1], "new policy")
	edit, err := f.svc.SaveDraft(f.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "new version draft"}, d)
	testOK(t, err)
	accept := f.submit(t, edit, "new version publish")
	f.g.confirm(t, accept)
	_, err = f.svc.ReconcilePublication(f.ctx, accept)
	testOK(t, err)
	historic, err := f.svc.PublishedRoute(f.ctx, old.Pin)
	testOK(t, err)
	if historic.Tail.Egress != first {
		t.Fatal("old pin replaced by current version")
	}
	testOK(t, f.r.DB.Write(f.ctx, func(q *dbgen.Queries) error {
		retire, err := RetireInternalReference(f.ctx, q, op, f.g.now)
		if err == nil && !retire {
			t.Fatal("last releasable reference did not mark link retiring")
		}
		return err
	}))
	checkPort := func(held bool) {
		t.Helper()
		err := f.r.DB.Read(f.ctx, func(q *dbgen.Queries) error {
			_, e := ports.CheckAvailable(f.ctx, q, string(f.hops[1].NodeID), ports.Parse("0.0.0.0", int64(old.Tail.Hops[1].Endpoint.Port), "tcp"), ports.Owner{})
			return e
		})
		if held {
			testCode(t, err, domain.PortInUse)
		} else {
			testOK(t, err)
		}
	}
	checkPort(true)
	task, _ := f.g.ids("task")
	confirm := func(id domain.ID) error {
		return f.r.DB.Write(f.ctx, func(q *dbgen.Queries) error { return ConfirmInternalCleanup(f.ctx, q, link.Link, id, f.g.now) })
	}
	testCode(t, confirm(task), domain.GrantOriginUncertain)
	hash := strings.Repeat("a", 64)
	testOK(t, f.r.DB.Write(f.ctx, func(q *dbgen.Queries) error {
		if err := q.InsertTask(f.ctx, dbgen.InsertTaskParams{ID: string(task), Kind: "cleanup", SubjectID: string(link.Link.ID), TargetRevision: 1, RequestDigest: hash,
			IdempotencyScope: "test_cleanup", IdempotencyKey: "retiring link", Deadline: timeText(f.g.now.Add(time.Hour)), CreatedAt: timeText(f.g.now)}); err != nil {
			return err
		}
		return q.InsertTaskNode(f.ctx, dbgen.InsertTaskNodeParams{TaskID: string(task), NodeID: string(f.hops[1].NodeID), TargetRevision: 4, TargetDigest: hash, RevocationEpoch: 1})
	}))
	testCode(t, confirm(task), domain.GrantOriginUncertain)
	checkPort(true)
	testOK(t, f.r.DB.Write(f.ctx, func(q *dbgen.Queries) error {
		_, err := q.RecordTaskResult(f.ctx, dbgen.RecordTaskResultParams{TaskID: string(task), NodeID: string(f.hops[1].NodeID), TargetRevision: 4, TargetDigest: hash,
			Applied: 1, Verification: "not_checked", RollbackState: "none", CleanupState: "confirmed", ResultSequence: 1, ResultDigest: sql.NullString{String: hash, Valid: true},
			ConfirmedAt: sql.NullString{String: timeText(f.g.now), Valid: true}, ResultSequence_2: 1})
		if err != nil {
			return err
		}
		_, err = q.B04SetTerminal(f.ctx, dbgen.B04SetTerminalParams{ID: string(task), State: "succeeded"})
		return err
	}))
	testOK(t, confirm(task))
	testOK(t, confirm(task))
	checkPort(false)
}
