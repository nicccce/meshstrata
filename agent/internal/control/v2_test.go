package control

import (
	"context"
	"net/http"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestAgentV2ObservationWorkerKeepsSessionBinding(t *testing.T) {
	session, _ := domain.NewID("session")
	stale, _ := domain.NewID("session")
	target := controltest.Pin("target", "provisioned")
	request := protocol.ObservationRequestV2{ID: controltest.ID("observation"), Deadline: time.Now().Add(10 * time.Second), Target: &target}
	done := make(chan error, 1)
	var connections, collected atomic.Int32
	server, config := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if connections.Add(1) != 1 {
			w.WriteHeader(503)
			return
		}
		up := websocket.Upgrader{Subprotocols: []string{protocol.SubprotocolV2}}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		message := testMessage("welcome")
		message.Version = 2
		if err := conn.WriteJSON(protocol.SessionV2{Message: message, SessionID: session}); err != nil {
			done <- err
			return
		}
		if _, _, err := conn.ReadMessage(); err != nil {
			done <- err
			return
		}
		message = testMessage("observe")
		message.Version = 2
		envelope := protocol.SessionV2{Message: message, SessionID: session, ObservationRequest: &request}
		if err := conn.WriteJSON(envelope); err != nil {
			done <- err
			return
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			done <- err
			return
		}
		report, err := protocol.DecodeSessionV2(raw, testNode())
		if err != nil || report.ObservationReport == nil || report.ObservationReport.ID != request.ID || report.SessionID != session {
			done <- ErrUnavailable
			return
		}
		envelope.SessionID = stale
		if err := conn.WriteJSON(envelope); err != nil {
			done <- err
			return
		}
		if _, _, err := conn.ReadMessage(); err == nil {
			done <- ErrUnavailable
			return
		}
		done <- nil
	})
	o := clientOptions(server, config, &v2Runtime{fakeRuntime: &fakeRuntime{}})
	o.EnableV2 = true
	o.ObserveV2 = func(_ context.Context, req protocol.ObservationRequestV2) (protocol.ObservationReportV2, error) {
		collected.Add(1)
		now := time.Now().UTC()
		return protocol.ObservationReportV2{ID: req.ID, Egress: &protocol.EgressObservationV2{NodeID: testNode(), NodeRevision: 1, DNS: controltest.Chain()[2].DNS.Pin, Target: target, Interface: "lo", SourceIP: netip.MustParseAddr("127.0.0.1"), Probe: netip.MustParseAddrPort("127.0.0.1:12345"), ObservedAt: now, ExpiresAt: now.Add(time.Minute)}}, nil
	}
	client, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)
	defer client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("observation worker stalled")
	}
	if collected.Load() != 1 {
		t.Fatal("old session reached local collector")
	}
}

type v2Runtime struct{ *fakeRuntime }

func (r *v2Runtime) Hello(ctx context.Context) (protocol.Hello, error) {
	h, err := r.fakeRuntime.Hello(ctx)
	h.Runtime = true
	return h, err
}

func (*v2Runtime) RecoveryFormat(context.Context) (int, error) { return 2, nil }

func TestAgentV2WelcomesOnlyExplicitRecoveryAndRejectsStaleReceipt(t *testing.T) {
	runtime := &v2Runtime{fakeRuntime: &fakeRuntime{}}
	runtime.outbox = []protocol.Result{{TaskID: testTask(), Sequence: 1, NodeResult: domain.NodeResult{NodeID: testNode(), TargetRevision: 1, TargetDigest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Verification: domain.NotChecked, Cleanup: "none", Rollback: "none"}}}
	session, _ := domain.NewID("session")
	stale, _ := domain.NewID("session")
	done := make(chan error, 1)
	var connections atomic.Int32
	server, config := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if connections.Add(1) != 1 {
			http.Error(w, "retry stopped", 503)
			return
		}
		up := websocket.Upgrader{Subprotocols: []string{protocol.SubprotocolV2}}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(time.Second))
		m := testMessage("welcome")
		m.Version = 2
		if err := c.WriteJSON(protocol.SessionV2{Message: m, SessionID: session}); err != nil {
			done <- err
			return
		}
		_, raw, err := c.ReadMessage()
		if err != nil {
			done <- err
			return
		}
		hello, err := protocol.DecodeSessionV2(raw, testNode())
		if err != nil || hello.SessionID != session || hello.RecoveryFormat != 2 || hello.Type != "hello" || len(hello.Hello.PendingResults) != 1 {
			done <- ErrUnavailable
			return
		}
		m = testMessage("receipt")
		m.Version = 2
		m.Receipt = &protocol.Receipt{TaskID: testTask(), NodeID: testNode(), Sequence: 1}
		if err := c.WriteJSON(protocol.SessionV2{Message: m, SessionID: stale}); err != nil {
			done <- err
			return
		}
		_, _, err = c.ReadMessage()
		if err == nil {
			done <- ErrUnavailable
			return
		}
		done <- nil
	})
	o := clientOptions(server, config, runtime)
	o.EnableV2 = true
	client, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.Run(ctx)
	defer client.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("v2 client session timed out")
	}
	if runtime.acked.Load() != 0 {
		t.Fatal("stale receipt erased durable result")
	}
}

func TestAgentV2ArtifactIsBoundToCurrentSessionAcrossResponse(t *testing.T) {
	payload := []byte("private-format-two-source")
	var client *Client
	session, _ := domain.NewID("session")
	next, _ := domain.NewID("session")
	server, config := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Meshstrata-Session") != string(session) {
			http.Error(w, "missing session", 403)
			return
		}
		// Simulate a reconnect while an authenticated HTTP response is in flight.
		client.mu.Lock()
		client.session = next
		client.mu.Unlock()
		w.Write(payload)
	})
	o := clientOptions(server, config, &v2Runtime{fakeRuntime: &fakeRuntime{}})
	o.EnableV2 = true
	var err error
	client, err = New(o)
	if err != nil {
		t.Fatal(err)
	}
	key := protocol.ArtifactKey{TaskID: testTask(), NodeID: testNode(), TargetRevision: 2, TargetDigest: protocol.DigestBytes(payload)}
	if _, err := client.DownloadArtifact(context.Background(), key, 1024); err == nil {
		t.Fatal("download without current v2 session")
	}
	client.session = session
	if _, err := client.DownloadArtifact(context.Background(), key, 1024); err == nil {
		t.Fatal("old session response accepted after reconnect")
	}
}

func TestAgentV2CannotUseRuntimeFlagAsRecoveryCapability(t *testing.T) {
	var requests atomic.Int32
	server, config := testServer(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) })
	o := clientOptions(server, config, &fakeRuntime{})
	o.EnableV2 = true
	client, err := New(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.runOnce(context.Background(), func() {}); err == nil || requests.Load() != 0 {
		t.Fatal("offered v2 before checking actual recovery capability")
	}
}
