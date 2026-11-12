package control

import (
	"context"
	"crypto/tls"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestControlOnlyPersistentPristineAndUnknownState(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	c, e := OpenControlOnly(dir, testNode())
	if e != nil {
		t.Fatal(e)
	}
	h, e := c.Hello(context.Background())
	if e != nil || h.AppliedRevision != 0 || len(h.PendingResults) != 0 || h.RevocationEpoch != 1 {
		t.Fatal(h, e)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "stage1-control.json"))
	if _, e = OpenControlOnly(dir, testNode()); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "stage1-control.json"))
	if string(before) != string(after) {
		t.Fatal("state changed on restart")
	}
	if _, e = OpenControlOnly(dir, domain.ID("node_"+strings.Repeat("8", 32))); e == nil {
		t.Fatal("node identity changed")
	}
	os.WriteFile(filepath.Join(dir, "checkpoint.json"), []byte(`{"applied":1}`), 0600)
	if _, e = c.Hello(context.Background()); e == nil {
		t.Fatal("unknown business state accepted")
	}
	fresh := t.TempDir()
	os.WriteFile(filepath.Join(fresh, "outbox"), []byte("pending"), 0600)
	if _, e = OpenControlOnly(fresh, testNode()); e == nil {
		t.Fatal("unknown state initialized")
	}
	if _, e = os.Stat(filepath.Join(fresh, "stage1-control.json")); !os.IsNotExist(e) {
		t.Fatal("created pristine marker over unknown state")
	}
	os.WriteFile(filepath.Join(dir, "stage1-control.json"), []byte(`{"version":1,"mode":"control_only","applied_revision":2}`), 0600)
	if _, e = OpenControlOnly(dir, testNode()); e == nil {
		t.Fatal("nonpristine accepted")
	}
}

func TestControlOnlyRejectsCommandAndUnmatchedReceiptWithoutResult(t *testing.T) {
	for _, typ := range []string{"command", "receipt"} {
		t.Run(typ, func(t *testing.T) {
			dir := t.TempDir()
			os.Chmod(dir, 0700)
			source, e := OpenControlOnly(dir, testNode())
			if e != nil {
				t.Fatal(e)
			}
			observed := make(chan error, 1)
			server, trust := testServer(t, func(w http.ResponseWriter, r *http.Request) {
				conn, e := (&websocket.Upgrader{Subprotocols: []string{Subprotocol}}).Upgrade(w, r, nil)
				if e != nil {
					return
				}
				defer conn.Close()
				conn.SetReadDeadline(time.Now().Add(3 * time.Second))
				_, raw, e := conn.ReadMessage()
				if e != nil {
					observed <- e
					return
				}
				m, e := protocol.Decode(raw, testNode())
				if e != nil || m.Type != "hello" {
					observed <- ErrUnavailable
					return
				}
				message := testMessage(typ)
				if typ == "command" {
					message.Command = &protocol.Command{TaskID: testTask(), NodeID: testNode(), Kind: domain.Publish, TargetRevision: 1, TargetDigest: strings.Repeat("a", 64), IdempotencyKey: "unexpected", Deadline: time.Now().Add(time.Minute), DependsOn: []domain.ID{}, RevocationEpoch: 1}
				} else {
					message.Receipt = &protocol.Receipt{TaskID: testTask(), NodeID: testNode(), Sequence: 1}
				}
				if e = conn.WriteJSON(message); e != nil {
					observed <- e
					return
				}
				_, _, e = conn.ReadMessage()
				if websocket.IsCloseError(e, websocket.ClosePolicyViolation) {
					observed <- nil
				} else {
					observed <- ErrUnavailable
				}
			})
			c, e := New(Options{URL: "wss" + strings.TrimPrefix(server.URL, "https") + "/control/v1", NodeID: testNode(), TLSConfig: func() (*tls.Config, error) { return trust, nil }, ControlOnly: source})
			if e != nil {
				t.Fatal(e)
			}
			if e = c.runOnce(context.Background()); e == nil {
				t.Fatal("unexpected message accepted")
			}
			select {
			case e = <-observed:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(4 * time.Second):
				t.Fatal("no explicit rejection")
			}
			h, e := source.Hello(context.Background())
			if e != nil || h.AppliedRevision != 0 || len(h.PendingResults) != 0 {
				t.Fatal("business result fabricated", h, e)
			}
		})
	}
}
