package control

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
)

func testNode() domain.ID { return domain.ID("node_" + strings.Repeat("2", 32)) }
func testTask() domain.ID { return domain.ID("task_" + strings.Repeat("3", 32)) }
func testMessage(typ string) protocol.Message {
	id, _ := domain.NewID("msg")
	return protocol.Message{Version: 1, Type: typ, NodeID: testNode(), MessageID: id, SentAt: time.Now()}
}

// Runtime here is only a transport boundary fake. B05's durable local database
// is intentionally not simulated or claimed by these tests.
type fakeRuntime struct {
	mu       sync.Mutex
	outbox   []protocol.Result
	executed atomic.Int32
	acked    atomic.Int32
	delay    time.Duration
	fail     bool
}

func (r *fakeRuntime) Hello(context.Context) (protocol.Hello, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	pending := append([]protocol.Result{}, r.outbox...)
	if len(pending) > 100 {
		pending = pending[:100]
	}
	return protocol.Hello{NodeID: testNode(), RevocationEpoch: 1, PendingResults: pending}, nil
}
func (r *fakeRuntime) Execute(ctx context.Context, c protocol.Command) (protocol.Result, error) {
	timer := time.NewTimer(r.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return protocol.Result{}, ctx.Err()
	case <-timer.C:
	}
	if r.fail {
		return protocol.Result{}, ErrUnavailable
	}
	result := protocol.Result{TaskID: c.TaskID, NodeResult: domain.NodeResult{NodeID: c.NodeID, TargetRevision: c.TargetRevision, TargetDigest: c.TargetDigest, Verification: domain.NotChecked, Rollback: "none", Cleanup: "none"}, Sequence: 1}
	r.mu.Lock()
	r.outbox = append(r.outbox, result)
	r.mu.Unlock()
	r.executed.Add(1)
	return result, nil
}
func (r *fakeRuntime) Acknowledge(_ context.Context, receipt protocol.Receipt) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, result := range r.outbox {
		if result.TaskID == receipt.TaskID && result.Sequence == receipt.Sequence {
			r.outbox = append(r.outbox[:i], r.outbox[i+1:]...)
			r.acked.Add(1)
			return nil
		}
	}
	return ErrUnavailable
}
func clientTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	ca, key, e := pki.GenerateCA(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	clear(key)
	pair, e := ca.ServerCertificate([]string{"127.0.0.1"}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	clientKey, csr, e := pki.NewKeyCSR()
	if e != nil {
		t.Fatal(e)
	}
	pem, e := ca.SignNode(csr, testNode(), time.Now(), time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	clientPair, e := pki.ClientCertificate(pem, clientKey, ca.PEM, testNode(), time.Now())
	clear(clientKey)
	if e != nil {
		t.Fatal(e)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Certificate)
	server := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool}
	client, e := pki.Trust(ca.PEM, pki.Fingerprint(ca.Certificate), "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	client.Certificates = []tls.Certificate{clientPair}
	return server, client
}
func testServer(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *tls.Config) {
	t.Helper()
	serverConfig, config := clientTLS(t)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = serverConfig
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, config
}
func clientOptions(server *httptest.Server, config *tls.Config, r Runtime) Options {
	return Options{URL: "wss" + strings.TrimPrefix(server.URL, "https") + "/control/v1", NodeID: testNode(), TLSConfig: func() (*tls.Config, error) { return config.Clone(), nil }, Runtime: r, MinBackoff: 20 * time.Millisecond, MaxBackoff: 50 * time.Millisecond, PingInterval: 20 * time.Millisecond, PongTimeout: 150 * time.Millisecond}
}
func TestAgentWSLongCommandKeepsPongAndReceiptsDurableOutbox(t *testing.T) {
	var serverErr atomic.Value
	finished := make(chan struct{})
	r := &fakeRuntime{delay: 400 * time.Millisecond}
	server, config := testServer(t, func(w http.ResponseWriter, req *http.Request) {
		up := websocket.Upgrader{Subprotocols: []string{Subprotocol}}
		conn, e := up.Upgrade(w, req, nil)
		if e != nil {
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, raw, e := conn.ReadMessage()
		if e != nil {
			serverErr.Store(e)
			return
		}
		hello, e := protocol.Decode(raw, testNode())
		if e != nil || hello.Type != "hello" {
			serverErr.Store(ErrUnavailable)
			return
		}
		ctx, cancel := context.WithCancel(req.Context())
		defer cancel()
		go func() {
			tick := time.NewTicker(20 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-tick.C:
					conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
				}
			}
		}()
		m := testMessage("command")
		m.Command = &protocol.Command{TaskID: testTask(), NodeID: testNode(), Kind: domain.QueryStatus, TargetRevision: 1, TargetDigest: strings.Repeat("a", 64), IdempotencyKey: "cmd", Deadline: time.Now().Add(2 * time.Second), DependsOn: []domain.ID{}, RevocationEpoch: 1}
		conn.WriteJSON(m)
		_, raw, e = conn.ReadMessage()
		if e != nil {
			serverErr.Store(e)
			return
		}
		result, e := protocol.Decode(raw, testNode())
		if e != nil || result.Type != "result" || r.executed.Load() != 1 {
			serverErr.Store(ErrUnavailable)
			return
		}
		receipt := testMessage("receipt")
		receipt.Receipt = &protocol.Receipt{TaskID: testTask(), NodeID: testNode(), Sequence: 1}
		conn.WriteJSON(receipt)
		deadline := time.Now().Add(time.Second)
		for r.acked.Load() != 1 {
			if time.Now().After(deadline) {
				serverErr.Store(ErrUnavailable)
				return
			}
			time.Sleep(time.Millisecond)
		}
		close(finished)
	})
	client, e := New(clientOptions(server, config, r))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		if e := serverErr.Load(); e != nil {
			t.Fatal(e)
		}
		t.Fatal("command did not finish")
	}
	cancel()
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close hung")
	}
	if r.executed.Load() != 1 || r.acked.Load() != 1 {
		t.Fatal("runtime boundary")
	}
}
func TestAgentReconnectAndCancelBackoff(t *testing.T) {
	var attempts atomic.Int32
	connected := make(chan struct{}, 1)
	server, config := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{Subprotocols: []string{Subprotocol}}
		conn, e := up.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer conn.Close()
		n := attempts.Add(1)
		_, _, e = conn.ReadMessage()
		if e != nil {
			return
		}
		if n == 1 {
			return
		}
		select {
		case connected <- struct{}{}:
		default:
		}
		for {
			if _, _, e = conn.ReadMessage(); e != nil {
				return
			}
		}
	})
	client, e := New(clientOptions(server, config, &fakeRuntime{}))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	select {
	case <-connected:
	case <-time.After(2 * time.Second):
		t.Fatal("no reconnect")
	}
	cancel()
	client.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancel did not stop connection")
	}
	// Failed dials enter jittered backoff, which Close must interrupt immediately.
	server.Close()
	o := clientOptions(server, config, &fakeRuntime{})
	o.MinBackoff = 10 * time.Second
	o.MaxBackoff = 10 * time.Second
	backoff, e := New(o)
	if e != nil {
		t.Fatal(e)
	}
	done = make(chan error, 1)
	go func() { done <- backoff.Run(context.Background()) }()
	time.Sleep(30 * time.Millisecond)
	start := time.Now()
	backoff.Close()
	if time.Since(start) > time.Second {
		t.Fatal("backoff not canceled")
	}
	<-done
}
func TestAgentRejectsDirectionIdentityOversizeAndMissingRuntime(t *testing.T) {
	for _, mode := range []string{"direction", "identity", "oversize", "unsupported-version"} {
		t.Run(mode, func(t *testing.T) {
			server, config := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				up := websocket.Upgrader{Subprotocols: []string{Subprotocol}}
				conn, e := up.Upgrade(w, r, nil)
				if e != nil {
					return
				}
				defer conn.Close()
				conn.ReadMessage()
				m := testMessage("hello")
				m.Hello = &protocol.Hello{NodeID: testNode(), RevocationEpoch: 1, PendingResults: []protocol.Result{}}
				if mode == "identity" {
					m.NodeID = domain.ID("node_" + strings.Repeat("f", 32))
				}
				if mode == "unsupported-version" {
					m.Version = 2
				}
				if mode == "oversize" {
					conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", protocol.MaxMessageBytes+1)))
				} else {
					conn.WriteJSON(m)
				}
				conn.SetReadDeadline(time.Now().Add(time.Second))
				conn.ReadMessage()
			})
			r := &fakeRuntime{}
			client, e := New(clientOptions(server, config, r))
			if e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if e = client.runOnce(ctx); e == nil {
				t.Fatal("invalid accepted")
			}
			if r.executed.Load() != 0 || r.acked.Load() != 0 {
				t.Fatal("invalid mutated runtime")
			}
			o := clientOptions(server, config, nil)
			if _, e = New(o); e == nil {
				t.Fatal("missing B05 hidden")
			}
		})
	}
}

func TestAgentHelloDrainsMoreThanOneHundredPendingResults(t *testing.T) {
	runtime := &fakeRuntime{}
	for range 120 {
		task, _ := domain.NewID("task")
		runtime.outbox = append(runtime.outbox, protocol.Result{TaskID: task, NodeResult: domain.NodeResult{NodeID: testNode(), TargetRevision: 1, TargetDigest: strings.Repeat("a", 64), Verification: domain.NotChecked, Rollback: "none", Cleanup: "none"}, Sequence: 1})
	}
	finished := make(chan struct{})
	server, config := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{Subprotocols: []string{Subprotocol}}
		conn, e := up.Upgrade(w, r, nil)
		if e != nil {
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		for batch := 0; batch < 2; batch++ {
			_, raw, e := conn.ReadMessage()
			if e != nil {
				return
			}
			m, e := protocol.Decode(raw, testNode())
			if e != nil || m.Type != "hello" || len(m.Hello.PendingResults) > 100 {
				return
			}
			for _, result := range m.Hello.PendingResults {
				receipt := testMessage("receipt")
				receipt.Receipt = &protocol.Receipt{TaskID: result.TaskID, NodeID: testNode(), Sequence: result.Sequence}
				if conn.WriteJSON(receipt) != nil {
					return
				}
			}
		}
		deadline := time.Now().Add(time.Second)
		for runtime.acked.Load() < 120 {
			if time.Now().After(deadline) {
				return
			}
			time.Sleep(time.Millisecond)
		}
		close(finished)
	})
	client, e := New(clientOptions(server, config, runtime))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Run(ctx) }()
	defer func() { cancel(); client.Close(); <-done }()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("outbox pagination did not drain")
	}
	if runtime.executed.Load() != 0 {
		t.Fatal("recovery executed commands")
	}
}
