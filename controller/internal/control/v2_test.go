package control

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func (f *wsFixture) dialV2(t *testing.T) (*websocket.Conn, domain.ID) {
	t.Helper()
	d := websocket.Dialer{TLSClientConfig: f.config, Subprotocols: []string{protocol.SubprotocolV2}, HandshakeTimeout: time.Second}
	c, response, err := d.Dial("wss"+strings.TrimPrefix(f.http.URL, "https")+"/control/v1", nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if c.Subprotocol() != protocol.SubprotocolV2 {
		t.Fatal("wrong subprotocol")
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	_, raw, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	welcome, err := protocol.DecodeSessionV2(raw, f.materials.NodeID)
	if err != nil || welcome.Type != "welcome" {
		t.Fatal("missing authenticated welcome", err)
	}
	return c, welcome.SessionID
}
func (f *wsFixture) v2Envelope(kind string, session domain.ID) protocol.SessionV2 {
	m := f.envelope(kind)
	m.Version = 2
	return protocol.SessionV2{Message: m, SessionID: session}
}

func TestV2MTLSRecoveryReceiptAndReconnectSessionFence(t *testing.T) {
	f := wsSetup(t)
	f.control.o.EnableV2 = true
	f.control.o.RequireRuntime = true
	c, session := f.dialV2(t)
	if got, format := f.control.RecoverySession(f.materials.NodeID); got != "" || format != 0 {
		t.Fatal("advertised before recovery hello")
	}
	hello := f.v2Envelope("hello", session)
	hello.RecoveryFormat = 2
	hello.Hello = &protocol.Hello{NodeID: f.materials.NodeID, Runtime: true, RevocationEpoch: 1, PendingResults: []protocol.Result{f.result()}}
	if err := c.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	_, raw, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := protocol.DecodeSessionV2(raw, f.materials.NodeID)
	if err != nil || receipt.Type != "receipt" || receipt.SessionID != session || receipt.Receipt.Sequence != 1 {
		t.Fatal("pending recovery result not receipted", err)
	}
	deadline := time.Now().Add(time.Second)
	for !f.control.RuntimeConnected(f.materials.NodeID) {
		if time.Now().After(deadline) {
			t.Fatal("v2 peer not ready")
		}
		time.Sleep(time.Millisecond)
	}
	if got, format := f.control.RecoverySession(f.materials.NodeID); got != string(session) || format != 2 {
		t.Fatal("missing recovery capability")
	}
	command := protocol.Command{TaskID: f.task, NodeID: f.materials.NodeID, Kind: domain.QueryStatus, TargetRevision: 1, TargetDigest: strings.Repeat("a", 64), DependsOn: []domain.ID{}, IdempotencyKey: "v2-wire-test", Deadline: time.Now().Add(time.Minute), RevocationEpoch: 1}
	if err := f.control.Deliver(context.Background(), command); err == nil {
		t.Fatal("legacy dispatch crossed v2 session")
	}
	if err := f.control.DeliverV2(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	_, raw, err = c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	delivered, err := protocol.DecodeSessionV2(raw, f.materials.NodeID)
	if err != nil || delivered.Command == nil || delivered.SessionID != session {
		t.Fatal("v2 command binding", err)
	}
	replacement, fresh := f.dialV2(t)
	if fresh == session {
		t.Fatal("session reused on reconnect")
	}
	if got, _ := f.control.RecoverySession(f.materials.NodeID); got != "" {
		t.Fatal("capability survived without new recovery hello")
	}
	forged := f.v2Envelope("hello", session)
	forged.RecoveryFormat = 2
	forged.Hello = hello.Hello
	if err := replacement.WriteJSON(forged); err != nil {
		t.Fatal(err)
	}
	if _, _, err := replacement.ReadMessage(); err == nil {
		t.Fatal("new socket accepted stale session")
	}
	if f.sink.inserted.Load() != 1 {
		t.Fatal("stale session mutated results")
	}
}

func TestV2IsExplicitlyDisabledByDefault(t *testing.T) {
	f := wsSetup(t)
	d := websocket.Dialer{TLSClientConfig: f.config, Subprotocols: []string{protocol.SubprotocolV2}, HandshakeTimeout: time.Second}
	c, response, err := d.Dial("wss"+strings.TrimPrefix(f.http.URL, "https")+"/control/v1", nil)
	if c != nil {
		c.Close()
	}
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err == nil {
		t.Fatal("unfinished production v2 enabled")
	}
}
