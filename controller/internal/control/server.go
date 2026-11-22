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
	"log/slog"
	"net/http"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

const Subprotocol = "meshstrata.control.v1"

var ErrUnavailable = errors.New("control dependency unavailable")
var ErrObservationBusy = errors.New("observation already pending")
var ErrObservationBackoff = errors.New("observation retry not due")

type observationKey struct {
	target   protocol.Pin
	revision domain.Revision
	dns      protocol.Pin
}
type observationRetry struct {
	failures uint
	next     time.Time
}

func observationBinding(r protocol.ObservationRequestV2) observationKey {
	var key observationKey
	if r.Target != nil {
		key.target = *r.Target
	}
	if r.Preparation != nil {
		key.revision, key.dns = r.Preparation.NodeRevision, r.Preparation.DNS.Pin
	}
	return key
}

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
type ObservationStore interface {
	RecordObservation(context.Context, domain.ID, protocol.ObservationReportV2) error
}
type Options struct {
	EnableV2         bool // explicit opt-in; requires recovered dual-format execution
	RequireRuntime   bool
	Authenticator    Authenticator
	Results          ResultStore
	Recovery         Recovery
	Artifacts        protocol.ArtifactSource
	ArtifactResolver ArtifactResolver
	Observations     ObservationStore
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
	session            domain.ID
	v2                 bool
	observationStatus  bool
	recovery           atomic.Int32
	ready              atomic.Bool
	runtime            atomic.Bool
	conn               *websocket.Conn
	cert               *x509.Certificate
	ctx                context.Context
	cancel             context.CancelFunc
	write              sync.Mutex
	observationMu      sync.Mutex
	observation        *protocol.ObservationRequestV2
	observationRetries map[observationKey]observationRetry
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
	v2 := false
	observationStatus := false
	for _, v := range websocket.Subprotocols(r) {
		if v == protocol.SubprotocolV2ObservationStatus && s.o.EnableV2 {
			supported, v2, observationStatus = true, true, true
		}
		if v == protocol.SubprotocolV2 && s.o.EnableV2 {
			supported = true
			v2 = true
		}
		if v == Subprotocol {
			supported = true
		}
	}
	if !supported || r.Header.Get("Origin") != "" {
		reply(w, 400, "INVALID_ARGUMENT")
		return
	}
	selected := Subprotocol
	if v2 {
		selected = protocol.SubprotocolV2
	}
	if observationStatus {
		selected = protocol.SubprotocolV2ObservationStatus
	}
	up := websocket.Upgrader{Subprotocols: []string{selected}, ReadBufferSize: 4096, WriteBufferSize: 4096, HandshakeTimeout: 10 * time.Second, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }, EnableCompression: false}
	conn, e := up.Upgrade(w, r, http.Header{"Cache-Control": []string{"no-store"}, "Referrer-Policy": []string{"no-referrer"}})
	if e != nil {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	session, e := domain.NewID("session")
	if e != nil {
		cancel()
		conn.Close()
		return
	}
	ctx = protocol.WithSession(ctx, session)
	p := &peer{conn: conn, cert: cert, ctx: ctx, cancel: cancel, session: session, v2: v2, observationStatus: observationStatus}
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
	if p.v2 {
		msgID, e := domain.NewID("msg")
		if e != nil {
			return
		}
		if s.send(p, protocol.Message{Type: "welcome", MessageID: msgID, NodeID: id, SentAt: s.o.Now()}) != nil {
			return
		}
	}
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
		var m protocol.Message
		var report *protocol.ObservationReportV2
		if p.v2 {
			var v protocol.SessionV2
			v, e = protocol.DecodeSessionV2(data, id)
			if e == nil && v.SessionID != p.session {
				e = ErrUnavailable
			}
			m = v.Message
			report = v.ObservationReport
			m.Version = protocol.Version
			if e == nil && v.Type == "hello" {
				p.recovery.Store(int32(v.RecoveryFormat))
			}
		} else {
			m, e = protocol.Decode(data, id)
		}
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
		if first {
			p.runtime.Store(m.Hello.Runtime)
		}
		if s.o.RequireRuntime && !p.acceptsRuntimeMessage(m) {
			p.closeProtocol()
			return
		}
		first = false
		switch m.Type {
		case "observation":
			if !p.ready.Load() || !p.runtime.Load() || report == nil || s.recordObservation(p, id, *report) != nil {
				return
			}
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
func (p *peer) acceptsRuntimeMessage(m protocol.Message) bool {
	if h := m.Hello; h != nil {
		return h.Runtime == p.runtime.Load() &&
			(h.Runtime || (len(h.PendingResults) == 0 && h.AppliedRevision == 0 && h.RevocationEpoch == 1))
	}
	return p.runtime.Load()
}

func (p *peer) closeProtocol() {
	_ = p.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "invalid control message"), time.Now().Add(time.Second))
}
func (s *Server) send(p *peer, m protocol.Message) error {
	if !p.v2 {
		if e := m.Validate(m.NodeID); e != nil {
			return e
		}
	}
	var payload any = m
	if p.v2 {
		m.Version = 2
		v := protocol.SessionV2{Message: m, SessionID: p.session}
		return s.sendV2(p, v)
	}
	data, e := json.Marshal(payload)
	if e != nil || len(data) > protocol.MaxMessageBytes {
		return ErrUnavailable
	}
	p.write.Lock()
	defer p.write.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(s.o.WriteTimeout))
	return p.conn.WriteMessage(websocket.TextMessage, data)
}

func (s *Server) sendV2(p *peer, v protocol.SessionV2) error {
	if v.Validate(v.NodeID) != nil {
		return ErrUnavailable
	}
	data, e := json.Marshal(v)
	if e != nil || len(data) > protocol.MaxMessageBytes {
		return ErrUnavailable
	}
	p.write.Lock()
	defer p.write.Unlock()
	_ = p.conn.SetWriteDeadline(time.Now().Add(s.o.WriteTimeout))
	return p.conn.WriteMessage(websocket.TextMessage, data)
}

// ObserveV2 accepts references from a trusted scheduler, never an HTTP probe URL.
// One outstanding request per connection bounds both work and replay state.
func (s *Server) ObserveV2(ctx context.Context, node domain.ID, request protocol.ObservationRequestV2) error {
	now := s.o.Now()
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.o.Observations == nil || !request.Valid() || !request.Deadline.After(now) || request.Deadline.After(now.Add(30*time.Second)) {
		return ErrUnavailable
	}
	s.mu.Lock()
	p := s.peers[node]
	s.mu.Unlock()
	if p == nil || !p.v2 || p.recovery.Load() != 2 || !p.ready.Load() || !p.runtime.Load() || !s.current(node, p) {
		slog.Warn("Observation unavailable", "node", node, "request", request.ID, "stage", "connection", "code", "OBSERVATION_UNAVAILABLE")
		return ErrUnavailable
	}
	if _, e := s.o.Authenticator.Authenticate(ctx, p.cert); e != nil {
		return e
	}
	p.observationMu.Lock()
	defer p.observationMu.Unlock()
	if p.observation != nil && p.observation.Deadline.After(now) {
		return ErrObservationBusy
	}
	if p.observationRetries[observationBinding(request)].next.After(now) {
		return ErrObservationBackoff
	}
	if p.observation != nil {
		slog.Warn("Observation slot expired", "node", node, "request", p.observation.ID, "stage", "deadline", "code", "OBSERVATION_EXPIRED")
	}
	copy := request
	if request.Target != nil {
		target := *request.Target
		copy.Target = &target
	}
	id, e := domain.NewID("msg")
	if e != nil {
		return e
	}
	p.observation = &copy
	if e = s.sendV2(p, protocol.SessionV2{Message: protocol.Message{Version: 2, Type: "observe", MessageID: id, NodeID: node, SentAt: now}, SessionID: p.session, ObservationRequest: &copy}); e != nil {
		p.observation = nil
	} else {
		revision := domain.Revision(0)
		if copy.Preparation != nil {
			revision = copy.Preparation.NodeRevision
		}
		slog.Info("Observation requested", "node", node, "session", p.session, "request", copy.ID, "node_revision", revision, "egress", copy.Target != nil, "stage", "sent")
	}
	return e
}

func (s *Server) recordObservation(p *peer, node domain.ID, report protocol.ObservationReportV2) error {
	if s.o.Observations == nil || !s.current(node, p) {
		return ErrUnavailable
	}
	p.observationMu.Lock()
	defer p.observationMu.Unlock()
	r := p.observation
	if r == nil || !report.Valid(node) {
		return ErrUnavailable
	}
	if report.Failure != nil && !p.observationStatus {
		return ErrUnavailable
	}
	// A reply to an expired/replaced request must never consume the current slot
	// or disconnect the session carrying its replacement and durable task receipts.
	if r.ID != report.ID {
		slog.Warn("Observation not recorded", "node", node, "request", report.ID, "stage", "correlation", "code", "OBSERVATION_SUPERSEDED")
		return nil
	}
	p.observation = nil
	if report.Failure != nil {
		p.failedObservation(*r, s.o.Now())
		slog.Warn("Observation failed", "node", node, "session", p.session, "request", report.ID, "stage", "collect", "code", report.Failure.Code)
		return nil
	}
	if !r.Deadline.After(s.o.Now()) {
		slog.Warn("Observation not recorded", "node", node, "request", report.ID, "stage", "deadline", "code", "OBSERVATION_EXPIRED")
		return nil
	}
	if r.Target == nil && report.Inventory == nil || r.Target != nil && (report.Egress == nil || *r.Target != report.Egress.Target) {
		return ErrUnavailable
	}
	if prep := r.Preparation; prep != nil {
		if v := report.Inventory; v != nil {
			if v.NodeRevision != prep.NodeRevision || !reflect.DeepEqual(v.DNS, prep.DNS) || !reflect.DeepEqual(v.Sockets, prep.Sockets) {
				return ErrUnavailable
			}
		} else if v := report.Egress; v.NodeRevision != prep.NodeRevision || v.DNS != prep.DNS.Pin {
			return ErrUnavailable
		}
	}
	// Keep the slot serialized through persistence. The caller's short scheduling
	// context ends after send; persistence belongs to this authenticated session.
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	err := s.o.Observations.RecordObservation(ctx, node, report)
	if err != nil {
		code := domain.ErrorCode("INTERNAL_ERROR")
		var problem *domain.Problem
		if errors.As(err, &problem) {
			code = problem.Code
		}
		observed, expires := time.Time{}, time.Time{}
		revision := domain.Revision(0)
		if v := report.Inventory; v != nil {
			observed, expires, revision = v.ObservedAt, v.ExpiresAt, v.NodeRevision
		} else if v := report.Egress; v != nil {
			observed, expires, revision = v.ObservedAt, v.ExpiresAt, v.NodeRevision
		}
		slog.Warn("Observation not recorded", "node", node, "session", p.session, "request", report.ID, "node_revision", revision, "stage", "persist", "code", code, "age_ms", s.o.Now().Sub(observed).Milliseconds(), "ttl_ms", expires.Sub(s.o.Now()).Milliseconds())
		// A node CAS may advance during a real probe. Rejection is not a protocol
		// violation; the next current request can use the same authenticated session.
		if problem != nil {
			return nil
		}
	} else {
		delete(p.observationRetries, observationBinding(*r))
		slog.Info("Observation recorded", "node", node, "session", p.session, "request", report.ID, "stage", "persist")
	}
	return err
}

// Bounded session-local scheduling metadata, never evidence. Credential/target,
// node revision and DNS changes use distinct keys; a failed proxy cannot delay
// inventory or another target. Persistent operations remain the facts to retry.
func (p *peer) failedObservation(r protocol.ObservationRequestV2, now time.Time) {
	if p.observationRetries == nil {
		p.observationRetries = map[observationKey]observationRetry{}
	}
	key := observationBinding(r)
	prior := p.observationRetries[key]
	prior.failures = min(prior.failures+1, 5)
	prior.next = now.Add(time.Second * time.Duration(1<<prior.failures))
	if len(p.observationRetries) >= 128 {
		var oldest observationKey
		var earliest time.Time
		for k, v := range p.observationRetries {
			if earliest.IsZero() || v.next.Before(earliest) {
				oldest, earliest = k, v.next
			}
		}
		delete(p.observationRetries, oldest)
	}
	p.observationRetries[key] = prior
}
func (s *Server) result(p *peer, id domain.ID, r protocol.Result) error {
	if s.o.Results == nil || !s.current(id, p) {
		return ErrUnavailable
	}
	if _, e := s.o.Results.RecordResult(p.ctx, id, r); e != nil {
		return e
	}
	if !s.current(id, p) {
		return ErrUnavailable
	}
	msgID, e := domain.NewID("msg")
	if e != nil {
		return e
	}
	return s.send(p, protocol.Message{Version: protocol.Version, Type: "receipt", MessageID: msgID, NodeID: id, SentAt: s.o.Now(), Receipt: &protocol.Receipt{TaskID: r.TaskID, NodeID: id, Sequence: r.Sequence}})
}

// Deliver is called by the B04 scheduler only after its ledger/dependency checks.
func (s *Server) Deliver(ctx context.Context, c protocol.Command) error {
	return s.deliver(ctx, c, false)
}
func (s *Server) DeliverV2(ctx context.Context, c protocol.Command) error {
	return s.deliver(ctx, c, true)
}
func (s *Server) deliver(ctx context.Context, c protocol.Command, v2 bool) error {
	s.mu.Lock()
	p := s.peers[c.NodeID]
	s.mu.Unlock()
	if p == nil || p.v2 != v2 || v2 && p.recovery.Load() != 2 || !p.ready.Load() || s.o.Results == nil || s.o.RequireRuntime && !p.runtime.Load() {
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
func (s *Server) RuntimeConnected(id domain.ID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peers[id]
	return p != nil && p.ready.Load() && p.runtime.Load()
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
	id, cert, e := s.authenticate(r)
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
	if formats, ok := s.o.ArtifactResolver.(interface {
		ArtifactFormat(context.Context, domain.ID, domain.ID) (int, error)
	}); ok {
		format, err := formats.ArtifactFormat(r.Context(), id, task)
		if err != nil || format != 1 && format != 2 {
			reply(w, 403, "FORBIDDEN")
			return
		}
		if format == 2 {
			session, recovery := s.RecoverySession(id)
			if recovery != 2 || session == "" || r.Header.Get("X-Meshstrata-Session") != session || !s.RuntimeConnected(id) {
				reply(w, 403, "FORBIDDEN")
				return
			}
			r = r.WithContext(protocol.WithSession(r.Context(), domain.ID(session)))
		}
	}
	reader, e := s.o.Artifacts.Open(r.Context(), id, key)
	if e != nil {
		reply(w, 403, "FORBIDDEN")
		return
	}
	defer reader.Close()
	const maximum = 16 << 20
	data, e := io.ReadAll(io.LimitReader(reader, maximum+1))
	defer clear(data)
	if e != nil || len(data) > maximum {
		reply(w, 500, "ARTIFACT_UNAVAILABLE")
		return
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != key.TargetDigest {
		reply(w, 500, "ARTIFACT_UNAVAILABLE")
		return
	}
	// Opening an immutable artifact does not authorize a later response after
	// the certificate has been revoked or the authenticated session replaced.
	if current, err := s.o.Authenticator.Authenticate(r.Context(), cert); err != nil || current != id {
		reply(w, 403, "FORBIDDEN")
		return
	}
	if session := protocol.SessionFromContext(r.Context()); session != "" {
		current, recovery := s.RecoverySession(id)
		if recovery != 2 || current != string(session) || !s.RuntimeConnected(id) {
			reply(w, 403, "FORBIDDEN")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

func (s *Server) current(node domain.ID, p *peer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.peers[node] == p && p.ctx.Err() == nil
}

// RecoverySession is a nonblocking read of the current authenticated connection.
func (s *Server) RecoverySession(node domain.ID) (string, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.peers[node]
	if p == nil || !p.v2 || p.ctx.Err() != nil || p.recovery.Load() != 2 {
		return "", 0
	}
	return string(p.session), 2
}
