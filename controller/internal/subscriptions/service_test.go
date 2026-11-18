package subscriptions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/auth"
	"github.com/nicccce/meshstrata/controller/internal/secrets"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

func testID(kind string, n int) domain.ID { return domain.ID(fmt.Sprintf("%s_%032x", kind, n)) }
func mutation(rev domain.Revision, key string) domain.Mutation {
	return domain.Mutation{ExpectedRevision: rev, IdempotencyKey: key}
}

type deterministic struct{ call uint64 }

func (r *deterministic) Read(b []byte) (int, error) {
	r.call++
	for i := range b {
		b[i] = byte(r.call*37 + uint64(i))
	}
	return len(b), nil
}

type rig struct {
	otherRoute                                     stage3.PublishedRoute
	t                                              *testing.T
	ctx                                            context.Context
	dir                                            string
	db                                             *storage.Store
	opts                                           stage3.SubscriptionsOptions
	s                                              *Service
	now                                            time.Time
	seq                                            int
	random                                         deterministic
	route                                          stage3.PublishedRoute
	profile                                        stage3.ProfileSnapshot
	leases                                         map[domain.ID]stage3.IngressLease
	reservations                                   map[domain.ID]stage3.EntryReservation
	changes                                        map[domain.ID]stage3.EntryChange
	tasks                                          map[domain.ID]domain.ID
	results                                        map[domain.ID]stage3.ReleaseState
	reserveCalls, applyCalls                       int
	afterReserve, afterApply, beforeState, onBuild func()
	dropReserve, dropApply, offline                bool
	lastCredential                                 control.PrivateCredential
	rawPayload                                     []byte
	guard                                          *auth.Service
	bootstrap                                      string
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, ctx: context.Background(), dir: t.TempDir(),
		now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC), seq: 100,
		leases: map[domain.ID]stage3.IngressLease{}, reservations: map[domain.ID]stage3.EntryReservation{},
		changes: map[domain.ID]stage3.EntryChange{}, tasks: map[domain.ID]domain.ID{}, results: map[domain.ID]stage3.ReleaseState{}}
	var err error
	r.db, err = storage.Open(r.ctx, r.dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.db.Close() })
	cipher, err := secrets.New(secrets.Options{CurrentKeyID: "key1", Keys: map[string][]byte{"key1": bytes.Repeat([]byte{8}, 32)}, Random: &deterministic{}})
	if err != nil {
		t.Fatal(err)
	}
	node := testID("node", 1)
	newPin := func(id domain.ID, v any) control.Pin {
		p, err := stage3.NewPin(id, 1, v)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	r.profile.Spec = stage3.ProfileSpec{Rules: []stage3.RuleLine{{Kind: "MATCH", Target: stage3.RuleTarget{Group: stage3.AllProxies}}}}
	r.profile.Pin = newPin(testID("profile", 1), r.profile.Spec)
	r.route = stage3.PublishedRoute{Name: "One Hop", EgressIsDefault: true, Verification: domain.NotChecked,
		Tail: control.RouteTail{Egress: newPin(testID("egress", 1), "node_default"), Hops: []control.HopSpec{
			{NodeID: node, DNS: newPin(testID("dns", 1), "dns"), NodeConfig: newPin(node, "config"),
				DialIP: "192.0.2.8", Endpoint: control.ProxyEndpoint{Host: "192.0.2.8", Port: 33000, Protocol: domain.Trojan, ServerName: "edge.example.invalid"}},
		}}}
	d, err := stage3.RouteDigest(r.route)
	if err != nil {
		t.Fatal(err)
	}
	r.route.Pin = control.Pin{ID: testID("route", 1), Revision: 1, Digest: d}
	r.exec(`INSERT INTO nodes(id,name,connection_mode,role,control_address,updated_at) VALUES (?,'edge','direct','node','unused',?);`, node, stamp(r.now))
	r.exec(`INSERT INTO node_dns_revisions(node_id,revision,outbound,digest) VALUES (?,1,'node_default',?);`, node, strings.Repeat("a", 64))
	r.exec(`INSERT INTO routes(id,name,revision,updated_at) VALUES (?,'One Hop',1,?);`, r.route.Pin.ID, stamp(r.now))
	r.exec(`INSERT INTO rule_profiles(id,name,revision,updated_at) VALUES (?,'fixture',1,?);`, r.profile.Pin.ID, stamp(r.now))
	r.exec(`INSERT INTO rule_profile_revisions(profile_id,revision,fallback_target,source_text) VALUES (?,1,'All-Proxies','MATCH,All-Proxies');`, r.profile.Pin.ID)
	r.exec(`INSERT INTO b07_profile_pins(profile_id,revision,digest,match_line,match_column) VALUES (?,1,?,1,1)`, r.profile.Pin.ID, r.profile.Pin.Digest)
	r.bootstrap = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32))
	h := sha256.Sum256([]byte(r.bootstrap))
	r.guard, err = auth.New(auth.NewRepository(r.db), auth.Options{Clock: func() time.Time { return r.now }, AllowedOrigins: []string{"https://panel.example.invalid"},
		Bootstrap: &auth.Bootstrap{SHA256: h[:], ExpiresAt: r.now.Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	r.opts = stage3.SubscriptionsOptions{Runtime: stage3.Runtime{DB: r.db, Now: func() time.Time { return r.now },
		NewID: func(k string) (domain.ID, error) { r.seq++; return testID(k, r.seq), nil }, Random: &r.random},
		Cipher: cipher, Guard: r.guard, Routes: r, Rules: r, Client: r, Publisher: r, PublicOrigin: "https://panel.example.invalid"}
	r.makeService()
	return r
}

func (r *rig) makeService() {
	r.t.Helper()
	var err error
	r.s, err = NewService(r.opts)
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) restart() {
	r.t.Helper()
	if err := r.db.Close(); err != nil {
		r.t.Fatal(err)
	}
	var err error
	r.db, err = storage.Open(r.ctx, r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	r.opts.DB = r.db
	r.makeService()
}

// Raw SQL is isolated to fixture setup/tampering or the fake B04's own
// transaction (including a real listener and allocated port).
func (r *rig) sql() *sql.DB {
	r.t.Helper()
	d, err := sql.Open("sqlite", "file:"+filepath.Join(r.dir, "controller.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		r.t.Fatal(err)
	}
	return d
}

func (r *rig) exec(q string, args ...any) {
	r.t.Helper()
	d := r.sql()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) PublishedRoute(_ context.Context, p control.Pin) (stage3.PublishedRoute, error) {
	switch p {
	case r.route.Pin:
		return r.route, nil
	case r.otherRoute.Pin:
		return r.otherRoute, nil
	default:
		return stage3.PublishedRoute{}, problem(domain.NotFound)
	}
}

func (r *rig) Profile(_ context.Context, p control.Pin) (stage3.ProfileSnapshot, error) {
	if p != r.profile.Pin {
		return stage3.ProfileSnapshot{}, problem(domain.NotFound)
	}
	return r.profile, nil
}

func (r *rig) Preview(v stage3.ClientSnapshot) (stage3.Preview, error) {
	if v.Preferences.Mode == "tun" && len(v.Preferences.DNS.Business) == 0 {
		r.t.Fatal("generation preview used implicit DNS")
	}
	return stage3.Preview{}, nil
}

func (r *rig) BuildFull(v stage3.GenerationInput) (stage3.FullConfig, error) {
	return r.build(v, "full")
}
func (r *rig) BuildProvider(v stage3.GenerationInput) (stage3.ProxyProvider, error) {
	return r.build(v, "provider")
}
func (r *rig) build(v stage3.GenerationInput, kind string) ([]byte, error) {
	if err := stage3.ValidateGenerationBinding(v); err != nil {
		r.t.Fatal(err)
	}
	r.lastCredential = v.Secrets[0].Credential
	r.rawPayload = []byte(kind + ":" + v.Secrets[0].Credential.Password)
	if r.onBuild != nil {
		r.onBuild()
	}
	return r.rawPayload, nil
}

func (r *rig) ReserveIngress(ctx context.Context, p stage3.EntryReservation) (stage3.IngressLease, error) {
	r.reserveCalls++
	if saved, exists := r.leases[p.OperationID]; exists {
		if !equalReservation(r.reservations[p.OperationID], p) {
			r.t.Fatal("reservation replay changed")
		}
		return saved, nil
	}
	if err := r.db.Read(ctx, func(q *dbgen.Queries) error {
		if err := CheckReservation(ctx, q, p, r.now); err != nil {
			return err
		}
		_, err := q.B08LegacyEntryByRoute(ctx, dbgen.B08LegacyEntryByRouteParams{SubscriptionID: string(p.SubscriptionID), RouteID: string(p.Route.ID)})
		if p.Authorization.Generation == 1 && !errors.Is(err, sql.ErrNoRows) {
			r.t.Fatal("fake gateway saw first 002 FK before reserve")
		}
		_, err = q.B01GetCredential(ctx, dbgen.B01GetCredentialParams{ID: string(p.CredentialRef), Purpose: "proxy_entry"})
		return err
	}); err != nil {
		return stage3.IngressLease{}, err
	}
	n := len(r.leases) + 1
	route, err := r.PublishedRoute(ctx, p.Route)
	if err != nil {
		return stage3.IngressLease{}, err
	}
	l := stage3.IngressLease{ListenerID: testID("listener", n), ReservationID: testID("lease", n), Route: p.Route, Endpoint: route.Tail.Hops[0].Endpoint}
	l.Endpoint.Port = uint16(34000 + n)
	db := r.sql()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO listeners(id,node_id,name,revision,protocol,listen_address,listen_port,outer_transport,credential_ref,config_digest,dns_revision,updated_at)
VALUES (?,?,'lease',1,?,'0.0.0.0',?,'tcp',?,?,1,?);`, l.ListenerID, r.route.Tail.Hops[0].NodeID, l.Endpoint.Protocol, l.Endpoint.Port, p.CredentialRef, p.Route.Digest, stamp(r.now)); err != nil {
		r.t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO port_reservations(id,node_id,listener_id,purpose,address,transport,port) VALUES (?,?,?,'listener','0.0.0.0','tcp',?);`,
		l.ReservationID, r.route.Tail.Hops[0].NodeID, l.ListenerID, l.Endpoint.Port); err != nil {
		r.t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		r.t.Fatal(err)
	}
	db.Close()
	r.leases[p.OperationID], r.reservations[p.OperationID] = l, p
	if r.afterReserve != nil {
		r.afterReserve()
	}
	if r.dropReserve {
		r.dropReserve = false
		return stage3.IngressLease{}, errors.New("hidden reservation data")
	}
	return l, nil
}

func (r *rig) ApplyEntry(ctx context.Context, c stage3.EntryChange) (domain.TaskAccepted, error) {
	r.applyCalls++
	op := c.Entry.OperationID
	if old, exists := r.tasks[op]; exists {
		if !reflect.DeepEqual(c, r.changes[op]) {
			r.t.Fatal("ApplyEntry replay changed request")
		}
		return domain.TaskAccepted{TaskID: old}, nil
	}
	if err := r.db.Read(ctx, func(q *dbgen.Queries) error {
		out, err := CheckDispatch(ctx, q, op, c.Kind, c.Subject, r.now)
		if err == nil && !reflect.DeepEqual(out, c) {
			r.t.Fatal("dispatch payload does not match durable grant")
		}
		return err
	}); err != nil {
		return domain.TaskAccepted{}, err
	}
	task := testID("task", len(r.tasks)+1)
	r.tasks[op], r.changes[op] = task, c
	r.results[task] = stage3.ReleaseState{TaskID: task, Subject: c.Subject, EntryOperationID: op, State: "running"}
	if r.afterApply != nil {
		r.afterApply()
	}
	if r.dropApply {
		r.dropApply = false
		return domain.TaskAccepted{}, errors.New("hidden task data")
	}
	return domain.TaskAccepted{TaskID: task}, nil
}

func (r *rig) ReleaseState(_ context.Context, task domain.ID) (stage3.ReleaseState, error) {
	out := r.results[task]
	if r.beforeState != nil {
		r.beforeState()
	}
	if r.offline {
		out.State = "queued"
	}
	return out, nil
}

func (r *rig) publish(op domain.ID, cleanup bool) {
	r.t.Helper()
	task := r.tasks[op]
	if task == "" {
		r.t.Fatal("missing task")
	}
	out := r.results[task]
	out.State = "published"
	if cleanup {
		out.Nodes = []domain.NodeResult{{NodeID: r.route.Tail.Hops[0].NodeID, Cleanup: "confirmed", Verification: domain.NotChecked}}
	}
	r.results[task] = out
}

func (r *rig) draft() stage3.SubscriptionDraft {
	return stage3.SubscriptionDraft{Name: "Work", ExpiresAt: r.now.Add(time.Hour), Profile: r.profile.Pin,
		Routes: []control.Pin{r.route.Pin}, Preferences: stage3.ClientPreferences{
			DNS: stage3.ClientDNS{Bootstrap: []string{"192.0.2.1"}, ProxyServers: []string{"192.0.2.2"}, Business: []string{"https://192.0.2.3/dns-query"}},
		}}
}

func (r *rig) create(k string) (stage3.SubscriptionView, domain.ID, string) {
	r.t.Helper()
	v, err := r.s.Create(r.ctx, mutation(0, k), r.draft())
	if err != nil {
		r.t.Fatal(err)
	}
	ops, err := r.s.PendingIntents(r.ctx, "", 128)
	if err != nil {
		r.t.Fatal(err)
	}
	var op domain.ID
	for _, o := range ops {
		i := r.intent(o)
		if i.SubscriptionID == string(v.Entity.ID) {
			op = o
		}
	}
	if op == "" {
		r.t.Fatal("missing durable intent")
	}
	return v, op, r.token(v.Entity.ID)
}

func (r *rig) token(id domain.ID) string {
	r.t.Helper()
	out, err := r.s.DownloadLink(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	token, ok := strings.CutPrefix(out.DownloadURL, r.opts.PublicOrigin+configPath)
	if !ok {
		r.t.Fatal("unexpected fixed origin or path")
	}
	return token
}

func (r *rig) intent(op domain.ID) (i dbgen.B08Intent) {
	r.t.Helper()
	err := r.db.Read(r.ctx, func(q *dbgen.Queries) error {
		var err error
		i, err = q.B08Intent(r.ctx, string(op))
		return err
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return
}

func (r *rig) run(op domain.ID) {
	r.t.Helper()
	if err := r.s.Reconcile(r.ctx, op); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) deny(token string) {
	r.t.Helper()
	if b, err := r.s.DownloadFull(r.ctx, token); err == nil || err.Error() != string(domain.NotFound) || b != nil {
		r.t.Fatal("unauthorized request was not uniformly denied")
	}
}

func TestDurableReservationAndApplyReplayIndependentEntries(t *testing.T) {
	r := newRig(t)
	a, ao, at := r.create("create-a")
	if a.Sync != "pending" || a.Entries[0].Endpoint.Port != 0 {
		t.Fatal("unreserved intent asserted a client port")
	}
	r.deny(at)
	b, bo, bt := r.create("create-b")
	if a.Entity.ID == b.Entity.ID || ao == bo || at == bt {
		t.Fatal("subscriptions shared identity or token")
	}
	r.dropReserve = true
	err := r.s.Reconcile(r.ctx, ao)
	if err == nil || strings.Contains(err.Error(), "hidden") || r.intent(ao).State != "prepared" {
		t.Fatal("lost lease response was not recoverable/redacted")
	}
	r.restart()
	r.dropApply = true
	err = r.s.Reconcile(r.ctx, ao)
	if err == nil || r.intent(ao).State != "dispatching" {
		t.Fatal("lost apply response did not preserve operation")
	}
	r.deny(at)
	r.restart()
	r.run(ao)
	// A forged acknowledgment is never equivalent to the exact release.
	task := r.tasks[ao]
	old := r.results[task]
	bad := old
	bad.State = "published"
	bad.EntryOperationID = bo
	r.results[task] = bad
	if err := r.s.Reconcile(r.ctx, ao); err == nil || err.Error() != string(domain.GrantOriginUncertain) {
		t.Fatal("wrong operation accepted")
	}
	r.deny(at)
	r.results[task] = old
	r.publish(ao, false)
	r.run(ao)
	r.run(ao)
	r.run(bo)
	r.publish(bo, false)
	r.run(bo)
	first, err := r.s.DownloadFull(r.ctx, at)
	if err != nil || !bytes.HasPrefix(first, []byte("full:")) {
		t.Fatal("first config unavailable")
	}
	firstCredential := r.lastCredential
	second, err := r.s.DownloadProvider(r.ctx, bt)
	if err != nil || !bytes.HasPrefix(second, []byte("provider:")) || r.lastCredential.Ref == firstCredential.Ref ||
		r.lastCredential.Password == firstCredential.Password || r.lastCredential.Audience != b.Entity.ID ||
		r.leases[ao].Endpoint.Port == r.leases[bo].Endpoint.Port {
		t.Fatal("entry credentials or ports were shared")
	}
	if r.reserveCalls != 3 || r.applyCalls != 3 || len(r.tasks) != 2 {
		t.Fatal("responses created duplicate leases/tasks")
	}
	v, err := r.s.Get(r.ctx, a.Entity.ID)
	if err != nil {
		t.Fatal(err)
	}
	v.Entries[0].Route.Tail.Hops[0].Endpoint.Port = 1
	reloaded, err := r.s.Get(r.ctx, a.Entity.ID)
	if err != nil || reloaded.Entries[0].Route.Tail.Hops[0].Endpoint.Port == 1 {
		t.Fatal("view shared route backing data")
	}
	replay, err := r.s.Create(r.ctx, mutation(0, "create-a"), r.draft())
	if err != nil || !reflect.DeepEqual(replay, a) {
		t.Fatal("retry did not return original pending response")
	}
	d := r.draft()
	d.Name = "Different"
	if _, err := r.s.Create(r.ctx, mutation(0, "create-a"), d); err == nil || err.Error() != string(domain.IdempotencyConflict) {
		t.Fatal("idempotency key changed payload")
	}
	wire, err := json.Marshal(ProjectManagement(reloaded))
	if err != nil || bytes.Contains(wire, []byte(at)) || bytes.Contains(wire, []byte(firstCredential.Password)) ||
		bytes.Contains(wire, []byte("credential")) || bytes.Contains(wire, []byte("tail")) {
		t.Fatal("management projection leaked private fields")
	}
}

func TestLostCASDoesNotCommitEntryOrApply(t *testing.T) {
	r := newRig(t)
	v, op, token := r.create("create")
	r.afterReserve = func() {
		_, err := r.s.Delete(r.ctx, mutation(1, "delete"), v.Entity.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	r.run(op)
	i := r.intent(op)
	if r.applyCalls != 0 || !i.ListenerID.Valid || i.IdentityID.Valid || i.CleanupState != "pending" {
		t.Fatal("late lease passed grant CAS or was released")
	}
	r.deny(token)
	// The fake gateway models the atomic never-armed evidence handoff.
	if err := r.db.Write(r.ctx, func(q *dbgen.Queries) error {
		return ConfirmUnarmed(r.ctx, q, op, domain.ID(i.ListenerID.String), r.now)
	}); err != nil {
		t.Fatal(err)
	}
	if r.intent(op).CleanupState != "confirmed" {
		t.Fatal("unarmed compensation was not durable")
	}
}

func TestOldReceiptRenewalAndOfflineDeletion(t *testing.T) {
	r := newRig(t)
	v, old, token := r.create("create")
	r.run(old)
	r.publish(old, false)
	// The result arrives across a committed expiry and renewal.
	r.beforeState = func() {
		r.beforeState = nil
		r.now = v.ExpiresAt
		_, err := r.s.Renew(r.ctx, mutation(1, "renew"), v.Entity.ID, r.now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	r.run(old)
	r.deny(token)
	ops, err := r.s.PendingIntents(r.ctx, "", 128)
	if err != nil || len(ops) != 2 {
		t.Fatal("lost cleanup or second generation")
	}
	newOp := ops[1]
	if newOp == old {
		newOp = ops[0]
	}
	n, o := r.intent(newOp), r.intent(old)
	if n.Generation != o.Generation+1 || n.EntryKey != o.EntryKey || n.IdentityKey == o.IdentityKey || n.CredentialRef == o.CredentialRef || !o.RevokedAt.Valid {
		t.Fatal("renew mutated old identity instead of retaining a tombstone")
	}
	r.offline = true
	r.run(old)
	if r.intent(old).CleanupState != "pending" || !r.intent(old).CleanupTaskID.Valid || r.changes[domain.ID(o.CleanupID)].Entry.Authorization.IdentityID != domain.ID(o.IdentityKey) {
		t.Fatal("offline cleanup did not use the old identity")
	}
	var released sql.NullString
	d := r.sql()
	defer d.Close()
	if err := d.QueryRow("SELECT released_at FROM listener_references WHERE id=?", old).Scan(&released); err != nil || released.Valid {
		t.Fatal("released a possibly armed reference before node confirmation")
	}
	r.offline = false
	r.run(newOp)
	r.publish(newOp, false)
	r.run(newOp)
	if _, err := r.s.DownloadFull(r.ctx, token); err != nil {
		t.Fatal("confirmed renewal unavailable")
	}
	r.publish(domain.ID(o.CleanupID), true)
	r.run(old)
	if r.intent(old).CleanupState != "confirmed" {
		t.Fatal("old confirmation not recorded")
	}
	del, err := r.s.Delete(r.ctx, mutation(2, "delete"), v.Entity.ID)
	if err != nil || del.Sync != "pending" || del.RevokedAt == nil {
		t.Fatal("delete claimed remote success")
	}
	r.restart()
	r.deny(token)
	if _, err := r.s.Renew(r.ctx, mutation(3, "bad-renew"), v.Entity.ID, r.now.Add(2*time.Hour)); err == nil {
		t.Fatal("deleted subscription resurrected")
	}
}

func TestDownloadRacesDiscardGeneratedBytes(t *testing.T) {
	for _, which := range []string{"expire", "reset", "delete", "config", "cancel"} {
		t.Run(which, func(t *testing.T) {
			r := newRig(t)
			v, op, token := r.create("create")
			r.run(op)
			r.publish(op, false)
			r.run(op)
			ctx, cancel := context.WithCancel(r.ctx)
			defer cancel()
			r.onBuild = func() {
				var err error
				switch which {
				case "expire":
					r.now = v.ExpiresAt
				case "reset":
					_, err = r.s.ResetDownloadToken(r.ctx, mutation(1, "reset"), v.Entity.ID)
				case "delete":
					_, err = r.s.Delete(r.ctx, mutation(1, "delete"), v.Entity.ID)
				case "config":
					d := r.draft()
					d.ID = v.Entity.ID
					d.Preferences.Mode = "tun"
					d.Name = "Edited"
					_, err = r.s.UpdateConfig(r.ctx, mutation(1, "edit"), d)
				case "cancel":
					cancel()
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			out, err := r.s.DownloadFull(ctx, token)
			if err == nil || out != nil || !bytes.Equal(r.rawPayload, make([]byte, len(r.rawPayload))) {
				t.Fatal("racing old payload was returned or not cleared")
			}
			if which == "expire" {
				r.restart()
				r.now = v.ExpiresAt.Add(-time.Minute)
				r.deny(token)
			}
			if which == "config" {
				if r.intent(op).State != "confirmed" || len(r.tasks) != 1 {
					t.Fatal("client config-only edit rotated entry")
				}
			}
		})
	}
}

func TestTokenBindingAndRotation(t *testing.T) {
	r := newRig(t)
	v, op, token := r.create("create")
	other, _, _ := r.create("other")
	r.run(op)
	r.publish(op, false)
	r.run(op)
	if _, err := r.s.DownloadFull(r.ctx, token); err != nil {
		t.Fatal(err)
	}
	original := r.lastCredential
	r.restart()
	if token != r.token(v.Entity.ID) {
		t.Fatal("copy link reset on restart")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != stage3.DownloadTokenBytes {
		t.Fatal("incorrect entropy encoding")
	}
	d := r.sql()
	defer d.Close()
	var storedHash, cipher []byte
	var ref, key string
	var version int64
	q := `SELECT s.download_token_hash,c.ciphertext,c.id,c.key_id,c.token_version FROM subscriptions s JOIN subscription_download_credentials c ON s.download_token_credential_ref=c.id WHERE s.id=?`
	if err := d.QueryRow(q, v.Entity.ID).Scan(&storedHash, &cipher, &ref, &key, &version); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if !sameHash(storedHash, hash[:]) || bytes.Contains(cipher, raw) {
		t.Fatal("token not separately hashed and encrypted")
	}
	if _, err := r.opts.Cipher.Open(r.ctx, tokenBinding(string(v.Entity.ID), ref, version+1), secretstore.Sealed{KeyID: key, Ciphertext: cipher}); err == nil {
		t.Fatal("cipher accepted wrong version")
	}
	if _, err := d.Exec("UPDATE subscriptions SET download_token_credential_ref=? WHERE id=?", ref, other.Entity.ID); err == nil {
		t.Fatal("SQLite allowed cross-subscription pointer")
	}
	var otherRef string
	if err := d.QueryRow("SELECT download_token_credential_ref FROM subscriptions WHERE id=?", other.Entity.ID).Scan(&otherRef); err != nil {
		t.Fatal(err)
	}
	r.exec("UPDATE subscription_download_credentials SET ciphertext=? WHERE id=?", cipher, otherRef)
	if _, err := r.s.DownloadLink(r.ctx, other.Entity.ID); err == nil {
		t.Fatal("cross-substitution decrypted")
	}
	if _, err := r.s.ResetDownloadToken(r.ctx, mutation(1, "reset"), v.Entity.ID); err != nil {
		t.Fatal(err)
	}
	r.deny(token)
	recent := r.token(v.Entity.ID)
	if recent == token {
		t.Fatal("token was not rotated")
	}
	if _, err := r.s.DownloadFull(r.ctx, recent); err != nil || original != r.lastCredential {
		t.Fatal("reset rotated proxy credential")
	}
	if _, err := r.s.ResetDownloadToken(r.ctx, mutation(1, "stale"), v.Entity.ID); err == nil {
		t.Fatal("stale CAS accepted")
	}
}

func TestDownloadHandlerWithRealAdminGuard(t *testing.T) {
	r := newRig(t)
	v, op, token := r.create("create")
	r.run(op)
	r.publish(op, false)
	r.run(op)
	const origin = "https://panel.example.invalid"
	mux := r.guard.Handler()
	call := func(h http.Handler, method, target string, headers map[string]string, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		req.Host = "untrusted.example.invalid"
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res
	}
	in := `{"username":"admin","password":"unrelated test password 012345!"}`
	set := call(mux, "POST", "/api/v1/auth/initialize", map[string]string{"Origin": origin, "Content-Type": "application/json", "X-Meshstrata-Bootstrap": r.bootstrap}, in, nil)
	if set.Code != 204 {
		t.Fatalf("initializer: %d", set.Code)
	}
	login := call(mux, "POST", "/api/v1/auth/login", map[string]string{"Origin": origin, "Content-Type": "application/json"}, in, nil)
	if login.Code != 204 || len(login.Result().Cookies()) != 1 {
		t.Fatalf("login: %d", login.Code)
	}
	cookie := login.Result().Cookies()[0]
	sess := call(mux, "GET", "/api/v1/auth/session", nil, "", cookie)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	if err := json.Unmarshal(sess.Body.Bytes(), &session); err != nil || session.CSRF == "" {
		t.Fatal("missing CSRF from real session")
	}
	h := r.s.HTTPHandler()
	copyPath := managementPrefix + string(v.Entity.ID) + copySuffix
	for _, c := range []struct {
		method, path string
		headers      map[string]string
		cookie       *http.Cookie
		status       int
		secret       bool
	}{
		{"POST", copyPath, nil, nil, 401, false},
		{"POST", copyPath, map[string]string{"Origin": origin}, cookie, 403, false},
		{"POST", copyPath, map[string]string{"Origin": "https://untrusted.example.invalid", "X-CSRF-Token": session.CSRF}, cookie, 403, false},
		{"POST", copyPath, map[string]string{"Origin": origin, "X-CSRF-Token": session.CSRF}, cookie, 200, true},
		{"GET", configPath + token, nil, nil, 200, false},
		{"GET", configPath + token + "?token=" + token, nil, nil, 404, false},
		{"GET", configPath + "./" + token, nil, nil, 404, false},
		{"GET", configPath + token + "/", nil, nil, 404, false},
		{"GET", configPath + "%41" + token[1:], nil, nil, 404, false},
		{"GET", strings.TrimSuffix(configPath, "/"), map[string]string{"Authorization": "Bearer " + cookie.Value}, cookie, 404, false},
	} {
		res := call(h, c.method, c.path, c.headers, "", c.cookie)
		if res.Code != c.status || res.Header().Get("Cache-Control") != "no-store" || res.Header().Get("Referrer-Policy") != "no-referrer" ||
			res.Header().Get("Location") != "" || strings.Contains(res.Body.String(), token) != c.secret || strings.Contains(res.Body.String(), "untrusted.example") {
			t.Fatalf("bad sensitive response: method %s got %d, wanted %d", c.method, res.Code, c.status)
		}
		if c.secret && !strings.Contains(res.Body.String(), origin+configPath) {
			t.Fatal("link did not use trusted HTTPS origin")
		}
	}
}

func TestLegacyHashOnlyRequiresExplicitReset(t *testing.T) {
	r := newRig(t)
	id := testID("sub", 9)
	raw := bytes.Repeat([]byte{5}, 32)
	hash := sha256.Sum256(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	r.exec(`INSERT INTO subscriptions(id,name,revision,profile_id,profile_revision,expires_at,download_token_hash,token_version,updated_at)
VALUES (?,'Legacy',1,?,1,?,?,7,?);`, id, r.profile.Pin.ID, stamp(r.now.Add(time.Hour)), hash[:], stamp(r.now))
	if l, err := r.s.DownloadLink(r.ctx, id); err == nil || l.DownloadURL != "" {
		t.Fatal("legacy hash yielded a guessed link")
	}
	r.deny(token)
	if _, err := r.s.ResetDownloadToken(r.ctx, mutation(1, "legacy-reset"), id); err != nil {
		t.Fatal(err)
	}
	got := r.token(id)
	if got == token {
		t.Fatal("legacy token was not replaced")
	}
	r.deny(got) // no reviewed 002 authorization was guessed at migration time
	r.deny(token)
}

func TestAddingRoutePreservesOnlyStillAuthorizedSnapshot(t *testing.T) {
	r := newRig(t)
	v, op, token := r.create("create")
	r.run(op)
	r.publish(op, false)
	r.run(op)
	if _, err := r.s.DownloadFull(r.ctx, token); err != nil {
		t.Fatal(err)
	}
	original := r.lastCredential
	r.otherRoute = r.route
	r.otherRoute.Name = "Additional"
	sum, err := stage3.RouteDigest(r.otherRoute)
	if err != nil {
		t.Fatal(err)
	}
	r.otherRoute.Pin = control.Pin{ID: testID("route", 2), Revision: 1, Digest: sum}
	r.exec("INSERT INTO routes(id,name,revision,updated_at) VALUES (?,'Additional',1,?)", r.otherRoute.Pin.ID, stamp(r.now))
	d := r.draft()
	d.ID = v.Entity.ID
	d.Preferences.Mode = "tun"
	d.Routes = append(d.Routes, r.otherRoute.Pin)
	edit, err := r.s.UpdateConfig(r.ctx, mutation(1, "add"), d)
	if err != nil || len(edit.Entries) != 2 || edit.Entries[1].Sync != "pending" {
		t.Fatal("new route not staged")
	}
	if _, err := r.s.DownloadFull(r.ctx, token); err != nil || r.lastCredential != original {
		t.Fatal("existing authorized snapshot was not preserved")
	}
	ops, err := r.s.PendingIntents(r.ctx, "", 128)
	if err != nil || len(ops) != 1 {
		t.Fatal("retained route was unexpectedly queued")
	}
	add := ops[0]
	r.run(add)
	r.publish(add, false)
	r.run(add)
	fresh, err := r.s.Get(r.ctx, v.Entity.ID)
	if err != nil || len(fresh.Entries) != 2 || fresh.Entries[1].Sync != "confirmed" {
		t.Fatal("both routes should have been acknowledged")
	}
	d.Routes = []control.Pin{r.otherRoute.Pin}
	if _, err := r.s.UpdateConfig(r.ctx, mutation(2, "remove"), d); err != nil {
		t.Fatal(err)
	}
	if _, err := r.s.DownloadFull(r.ctx, token); err != nil || r.lastCredential == original || r.intent(op).CleanupState != "pending" {
		t.Fatal("removed route was still authorized or inappropriately released")
	}
}

func TestTamperedProxyEnvelopeCannotCrossSubscriptions(t *testing.T) {
	r := newRig(t)
	_, a, at := r.create("first")
	_, b, bt := r.create("second")
	r.run(a)
	r.publish(a, false)
	r.run(a)
	r.run(b)
	r.publish(b, false)
	r.run(b)
	r.exec("UPDATE credentials SET ciphertext=(SELECT ciphertext FROM credentials WHERE id=?) WHERE id=?",
		r.intent(a).CredentialRef, r.intent(b).CredentialRef)
	r.deny(bt)
	if _, err := r.s.DownloadFull(r.ctx, at); err != nil {
		t.Fatal("tamper affected unrelated subscription")
	}
}

func TestFailedReleaseRetiresCandidateWithoutPublishing(t *testing.T) {
	r := newRig(t)
	v, op, token := r.create("create")
	r.run(op)
	task := r.tasks[op]
	res := r.results[task]
	res.State = "rollback_failed"
	r.results[task] = res
	r.run(op)
	i := r.intent(op)
	out, err := r.s.Get(r.ctx, v.Entity.ID)
	if err != nil || out.Sync != "failed" || i.State != "failed" || !i.RevokedAt.Valid || i.CleanupState != "pending" {
		t.Fatal("failed grant was not durably retired")
	}
	r.deny(token)
	r.publish(op, false)
	r.run(op)
	r.deny(token)
}
