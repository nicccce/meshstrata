package releases

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/auth"
	"github.com/nicccce/meshstrata/controller/internal/clientconfig"
	"github.com/nicccce/meshstrata/controller/internal/egress"
	"github.com/nicccce/meshstrata/controller/internal/routes"
	"github.com/nicccce/meshstrata/controller/internal/rules"
	"github.com/nicccce/meshstrata/controller/internal/secrets"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/controller/internal/subscriptions"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type joint struct {
	*fixture
	routes    *routes.Service
	rules     *rules.Service
	egress    *egress.Service
	subs      *subscriptions.Service
	reports   map[domain.ID]InventoryReport
	profile   control.Pin
	capable   bool
	loseApply bool
	buildHook func()
}

func jointFor(t *testing.T, nodes int) *joint {
	f := fixtureFor(t)
	j := &joint{fixture: f, reports: map[domain.ID]InventoryReport{}, capable: true}
	j.compose()
	for i := 0; i < nodes; i++ {
		node := f.node()
		dns := control.DNSInput{Bootstrap: []string{"1.1.1.1"}, Resolvers: []string{"1.1.1.1"}}
		dns.Pin = j.pin("dns", control.DNSPolicy{Bootstrap: dns.Bootstrap, Resolvers: dns.Resolvers})
		spec := control.SocketSpec{Address: "0.0.0.0", RequestedPort: 30000, Transport: "tcp", ServerNames: []string{}, ShortIDs: []string{}}
		r := InventoryReport{NodeID: node, NodeRevision: 1, DNS: dns, ObservedAt: f.now, ExpiresAt: f.now.Add(10 * time.Minute), Sockets: []stage3.PinnedSocketSpec{{Protocol: domain.SS2022, Pin: j.pin("socket", spec), Spec: spec, PoolFirst: 30000, PoolLast: 30010}}, Capabilities: []stage3.ProtocolCapability{{Available: true, Protocol: domain.SS2022, BusinessUDP: domain.NotChecked}}, Loops: control.LoopInventory{LocalAddresses: []netip.Addr{netip.MustParseAddr("192.0.2.1")}, ProtectedEndpoints: []netip.AddrPort{}, ProtectedNetworks: []netip.Prefix{netip.MustParsePrefix("198.18.0.0/15")}, ProtectedInterfaces: []string{"Mihomo"}}, Interfaces: []InterfaceObservation{{Name: "eth0", Address: netip.MustParseAddr("192.0.2.1"), Up: true}}}
		must(t, f.s.RecordInventory(f.ctx, r))
		j.reports[node] = r
	}
	return j
}
func (j *joint) pin(kind string, value any) control.Pin {
	j.t.Helper()
	p, e := stage3.NewPin(idFor(j.t, kind), 1, value)
	must(j.t, e)
	return p
}
func (j *joint) compose() {
	j.t.Helper()
	f := j.fixture
	guard, e := auth.New(auth.NewRepository(f.store), auth.Options{Clock: func() time.Time { return f.now }, AllowedOrigins: []string{"https://admin.test"}})
	must(j.t, e)
	f.s.guard = guard
	f.s.options.V2Session = func(node domain.ID) V2Session {
		if !j.capable {
			return V2Session{}
		}
		return V2Session{SessionID: "authenticated-test-session", RecoveryFormat: 2}
	}
	f.s.options.V2Deliver = f.s.options.Deliver
	runtime := stage3.Runtime{DB: f.store, Now: func() time.Time { return f.now }, NewID: domain.NewID}
	secretReader, e := secrets.NewCredentials(secrets.NewRepository(f.store), f.cipher, secrets.CredentialOptions{Clock: runtime.Now})
	must(j.t, e)
	j.egress, e = egress.NewService(stage3.EgressOptions{Runtime: runtime, Cipher: f.cipher})
	must(j.t, e)
	j.routes, e = routes.NewService(stage3.RoutesOptions{Runtime: runtime, Infra: f.s, Publisher: f.s, Egress: j.egress, Cipher: f.cipher, Secrets: secretReader})
	must(j.t, e)
	j.rules, e = rules.NewService(stage3.RulesOptions{Runtime: runtime})
	must(j.t, e)
	builder, e := clientconfig.NewBuilder(stage3.ClientOptions{})
	must(j.t, e)
	var entryRoutes stage3.EntryRoutes = j.routes
	if j.loseApply {
		entryRoutes = &lostApplyReply{EntryRoutes: j.routes}
	}
	var client stage3.ClientBuilder = builder
	if j.buildHook != nil {
		client = &racingBuilder{ClientBuilder: builder, hook: j.buildHook}
	}
	j.subs, e = subscriptions.NewService(stage3.SubscriptionsOptions{Runtime: runtime, Cipher: f.cipher, Guard: guard, Routes: entryRoutes, Rules: j.rules, Client: client, Publisher: f.s, PublicOrigin: "https://admin.test"})
	must(j.t, e)
}

type lostApplyReply struct{ stage3.EntryRoutes }

type racingBuilder struct {
	stage3.ClientBuilder
	hook func()
}

func (b *racingBuilder) BuildFull(in stage3.GenerationInput) (stage3.FullConfig, error) {
	out, err := b.ClientBuilder.BuildFull(in)
	if err == nil {
		b.hook()
	}
	return out, err
}

func (l *lostApplyReply) ApplyEntry(ctx context.Context, c stage3.EntryChange) (domain.TaskAccepted, error) {
	_, err := l.EntryRoutes.ApplyEntry(ctx, c)
	if err != nil {
		return domain.TaskAccepted{}, err
	}
	return domain.TaskAccepted{}, errors.New("injected lost reply")
}
func (j *joint) refresh() {
	j.t.Helper()
	for node, r := range j.reports {
		must(j.t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
			n, e := q.GetNode(j.ctx, string(node))
			r.NodeRevision = domain.Revision(n.Revision)
			return e
		}))
		r.ObservedAt = j.now
		r.ExpiresAt = j.now.Add(10 * time.Minute)
		must(j.t, j.s.RecordInventory(j.ctx, r))
		j.reports[node] = r
	}
}
func (j *joint) draft(name string, ids []domain.ID) stage3.RouteDraft {
	j.t.Helper()
	node := ids[len(ids)-1]
	r := j.reports[node]
	target, e := j.egress.SaveTarget(j.ctx, domain.Mutation{IdempotencyKey: name + "-target"}, stage3.EgressTargetInput{Name: "Node Default", Spec: control.EgressTarget{NodeID: node, Type: "node_default"}})
	must(j.t, e)
	policy, e := j.egress.SavePolicy(j.ctx, domain.Mutation{IdempotencyKey: name + "-policy"}, stage3.EgressDraft{Name: "Default", Spec: control.EgressSpec{NodeID: node, DNS: r.DNS.Pin, Targets: []control.PinnedEgressTarget{{Pin: target, Spec: control.EgressTarget{NodeID: node, Type: "node_default"}}}, Rules: []control.EgressRule{{Destination: "MATCH", Target: &target}}}})
	must(j.t, e)
	d := stage3.RouteDraft{Name: name, Egress: policy, EgressIsDefault: true}
	for _, node := range ids {
		r := j.reports[node]
		d.Hops = append(d.Hops, stage3.HopDraft{NodeID: node, NodeConfig: r.Sockets[0].Pin, DNS: r.DNS.Pin, DialIP: "192.0.2.1", Endpoint: control.ProxyEndpoint{Host: "192.0.2.1", Port: r.Sockets[0].Spec.RequestedPort, Protocol: domain.SS2022, Cipher: "2022-blake3-aes-128-gcm"}})
	}
	return d
}
func (j *joint) publish(d stage3.RouteDraft) stage3.PublishedRoute {
	j.t.Helper()
	p, e := j.routes.SaveDraft(j.ctx, domain.Mutation{IdempotencyKey: d.Name + "-save"}, d)
	must(j.t, e)
	review, e := j.routes.ValidateDraft(j.ctx, p)
	must(j.t, e)
	if len(review.Blocking) != 0 {
		j.t.Fatalf("review: %+v", review)
	}
	task, e := j.routes.PublishDraft(j.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: d.Name + "-publish"}, p, j.now.Add(10*time.Minute))
	must(j.t, e)
	j.finish(task.TaskID, false)
	state, e := j.routes.ReconcilePublication(j.ctx, task.TaskID)
	must(j.t, e)
	if state.State != "published" {
		j.t.Fatal(state)
	}
	var pin control.Pin
	must(j.t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
		v, e := q.B06PublishedVersion(j.ctx, dbgen.B06PublishedVersionParams{RouteID: string(p.ID), Revision: 1})
		pin = control.Pin{ID: p.ID, Revision: 1, Digest: v.RouteDigest}
		return e
	}))
	view, e := j.routes.PublishedRoute(j.ctx, pin)
	must(j.t, e)
	return view
}

// Only the authenticated Agent/network boundary is simulated. Fetch, encrypted
// artifact, grants, B04 result persistence, receipts and business services are real.
func (j *joint) finish(task domain.ID, cleanup bool) {
	j.t.Helper()
	for step := 0; step < 12; step++ {
		sent := j.dispatch()
		for _, c := range sent {
			key, e := j.s.Resolve(j.ctx, c.NodeID, c.TaskID)
			must(j.t, e)
			reader, e := j.s.Open(j.ctx, c.NodeID, key)
			must(j.t, e)
			var b control.BundleV2
			must(j.t, json.NewDecoder(reader).Decode(&b))
			reader.Close()
			must(j.t, b.BindCommand(c))
			if b.Format != 2 {
				j.t.Fatal("format")
			}
			result := j.result(c, 1, true)
			result.EffectiveDigest = strings.Repeat("b", 64)
			if cleanup || c.Kind == domain.Cleanup {
				result.Cleanup = "confirmed"
			}
			fresh, e := j.s.RecordResult(j.ctx, c.NodeID, result)
			must(j.t, e)
			if !fresh {
				j.t.Fatal("missing receipt commit")
			}
			fresh, e = j.s.RecordResult(j.ctx, c.NodeID, result)
			must(j.t, e)
			if fresh {
				j.t.Fatal("duplicate persisted")
			}
		}
		state, e := j.s.ReleaseState(j.ctx, task)
		must(j.t, e)
		if state.State == "published" {
			j.refresh()
			return
		}
		if len(sent) == 0 {
			j.t.Fatalf("stalled: %+v", state)
		}
	}
	j.t.Fatal("did not finish")
}
func (j *joint) subscription(name string, rs ...stage3.PublishedRoute) stage3.SubscriptionView {
	j.t.Helper()
	if j.profile.ID == "" {
		p, issues, e := j.rules.SaveProfile(j.ctx, domain.Mutation{IdempotencyKey: "profile"}, stage3.ProfileInput{Name: "Default", Source: "MATCH,All-Proxies"})
		must(j.t, e)
		if len(issues) > 0 {
			j.t.Fatal(issues)
		}
		j.profile = p
	}
	d := stage3.SubscriptionDraft{Name: name, ExpiresAt: j.now.Add(time.Hour), Profile: j.profile, Preferences: stage3.ClientPreferences{DNS: stage3.ClientDNS{Bootstrap: []string{"1.1.1.1"}, ProxyServers: []string{"https://1.1.1.1/dns-query"}, Business: []string{"https://1.0.0.1/dns-query"}}}}
	for _, r := range rs {
		d.Routes = append(d.Routes, r.Pin)
	}
	v, e := j.subs.Create(j.ctx, domain.Mutation{IdempotencyKey: name}, d)
	must(j.t, e)
	j.drive(v.Entity.ID)
	v, e = j.subs.Get(j.ctx, v.Entity.ID)
	must(j.t, e)
	return v
}
func (j *joint) drive(sub domain.ID) {
	j.t.Helper()
	for tries := 0; tries < 8; tries++ {
		var pending []dbgen.B08Intent
		must(j.t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
			var e error
			pending, e = q.B08AllLiveIntents(j.ctx, string(sub))
			return e
		}))
		for _, i := range pending {
			if i.State == "confirmed" {
				continue
			}
			j.refresh()
			must(j.t, j.subs.Reconcile(j.ctx, domain.ID(i.ID)))
			must(j.t, j.store.Read(j.ctx, func(q *dbgen.Queries) error { i2, e := q.B08Intent(j.ctx, i.ID); i = i2; return e }))
			if i.TaskID.Valid {
				j.finish(domain.ID(i.TaskID.String), false)
				must(j.t, j.subs.Reconcile(j.ctx, domain.ID(i.ID)))
			}
		}
		v, e := j.subs.Get(j.ctx, sub)
		must(j.t, e)
		if v.Sync == "confirmed" {
			return
		}
	}
	j.t.Fatal("subscription did not confirm")
}
func (j *joint) cleanup(sub domain.ID) {
	j.t.Helper()
	var ids []string
	db := j.raw()
	rows, e := db.Query("SELECT id FROM b08_intents WHERE subscription_id=? AND cleanup_state='pending'", sub)
	must(j.t, e)
	for rows.Next() {
		var id string
		must(j.t, rows.Scan(&id))
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		j.refresh()
		must(j.t, j.subs.Reconcile(j.ctx, domain.ID(id)))
		var i dbgen.B08Intent
		must(j.t, j.store.Read(j.ctx, func(q *dbgen.Queries) error { var e error; i, e = q.B08Intent(j.ctx, id); return e }))
		if i.CleanupTaskID.Valid {
			j.finish(domain.ID(i.CleanupTaskID.String), true)
			must(j.t, j.subs.Reconcile(j.ctx, domain.ID(id)))
		}
	}
}
func TestI03RealServicesTwoSubscriptionsAndCleanup(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	route := j.publish(j.draft("香港 Line / 一", []domain.ID{node}))
	one := j.subscription("Laptop", route)
	two := j.subscription("Phone", route)
	if one.Sync != "confirmed" || two.Sync != "confirmed" || one.Entries[0].Endpoint.Port == two.Entries[0].Endpoint.Port || one.Entries[0].IdentityID == two.Entries[0].IdentityID {
		t.Fatal("identity or port isolation lost")
	}
	for _, v := range []stage3.SubscriptionView{one, two} {
		link, e := j.subs.DownloadLink(j.ctx, v.Entity.ID)
		must(t, e)
		token := link.DownloadURL[strings.LastIndex(link.DownloadURL, "/")+1:]
		full, e := j.subs.DownloadFull(j.ctx, token)
		must(t, e)
		provider, e := j.subs.DownloadProvider(j.ctx, token)
		must(t, e)
		if !strings.Contains(string(full), "Entry-"+string(v.Entries[0].ID)) || strings.Contains(string(provider), "proxy-groups") {
			t.Fatal("generation projection")
		}
	}
	_, e := j.subs.Delete(j.ctx, domain.Mutation{ExpectedRevision: one.Entity.Revision, IdempotencyKey: "delete"}, one.Entity.ID)
	must(t, e)
	j.cleanup(one.Entity.ID)
	var released int
	must(t, j.raw().QueryRow("SELECT count(*) FROM i03_ingress_leases WHERE released_at IS NOT NULL").Scan(&released))
	if released != 1 {
		t.Fatal("cleanup did not settle port")
	}
	v, e := j.subs.Get(j.ctx, two.Entity.ID)
	must(t, e)
	if v.Sync != "confirmed" || v.Entries[0].IdentityID != two.Entries[0].IdentityID {
		t.Fatal("other subscription changed")
	}
	j.reopen()
	j.compose()
	j.refresh()
	v, e = j.subs.Get(j.ctx, two.Entity.ID)
	must(t, e)
	if v.Sync != "confirmed" {
		t.Fatal("reopen lost confirmation")
	}
}

func TestI03ThreeNodesSharedTailAndCapabilityFence(t *testing.T) {
	j := jointFor(t, 3)
	ids := []domain.ID{}
	for node, r := range j.reports {
		r.Sockets[0].Spec.RequestedPort = 0
		r.Sockets[0].Pin = j.pin("socket", r.Sockets[0].Spec)
		must(t, j.s.RecordInventory(j.ctx, r))
		j.reports[node] = r
		ids = append(ids, node)
	}
	d := j.draft("first", ids)
	one := j.publish(d)
	d.Name = "second"
	two := j.publish(d)
	if one.Tail.Hops[1].Endpoint.Port != two.Tail.Hops[1].Endpoint.Port {
		t.Fatal("tail not shared")
	}
	var links int
	must(t, j.raw().QueryRow("SELECT count(*) FROM b06_internal_links").Scan(&links))
	if links != 2 {
		t.Fatal("created duplicate tail")
	}
	j.capable = false
	d.Name = "blocked"
	p, e := j.routes.SaveDraft(j.ctx, domain.Mutation{IdempotencyKey: "blocked"}, d)
	must(t, e)
	_, e = j.routes.PublishDraft(j.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "blocked-publish"}, p, j.now.Add(time.Minute))
	var prob *domain.Problem
	if !errors.As(e, &prob) || prob.Code != domain.NotImplemented {
		t.Fatalf("capability: %v", e)
	}
	var queued int
	must(t, j.raw().QueryRow("SELECT count(*) FROM tasks WHERE subject_id=?", p.ID).Scan(&queued))
	if queued != 0 {
		t.Fatal("unsupported runtime accepted task")
	}
}

func TestI03TerminalCleanupRecoveryAndLateReceipt(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	route := j.publish(j.draft("cleanup", []domain.ID{node}))
	sub := j.subscription("laptop", route)
	_, e := j.subs.Delete(j.ctx, domain.Mutation{ExpectedRevision: sub.Entity.Revision, IdempotencyKey: "delete"}, sub.Entity.ID)
	must(t, e)
	var i dbgen.B08Intent
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
		rows, e := q.B08RevisionEntries(j.ctx, dbgen.B08RevisionEntriesParams{SubscriptionID: string(sub.Entity.ID), Revision: 1})
		if e == nil {
			i = rows[0]
		}
		return e
	}))
	must(t, j.subs.Reconcile(j.ctx, domain.ID(i.ID)))
	sent := j.dispatch()
	if len(sent) != 1 {
		t.Fatal("missing cleanup")
	}
	c := sent[0]
	key, e := j.s.Resolve(j.ctx, node, c.TaskID)
	must(t, e)
	reader, e := j.s.Open(j.ctx, node, key)
	must(t, e)
	reader.Close()
	failed := j.result(c, 1, false)
	failed.Verification = domain.Failed
	failed.Applied = false
	failed.Rollback = "confirmed"
	failed.Cleanup = "pending"
	failed.EffectiveDigest = ""
	failed.ConfirmedAt = &j.now
	_, e = j.s.RecordResult(j.ctx, node, failed)
	must(t, e)
	// A real local rollback confirmation is injected at the Agent boundary.
	must(t, j.subs.Reconcile(j.ctx, domain.ID(i.ID)))
	j.reopen()
	j.compose()
	j.refresh()
	must(t, j.subs.Reconcile(j.ctx, domain.ID(i.ID)))
	var recovered dbgen.B08Intent
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error { var e error; recovered, e = q.B08Intent(j.ctx, i.ID); return e }))
	if recovered.CleanupGeneration != 1 || recovered.CleanupTaskID == i.CleanupTaskID || !recovered.CleanupTaskID.Valid {
		t.Fatal("no durable recovery generation")
	}
	j.finish(domain.ID(recovered.CleanupTaskID.String), true)
	must(t, j.subs.Reconcile(j.ctx, domain.ID(i.ID)))
	fresh, e := j.s.RecordResult(j.ctx, node, failed)
	must(t, e)
	if fresh {
		t.Fatal("duplicate late result")
	}
	var n int
	must(t, j.raw().QueryRow("SELECT count(*) FROM i03_cleanup_attempts WHERE operation_id=?", i.CleanupID).Scan(&n))
	if n != 2 {
		t.Fatal("attempt history lost")
	}
}

func TestI03RenewRemoveReaddKeepsHistory(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	d := j.draft("one", []domain.ID{node})
	one := j.publish(d)
	d.Name = "two"
	two := j.publish(d)
	sub := j.subscription("sub", one, two)
	old := sub.Entries[0]
	renewed, e := j.subs.Renew(j.ctx, domain.Mutation{ExpectedRevision: sub.Entity.Revision, IdempotencyKey: "renew"}, sub.Entity.ID, j.now.Add(2*time.Hour))
	must(t, e)
	j.cleanup(sub.Entity.ID)
	j.drive(sub.Entity.ID)
	if renewed.Entries[0].IdentityID == old.IdentityID {
		t.Fatal("renewal reused identity")
	}
	current, e := j.subs.Get(j.ctx, sub.Entity.ID)
	must(t, e)
	prefs := stage3.ClientPreferences{Mode: "tun", AllRoutesGroup: true, GroupByExit: true, DNS: stage3.ClientDNS{Bootstrap: []string{"1.1.1.1"}, ProxyServers: []string{"https://1.1.1.1/dns-query"}, Business: []string{"https://1.0.0.1/dns-query"}}}
	change := stage3.SubscriptionDraft{ID: sub.Entity.ID, Name: "sub", ExpiresAt: current.ExpiresAt, Profile: j.profile, Preferences: prefs, Routes: []control.Pin{two.Pin}}
	removed, e := j.subs.UpdateConfig(j.ctx, domain.Mutation{ExpectedRevision: current.Entity.Revision, IdempotencyKey: "remove"}, change)
	must(t, e)
	j.cleanup(sub.Entity.ID)
	change.Routes = []control.Pin{one.Pin, two.Pin}
	readded, e := j.subs.UpdateConfig(j.ctx, domain.Mutation{ExpectedRevision: removed.Entity.Revision, IdempotencyKey: "readd"}, change)
	must(t, e)
	j.drive(sub.Entity.ID)
	if readded.Entries[0].ID == old.ID || readded.Entries[0].IdentityID == old.IdentityID || readded.Entries[0].Generation <= renewed.Entries[0].Generation {
		t.Fatal("removed identity revived")
	}
	var n int
	must(t, j.raw().QueryRow("SELECT count(*) FROM subscription_entries WHERE subscription_id=? AND route_id=?", sub.Entity.ID, one.Pin.ID).Scan(&n))
	if n != 2 {
		t.Fatal("history lost")
	}
	must(t, j.raw().QueryRow("SELECT count(*) FROM subscription_entries WHERE subscription_id=? AND route_id=? AND revoked_at IS NULL", sub.Entity.ID, one.Pin.ID).Scan(&n))
	if n != 1 {
		t.Fatal("multiple current relationships")
	}
}

func TestI03CompensateLostReservationAndFenceLateArm(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	route := j.publish(j.draft("route", []domain.ID{node}))
	p, _, e := j.rules.SaveProfile(j.ctx, domain.Mutation{IdempotencyKey: "profile"}, stage3.ProfileInput{Name: "Default", Source: "MATCH,All-Proxies"})
	must(t, e)
	d := stage3.SubscriptionDraft{Name: "lost", ExpiresAt: j.now.Add(time.Hour), Profile: p, Routes: []control.Pin{route.Pin}, Preferences: stage3.ClientPreferences{DNS: stage3.ClientDNS{Bootstrap: []string{"1.1.1.1"}, ProxyServers: []string{"https://1.1.1.1/dns-query"}, Business: []string{"https://1.0.0.1/dns-query"}}}}
	sub, e := j.subs.Create(j.ctx, domain.Mutation{IdempotencyKey: "lost"}, d)
	must(t, e)
	var i dbgen.B08Intent
	must(t, j.store.Write(j.ctx, func(q *dbgen.Queries) error {
		rows, e := q.B08RevisionEntries(j.ctx, dbgen.B08RevisionEntriesParams{SubscriptionID: string(sub.Entity.ID), Revision: 1})
		if e != nil {
			return e
		}
		i = rows[0]
		_, e = q.B08BeginReserve(j.ctx, i.ID)
		return e
	}))
	in := stage3.EntryReservation{OperationID: domain.ID(i.ID), SubscriptionID: sub.Entity.ID, Route: route.Pin, CredentialRef: domain.ID(i.CredentialRef), Authorization: control.BundleAuthorization{EntryID: domain.ID(i.EntryKey), IdentityID: domain.ID(i.IdentityKey), Generation: domain.Revision(i.Generation), ExpiresAt: sub.ExpiresAt}}
	in.Deadline, e = time.Parse(time.RFC3339Nano, i.Deadline)
	must(t, e)
	lease, e := j.routes.ReserveIngress(j.ctx, in)
	must(t, e) // response intentionally not delivered to B08
	_, e = j.subs.Delete(j.ctx, domain.Mutation{ExpectedRevision: sub.Entity.Revision, IdempotencyKey: "delete"}, sub.Entity.ID)
	must(t, e)
	j.reopen()
	j.compose()
	must(t, j.subs.Reconcile(j.ctx, domain.ID(i.ID)))
	var blocked int
	must(t, j.raw().QueryRow("SELECT count(*) FROM i03_ingress_leases WHERE operation_id=? AND blocked_at IS NOT NULL AND released_at IS NOT NULL", i.ID).Scan(&blocked))
	if blocked != 1 {
		t.Fatal("unarmed reservation not compensated")
	}
	c := stage3.EntryChange{Kind: domain.Publish, Entry: in, Lease: lease, Subject: j.pin("subject", "late"), Epoch: 1, Mutation: domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "late"}}
	_, e = j.routes.ApplyEntry(j.ctx, c)
	if e == nil {
		t.Fatal("late grant armed")
	}
}

func TestI03CapabilityLossAndImmutableEncryptedArtifact(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	d := j.draft("caps", []domain.ID{node})
	p, e := j.routes.SaveDraft(j.ctx, domain.Mutation{IdempotencyKey: "save"}, d)
	must(t, e)
	task, e := j.routes.PublishDraft(j.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "publish"}, p, j.now.Add(time.Minute))
	must(t, e)
	// An authenticated runtime flag does not substitute for format-2 recovery.
	j.capable = false
	if sent := j.dispatch(); len(sent) != 0 {
		t.Fatal("disconnected capability armed")
	}
	j.capable = true
	j.now = j.now.Add(redelivery + time.Second)
	sent := j.dispatch()
	if len(sent) != 1 {
		t.Fatal("capable peer not armed")
	}
	c := sent[0]
	key, e := j.s.Resolve(j.ctx, node, c.TaskID)
	must(t, e)
	j.capable = false
	if _, e = j.s.Open(j.ctx, node, key); e == nil {
		t.Fatal("capability missing during fetch")
	}
	j.capable = true
	var before []byte
	must(t, j.raw().QueryRow("SELECT ciphertext FROM b04_artifacts WHERE task_id=?", task.TaskID).Scan(&before))
	if _, e = j.raw().Exec("UPDATE b04_artifacts SET ciphertext=x'00' WHERE task_id=?", task.TaskID); e == nil {
		t.Fatal("mutable V2 artifact")
	}
	real := j.s.cipher
	wrong, e := secrets.New(secrets.Options{CurrentKeyID: "test", Keys: map[string][]byte{"test": make([]byte, 32)}})
	must(t, e)
	j.s.cipher = wrong
	if _, e = j.s.Open(j.ctx, node, key); e == nil {
		t.Fatal("wrong key opened artifact")
	}
	j.s.cipher = real
	var after []byte
	must(t, j.raw().QueryRow("SELECT ciphertext FROM b04_artifacts WHERE task_id=?", task.TaskID).Scan(&after))
	if !bytes.Equal(before, after) {
		t.Fatal("failed open changed ciphertext")
	}
	reader, e := j.s.Open(j.ctx, node, key)
	must(t, e)
	reader.Close()
	r := j.result(c, 1, true)
	r.EffectiveDigest = strings.Repeat("c", 64)
	_, e = j.s.RecordResult(j.ctx, node, r)
	must(t, e)
	j.refresh()
	snap, e := j.s.NodeSnapshot(j.ctx, node)
	must(t, e)
	if snap.Artifact.TargetDigest == snap.EffectiveDigest || snap.EffectiveDigest != r.EffectiveDigest {
		t.Fatal("artifact and effective digest conflated")
	}
}

func TestI03TrustedEgressEvidenceBoundary(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	r := j.reports[node]
	target := control.EgressTarget{NodeID: node, Type: "socks5", Host: "203.0.113.10", DialIP: "203.0.113.10", Port: 1080}
	pin, e := j.egress.SaveTarget(j.ctx, domain.Mutation{IdempotencyKey: "target"}, stage3.EgressTargetInput{Name: "explicit proxy", Spec: target})
	must(t, e)
	observation := EgressObservation{NodeID: node, NodeRevision: r.NodeRevision, DNS: r.DNS.Pin, Target: pin, Interface: "eth0", SourceIP: netip.MustParseAddr("192.0.2.1"), Probe: netip.MustParseAddrPort("203.0.113.20:443"), ObservedAt: j.now, ExpiresAt: j.now.Add(time.Minute)}
	if e = j.s.RecordEgressObservation(j.ctx, observation); e == nil {
		t.Fatal("arbitrary probe accepted")
	}
	j.s.options.EgressProbes = []netip.AddrPort{observation.Probe}
	must(t, j.s.RecordEgressObservation(j.ctx, observation))
	observation.NodeRevision++
	if e = j.s.RecordEgressObservation(j.ctx, observation); e == nil {
		t.Fatal("stale evidence accepted")
	}
	var n int
	must(t, j.raw().QueryRow("SELECT count(*) FROM i03_egress_observations").Scan(&n))
	if n != 1 {
		t.Fatal("evidence provenance missing")
	}
}

func TestI03V2CancelRetryRollbackAreFormatAware(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	d := j.draft("actions", []domain.ID{node})
	pin, e := j.routes.SaveDraft(j.ctx, domain.Mutation{IdempotencyKey: "save"}, d)
	must(t, e)
	task, e := j.routes.PublishDraft(j.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "publish"}, pin, j.now.Add(time.Minute))
	must(t, e)
	_, e = j.s.action(j.ctx, task.TaskID, "cancel", domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "cancel"})
	must(t, e)
	j.refresh()
	retry, e := j.s.action(j.ctx, task.TaskID, "retry", domain.Mutation{ExpectedRevision: 2, IdempotencyKey: "retry"})
	must(t, e)
	j.finish(retry.TaskID, false)
	again, e := j.s.action(j.ctx, task.TaskID, "retry", domain.Mutation{ExpectedRevision: 2, IdempotencyKey: "retry"})
	must(t, e)
	if again != retry {
		t.Fatal("action replay changed task")
	}
	var rev int64
	must(t, j.raw().QueryRow("SELECT revision FROM tasks WHERE id=?", retry.TaskID).Scan(&rev))
	rollback, e := j.s.action(j.ctx, retry.TaskID, "rollback", domain.Mutation{ExpectedRevision: domain.Revision(rev), IdempotencyKey: "rollback"})
	must(t, e)
	j.finish(rollback.TaskID, false)
	state, e := j.s.ReleaseState(j.ctx, rollback.TaskID)
	must(t, e)
	if state.State != "published" {
		t.Fatal(state)
	}
}

func TestI03InternalRetirementWaitsForRealCleanup(t *testing.T) {
	j := jointFor(t, 2)
	var nodes []domain.ID
	for id := range j.reports {
		nodes = append(nodes, id)
	}
	d := j.draft("partial", nodes)
	pin, err := j.routes.SaveDraft(j.ctx, domain.Mutation{IdempotencyKey: "save"}, d)
	must(t, err)
	task, err := j.routes.PublishDraft(j.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "publish"}, pin, j.now.Add(time.Minute))
	must(t, err)
	for step := 0; step < 2; step++ {
		sent := j.dispatch()
		if len(sent) != 1 {
			t.Fatal("expected dependency order", sent)
		}
		c := sent[0]
		key, e := j.s.Resolve(j.ctx, c.NodeID, c.TaskID)
		must(t, e)
		r, e := j.s.Open(j.ctx, c.NodeID, key)
		must(t, e)
		r.Close()
		result := j.result(c, 1, step == 0)
		result.EffectiveDigest = strings.Repeat("b", 64)
		if step == 1 {
			result.Verification = domain.Failed
			result.EffectiveDigest = ""
			result.Applied = false
			result.Rollback = "confirmed"
			at := j.now
			result.ConfirmedAt = &at
		}
		j.record(result)
	}
	state, err := j.routes.ReconcilePublication(j.ctx, task.TaskID)
	must(t, err)
	if state.State != "failed" {
		t.Fatal(state)
	}
	var op string
	must(t, j.raw().QueryRow("SELECT operation_id FROM b06_link_references").Scan(&op))
	must(t, j.store.Write(j.ctx, func(q *dbgen.Queries) error {
		retired, e := routes.RetireInternalReference(j.ctx, q, domain.ID(op), j.now)
		if e == nil && !retired {
			t.Fatal("last reference retained")
		}
		return e
	}))
	var link control.Pin
	var listener string
	must(t, j.raw().QueryRow("SELECT id,revision,suffix_digest,listener_id FROM b06_internal_links").Scan(&link.ID, &link.Revision, &link.Digest, &listener))
	j.refresh()
	cleanup, err := j.s.RetireInternal(j.ctx, domain.Mutation{ExpectedRevision: link.Revision, IdempotencyKey: "retire"}, link)
	must(t, err)
	var active int
	must(t, j.raw().QueryRow("SELECT count(*) FROM port_reservations WHERE listener_id=? AND state='pending_cleanup'", listener).Scan(&active))
	if active != 1 {
		t.Fatal("port released before receipt")
	}
	j.finish(cleanup.TaskID, true)
	j.dispatch() // durable settlement sweep after the receipt transaction
	var status string
	must(t, j.raw().QueryRow("SELECT state FROM b06_internal_links WHERE id=?", link.ID).Scan(&status))
	if status != "released" {
		t.Fatal(status)
	}
	replay, err := j.s.RetireInternal(j.ctx, domain.Mutation{ExpectedRevision: link.Revision, IdempotencyKey: "retire"}, link)
	must(t, err)
	if replay != cleanup {
		t.Fatal("retirement replay changed task")
	}
}

func TestI03LostTaskReplyNeverArmedCompensation(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	route := j.publish(j.draft("lost-task", []domain.ID{node}))
	p, _, err := j.rules.SaveProfile(j.ctx, domain.Mutation{IdempotencyKey: "profile"}, stage3.ProfileInput{Name: "Default", Source: "MATCH,All-Proxies"})
	must(t, err)
	j.loseApply = true
	j.compose()
	sub, err := j.subs.Create(j.ctx, domain.Mutation{IdempotencyKey: "sub"}, stage3.SubscriptionDraft{Name: "sub", ExpiresAt: j.now.Add(time.Hour), Profile: p, Routes: []control.Pin{route.Pin}, Preferences: stage3.ClientPreferences{DNS: stage3.ClientDNS{Bootstrap: []string{"1.1.1.1"}, ProxyServers: []string{"https://1.1.1.1/dns-query"}, Business: []string{"https://1.0.0.1/dns-query"}}}})
	must(t, err)
	var op string
	must(t, j.raw().QueryRow("SELECT id FROM b08_intents WHERE subscription_id=?", sub.Entity.ID).Scan(&op))
	if err = j.subs.Reconcile(j.ctx, domain.ID(op)); err == nil {
		t.Fatal("reply not lost")
	}
	var queued int
	must(t, j.raw().QueryRow("SELECT count(*) FROM b04_target_grants g JOIN b04_targets t USING(task_id,node_id) WHERE t.phase='queued'").Scan(&queued))
	if queued != 1 {
		t.Fatal("task did not commit")
	}
	_, err = j.subs.Delete(j.ctx, domain.Mutation{ExpectedRevision: sub.Entity.Revision, IdempotencyKey: "delete"}, sub.Entity.ID)
	must(t, err)
	j.reopen()
	j.loseApply = false
	j.compose()
	must(t, j.subs.Reconcile(j.ctx, domain.ID(op)))
	var status string
	must(t, j.raw().QueryRow("SELECT cleanup_state FROM b08_intents WHERE id=?", op).Scan(&status))
	if status != "confirmed" {
		t.Fatal(status)
	}
	if len(j.dispatch()) != 0 {
		t.Fatal("cancelled grant armed")
	}
}

func TestI03RealBuilderRevocationRaceDiscardsOutput(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	sub := j.subscription("race", j.publish(j.draft("race", []domain.ID{node})))
	link, err := j.subs.DownloadLink(j.ctx, sub.Entity.ID)
	must(t, err)
	token := link.DownloadURL[strings.LastIndex(link.DownloadURL, "/")+1:]
	j.buildHook = func() {
		_, err := j.subs.Delete(j.ctx, domain.Mutation{ExpectedRevision: sub.Entity.Revision, IdempotencyKey: "revoke-during-build"}, sub.Entity.ID)
		must(t, err)
	}
	j.compose()
	out, err := j.subs.DownloadFull(j.ctx, token)
	var p *domain.Problem
	if len(out) != 0 || !errors.As(err, &p) || p.Code != domain.NotFound {
		t.Fatal("revoked output escaped", err)
	}
	out, err = j.subs.DownloadFull(j.ctx, token)
	if len(out) != 0 || err == nil {
		t.Fatal("revoked token reused")
	}
	var pending int
	must(t, j.raw().QueryRow("SELECT count(*) FROM i03_ingress_leases WHERE released_at IS NULL").Scan(&pending))
	if pending != 1 {
		t.Fatal("port released before cleanup")
	}
}

func TestI03MultiHopIndependentSubscriptionsShareReceiverFence(t *testing.T) {
	j := jointFor(t, 3)
	ids := []domain.ID{}
	for node := range j.reports {
		ids = append(ids, node)
	}
	slices.Sort(ids)
	route := j.publish(j.draft("shared three-hop route", ids))
	one := j.subscription("one", route)
	two := j.subscription("two", route)
	if one.Entries[0].IdentityID == two.Entries[0].IdentityID || one.Entries[0].Endpoint.Port == two.Entries[0].Endpoint.Port {
		t.Fatal("entry isolation lost")
	}
	for _, v := range []stage3.SubscriptionView{one, two} {
		if v.Sync != "confirmed" {
			t.Fatal("multi-hop subscription not confirmed")
		}
	}
}

func TestI03UnsubmittedEntryPlanRebasesAfterReconnect(t *testing.T) {
	hasCode := func(e error, code domain.ErrorCode) bool {
		var p *domain.Problem
		return errors.As(e, &p) && p.Code == code
	}
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	route := j.publish(j.draft("reconnect-plan", []domain.ID{node}))
	one := j.subscription("reconnect-one", route)
	d := stage3.SubscriptionDraft{Name: "reconnect-two", ExpiresAt: j.now.Add(time.Hour), Profile: j.profile, Routes: []control.Pin{route.Pin}, Preferences: stage3.ClientPreferences{DNS: stage3.ClientDNS{Bootstrap: []string{"1.1.1.1"}, ProxyServers: []string{"https://1.1.1.1/dns-query"}, Business: []string{"https://1.0.0.1/dns-query"}}}}
	two, e := j.subs.Create(j.ctx, domain.Mutation{IdempotencyKey: "reconnect-two"}, d)
	must(t, e)
	var pending dbgen.B08Intent
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
		rows, e := q.B08AllLiveIntents(j.ctx, string(two.Entity.ID))
		if e == nil {
			pending = rows[0]
		}
		return e
	}))
	j.capable = false
	if e = j.subs.Reconcile(j.ctx, domain.ID(pending.ID)); !hasCode(e, domain.NotImplemented) {
		t.Fatalf("offline submission: %v", e)
	}
	j.capable = true
	// Another durable operation wins the node CAS after reconnect.
	j.subscription("concurrent-subscription", route)
	for n := 0; n < 3; n++ {
		j.refresh()
		e = j.subs.Reconcile(j.ctx, domain.ID(pending.ID))
		if e == nil {
			break
		}
		if !hasCode(e, domain.RevisionConflict) {
			t.Fatal(e)
		}
		if n == 0 {
			j.reopen()
			j.compose()
		}
	}
	must(t, e)
	j.drive(two.Entity.ID)
	v, e := j.subs.Get(j.ctx, one.Entity.ID)
	must(t, e)
	if v.Sync != "confirmed" {
		t.Fatal("other subscription lost confirmation")
	}
	var candidates, tasks int
	must(t, j.raw().QueryRow("SELECT count(*) FROM i03c_entry_plan_history h JOIN b06_entry_plans p USING(kind,idem_key) WHERE p.operation_id=?", pending.ID).Scan(&candidates))
	must(t, j.raw().QueryRow("SELECT count(*) FROM i03_v2_tasks WHERE entry_operation_id=?", pending.ID).Scan(&tasks))
	if candidates != 2 || tasks != 1 {
		t.Fatalf("candidate history or single acceptance lost: candidates=%d tasks=%d", candidates, tasks)
	}
}

func TestI03ConfirmedOfflineRestrictionAllowsLaterCleanup(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	route := j.publish(j.draft("offline-expiry", []domain.ID{node}))
	one := j.subscription("expires", route)
	j.now = j.now.Add(time.Minute)
	j.refresh()
	two := j.subscription("retained", route)
	var grant dbgen.B04Grant
	var tn dbgen.TaskNode
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
		var e error
		grant, e = q.B04GetGrant(j.ctx, string(one.Entries[0].IdentityID))
		if e != nil {
			return e
		}
		tn, e = q.GetTaskNode(j.ctx, dbgen.GetTaskNodeParams{TaskID: grant.OriginTaskID.String, NodeID: string(node)})
		return e
	}))
	expires, eTime := time.Parse(time.RFC3339Nano, grant.ExpiresAt)
	must(t, eTime)
	j.fixture.now = expires.Add(time.Second)
	j.refresh()
	c := control.Command{TaskID: domain.ID(grant.OriginTaskID.String), NodeID: node, TargetRevision: domain.Revision(tn.TargetRevision), TargetDigest: tn.TargetDigest}
	r := j.result(c, 2, false)
	r.Applied = false
	r.Cleanup = "pending"
	r.Restrictions = []control.Restriction{{IdentityID: domain.ID(grant.IdentityID), ListenerID: domain.ID(grant.ListenerID), Generation: domain.Revision(grant.Generation), Reason: "expired", Epoch: 1, ObservedAt: j.now, EffectiveDigest: strings.Repeat("c", 64)}}
	_, e := j.s.RecordResult(j.ctx, node, r)
	must(t, e)
	if _, e = j.s.NodeSnapshot(j.ctx, node); e == nil {
		t.Fatal("pending restriction admitted as baseline")
	}
	r.Sequence = 3
	r.Cleanup = "confirmed"
	r.ConfirmedAt = &j.now
	_, e = j.s.RecordResult(j.ctx, node, r)
	must(t, e)
	_, e = j.s.NodeSnapshot(j.ctx, node)
	must(t, e)
	_, e = j.subs.Delete(j.ctx, domain.Mutation{ExpectedRevision: two.Entity.Revision, IdempotencyKey: "delete-after-expiry"}, two.Entity.ID)
	must(t, e)
	j.cleanup(two.Entity.ID)
}
