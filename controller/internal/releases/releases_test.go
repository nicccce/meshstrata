package releases

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/enrollment"
	"github.com/nicccce/meshstrata/controller/internal/secrets"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

type testGuard struct{}

func (testGuard) Read(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Test-Admin") != "yes" {
			w.WriteHeader(401)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (g testGuard) Write(next http.Handler) http.Handler {
	return g.Read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != "https://admin.test" || r.Header.Get("X-CSRF-Token") != "yes" {
			w.WriteHeader(403)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

type fixture struct {
	t           *testing.T
	ctx         context.Context
	dir         string
	store       *storage.Store
	s           *Service
	now         time.Time
	cipher      secretstore.Cipher
	connections map[domain.ID]bool
	sent        []control.Command
}

func fixtureFor(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, ctx: context.Background(), dir: t.TempDir(), now: time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC), connections: map[domain.ID]bool{}}
	file, err := os.OpenFile(filepath.Join(f.dir, "controller.db"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	must(t, err)
	must(t, file.Close())
	key := make([]byte, 32)
	_, err = rand.Read(key)
	must(t, err)
	f.cipher, err = secrets.New(secrets.Options{CurrentKeyID: "test", Keys: map[string][]byte{"test": key}})
	clear(key)
	must(t, err)
	f.reopen()
	t.Cleanup(func() { _ = f.store.Close() })
	return f
}
func (f *fixture) reopen() {
	f.t.Helper()
	if f.store != nil {
		must(f.t, f.s.Close())
		must(f.t, f.store.Close())
	}
	var err error
	f.store, err = storage.Open(f.ctx, f.dir)
	must(f.t, err)
	f.s, err = New(NewRepository(f.store), f.cipher, testGuard{}, Options{
		Now: func() time.Time { return f.now }, NewID: domain.NewID,
		Connected: func(id domain.ID) bool { return f.connections[id] },
		Deliver:   func(_ context.Context, c control.Command) error { f.sent = append(f.sent, c); return nil },
		RemovalIntent: func(ctx context.Context, task domain.ID) (enrollment.RemovalIntent, error) {
			var out enrollment.RemovalIntent
			err := f.store.Read(ctx, func(q *dbgen.Queries) error {
				row, err := q.B02GetRemovalIntent(ctx, nullable(string(task)))
				out = enrollment.RemovalIntent{TaskID: task, NodeID: domain.ID(row.NodeID), Revision: domain.Revision(row.Revision)}
				return err
			})
			return out, err
		},
	})
	must(f.t, err)
}
func (f *fixture) node() domain.ID {
	f.t.Helper()
	id := idFor(f.t, "node")
	must(f.t, f.store.Write(f.ctx, func(q *dbgen.Queries) error {
		return q.CreateNode(f.ctx, dbgen.CreateNodeParams{ID: string(id), Name: "test", ConnectionMode: "direct", Role: "node", ControlAddress: "localhost", UpdatedAt: stamp(f.now)})
	}))
	f.connections[id] = true
	return id
}
func (f *fixture) listener(node domain.ID, port uint16, address string) control.BundleListener {
	f.t.Helper()
	raw := make([]byte, 16)
	_, err := rand.Read(raw)
	must(f.t, err)
	return control.BundleListener{
		ListenerID: idFor(f.t, "listener"), NodeID: node, Protocol: domain.SS2022, Address: address, Port: port, Transport: "tcp",
		Authorization: control.BundleAuthorization{EntryID: idFor(f.t, "entry"), IdentityID: idFor(f.t, "identity"), Generation: 1, ExpiresAt: f.now.Add(time.Hour)},
		Credential:    control.PrivateCredential{Ref: idFor(f.t, "credential"), Audience: node, Password: base64.StdEncoding.EncodeToString(raw)},
		Cipher:        "2022-blake3-aes-128-gcm", ServerNames: []string{}, ShortIDs: []string{},
	}
}
func (f *fixture) target(node domain.ID, rev domain.Revision, listeners ...control.BundleListener) NodePlan {
	f.t.Helper()
	var b control.Bundle
	b.Format, b.NodeID, b.TargetRevision, b.RevocationEpoch = 1, node, rev+1, 1
	b.Rules.Default, b.Egress.Mode = "DIRECT", "direct"
	b.DNS.Bootstrap, b.DNS.Resolvers = []string{"1.1.1.1"}, []string{"1.1.1.1"}
	pin := func(kind string, payload any) control.Pin {
		digest, err := control.ComponentDigest(payload)
		must(f.t, err)
		return control.Pin{ID: idFor(f.t, kind), Revision: 1, Digest: digest}
	}
	b.Rules.Pin = pin("rules", b.Rules.Default)
	b.Egress.Pin = pin("egress", b.Egress.Mode)
	b.DNS.Pin = pin("dns", control.DNSPolicy{Bootstrap: b.DNS.Bootstrap, Resolvers: b.DNS.Resolvers})
	b.Listeners = append([]control.BundleListener{}, listeners...)
	slices.SortFunc(b.Listeners, func(a, b control.BundleListener) int {
		return strings.Compare(string(a.ListenerID), string(b.ListenerID))
	})
	b.Tombstones = []control.Tombstone{}
	b.Resources = []control.Resource{}
	return NodePlan{NodeID: node, ExpectedRevision: rev, Template: b}
}
func (f *fixture) plan(nodes ...NodePlan) Plan {
	return Plan{Kind: domain.Publish, Subject: domain.RevisionRef{ID: idFor(f.t, "subject"), Revision: 1}, Deadline: f.now.Add(time.Minute), Nodes: nodes}
}
func (f *fixture) submit(key string, p Plan) domain.ID {
	f.t.Helper()
	res, err := f.s.Submit(f.ctx, mutation(key, 1), p)
	must(f.t, err)
	return res.TaskID
}
func (f *fixture) artifact(c control.Command) []byte {
	f.t.Helper()
	key, err := f.s.Resolve(f.ctx, c.NodeID, c.TaskID)
	must(f.t, err)
	r, err := f.s.Open(f.ctx, c.NodeID, key)
	must(f.t, err)
	defer r.Close()
	data, err := io.ReadAll(r)
	must(f.t, err)
	b, err := control.DecodeBundle(data, key, c.NodeID)
	must(f.t, err)
	must(f.t, b.BindCommand(c))
	return data
}
func (f *fixture) dispatch() []control.Command {
	f.t.Helper()
	f.sent = nil
	must(f.t, f.s.dispatch(f.ctx))
	return f.sent
}
func (f *fixture) result(c control.Command, seq int64, verified bool) control.Result {
	r := control.Result{TaskID: c.TaskID, Sequence: seq, NodeResult: domain.NodeResult{NodeID: c.NodeID, TargetRevision: c.TargetRevision,
		TargetDigest: c.TargetDigest, Applied: true, Verification: domain.Incomplete, Rollback: "none", Cleanup: "none"}}
	if verified {
		r.Verification = domain.Verified
		at := f.now
		r.ConfirmedAt = &at
	}
	return r
}
func (f *fixture) record(r control.Result) {
	f.t.Helper()
	fresh, err := f.s.RecordResult(f.ctx, r.NodeID, r)
	must(f.t, err)
	if !fresh {
		f.t.Fatal("expected new committed result")
	}
}
func (f *fixture) row(task domain.ID) dbgen.Task {
	f.t.Helper()
	var row dbgen.Task
	must(f.t, f.store.Read(f.ctx, func(q *dbgen.Queries) error {
		var err error
		row, err = q.GetTask(f.ctx, string(task))
		return err
	}))
	return row
}
func (f *fixture) raw() *sql.DB {
	f.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "controller.db")+"?_pragma=foreign_keys(1)")
	must(f.t, err)
	f.t.Cleanup(func() { _ = db.Close() })
	return db
}
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func code(t *testing.T, err error, expected domain.ErrorCode) {
	t.Helper()
	var p *domain.Problem
	if !errors.As(err, &p) || p.Code != expected {
		t.Fatalf("expected %s, got %v", expected, err)
	}
}
func idFor(t *testing.T, kind string) domain.ID {
	t.Helper()
	id, err := domain.NewID(kind)
	must(t, err)
	return id
}
func mutation(key string, rev domain.Revision) domain.Mutation {
	return domain.Mutation{IdempotencyKey: key, ExpectedRevision: rev}
}

func TestSubmitAtomicCASAndReplay(t *testing.T) {
	f := fixtureFor(t)
	node, other := f.node(), f.node()
	l := f.listener(node, 24001, "0.0.0.0")
	p := f.plan(f.target(node, 1, l), f.target(other, 1, f.listener(other, 24001, "::")))
	db := f.raw()
	_, err := db.Exec("CREATE TRIGGER abort_artifact BEFORE INSERT ON b04_artifacts WHEN NEW.node_id='" + string(p.Nodes[0].NodeID) + "' BEGIN SELECT RAISE(ABORT,'injected'); END")
	must(t, err)
	_, err = f.s.Submit(f.ctx, mutation("broken", 1), p)
	if err == nil {
		t.Fatal("injected SQL failure passed")
	}
	for _, table := range []string{"tasks", "task_nodes", "b04_artifacts", "b04_ports", "b04_grants", "b04_references"} {
		var count int
		must(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
		if count != 0 {
			t.Fatalf("%s leaked rows", table)
		}
	}
	_, err = db.Exec("DROP TRIGGER abort_artifact")
	must(t, err)
	var wg sync.WaitGroup
	type attempt struct {
		id  domain.ID
		err error
	}
	out := make(chan attempt, 2)
	for range 2 {
		wg.Go(func() {
			r, err := f.s.Submit(f.ctx, mutation("same", 1), p)
			out <- attempt{r.TaskID, err}
		})
	}
	wg.Wait()
	first, second := <-out, <-out
	must(t, first.err)
	must(t, second.err)
	if first.id == "" || first.id != second.id {
		t.Fatal("replay changed task ID")
	}
	p.Nodes[0], p.Nodes[1] = p.Nodes[1], p.Nodes[0]
	if f.submit("same", p) != first.id {
		t.Fatal("targets were not canonicalized")
	}
	p.Deadline = p.Deadline.Add(time.Second)
	_, err = f.s.Submit(f.ctx, mutation("same", 1), p)
	code(t, err, domain.IdempotencyConflict)
	_, err = f.s.Submit(f.ctx, mutation("other", 1), p)
	code(t, err, domain.RevisionConflict)

	n := f.node()
	p = f.plan(f.target(n, 1, f.listener(n, 24003, "::1")))
	for _, key := range []string{"cas1", "cas2"} {
		wg.Go(func() {
			r, err := f.s.Submit(f.ctx, mutation(key, 1), p)
			out <- attempt{r.TaskID, err}
		})
	}
	wg.Wait()
	first, second = <-out, <-out
	if first.err != nil {
		first, second = second, first
	}
	must(t, first.err)
	code(t, second.err, domain.RevisionConflict)
}

func TestDAGRestartSerialExpiryAndLateHistory(t *testing.T) {
	f := fixtureFor(t)
	entry, next := f.node(), f.node()
	downstream := f.listener(next, 24006, "::")
	a := f.target(entry, 1, f.listener(entry, 24005, "::"))
	b := f.target(next, 1, downstream)
	a.DependsOn = []domain.ID{next}
	b.DependsOn = []domain.ID{entry}
	_, err := f.s.Submit(f.ctx, mutation("cycle", 1), f.plan(a, b))
	code(t, err, domain.InvalidArgument)
	b.DependsOn = nil
	f.connections[next] = false
	task := f.submit("dag", f.plan(a, b))
	newer := f.submit("second", f.plan(f.target(next, 2, downstream)))
	if len(f.dispatch()) != 0 {
		t.Fatal("offline dependency or same-node queue sent")
	}
	f.connections[next] = true
	f.now = f.now.Add(redelivery)
	c := f.dispatch()
	if len(c) != 1 || c[0].TaskID != task || c[0].NodeID != next {
		t.Fatal("dependency must dispatch first")
	}
	down := c[0]
	clear(f.artifact(down))
	f.record(f.result(down, 2, false))
	f.record(f.result(down, 1, true))
	if len(f.dispatch()) != 0 {
		t.Fatal("old verified result advanced newer incomplete result")
	}
	f.reopen()
	h := control.Hello{NodeID: next, AppliedRevision: down.TargetRevision, AppliedDigest: down.TargetDigest, RevocationEpoch: 1, PendingResults: []control.Result{f.result(down, 2, false)}}
	f.connections[next] = false
	must(t, f.s.Hello(f.ctx, next, h))
	f.connections[next] = true
	if c := f.dispatch(); len(c) != 1 || c[0].TargetDigest != down.TargetDigest {
		t.Fatal("restart did not redeliver exact target")
	}
	f.record(f.result(down, 3, true))
	c = f.dispatch()
	if len(c) != 1 || c[0].NodeID != entry {
		t.Fatal("entry did not follow verification")
	}
	up := c[0]
	clear(f.artifact(up))
	f.record(f.result(up, 1, true))
	if f.row(task).State != "succeeded" {
		t.Fatal("DAG did not finish")
	}
	if c := f.dispatch(); len(c) != 1 || c[0].TaskID != newer {
		t.Fatal("same-node newer task did not run")
	}
	second := f.sent[0]
	clear(f.artifact(second))
	f.now = f.now.Add(time.Minute)
	if len(f.dispatch()) != 0 || f.row(newer).State != "timed_out" {
		t.Fatal("deadline did not fence dispatch")
	}
	f.record(f.result(second, 1, true))
	if f.row(newer).State != "timed_out" {
		t.Fatal("late result revived expired task")
	}
	var stored int
	must(t, f.raw().QueryRow("SELECT count(*) FROM b04_results WHERE task_id=?", newer).Scan(&stored))
	if stored != 1 {
		t.Fatal("late result missing")
	}
}

func TestRestrictionEvidenceAndProofGatedPort(t *testing.T) {
	f := fixtureFor(t)
	node, peer := f.node(), f.node()
	l := f.listener(node, 24007, "0.0.0.0")
	untouched := f.listener(node, 24008, "0.0.0.0")
	untouched.Authorization.ExpiresAt = untouched.Authorization.ExpiresAt.Add(time.Hour)
	task := f.submit("grant", f.plan(f.target(node, 1, l, untouched)))
	c := f.dispatch()[0]
	clear(f.artifact(c))
	f.record(f.result(c, 1, true))
	db := f.raw()
	portState := func(port uint16, want string) {
		t.Helper()
		var got string
		must(t, db.QueryRow("SELECT state FROM b04_ports WHERE node_id=? AND port=? ORDER BY rowid DESC LIMIT 1", node, port).Scan(&got))
		if got != want {
			t.Fatalf("port state %s want %s", got, want)
		}
	}
	f.now = l.Authorization.ExpiresAt
	r := f.result(c, 3, false)
	r.Applied, r.Cleanup = false, "pending"
	r.Restrictions = []control.Restriction{{IdentityID: l.Authorization.IdentityID, ListenerID: l.ListenerID, Generation: 1, Reason: "expired", Epoch: 1,
		ObservedAt: f.now, EffectiveDigest: strings.Repeat("a", 64)}}
	bad := r
	bad.Restrictions = append([]control.Restriction(nil), r.Restrictions...)
	bad.Restrictions[0].IdentityID = untouched.Authorization.IdentityID
	_, err := f.s.RecordResult(f.ctx, node, bad)
	code(t, err, domain.RevisionConflict)
	_, err = f.s.RecordResult(f.ctx, peer, r)
	code(t, err, domain.InvalidArgument)

	_, err = db.Exec("CREATE TRIGGER abort_restriction BEFORE INSERT ON b04_restrictions BEGIN SELECT RAISE(ABORT,'injected'); END")
	must(t, err)
	fresh, err := f.s.RecordResult(f.ctx, node, r)
	if fresh || err == nil {
		t.Fatal("commit failure allowed receipt")
	}
	_, err = db.Exec("DROP TRIGGER abort_restriction")
	must(t, err)
	f.record(r)
	portState(l.Port, "pending_cleanup")
	portState(untouched.Port, "reserved")
	var count int
	must(t, db.QueryRow("SELECT count(*) FROM b04_references WHERE identity_id=? AND released_at IS NULL", l.Authorization.IdentityID).Scan(&count))
	if count != 0 {
		t.Fatal("restriction did not retire only its references")
	}
	fresh, err = f.s.RecordResult(f.ctx, node, r)
	must(t, err)
	if fresh {
		t.Fatal("duplicate result was treated as new")
	}
	old := r
	old.Sequence = 2
	old.Restrictions = append([]control.Restriction(nil), r.Restrictions...)
	old.Restrictions[0].EffectiveDigest = strings.Repeat("b", 64)
	f.record(old)
	old.Cleanup = "confirmed"
	at := f.now
	old.ConfirmedAt = &at
	_, err = f.s.RecordResult(f.ctx, node, old)
	code(t, err, domain.IdempotencyConflict)
	h := control.Hello{NodeID: node, AppliedRevision: c.TargetRevision, AppliedDigest: c.TargetDigest, RevocationEpoch: 1, PendingResults: []control.Result{r}}
	must(t, f.s.Hello(f.ctx, node, h))
	portState(l.Port, "pending_cleanup")
	confirmed := r
	confirmed.Sequence, confirmed.Cleanup, confirmed.ConfirmedAt = 4, "confirmed", &at
	f.record(confirmed)
	portState(l.Port, "released")
	portState(untouched.Port, "reserved")
	var verified, effective string
	var applied int
	must(t, db.QueryRow("SELECT verification,applied FROM task_nodes WHERE task_id=?", task).Scan(&verified, &applied))
	must(t, db.QueryRow("SELECT effective_digest FROM b04_node_state WHERE node_id=?", node).Scan(&effective))
	if applied != 1 || verified != "verified" || f.row(task).State != "succeeded" || effective != strings.Repeat("a", 64) {
		t.Fatal("restriction rewrote publish or rolled back effective state")
	}
	must(t, db.QueryRow("SELECT count(*) FROM b04_restrictions WHERE task_id=?", task).Scan(&count))
	if count != 3 {
		t.Fatalf("expected all 3 unique sequences, got %d", count)
	}
}

func TestArtifactAndManagementActions(t *testing.T) {
	f := fixtureFor(t)
	node, wrong := f.node(), f.node()
	l := f.listener(node, 24012, "::")
	task := f.submit("original", f.plan(f.target(node, 1, l)))
	if _, err := f.s.Resolve(f.ctx, node, task); err == nil {
		t.Fatal("queued artifact escaped")
	}
	c := f.dispatch()[0]
	key, err := f.s.Resolve(f.ctx, node, task)
	must(t, err)
	if out, err := f.s.Open(f.ctx, wrong, key); out != nil || err == nil {
		t.Fatal("wrong node authorized")
	}
	bad := key
	bad.TargetRevision++
	if out, err := f.s.Open(f.ctx, node, bad); out != nil || err == nil {
		t.Fatal("wrong revision authorized")
	}
	db := f.raw()
	var ciphertext []byte
	must(t, db.QueryRow("SELECT ciphertext FROM b04_artifacts WHERE task_id=?", task).Scan(&ciphertext))
	if bytes.Contains(ciphertext, []byte(l.Credential.Password)) {
		t.Fatal("credential persisted in clear")
	}
	_, err = db.Exec("UPDATE b04_artifacts SET ciphertext=? WHERE task_id=?", []byte{1}, task)
	must(t, err)
	if out, err := f.s.Open(f.ctx, node, key); out != nil || err == nil {
		t.Fatal("invalid ciphertext authorized")
	}
	var artifactID domain.ID
	must(t, db.QueryRow("SELECT id FROM b04_artifacts WHERE task_id=?", task).Scan(&artifactID))
	tampered, err := f.cipher.Seal(f.ctx, binding(artifactID), []byte("valid envelope, wrong digest"))
	must(t, err)
	_, err = db.Exec("UPDATE b04_artifacts SET ciphertext=? WHERE task_id=?", tampered.Ciphertext, task)
	must(t, err)
	if out, err := f.s.Open(f.ctx, node, key); out != nil || err == nil {
		t.Fatal("digest mismatch authorized")
	}
	_, err = db.Exec("UPDATE b04_artifacts SET ciphertext=? WHERE task_id=?", ciphertext, task)
	must(t, err)
	clear(f.artifact(c))
	f.record(f.result(c, 1, true))
	handler := f.s.ManagementHandler()
	request := func(method, path, body string, auth, write bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		if auth {
			r.Header.Set("X-Test-Admin", "yes")
		}
		if write {
			r.Header.Set("Origin", "https://admin.test")
			r.Header.Set("X-CSRF-Token", "yes")
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	path := "/api/v1/tasks/" + string(task)
	if w := request("GET", path, "", false, false); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if w := request("GET", path, "", true, false); w.Code != 200 || strings.Contains(w.Body.String(), l.Credential.Password) || strings.Contains(w.Body.String(), "ciphertext") {
		t.Fatal("invalid public task DTO")
	}
	if w := request("GET", "/api/v1/tasks/"+string(idFor(t, "task")), "", true, false); w.Code != 404 {
		t.Fatal(w.Code)
	}
	m := mutation("rollback-once", domain.Revision(f.row(task).Revision))
	body, err := json.Marshal(m)
	must(t, err)
	if w := request("POST", path+"/rollback", string(body), true, false); w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request("POST", path+"/rollback", `{"bundle":{}}`, true, true); w.Code != 400 {
		t.Fatal(w.Code)
	}
	w := request("POST", path+"/rollback", string(body), true, true)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	var rolled domain.TaskAccepted
	must(t, json.Unmarshal(w.Body.Bytes(), &rolled))
	if rolled.TaskID == task {
		t.Fatal("rollback reused original task")
	}
	w = request("POST", path+"/rollback", string(body), true, true)
	if w.Code != 202 || !strings.Contains(w.Body.String(), string(rolled.TaskID)) {
		t.Fatal("rollback replay drift")
	}
	rb := f.dispatch()[0]
	if rb.Kind != domain.Rollback || rb.TargetRevision <= c.TargetRevision || rb.TargetDigest == c.TargetDigest {
		t.Fatal("rollback reused old target")
	}
	clear(f.artifact(rb))
	f.record(f.result(rb, 1, true))
	queued := f.submit("cancel", f.plan(f.target(node, 3, l)))
	canceled, err := f.s.action(f.ctx, queued, "cancel", mutation("cancel", 1))
	must(t, err)
	if canceled.TaskID != queued || f.row(queued).State != "cancelled" || len(f.dispatch()) != 0 {
		t.Fatal("cancel did not persist")
	}
	if again, err := f.s.action(f.ctx, queued, "cancel", mutation("cancel", 1)); err != nil || again != canceled {
		t.Fatal("cancel replay", err)
	}
	retry, err := f.s.action(f.ctx, queued, "retry", mutation("retry", 2))
	must(t, err)
	newCommand := f.dispatch()[0]
	if retry.TaskID == queued || newCommand.TargetRevision != 5 {
		t.Fatal("retry reused old target")
	}
	newKey, err := f.s.Resolve(f.ctx, node, retry.TaskID)
	must(t, err)
	_, err = db.Exec("UPDATE nodes SET removed_at=?,revocation_epoch=2 WHERE id=?", stamp(f.now), node)
	must(t, err)
	if out, err := f.s.Open(f.ctx, node, newKey); out != nil || err == nil {
		t.Fatal("removed node recovered artifact")
	}
}

func TestFixedReservationChecksBothFamiliesAndTransports(t *testing.T) {
	f := fixtureFor(t)
	node := f.node()
	const port = 24101
	v4 := f.listener(node, port, "0.0.0.0")
	v6 := f.listener(node, port, "2001:db8::1")
	udp := f.listener(node, port, "::")
	udp.Protocol, udp.Transport, udp.Cipher = domain.Hysteria2, "udp", ""
	udp.Certificate = &control.LocalMaterial{ID: idFor(t, "cert"), Purpose: "tls_certificate"}
	udp.PrivateKey = &control.LocalMaterial{ID: idFor(t, "material"), Purpose: "tls_private_key"}
	f.submit("three-bindings", f.plan(f.target(node, 1, v4, v6, udp)))
	overlap := f.listener(node, port, "::")
	_, err := f.s.Submit(f.ctx, mutation("conflicting-fixed", 1), f.plan(f.target(node, 2, overlap)))
	code(t, err, domain.PortInUse)
	db := f.raw()
	var n int
	must(t, db.QueryRow("SELECT count(*) FROM b04_ports WHERE node_id=? AND port=?", node, port).Scan(&n))
	if n != 3 {
		t.Fatal("failed fixed-port task changed reservations")
	}
	must(t, db.QueryRow("SELECT count(*) FROM b04_ports WHERE node_id=? AND port!=?", node, port).Scan(&n))
	if n != 0 {
		t.Fatal("fixed port silently changed")
	}
	must(t, db.QueryRow("SELECT count(*) FROM b04_references WHERE kind='certificate'").Scan(&n))
	if n != 2 {
		t.Fatal("material references missing from strongly typed listener")
	}
}

func TestRemovalKeepsOfflineLinksAndCleansAuthorizedPeer(t *testing.T) {
	f := fixtureFor(t)
	gone, peer := f.node(), f.node()
	lg, lp := f.listener(gone, 24201, "::"), f.listener(peer, 24202, "::")
	f.submit("nodes", f.plan(f.target(gone, 1, lg), f.target(peer, 1, lp)))
	for _, c := range f.dispatch() {
		clear(f.artifact(c))
		f.record(f.result(c, 1, true))
	}
	db := f.raw()
	exec := func(query string, args ...any) {
		t.Helper()
		_, err := db.Exec(query, args...)
		must(t, err)
	}
	digest := strings.Repeat("a", 64)
	for _, l := range []control.BundleListener{lg, lp} {
		sealed, err := f.cipher.Seal(f.ctx, secretstore.Binding{Ref: l.Credential.Ref, Purpose: secretstore.ProxyEntry}, []byte(l.Credential.Password))
		must(t, err)
		exec("INSERT INTO credentials(id,purpose,key_id,ciphertext,created_at) VALUES (?,'proxy_entry',?,?,?)",
			l.Credential.Ref, sealed.KeyID, sealed.Ciphertext, stamp(f.now))
		exec("INSERT INTO node_dns_revisions(node_id,revision,outbound,digest) VALUES (?,1,'direct',?)", l.NodeID, digest)
		exec("INSERT INTO listeners(id,node_id,name,revision,protocol,listen_address,listen_port,outer_transport,credential_ref,config_digest,dns_revision,updated_at) VALUES (?,?,'test',1,'ss2022',?,?,'tcp',?,?,1,?)",
			l.ListenerID, l.NodeID, l.Address, l.Port, l.Credential.Ref, digest, stamp(f.now))
	}
	route := idFor(t, "route")
	exec("INSERT INTO routes(id,name,revision,updated_at) VALUES (?,'test',1,?)", route, stamp(f.now))
	exec("INSERT INTO route_revisions(route_id,revision,entry_listener_id,terminal_node_id,digest) VALUES (?,1,?,?,?)", route, lp.ListenerID, gone, digest)
	exec("UPDATE routes SET published_revision=1 WHERE id=?", route)
	exec("INSERT INTO route_hops(route_id,revision,position,node_id,listener_id) VALUES (?,1,0,?,?),(?,1,1,?,?)", route, peer, lp.ListenerID, route, gone, lg.ListenerID)
	auth, err := enrollment.New(enrollment.NewRepository(f.store), f.cipher, testGuard{}, enrollment.Options{
		ControllerURL: "https://controller.test", ServerNames: []string{"controller.test"},
		Now: func() time.Time { return f.now }, Context: f.ctx,
	})
	must(t, err)
	f.s.options.RemovalIntent = auth.RemovalIntent
	removed, err := auth.RemoveTask(f.ctx, gone, 2, "remove")
	must(t, err)
	f.connections[peer] = false
	f.now = f.now.Add(redelivery)
	if len(f.dispatch()) != 0 {
		t.Fatal("offline or removed node received work")
	}
	var child sql.NullString
	must(t, db.QueryRow("SELECT cleanup_task_id FROM b04_removal_links WHERE removal_task_id=? AND node_id=? AND listener_id=?",
		removed.TaskID, peer, lp.ListenerID).Scan(&child))
	if child.Valid {
		t.Fatal("offline peer started cleanup")
	}
	f.connections[peer] = true
	f.now = f.now.Add(redelivery)
	sent := f.dispatch()
	if len(sent) != 1 || sent[0].NodeID != peer || sent[0].Kind != domain.Cleanup || sent[0].TaskID == removed.TaskID {
		t.Fatal("removal did not create independent peer cleanup")
	}
	c := sent[0]
	data := f.artifact(c)
	var payload control.Bundle
	must(t, json.Unmarshal(data, &payload))
	clear(data)
	if len(payload.Listeners) != 0 || len(payload.Tombstones) != 1 || payload.Tombstones[0].IdentityID != lp.Authorization.IdentityID {
		t.Fatal("child did not pin exact removed peer listener")
	}
	r := f.result(c, 1, false)
	r.Cleanup = "pending"
	f.record(r)
	var still string
	must(t, db.QueryRow("SELECT state FROM b04_ports WHERE listener_id=?", lp.ListenerID).Scan(&still))
	if still != "pending_cleanup" {
		t.Fatal("non-confirmed result released peer port")
	}
	r.Sequence, r.Cleanup = 2, "confirmed"
	at := f.now
	r.ConfirmedAt = &at
	f.record(r)
	if f.row(c.TaskID).State != "succeeded" || f.row(removed.TaskID).State != "queued" {
		t.Fatal("peer cleanup forged removed node confirmation")
	}
	must(t, db.QueryRow("SELECT state FROM b04_ports WHERE listener_id=?", lp.ListenerID).Scan(&still))
	if still != "released" {
		t.Fatal("peer proof did not release peer port")
	}
	must(t, db.QueryRow("SELECT state FROM b04_ports WHERE listener_id=?", lg.ListenerID).Scan(&still))
	if still != "pending_cleanup" {
		t.Fatal("removed-node port was changed by peer proof")
	}
}

func TestSchedulerLifecycleRunsAfterConstruction(t *testing.T) {
	f := fixtureFor(t)
	node := f.node()
	f.submit("lifecycle", f.plan(f.target(node, 1, f.listener(node, 24021, "::1"))))
	if len(f.sent) != 0 {
		t.Fatal("delivery during construction or Submit")
	}
	sent := make(chan control.Command, 1)
	f.s.options.Deliver = func(_ context.Context, c control.Command) error { sent <- c; return nil }
	done := make(chan error, 1)
	go func() { done <- f.s.Run(f.ctx) }()
	select {
	case c := <-sent:
		if c.NodeID != node {
			t.Fatal("wrong scheduler target")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("scheduler did not dispatch")
	}
	must(t, f.s.Close())
	must(t, <-done)
}

func TestRollbackCannotResurrectConfirmedRevocation(t *testing.T) {
	f := fixtureFor(t)
	node := f.node()
	a, b := f.listener(node, 24051, "::"), f.listener(node, 24052, "::")
	originalNode := f.target(node, 1, a, b)
	original := f.submit("old", f.plan(originalNode))
	published := f.dispatch()[0]
	clear(f.artifact(published))
	f.record(f.result(published, 1, true))
	keep := f.target(node, 2, b)
	keep.Template.Tombstones = []control.Tombstone{{IdentityID: a.Authorization.IdentityID, Generation: 1, Epoch: 1, At: f.now, Reason: "revoked"}}
	revocation := f.plan(keep)
	revocation.Kind = domain.Revoke
	f.submit("revoke", revocation)
	removed := f.dispatch()[0]
	clear(f.artifact(removed))
	r := f.result(removed, 1, true)
	r.Cleanup = "confirmed"
	f.record(r)
	_, err := f.s.action(f.ctx, original, "rollback", mutation("recover-old-policy", domain.Revision(f.row(original).Revision)))
	must(t, err)
	recompiled := f.dispatch()[0]
	data := f.artifact(recompiled)
	defer clear(data)
	var bundle control.Bundle
	must(t, json.Unmarshal(data, &bundle))
	if len(bundle.Listeners) != 1 || bundle.Listeners[0].Authorization.IdentityID != b.Authorization.IdentityID {
		t.Fatal("rollback attempted to restore a revoked identity")
	}
	if bundle.Rules.Pin != originalNode.Template.Rules.Pin {
		t.Fatal("rollback did not retain previous content pin")
	}
}

func TestFailedRestoreRetainsLastConfirmedArtifactAndReservations(t *testing.T) {
	f := fixtureFor(t)
	node := f.node()
	old := f.listener(node, 24060, "127.0.0.1")
	next := f.listener(node, 24061, "127.0.0.1")
	first := f.submit("verified-startup", f.plan(f.target(node, 1, old)))
	c := f.dispatch()[0]
	clear(f.artifact(c))
	verified := f.result(c, 1, true)
	verified.EffectiveDigest = strings.Repeat("a", 64)
	f.record(verified)

	second := f.submit("failed-candidate", f.plan(f.target(node, 2, old, next)))
	failed := f.dispatch()[0]
	clear(f.artifact(failed))
	out := f.result(failed, 1, false)
	out.Verification, out.Rollback = domain.Failed, "failed"
	f.record(out)
	if f.row(first).State != "succeeded" || f.row(second).State != "failed" {
		t.Fatal("restore failure rewrote historical task evidence")
	}

	db := f.raw()
	var reconciliation, artifact, effective string
	must(t, db.QueryRow("SELECT reconciliation, artifact_digest, effective_digest FROM b04_node_state WHERE node_id=?", node).
		Scan(&reconciliation, &artifact, &effective))
	if reconciliation != "pending" || artifact != c.TargetDigest || effective != verified.EffectiveDigest {
		t.Fatal("failed restore replaced the previous evidence or was treated as current confirmation")
	}
	for _, listener := range []control.BundleListener{old, next} {
		var state string
		must(t, db.QueryRow("SELECT state FROM b04_ports WHERE node_id=? AND port=?", node, listener.Port).Scan(&state))
		if state == "released" {
			t.Fatalf("released port %d despite failed restore and unconfirmed cleanup", listener.Port)
		}
	}
}
