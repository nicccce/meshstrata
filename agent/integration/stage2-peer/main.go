// Test-only mTLS observer for interruptions and replays at the transport boundary.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/nicccce/meshstrata/agent/internal/enrollment"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/privatefile"
)

type peer struct {
	tls *tls.Config
	url string
	id  domain.ID
}

func openPeer(path string) (peer, error) {
	cfg, err := config.LoadAgent(path)
	if err != nil || cfg.Control == nil {
		return peer{}, errors.New("test Agent configuration unavailable")
	}
	ca, err := privatefile.Read(cfg.Control.CAFile, 32<<10)
	if err != nil {
		return peer{}, err
	}
	identity, err := enrollment.Open(filepath.Join(cfg.StateDir, "identity"))
	if err != nil {
		return peer{}, err
	}
	id := domain.ID(cfg.Control.NodeID)
	c, err := enrollment.New(identity, enrollment.Options{
		ControllerURL: cfg.Control.ControllerURL, CAPEM: ca,
		CAFingerprint: cfg.Control.CAFingerprint, NodeID: id,
	})
	if err != nil {
		return peer{}, err
	}
	tc, err := c.TLSConfig()
	if err != nil {
		return peer{}, err
	}
	return peer{tc, strings.TrimSuffix(cfg.Control.ControllerURL, "/"), id}, nil
}

func (p peer) connect() (*websocket.Conn, error) {
	dial := websocket.Dialer{
		TLSClientConfig: p.tls, Subprotocols: []string{"meshstrata.control.v1"},
		HandshakeTimeout: 5 * time.Second,
	}
	conn, reply, err := dial.Dial("wss"+strings.TrimPrefix(p.url, "https")+"/control/v1", nil)
	if reply != nil && reply.Body != nil {
		reply.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(control.MaxMessageBytes)
	return conn, nil
}

func (p peer) send(conn *websocket.Conn, m control.Message) error {
	var err error
	m.Version, m.NodeID, m.SentAt = control.Version, p.id, time.Now().UTC()
	m.MessageID, err = domain.NewID("msg")
	if err != nil {
		return err
	}
	if err := conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	return conn.WriteJSON(m)
}

func (p peer) read(conn *websocket.Conn, wait time.Duration) (control.Message, error) {
	if err := conn.SetReadDeadline(time.Now().Add(wait)); err != nil {
		return control.Message{}, err
	}
	kind, raw, err := conn.ReadMessage()
	if err != nil {
		return control.Message{}, err
	}
	if kind != websocket.TextMessage {
		return control.Message{}, errors.New("unexpected control frame")
	}
	return control.Decode(raw, p.id)
}

func (p peer) request(task domain.ID, tlsConfig *tls.Config) (*http.Response, error) {
	tr := &http.Transport{TLSClientConfig: tlsConfig, DisableKeepAlives: true}
	defer tr.CloseIdleConnections()
	c := &http.Client{
		Transport: tr, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	ctx := context.Background()
	r, err := http.NewRequestWithContext(ctx, "GET", p.url+"/control/v1/tasks/"+string(task)+"/bundle", nil)
	if err != nil {
		return nil, err
	}
	return c.Do(r)
}

func (p peer) fetchDrop(conn *websocket.Conn, otherPath, wrongCAPath string) error {
	m, err := p.read(conn, 14*time.Second)
	if err != nil {
		return err
	}
	if m.Command == nil {
		return errors.New("expected armed command")
	}
	task := m.Command.TaskID
	if otherPath != "" {
		other, err := openPeer(otherPath)
		if err != nil {
			return err
		}
		resp, err := p.request(task, other.tls)
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			return fmt.Errorf("cross-node request returned %d", resp.StatusCode)
		}
	}
	if wrongCAPath != "" {
		root, err := privatefile.Read(wrongCAPath, 32<<10)
		if err != nil {
			return err
		}
		config := p.tls.Clone()
		config.RootCAs = x509.NewCertPool()
		if !config.RootCAs.AppendCertsFromPEM(root) {
			return errors.New("wrong-CA test fixture invalid")
		}
		if resp, err := p.request(task, config); err == nil {
			resp.Body.Close()
			return errors.New("artifact accepted unknown CA")
		} else {
			var unknown x509.UnknownAuthorityError
			if !errors.As(err, &unknown) {
				return err
			}
		}
	}
	resp, err := p.request(task, p.tls)
	if err != nil {
		return err
	}
	// The Controller has authorized Open. Drop the response without handing
	// any artifact bytes to the ordinary Agent or its durable grant ledger.
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("authorized artifact returned %d", resp.StatusCode)
	}
	return json.NewEncoder(os.Stdout).Encode(m.Command)
}

func (p peer) replay(conn *websocket.Conn, r control.Result, mode string) error {
	if mode == "conflict" {
		r.Applied = false
		r.Verification = domain.Incomplete
		r.EffectiveDigest = ""
	} else if mode == "wrong-digest" {
		r.Sequence += 1000
		r.TargetDigest = control.DigestBytes([]byte("wrong test digest"))
	}
	if err := p.send(conn, control.Message{Type: "result", Result: &r}); err != nil {
		return err
	}
	m, err := p.read(conn, 5*time.Second)
	if mode != "replay" {
		if websocket.IsUnexpectedCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
			return nil
		}
		return errors.New("expected rejection without receipt")
	}
	if err != nil {
		return err
	}
	if m.Receipt == nil || *m.Receipt != (control.Receipt{TaskID: r.TaskID, NodeID: p.id, Sequence: r.Sequence}) {
		return errors.New("wrong receipt for exact replay")
	}
	return nil
}

func (p peer) replayRestrictionPages(conn *websocket.Conn, r control.Result, events []control.Result) error {
	if len(events) != 2 || len(events[0].Restrictions) == 0 || len(events[1].Restrictions) == 0 ||
		events[0].Cleanup != "pending" || events[1].Cleanup != "confirmed" {
		return errors.New("expected prior pending and confirmed restrictions")
	}
	// Replay overlapping pages of genuine evidence. The production runtime's
	// distinct-result count and byte budgeting are covered by its own tests.
	for _, count := range []int{100, 4} {
		h := control.Hello{Runtime: true, NodeID: p.id, RevocationEpoch: 1,
			AppliedRevision: r.TargetRevision, AppliedDigest: r.TargetDigest,
			PendingResults: make([]control.Result, count)}
		for i := range h.PendingResults {
			h.PendingResults[i] = events[i%len(events)]
		}
		if err := p.send(conn, control.Message{Type: "hello", Hello: &h}); err != nil {
			return err
		}
		for _, result := range h.PendingResults {
			m, err := p.read(conn, 5*time.Second)
			if err != nil {
				return err
			}
			if m.Receipt == nil || *m.Receipt != (control.Receipt{TaskID: result.TaskID, NodeID: p.id, Sequence: result.Sequence}) {
				return errors.New("wrong receipt for restriction page replay")
			}
		}
	}
	return nil
}

func run(agentPath, other, wrongCA, mode string) error {
	p, err := openPeer(agentPath)
	if err != nil {
		return err
	}
	hello := control.Hello{Runtime: true, NodeID: p.id, RevocationEpoch: 1, PendingResults: []control.Result{}}
	var r control.Result
	var events []control.Result
	switch mode {
	case "fetch-drop":
	case "restriction-pages":
		var input struct {
			Checkpoint control.Result
			Events     []control.Result
		}
		if err := json.NewDecoder(io.LimitReader(os.Stdin, control.MaxMessageBytes)).Decode(&input); err != nil {
			return errors.New("missing historic restriction inputs")
		}
		r, events = input.Checkpoint, input.Events
		hello.AppliedRevision, hello.AppliedDigest = r.TargetRevision, r.TargetDigest
	case "replay", "conflict", "wrong-digest":
		if err := json.NewDecoder(io.LimitReader(os.Stdin, control.MaxMessageBytes)).Decode(&r); err != nil {
			return errors.New("missing historic result")
		}
		hello.AppliedRevision, hello.AppliedDigest = r.TargetRevision, r.TargetDigest
	default:
		return errors.New("unknown test action")
	}
	conn, err := p.connect()
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := p.send(conn, control.Message{Type: "hello", Hello: &hello}); err != nil {
		return err
	}
	if mode == "fetch-drop" {
		return p.fetchDrop(conn, other, wrongCA)
	}
	if mode == "restriction-pages" {
		return p.replayRestrictionPages(conn, r, events)
	}
	return p.replay(conn, r, mode)
}

func main() {
	agent := flag.String("config", "", "isolated enrolled Agent config")
	other := flag.String("other-config", "", "unrelated enrolled Agent config")
	ca := flag.String("wrong-ca", "", "isolated wrong CA fixture")
	mode := flag.String("mode", "", "fetch-drop, replay, conflict, restriction-pages or wrong-digest")
	flag.Parse()
	if err := run(*agent, *other, *ca, *mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
