// Package control transports authenticated v1 messages. B04 owns scheduling.
package control

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

const Subprotocol = "meshstrata.control.v1"

var ErrUnavailable = errors.New("control dependency unavailable")

type Authenticator interface {
	Authenticate(context.Context, *x509.Certificate) (domain.ID, error)
}
type ResultStore interface {
	RecordResult(context.Context, domain.ID, protocol.Result) (bool, error)
}

// Recovery consumes hello checkpoints after pending results have been committed.
// It must not infer Applied/Verified from the existence of a control connection.
type Recovery interface {
	Hello(context.Context, domain.ID, protocol.Hello) error
}
type ArtifactResolver interface {
	Resolve(context.Context, domain.ID, domain.ID) (protocol.ArtifactKey, error)
}
type Options struct {
	Authenticator    Authenticator
	Results          ResultStore
	Recovery         Recovery
	Artifacts        protocol.ArtifactSource
	ArtifactResolver ArtifactResolver
	Now              func() time.Time
	PingInterval     time.Duration
	PongTimeout      time.Duration
	WriteTimeout     time.Duration
}
type Server struct {
	o      Options
	mu     sync.Mutex
	peers  map[domain.ID]*peer
	closed bool
	wg     sync.WaitGroup
}
type peer struct {
	ready  atomic.Bool
	conn   *websocket.Conn
	cert   *x509.Certificate
	ctx    context.Context
	cancel context.CancelFunc
	write  sync.Mutex
}

func New(o Options) (*Server, error) {
	if o.Authenticator == nil {
		return nil, ErrUnavailable
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.PingInterval == 0 {
		o.PingInterval = 20 * time.Second
	}
	if o.PongTimeout == 0 {
		o.PongTimeout = 60 * time.Second
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.PingInterval <= 0 || o.PongTimeout <= o.PingInterval || o.WriteTimeout <= 0 {
		return nil, ErrUnavailable
	}
	return &Server{o: o, peers: map[domain.ID]*peer{}}, nil
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /control/v1", s.upgrade)
	mux.HandleFunc("GET /control/v1/tasks/{task_id}/bundle", s.artifact)
	return mux
}
func reply(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code}})
}
func (s *Server) authenticate(r *http.Request) (domain.ID, *x509.Certificate, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
		return "", nil, ErrUnavailable
	}
	c := r.TLS.PeerCertificates[0]
	id, e := s.o.Authenticator.Authenticate(r.Context(), c)
	return id, c, e
}
func (s *Server) upgrade(w http.ResponseWriter, r *http.Request) {
	id, cert, e := s.authenticate(r)
	if e != nil {
		reply(w, 401, "UNAUTHENTICATED")
		return
	}
	supported := false
	for _, v := range websocket.Subprotocols(r) {
		if v == Subprotocol {
			supported = true
		}
	}
	if !supported || r.Header.Get("Origin") != "" {
		reply(w, 400, "INVALID_ARGUMENT")
		return
	}
	up := websocket.Upgrader{Subprotocols: []string{Subprotocol}, ReadBufferSize: 4096, WriteBufferSize: 4096, HandshakeTimeout: 10 * time.Second, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }, EnableCompression: false}
	conn, e := up.Upgrade(w, r, http.Header{"Cache-Control": []string{"no-store"}, "Referrer-Policy": []string{"no-referrer"}})
	if e != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	p := &peer{conn: conn, cert: cert, ctx: ctx, cancel: cancel}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		conn.Close()
		return
	}
	if old := s.peers[id]; old != nil {
		old.cancel()
		old.conn.Close()
	}
	s.peers[id] = p
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() {
		cancel()
		conn.Close()
		s.mu.Lock()
		if s.peers[id] == p {
			delete(s.peers, id)
		}
		s.mu.Unlock()
		s.wg.Done()
	}()
	// Recheck after registration to close the authentication/removal race.
	if _, e = s.o.Authenticator.Authenticate(ctx, cert); e != nil {
		return
	}
	conn.SetReadLimit(protocol.MaxMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(s.o.PongTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(s.o.PongTimeout)) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(s.o.PingInterval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				conn.Close()
				return
			case <-tick.C:
				if _, e := s.o.Authenticator.Authenticate(ctx, cert); e != nil {
					conn.Close()
					return
				}
				if e := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(s.o.WriteTimeout)); e != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	first := true
	for {
		typ, data, e := conn.ReadMessage()
		if e != nil {
			return
		}
		if typ != websocket.TextMessage {
			p.closeProtocol()
			return
		}
		m, e := protocol.Decode(data, id)
		if e != nil {
			p.closeProtocol()
			return
		}
		if _, e = s.o.Authenticator.Authenticate(ctx, cert); e != nil {
			return
		}
		if first && m.Type != "hello" {
			p.closeProtocol()
			return
		}
		first = false
		switch m.Type {
		case "result":
			if e = s.result(p, id, *m.Result); e != nil {
				return
			}
		case "hello":
			for _, result := range m.Hello.PendingResults {
				if e = s.result(p, id, result); e != nil {
					return
				}
			}
			if s.o.Recovery != nil {
				if e = s.o.Recovery.Hello(ctx, id, *m.Hello); e != nil {
					return
				}
			}
		default:
			p.closeProtocol()
			return
		}
		p.ready.Store(true)
	}
}
func (p *peer) closeProtocol() {
	_ = p.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid control message"), time.Now().Add(time.Second))
}
func (s *Server) send(p *peer, m protocol.Message) error {
	if e := m.Validate(m.NodeID); e != nil {
		return e
	}
	data, e := json.Marshal(m)
	if e != nil || len(data) > protocol.MaxMessageBytes {
		return ErrUnavailable
	}
	p.write.Lock()
	defer p.write.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(s.o.WriteTimeout))
	return p.conn.WriteMessage(websocket.TextMessage, data)
}
func (s *Server) result(p *peer, id domain.ID, r protocol.Result) error {
	if s.o.Results == nil {
		return ErrUnavailable
	}
	if _, e := s.o.Results.RecordResult(p.ctx, id, r); e != nil {
		return e
	}
	msgID, e := domain.NewID("msg")
	if e != nil {
		return e
	}
	return s.send(p, protocol.Message{Version: protocol.Version, Type: "receipt", MessageID: msgID, NodeID: id, SentAt: s.o.Now(), Receipt: &protocol.Receipt{TaskID: r.TaskID, NodeID: id, Sequence: r.Sequence}})
}

// Deliver is called by the B04 scheduler only after its ledger/dependency checks.
func (s *Server) Deliver(ctx context.Context, c protocol.Command) error {
	s.mu.Lock()
	p := s.peers[c.NodeID]
	s.mu.Unlock()
	if p == nil || !p.ready.Load() || s.o.Results == nil {
		return ErrUnavailable
	}
	if _, e := s.o.Authenticator.Authenticate(ctx, p.cert); e != nil {
		return e
	}
	id, e := domain.NewID("msg")
	if e != nil {
		return e
	}
	return s.send(p, protocol.Message{Version: protocol.Version, Type: "command", MessageID: id, NodeID: c.NodeID, SentAt: s.o.Now(), Command: &c})
}
func (s *Server) Disconnect(id domain.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.peers[id]; p != nil {
		p.cancel()
		p.conn.Close()
		delete(s.peers, id)
	}
}
func (s *Server) Connected(id domain.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peers[id] != nil && s.peers[id].ready.Load()
}
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	for _, p := range s.peers {
		p.cancel()
		p.conn.Close()
	}
	s.mu.Unlock()
	s.wg.Wait()
	return nil
}
func (s *Server) artifact(w http.ResponseWriter, r *http.Request) {
	id, _, e := s.authenticate(r)
	if e != nil {
		reply(w, 401, "UNAUTHENTICATED")
		return
	}
	task := domain.ID(r.PathValue("task_id"))
	if !task.Valid() || r.URL.RawQuery != "" {
		reply(w, 400, "INVALID_ARGUMENT")
		return
	}
	if s.o.Artifacts == nil || s.o.ArtifactResolver == nil {
		reply(w, 501, "NOT_IMPLEMENTED")
		return
	}
	key, e := s.o.ArtifactResolver.Resolve(r.Context(), id, task)
	if e != nil || key.NodeID != id || key.TaskID != task || !key.TargetRevision.Valid() || len(key.TargetDigest) != 64 {
		reply(w, 403, "FORBIDDEN")
		return
	}
	reader, e := s.o.Artifacts.Open(r.Context(), id, key)
	if e != nil {
		reply(w, 403, "FORBIDDEN")
		return
	}
	defer reader.Close()
	const maximum = 16 << 20
	data, e := io.ReadAll(io.LimitReader(reader, maximum+1))
	if e != nil || len(data) > maximum {
		reply(w, 500, "ARTIFACT_UNAVAILABLE")
		return
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != key.TargetDigest {
		reply(w, 500, "ARTIFACT_UNAVAILABLE")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}
