package enrollment

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

// These test-only boundaries deliberately do not import B01 implementation.
type testCipher struct {
	key  []byte
	fail bool
}

func (c *testCipher) Seal(_ context.Context, b secretstore.Binding, p []byte) (secretstore.Sealed, error) {
	if c.fail {
		return secretstore.Sealed{}, secretstore.ErrKeyUnavailable
	}
	block, _ := aes.NewCipher(c.key)
	a, _ := cipher.NewGCM(block)
	nonce := make([]byte, a.NonceSize())
	rand.Read(nonce)
	return secretstore.Sealed{KeyID: "test-v1", Ciphertext: a.Seal(nonce, nonce, p, []byte(string(b.Ref)+":"+string(b.Purpose)))}, nil
}
func (c *testCipher) Open(_ context.Context, b secretstore.Binding, s secretstore.Sealed) ([]byte, error) {
	if c.fail {
		return nil, secretstore.ErrDecrypt
	}
	block, _ := aes.NewCipher(c.key)
	a, _ := cipher.NewGCM(block)
	if len(s.Ciphertext) < a.NonceSize() {
		return nil, secretstore.ErrDecrypt
	}
	p, e := a.Open(nil, s.Ciphertext[:a.NonceSize()], s.Ciphertext[a.NonceSize():], []byte(string(b.Ref)+":"+string(b.Purpose)))
	if e != nil {
		return nil, secretstore.ErrDecrypt
	}
	return p, nil
}

type testGuard struct {
	deny  bool
	calls atomic.Int32
}

func (g *testGuard) Read(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.calls.Add(1)
		if g.deny {
			failure(w, problem(domain.Unauthenticated))
			return
		}
		h.ServeHTTP(w, r)
	})
}
func (g *testGuard) Write(h http.Handler) http.Handler { return g.Read(h) }

type failingDB struct {
	storage.Database
	fail     atomic.Bool
	failRead atomic.Bool
}

var errTransaction = errors.New("injected rollback")

func (d *failingDB) Read(ctx context.Context, fn func(*dbgen.Queries) error) error {
	if d.failRead.Load() {
		return errTransaction
	}
	return d.Database.Read(ctx, fn)
}

func (d *failingDB) Write(ctx context.Context, fn func(*dbgen.Queries) error) error {
	return d.Database.Write(ctx, func(q *dbgen.Queries) error {
		if e := fn(q); e != nil {
			return e
		}
		if d.fail.Load() {
			return errTransaction
		}
		return nil
	})
}

type fixture struct {
	s       *Service
	db      *failingDB
	store   *storage.Store
	cipher  *testCipher
	guard   *testGuard
	now     atomic.Int64
	removed atomic.Int32
}

func setup(t *testing.T) *fixture {
	t.Helper()
	store, e := storage.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { store.Close() })
	f := &fixture{store: store, cipher: &testCipher{key: bytes.Repeat([]byte{7}, 32)}, guard: &testGuard{}}
	f.now.Store(time.Now().Unix())
	f.db = &failingDB{Database: store}
	s, e := New(NewRepository(f.db), f.cipher, f.guard, Options{ControllerURL: "https://localhost:9443", ServerNames: []string{"localhost", "127.0.0.1"}, Now: func() time.Time { return time.Unix(f.now.Load(), 0) }, OnIdentityChange: func(domain.ID) { f.removed.Add(1) }})
	if e != nil {
		t.Fatal(e)
	}
	f.s = s
	return f
}
func issue(t *testing.T, f *fixture, key string) Materials {
	t.Helper()
	m, e := f.s.Issue(context.Background(), "", IssueRequest{ExpectedRevision: 0, IdempotencyKey: key, Name: "Test node", Mode: "direct", Role: "node", ControlAddress: "controller.example:9443", ProxyAddress: "node.example"})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func csr(t *testing.T) ([]byte, []byte) {
	t.Helper()
	key, csr, e := pki.NewKeyCSR()
	if e != nil {
		t.Fatal(e)
	}
	return key, csr
}
func code(t *testing.T, e error, want domain.ErrorCode) {
	t.Helper()
	var p *domain.Problem
	if !errors.As(e, &p) || p.Code != want {
		t.Fatalf("got %v want %s", e, want)
	}
}
func certificate(t *testing.T, id Identity) *x509.Certificate {
	t.Helper()
	c, e := pki.ParseCertificate([]byte(id.CertificatePEM))
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestTokenAtomicConcurrencyReplayAndExpiry(t *testing.T) {
	f := setup(t)
	m := issue(t, f, "create")
	_, request := csr(t)
	var wg sync.WaitGroup
	results := make(chan Identity, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, e := f.s.Enroll(context.Background(), m.Token, request)
			results <- id
			errs <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	cert := ""
	for id := range results {
		if id.NodeID != m.NodeID {
			t.Fatal("node drift")
		}
		if cert == "" {
			cert = id.CertificatePEM
		}
		if id.CertificatePEM != cert {
			t.Fatal("duplicate certificate issued")
		}
	}
	if f.removed.Load() != 1 {
		t.Fatal("idempotent enrollment retriggered identity change")
	}
	_, other := csr(t)
	_, e := f.s.Enroll(context.Background(), m.Token, other)
	code(t, e, domain.IdempotencyConflict)
	// A response lost after commit can be recovered with the same CSR, even after
	// token expiry. Revocation and certificate expiry still take precedence.
	f.now.Add(16 * 60)
	id, e := f.s.Enroll(context.Background(), m.Token, request)
	if e != nil || id.CertificatePEM != cert {
		t.Fatal("lost response retry", e)
	}
	expired := issue(t, f, "expire")
	f.now.Add(15 * 60)
	_, e = f.s.Enroll(context.Background(), expired.Token, request)
	code(t, e, domain.TaskExpired)
	status, e := f.s.Status(context.Background(), expired.NodeID)
	if e != nil || status.State != "expired" {
		t.Fatal(status, e)
	}
}
func TestRollbackCannotHalfEnrollAndIssueIsIdempotent(t *testing.T) {
	f := setup(t)
	m := issue(t, f, "create")
	again := issue(t, f, "create")
	if again.NodeID != m.NodeID || again.Token != m.Token {
		t.Fatal("create replay drift")
	}
	_, request := csr(t)
	f.db.fail.Store(true)
	if _, e := f.s.Enroll(context.Background(), m.Token, request); !errors.Is(e, errTransaction) {
		t.Fatal(e)
	}
	status, e := f.s.Status(context.Background(), m.NodeID)
	if e != nil || status.State != "pending" {
		t.Fatal("partial enrollment", status, e)
	}
	f.db.fail.Store(false)
	if _, e = f.s.Enroll(context.Background(), m.Token, request); e != nil {
		t.Fatal(e)
	}
	f.db.fail.Store(true)
	if _, e = f.s.Remove(context.Background(), m.NodeID, m.Revision, "remove"); !errors.Is(e, errTransaction) {
		t.Fatal(e)
	}
	if _, e = f.s.Get(context.Background(), m.NodeID); e != nil {
		t.Fatal("partial remove", e)
	}
}
func TestCARestartFailureDoesNotReplaceAndRenewRevoke(t *testing.T) {
	f := setup(t)
	before := f.s.CAPEM()
	opts := f.s.options
	again, e := New(NewRepository(f.db), f.cipher, f.guard, opts)
	if e != nil || !bytes.Equal(before, again.CAPEM()) {
		t.Fatal("CA replaced", e)
	}
	f.cipher.fail = true
	if _, e = New(NewRepository(f.db), f.cipher, f.guard, opts); !errors.Is(e, secretstore.ErrDecrypt) {
		t.Fatal(e)
	}
	f.cipher.fail = false
	again, e = New(NewRepository(f.db), f.cipher, f.guard, opts)
	if e != nil || !bytes.Equal(before, again.CAPEM()) {
		t.Fatal("CA lost after missing key")
	}
	m := issue(t, f, "renew")
	_, request := csr(t)
	id, e := f.s.Enroll(context.Background(), m.Token, request)
	if e != nil {
		t.Fatal(e)
	}
	old := certificate(t, id)
	f.now.Add(86400)
	renewed, e := f.s.Renew(context.Background(), old, request)
	if e != nil || renewed.NodeID != m.NodeID {
		t.Fatal(e)
	}
	retry, e := f.s.Renew(context.Background(), old, request)
	if e != nil || retry.CertificatePEM != renewed.CertificatePEM {
		t.Fatal("renew replay", e)
	}
	if _, e = f.s.Authenticate(context.Background(), certificate(t, renewed)); e != nil {
		t.Fatal(e)
	}
	beforeRevision := m.Revision
	node, e := f.s.Get(context.Background(), m.NodeID)
	if e != nil || node.Revision != beforeRevision {
		t.Fatal("renew changed revision", e)
	}
	if _, e = f.s.Remove(context.Background(), m.NodeID, node.Revision, "remove"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Remove(context.Background(), m.NodeID, node.Revision, "remove"); e != nil {
		t.Fatal("remove not idempotent", e)
	}
	if _, e = f.s.Authenticate(context.Background(), certificate(t, renewed)); e == nil {
		t.Fatal("removed node accepted")
	}
	if _, e = f.s.Renew(context.Background(), old, request); e == nil {
		t.Fatal("revoked renewed")
	}
	if _, e = f.s.Enroll(context.Background(), m.Token, request); e == nil {
		t.Fatal("revoked replay accepted")
	}
}
func TestManagementGuardRedactionAndCAS(t *testing.T) {
	f := setup(t)
	f.guard.deny = true
	r := httptest.NewRequest("POST", "https://controller/api/v1/nodes", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	f.s.ManagementHandler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal(w.Code)
	}
	f.guard.deny = false
	m := issue(t, f, "guard")
	n, e := f.s.Rename(context.Background(), m.NodeID, m.Revision, "rename", "Renamed")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Rename(context.Background(), m.NodeID, m.Revision, "different", "Wrong"); e == nil {
		t.Fatal("CAS absent")
	}
	if _, e = f.s.Rename(context.Background(), m.NodeID, m.Revision, "rename", "Renamed"); e != nil {
		t.Fatal("rename replay", e)
	}
	if _, e = f.s.Rename(context.Background(), m.NodeID, n.Revision, "rename", "Changed"); e == nil {
		t.Fatal("key conflict absent")
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("GET", "https://controller/api/v1/nodes/"+string(m.NodeID)+"/enrollment", nil)
	f.s.ManagementHandler().ServeHTTP(w, r)
	if w.Code != 200 || strings.Contains(w.Body.String(), m.Token) || strings.Contains(w.Body.String(), "token_hash") || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("status leak", w.Code)
	}
	w = httptest.NewRecorder()
	r = httptest.NewRequest("POST", "http://controller/enroll/v1", strings.NewReader(`{}`))
	f.s.EnrollmentHandler().ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("plaintext enrollment allowed")
	}
}

func TestCertificateExpiryRevocationAndReenrollmentPreservesNode(t *testing.T) {
	f := setup(t)
	m := issue(t, f, "rejoin")
	_, request := csr(t)
	identity, e := f.s.Enroll(context.Background(), m.Token, request)
	if e != nil {
		t.Fatal(e)
	}
	cert := certificate(t, identity)
	revision, e := f.s.RevokeCertificates(context.Background(), m.NodeID, m.Revision, "cert-revoke")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.s.Authenticate(context.Background(), cert); e == nil {
		t.Fatal("revoked accepted")
	}
	fresh, e := f.s.Issue(context.Background(), m.NodeID, IssueRequest{ExpectedRevision: revision, IdempotencyKey: "new-token"})
	if e != nil {
		t.Fatal(e)
	}
	identity, e = f.s.Enroll(context.Background(), fresh.Token, request)
	if e != nil || identity.NodeID != m.NodeID {
		t.Fatal("rejoin node drift", e)
	}
	before := f.removed.Load()
	if _, e = f.s.RevokeCertificates(context.Background(), m.NodeID, m.Revision, "cert-revoke"); e != nil {
		t.Fatal(e)
	}
	if f.removed.Load() != before {
		t.Fatal("old revocation replay disconnected new identity")
	}
	cert = certificate(t, identity)
	f.now.Store(cert.NotAfter.Unix())
	if _, e = f.s.Authenticate(context.Background(), cert); e == nil {
		t.Fatal("expired certificate accepted")
	}
	if _, e = f.s.Renew(context.Background(), cert, request); e == nil {
		t.Fatal("expired certificate renewed")
	}
	fresh, e = f.s.Issue(context.Background(), m.NodeID, IssueRequest{ExpectedRevision: fresh.Revision, IdempotencyKey: "expired-rejoin"})
	if e != nil {
		t.Fatal(e)
	}
	identity, e = f.s.Enroll(context.Background(), fresh.Token, request)
	if e != nil || identity.NodeID != m.NodeID {
		t.Fatal("expired rejoin failed", e)
	}
}
func TestFailedIssueDoesNotAllocateHalfNodeAndReverseRequiresRelayPolicy(t *testing.T) {
	f := setup(t)
	f.db.fail.Store(true)
	_, e := f.s.Issue(context.Background(), "", IssueRequest{IdempotencyKey: "create-fail", Name: "Test", Mode: "direct", Role: "node", ControlAddress: "controller:9443", ProxyAddress: "node.example"})
	if !errors.Is(e, errTransaction) {
		t.Fatal(e)
	}
	f.db.fail.Store(false)
	nodes, e := f.s.List(context.Background(), domain.ListQuery{Limit: 50})
	if e != nil || len(nodes.Items) != 0 {
		t.Fatal("half node", e)
	}
	relay, e := f.s.Issue(context.Background(), "", IssueRequest{IdempotencyKey: "relay", Name: "Relay", Mode: "direct", Role: "both", ControlAddress: "controller:9443", ProxyAddress: "relay.example"})
	if e != nil {
		t.Fatal(e)
	}
	req := IssueRequest{IdempotencyKey: "reverse", Name: "Reverse", Mode: "reverse_tunnel", Role: "node", ControlAddress: "controller:9443", RelayNodeID: relay.NodeID}
	_, e = f.s.Issue(context.Background(), "", req)
	code(t, e, domain.NotImplemented)
	f.s.options.RelayAllowed = func(context.Context, domain.ID) bool { return true }
	reverse, e := f.s.Issue(context.Background(), "", req)
	if e != nil {
		t.Fatal(e)
	}
	status, e := f.s.Status(context.Background(), reverse.NodeID)
	if e != nil || status.RelayNodeID == nil || *status.RelayNodeID != relay.NodeID {
		t.Fatal("reverse expectation lost", e)
	}
}

func TestHTTPRemovalHasAtomicRealTaskAndIdempotentReplay(t *testing.T) {
	f := setup(t)
	m := issue(t, f, "remove-api")
	handler := f.s.ManagementHandler()
	var task domain.TaskAccepted
	for range 2 {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "https://controller/api/v1/nodes/"+string(m.NodeID)+"/remove", strings.NewReader(`{"expected_revision":1,"idempotency_key":"remove-task"}`))
		r.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		var got domain.TaskAccepted
		if json.Unmarshal(w.Body.Bytes(), &got) != nil || !got.TaskID.Valid() {
			t.Fatal("missing real task")
		}
		if task.TaskID != "" && task.TaskID != got.TaskID {
			t.Fatal("remove task replay drift")
		}
		task = got
	}
	intent, e := f.s.RemovalIntent(context.Background(), task.TaskID)
	if e != nil || intent.NodeID != m.NodeID || intent.Revision != 2 {
		t.Fatal("removal intent", intent, e)
	}
	e = f.store.Read(context.Background(), func(q *dbgen.Queries) error {
		saved, e := q.GetTask(context.Background(), string(task.TaskID))
		if e != nil {
			return e
		}
		if saved.State != "queued" {
			t.Fatal("task faked success")
		}
		target, e := q.GetTaskNode(context.Background(), dbgen.GetTaskNodeParams{TaskID: string(task.TaskID), NodeID: string(m.NodeID)})
		if e != nil {
			return e
		}
		if target.Applied != 0 || target.Verification != "not_checked" || target.CleanupState != "pending" || target.ResultSequence != 0 {
			t.Fatal("invented evidence")
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

func TestCertificateRevokeHTTPIsGuardedAndCAS(t *testing.T) {
	f := setup(t)
	m := issue(t, f, "revoke-http")
	_, csr := csr(t)
	identity, e := f.s.Enroll(context.Background(), m.Token, csr)
	if e != nil {
		t.Fatal(e)
	}
	handler := f.s.ManagementHandler()
	request := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "https://controller/api/v1/nodes/"+string(m.NodeID)+"/certificates/revoke", strings.NewReader(`{"expected_revision":1,"idempotency_key":"revoke-http"}`))
		r.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(w, r)
		return w
	}
	f.guard.deny = true
	if w := request(); w.Code != 401 {
		t.Fatal(w.Code)
	}
	if _, e = f.s.Authenticate(context.Background(), certificate(t, identity)); e != nil {
		t.Fatal("guard failure reached business")
	}
	f.guard.deny = false
	if w := request(); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e = f.s.Authenticate(context.Background(), certificate(t, identity)); e == nil {
		t.Fatal("HTTP revoke ineffective")
	}
}

func TestReadFailuresPreserveCauseAndDoNotBecomeBadToken(t *testing.T) {
	f := setup(t)
	m := issue(t, f, "read-failure")
	_, csr := csr(t)
	f.db.failRead.Store(true)
	if _, e := f.s.Enroll(context.Background(), m.Token, csr); !errors.Is(e, errTransaction) {
		t.Fatal("read cause lost", e)
	}
	f.db.failRead.Store(false)
	status, e := f.s.Status(context.Background(), m.NodeID)
	if e != nil || status.State != "pending" {
		t.Fatal("read failure consumed token", e)
	}
}
