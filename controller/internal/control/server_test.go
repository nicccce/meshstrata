package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nicccce/meshstrata/controller/internal/enrollment"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

type fakeCipher struct{}

func (fakeCipher) Seal(_ context.Context, _ secretstore.Binding, p []byte) (secretstore.Sealed, error) {
	return secretstore.Sealed{KeyID: "fake", Ciphertext: append([]byte(nil), p...)}, nil
}
func (fakeCipher) Open(_ context.Context, _ secretstore.Binding, s secretstore.Sealed) ([]byte, error) {
	return append([]byte(nil), s.Ciphertext...), nil
}

type fakeGuard struct{}

func (fakeGuard) Read(h http.Handler) http.Handler  { return h }
func (fakeGuard) Write(h http.Handler) http.Handler { return h }

type resultSink struct {
	store    *storage.Store
	fail     atomic.Bool
	inserted atomic.Int32
}

func (s *resultSink) RecordResult(ctx context.Context, node domain.ID, r protocol.Result) (bool, error) {
	if s.fail.Load() {
		return false, errors.New("injected persistence failure")
	}
	changed, e := s.store.RecordResult(ctx, node, r)
	if changed && e == nil {
		s.inserted.Add(1)
	}
	return changed, e
}

type wsFixture struct {
	service   *enrollment.Service
	control   *Server
	http      *httptest.Server
	pair      tls.Certificate
	config    *tls.Config
	materials enrollment.Materials
	task      domain.ID
	store     *storage.Store
	sink      *resultSink
}

func wsSetup(t *testing.T) *wsFixture {
	t.Helper()
	f := &wsFixture{}
	db, e := storage.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	f.store = db
	t.Cleanup(func() { db.Close() })
	f.sink = &resultSink{store: db}
	f.service, e = enrollment.New(enrollment.NewRepository(db), fakeCipher{}, fakeGuard{}, enrollment.Options{ControllerURL: "https://localhost:9443", ServerNames: []string{"127.0.0.1", "localhost"}, OnIdentityChange: func(id domain.ID) {
		if f.control != nil {
			f.control.Disconnect(id)
		}
	}})
	if e != nil {
		t.Fatal(e)
	}
	f.materials, e = f.service.Issue(context.Background(), "", enrollment.IssueRequest{IdempotencyKey: "ws", Name: "WS node", Mode: "direct", Role: "node", ControlAddress: "controller:9443", ProxyAddress: "node.example"})
	if e != nil {
		t.Fatal(e)
	}
	key, csr, e := pki.NewKeyCSR()
	if e != nil {
		t.Fatal(e)
	}
	identity, e := f.service.Enroll(context.Background(), f.materials.Token, csr)
	if e != nil {
		t.Fatal(e)
	}
	f.pair, e = pki.ClientCertificate([]byte(identity.CertificatePEM), key, f.service.CAPEM(), identity.NodeID, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	clear(key)
	f.task, e = db.EnqueueTask(context.Background(), "b02-test", "task", storage.TaskRequest{Kind: domain.QueryStatus, Subject: domain.RevisionRef{ID: f.materials.NodeID, Revision: 1}, Deadline: time.Now().Add(time.Minute), Targets: []storage.TaskTarget{{NodeID: f.materials.NodeID, Revision: 1, Digest: strings.Repeat("a", 64), RevocationEpoch: 1}}}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	f.control, e = New(Options{Authenticator: f.service, Results: f.sink, PingInterval: 30 * time.Millisecond, PongTimeout: 500 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.control.Close() })
	server := httptest.NewUnstartedServer(f.service.NodeHandler(f.control.Handler()))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	config := f.service.TLSConfig()
	pair, e := config.GetCertificate(&tls.ClientHelloInfo{})
	if e != nil {
		t.Fatal(e)
	}
	config.Certificates = []tls.Certificate{*pair}
	server.TLS = config
	server.StartTLS()
	f.http = server
	t.Cleanup(server.Close)
	f.config, e = pki.Trust(f.service.CAPEM(), f.materials.CAFingerprint, "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	f.config.Certificates = []tls.Certificate{f.pair}
	return f
}
func (f *wsFixture) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	d := websocket.Dialer{TLSClientConfig: f.config, Subprotocols: []string{Subprotocol}, HandshakeTimeout: time.Second}
	conn, resp, e := d.Dial("wss"+strings.TrimPrefix(f.http.URL, "https")+"/control/v1", nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}
func (f *wsFixture) envelope(typ string) protocol.Message {
	id, _ := domain.NewID("msg")
	return protocol.Message{Version: 1, Type: typ, MessageID: id, NodeID: f.materials.NodeID, SentAt: time.Now()}
}
func (f *wsFixture) hello(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	m := f.envelope("hello")
	m.Hello = &protocol.Hello{NodeID: f.materials.NodeID, RevocationEpoch: 1, PendingResults: []protocol.Result{}}
	if e := conn.WriteJSON(m); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(time.Second)
	for !f.control.Connected(f.materials.NodeID) {
		if time.Now().After(deadline) {
			t.Fatal("hello not processed")
		}
		time.Sleep(time.Millisecond)
	}
}
func (f *wsFixture) result() protocol.Result {
	return protocol.Result{TaskID: f.task, NodeResult: domain.NodeResult{NodeID: f.materials.NodeID, TargetRevision: 1, TargetDigest: strings.Repeat("a", 64), Verification: domain.NotChecked, Rollback: "none", Cleanup: "none"}, Sequence: 1}
}
func TestRealMTLSWebSocketCommandResultReceiptReplayAndRemoval(t *testing.T) {
	f := wsSetup(t)
	conn := f.dial(t)
	if conn.Subprotocol() != Subprotocol {
		t.Fatal("subprotocol")
	}
	f.hello(t, conn)
	command := protocol.Command{TaskID: f.task, NodeID: f.materials.NodeID, Kind: domain.QueryStatus, TargetRevision: 1, TargetDigest: strings.Repeat("a", 64), IdempotencyKey: "cmd", Deadline: time.Now().Add(time.Minute), DependsOn: []domain.ID{}, RevocationEpoch: 1}
	if e := f.control.Deliver(context.Background(), command); e != nil {
		t.Fatal(e)
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	_, raw, e := conn.ReadMessage()
	if e != nil {
		t.Fatal(e)
	}
	m, e := protocol.Decode(raw, f.materials.NodeID)
	if e != nil || m.Type != "command" {
		t.Fatal(m.Type, e)
	}
	for range 2 {
		m = f.envelope("result")
		result := f.result()
		m.Result = &result
		if e = conn.WriteJSON(m); e != nil {
			t.Fatal(e)
		}
		_, raw, e = conn.ReadMessage()
		if e != nil {
			t.Fatal(e)
		}
		m, e = protocol.Decode(raw, f.materials.NodeID)
		if e != nil || m.Type != "receipt" || m.Receipt.Sequence != 1 {
			t.Fatal("receipt missing", e)
		}
	}
	if f.sink.inserted.Load() != 1 {
		t.Fatal("duplicate advanced persistence")
	}
	var saved dbgen.TaskNode
	e = f.store.Read(context.Background(), func(q *dbgen.Queries) error {
		var e error
		saved, e = q.GetTaskNode(context.Background(), dbgen.GetTaskNodeParams{TaskID: string(f.task), NodeID: string(f.materials.NodeID)})
		return e
	})
	if e != nil || saved.ResultSequence != 1 || saved.Applied != 0 || saved.Verification != "not_checked" {
		t.Fatal("receipt without exact durable result", e)
	}
	if _, e = f.service.Remove(context.Background(), f.materials.NodeID, f.materials.Revision, "remove"); e != nil {
		t.Fatal(e)
	}
	_, _, e = conn.ReadMessage()
	if e == nil {
		t.Fatal("old control not closed")
	}
	d := websocket.Dialer{TLSClientConfig: f.config, Subprotocols: []string{Subprotocol}, HandshakeTimeout: time.Second}
	c, _, e := d.Dial("wss"+strings.TrimPrefix(f.http.URL, "https")+"/control/v1", nil)
	if e == nil {
		c.Close()
		t.Fatal("removed reconnect accepted")
	}
}
func TestPersistenceFailureNeverReceipts(t *testing.T) {
	f := wsSetup(t)
	f.sink.fail.Store(true)
	conn := f.dial(t)
	f.hello(t, conn)
	m := f.envelope("result")
	r := f.result()
	m.Result = &r
	conn.WriteJSON(m)
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, data, e := conn.ReadMessage(); e == nil {
		t.Fatalf("unexpected response after failed commit: %d bytes", len(data))
	}
	if f.sink.inserted.Load() != 0 {
		t.Fatal("failure advanced")
	}
}
func TestWSRejectsIdentityDirectionLimitAndNoMTLS(t *testing.T) {
	for _, mode := range []string{"wrong-inner", "wrong-outer", "wrong-direction", "oversize", "binary"} {
		t.Run(mode, func(t *testing.T) {
			f := wsSetup(t)
			conn := f.dial(t)
			f.hello(t, conn)
			m := f.envelope("result")
			r := f.result()
			m.Result = &r
			other := domain.ID("node_" + strings.Repeat("f", 32))
			switch mode {
			case "wrong-inner":
				m.Result.NodeID = other
			case "wrong-outer":
				m.NodeID = other
			case "wrong-direction":
				m = f.envelope("receipt")
				m.Receipt = &protocol.Receipt{TaskID: f.task, NodeID: f.materials.NodeID, Sequence: 1}
			}
			if mode == "oversize" {
				conn.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("x"), protocol.MaxMessageBytes+1))
			} else if mode == "binary" {
				raw, _ := json.Marshal(m)
				conn.WriteMessage(websocket.BinaryMessage, raw)
			} else {
				conn.WriteJSON(m)
			}
			conn.SetReadDeadline(time.Now().Add(time.Second))
			if _, _, e := conn.ReadMessage(); e == nil {
				t.Fatal("invalid message accepted")
			}
			if f.sink.inserted.Load() != 0 {
				t.Fatal("invalid persisted")
			}
		})
	}
	f := wsSetup(t)
	for _, mode := range []string{"no-cert", "no-protocol", "bad-cert"} {
		config := f.config.Clone()
		subs := []string{Subprotocol}
		if mode == "no-cert" {
			config.Certificates = nil
		}
		if mode == "no-protocol" {
			subs = nil
		}
		if mode == "bad-cert" {
			ca, key, _ := pki.GenerateCA(time.Now())
			clear(key)
			nodeKey, csr, _ := pki.NewKeyCSR()
			pem, _ := ca.SignNode(csr, f.materials.NodeID, time.Now(), time.Hour)
			cert, _ := pki.ClientCertificate(pem, nodeKey, ca.PEM, f.materials.NodeID, time.Now())
			clear(nodeKey)
			config.Certificates = []tls.Certificate{cert}
		}
		d := websocket.Dialer{TLSClientConfig: config, Subprotocols: subs, HandshakeTimeout: time.Second}
		conn, resp, e := d.Dial("wss"+strings.TrimPrefix(f.http.URL, "https")+"/control/v1", nil)
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		if e == nil {
			conn.Close()
			t.Fatal("invalid handshake accepted", mode)
		}
	}
	// Artifacts remain unavailable without B04, even for an authenticated node.
	transport := &http.Transport{TLSClientConfig: f.config}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport}
	resp, e := client.Get(f.http.URL + "/control/v1/tasks/" + string(f.task) + "/bundle")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 501 {
		t.Fatal("missing artifact dependency faked", resp.StatusCode)
	}
}

type testArtifact struct {
	node   domain.ID
	key    protocol.ArtifactKey
	data   []byte
	opened atomic.Int32
}

func (a *testArtifact) Resolve(_ context.Context, node, task domain.ID) (protocol.ArtifactKey, error) {
	if node != a.node || task != a.key.TaskID {
		return protocol.ArtifactKey{}, ErrUnavailable
	}
	return a.key, nil
}
func (a *testArtifact) Open(_ context.Context, node domain.ID, key protocol.ArtifactKey) (io.ReadCloser, error) {
	if node != a.node || key != a.key {
		return nil, ErrUnavailable
	}
	a.opened.Add(1)
	return io.NopCloser(bytes.NewReader(a.data)), nil
}
func TestArtifactOnlyViaAuthenticatedNodeScopedSource(t *testing.T) {
	f := wsSetup(t)
	data := []byte("test-only-private-bundle")
	hash := sha256.Sum256(data)
	source := &testArtifact{node: f.materials.NodeID, key: protocol.ArtifactKey{TaskID: f.task, NodeID: f.materials.NodeID, TargetRevision: 1, TargetDigest: hex.EncodeToString(hash[:])}, data: data}
	f.control.o.Artifacts = source
	f.control.o.ArtifactResolver = source
	transport := &http.Transport{TLSClientConfig: f.config}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: time.Second}
	resp, e := client.Get(f.http.URL + "/control/v1/tasks/" + string(f.task) + "/bundle")
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil || resp.StatusCode != 200 || !bytes.Equal(got, data) || source.opened.Load() != 1 {
		t.Fatal("source transfer", resp.StatusCode, e)
	}
	noCert := f.config.Clone()
	noCert.Certificates = nil
	unauthenticated := &http.Transport{TLSClientConfig: noCert}
	defer unauthenticated.CloseIdleConnections()
	client.Transport = unauthenticated
	resp, e = client.Get(f.http.URL + "/control/v1/tasks/" + string(f.task) + "/bundle")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 || source.opened.Load() != 1 {
		t.Fatal("unauthenticated source access")
	}
}
