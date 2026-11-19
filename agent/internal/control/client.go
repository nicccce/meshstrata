// Package control transports v1 commands; durable execution/outbox belong to B05.
package control

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
)

const Subprotocol = "meshstrata.control.v1"

var ErrUnavailable = errors.New("agent control dependency unavailable")

// Execute MUST persist checkpoint + result outbox before returning. Hello returns
// at most 100 durable pending results. Acknowledge durably removes only the exact
// matching receipt. No in-memory fallback is supplied when B05 is absent.
type Runtime interface {
	Hello(context.Context) (protocol.Hello, error)
	Execute(context.Context, protocol.Command) (protocol.Result, error)
	Acknowledge(context.Context, protocol.Receipt) error
}
type Options struct {
	EnableV2  bool // explicit; Runtime must independently attest completed recovery
	URL       string
	NodeID    domain.ID
	TLSConfig func() (*tls.Config, error)
	Runtime   Runtime
	// Local collector uses provisioned configuration and a numeric probe allowlist.
	ObserveV2    func(context.Context, protocol.ObservationRequestV2) (protocol.ObservationReportV2, error)
	ControlOnly  HelloSource
	OnRejected   func()
	Now          func() time.Time
	MinBackoff   time.Duration
	MaxBackoff   time.Duration
	PingInterval time.Duration
	PongTimeout  time.Duration
	WriteTimeout time.Duration
	// Called before each attempt; inject enrollment.Client.Renew when due.
	BeforeConnect func(context.Context) error
}
type Client struct {
	o       Options
	mu      sync.Mutex
	running bool
	cancel  context.CancelFunc
	conn    *websocket.Conn
	session domain.ID
	done    chan struct{}
	closed  bool
}

func New(o Options) (*Client, error) {
	u, e := url.Parse(o.URL)
	if e != nil || u.Scheme != "wss" || u.Host == "" || u.Path != "/control/v1" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || !o.NodeID.Valid() || o.TLSConfig == nil || (o.Runtime == nil) == (o.ControlOnly == nil) {
		return nil, ErrUnavailable
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MinBackoff == 0 {
		o.MinBackoff = time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = time.Minute
	}
	if o.PingInterval == 0 {
		o.PingInterval = 20 * time.Second
	}
	if o.PongTimeout == 0 {
		o.PongTimeout = time.Minute
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.MinBackoff <= 0 || o.MaxBackoff < o.MinBackoff || o.PingInterval <= 0 || o.PongTimeout <= o.PingInterval || o.WriteTimeout <= 0 {
		return nil, ErrUnavailable
	}
	return &Client{o: o}, nil
}
func (c *Client) Run(parent context.Context) error {
	c.mu.Lock()
	if c.running || c.closed {
		c.mu.Unlock()
		return ErrUnavailable
	}
	ctx, cancel := context.WithCancel(parent)
	c.running = true
	c.cancel = cancel
	c.done = make(chan struct{})
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		c.running = false
		c.conn = nil
		close(c.done)
		c.mu.Unlock()
	}()
	delay := c.o.MinBackoff
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		established := false
		_ = c.runOnce(ctx, func() { established = true })
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if established {
			delay = c.o.MinBackoff
		}
		jitter, e := rand.Int(rand.Reader, big.NewInt(int64(delay/2)+1))
		if e != nil {
			return ErrUnavailable
		}
		timer := time.NewTimer(delay/2 + time.Duration(jitter.Int64()))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < c.o.MaxBackoff {
			delay *= 2
			if delay > c.o.MaxBackoff {
				delay = c.o.MaxBackoff
			}
		}
	}
}
func (c *Client) runOnce(ctx context.Context, markEstablished func()) error {
	if c.o.BeforeConnect != nil {
		if e := c.o.BeforeConnect(ctx); e != nil {
			return e
		}
	}
	config, e := c.o.TLSConfig()
	if e != nil {
		return e
	}
	if config == nil || config.InsecureSkipVerify || config.RootCAs == nil || config.ServerName == "" || len(config.Certificates) == 0 {
		return ErrUnavailable
	}
	if len(config.Certificates[0].Certificate) == 0 {
		return ErrUnavailable
	}
	leaf, err := x509.ParseCertificate(config.Certificates[0].Certificate[0])
	if err != nil {
		return ErrUnavailable
	}
	node, err := pki.NodeID(leaf)
	if err != nil || node != c.o.NodeID {
		return ErrUnavailable
	}
	config = config.Clone()
	config.MinVersion = tls.VersionTLS13
	config.NextProtos = []string{"http/1.1"}
	selected := Subprotocol
	if c.o.EnableV2 {
		r, ok := c.o.Runtime.(interface {
			RecoveryFormat(context.Context) (int, error)
		})
		if !ok {
			return ErrUnavailable
		}
		format, err := r.RecoveryFormat(ctx)
		if err != nil || format != 2 {
			return ErrUnavailable
		}
		selected = protocol.SubprotocolV2
	}
	dialer := websocket.Dialer{TLSClientConfig: config, Subprotocols: []string{selected}, HandshakeTimeout: 10 * time.Second, ReadBufferSize: 4096, WriteBufferSize: 4096, EnableCompression: false}
	conn, resp, e := dialer.DialContext(ctx, c.o.URL, http.Header{})
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if e != nil {
		return ErrUnavailable
	}
	defer conn.Close()
	if conn.Subprotocol() != selected {
		return ErrUnavailable
	}
	var session domain.ID
	if c.o.EnableV2 {
		conn.SetReadLimit(protocol.MaxMessageBytes)
		_ = conn.SetReadDeadline(time.Now().Add(c.o.PongTimeout))
		typ, data, e := conn.ReadMessage()
		if e != nil || typ != websocket.TextMessage {
			return ErrUnavailable
		}
		w, e := protocol.DecodeSessionV2(data, c.o.NodeID)
		if e != nil || w.Type != "welcome" {
			return ErrUnavailable
		}
		session = w.SessionID
	}
	c.mu.Lock()
	c.session = session
	c.conn = conn
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
			c.session = ""
		}
		c.mu.Unlock()
	}()
	conn.SetReadLimit(protocol.MaxMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(c.o.PongTimeout))
	conn.SetPongHandler(func(string) error {
		markEstablished()
		return conn.SetReadDeadline(time.Now().Add(c.o.PongTimeout))
	})
	local, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(c.o.PingInterval)
		defer tick.Stop()
		for {
			select {
			case <-local.Done():
				conn.Close()
				return
			case <-tick.C:
				if e := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(c.o.WriteTimeout)); e != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-done }()
	writeV2 := func(v protocol.SessionV2) error {
		if v.Validate(c.o.NodeID) != nil {
			return ErrUnavailable
		}
		data, e := json.Marshal(v)
		if e != nil || len(data) > protocol.MaxMessageBytes {
			return ErrUnavailable
		}
		_ = conn.SetWriteDeadline(time.Now().Add(c.o.WriteTimeout))
		return conn.WriteMessage(websocket.TextMessage, data)
	}
	send := func(m protocol.Message) error {
		m.Version = protocol.Version
		m.NodeID = c.o.NodeID
		m.SentAt = c.o.Now()
		id, e := domain.NewID("msg")
		if e != nil {
			return e
		}
		m.MessageID = id
		if e = m.Validate(c.o.NodeID); e != nil {
			return e
		}
		var payload any = m
		if c.o.EnableV2 {
			m.Version = 2
			v := protocol.SessionV2{Message: m, SessionID: session}
			if m.Type == "hello" {
				v.RecoveryFormat = 2
			}
			return writeV2(v)
		}
		data, e := json.Marshal(payload)
		if e != nil || len(data) > protocol.MaxMessageBytes {
			return ErrUnavailable
		}
		_ = conn.SetWriteDeadline(time.Now().Add(c.o.WriteTimeout))
		return conn.WriteMessage(websocket.TextMessage, data)
	}

	// Keep the reader free to process ping/pong while B05 performs slow work.
	// One worker serializes all runtime calls and data writes; bounded queue fails
	// closed if the peer floods commands or receipts.
	type job struct {
		protocol.Message
		request *protocol.ObservationRequestV2
	}
	jobs := make(chan job, 128)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		defer conn.Close()
		type resultKey struct {
			task     domain.ID
			sequence int64
		}
		pending := map[resultKey]bool{}
		sendHello := func() error {
			hello, e := c.hello(local)
			if e != nil {
				return e
			}
			for _, r := range hello.PendingResults {
				pending[resultKey{r.TaskID, r.Sequence}] = true
			}
			return send(protocol.Message{Type: "hello", Hello: &hello})
		}
		if sendHello() != nil {
			return
		}
		// A Pong after the first Hello distinguishes an accepted short
		// session from an upgraded connection whose Hello was rejected.
		if conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(c.o.WriteTimeout)) != nil {
			return
		}
		for {
			select {
			case <-local.Done():
				return
			case m := <-jobs:
				switch m.Type {
				case "observe":
					if c.o.ObserveV2 == nil || m.request == nil || !m.request.Deadline.After(c.o.Now()) || m.request.Deadline.After(c.o.Now().Add(30*time.Second)) {
						return
					}
					probe, cancelProbe := context.WithDeadline(local, m.request.Deadline)
					report, err := c.o.ObserveV2(probe, *m.request)
					cancelProbe()
					if err != nil || report.ID != m.request.ID || !report.Valid(c.o.NodeID) {
						return
					}
					id, err := domain.NewID("msg")
					if err != nil || writeV2(protocol.SessionV2{Message: protocol.Message{Version: 2, Type: "observation", MessageID: id, NodeID: c.o.NodeID, SentAt: c.o.Now()}, SessionID: session, ObservationReport: &report}) != nil {
						return
					}
				case "command":
					if c.o.Runtime == nil {
						c.reject(conn)
						return
					}
					if !c.o.Now().Before(m.Command.Deadline) {
						return
					}
					execution, cancelExecution := context.WithDeadline(local, m.Command.Deadline)
					result, e := c.o.Runtime.Execute(execution, *m.Command)
					cancelExecution()
					if e != nil || result.TaskID != m.Command.TaskID || result.NodeID != c.o.NodeID || result.TargetRevision != m.Command.TargetRevision || result.TargetDigest != m.Command.TargetDigest {
						return
					}
					pending[resultKey{result.TaskID, result.Sequence}] = true
					if send(protocol.Message{Type: "result", Result: &result}) != nil {
						return
					}
				case "receipt":
					if c.o.Runtime == nil {
						c.reject(conn)
						return
					}
					if !pending[resultKey{m.Receipt.TaskID, m.Receipt.Sequence}] {
						return
					}
					if c.o.Runtime.Acknowledge(local, *m.Receipt) != nil {
						return
					}
					delete(pending, resultKey{m.Receipt.TaskID, m.Receipt.Sequence})
					if len(pending) == 0 {
						hello, e := c.hello(local)
						if e != nil {
							return
						}
						if len(hello.PendingResults) > 0 {
							for _, r := range hello.PendingResults {
								pending[resultKey{r.TaskID, r.Sequence}] = true
							}
							if send(protocol.Message{Type: "hello", Hello: &hello}) != nil {
								return
							}
						}
					}
				default:
					return
				}
			}
		}
	}()
	defer func() { cancel(); <-workerDone }()
	for {
		typ, data, e := conn.ReadMessage()
		if e != nil {
			return e
		}
		if typ != websocket.TextMessage {
			return ErrUnavailable
		}
		var m protocol.Message
		var request *protocol.ObservationRequestV2
		if c.o.EnableV2 {
			v, err := protocol.DecodeSessionV2(data, c.o.NodeID)
			e = err
			if v.SessionID != session {
				e = ErrUnavailable
			}
			m = v.Message
			request = v.ObservationRequest
			m.Version = protocol.Version
		} else {
			m, e = protocol.Decode(data, c.o.NodeID)
		}
		if e != nil {
			return e
		}
		if m.Type != "command" && m.Type != "receipt" && (m.Type != "observe" || request == nil) {
			return ErrUnavailable
		}
		markEstablished()
		select {
		case jobs <- job{Message: m, request: request}:
		case <-local.Done():
			return local.Err()
		default:
			return ErrUnavailable
		}
	}
}

func (c *Client) Close() error {
	c.mu.Lock()
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	if c.conn != nil {
		c.conn.Close()
	}
	done := c.done
	running := c.running
	c.mu.Unlock()
	if running {
		<-done
	}
	return nil
}

// Reconnect adopts a newly renewed identity without stopping the Run lifecycle.
func (c *Client) Reconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
	}
}

func (c *Client) hello(ctx context.Context) (protocol.Hello, error) {
	if c.o.ControlOnly != nil {
		return c.o.ControlOnly.Hello(ctx)
	}
	return c.o.Runtime.Hello(ctx)
}
func (c *Client) reject(conn *websocket.Conn) {
	if c.o.OnRejected != nil {
		c.o.OnRejected()
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "business execution unavailable"), time.Now().Add(c.o.WriteTimeout))
}
