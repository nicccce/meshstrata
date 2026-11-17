package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/platform"
)

type Kernel interface {
	Validate(context.Context, mihomo.Candidate) (mihomo.Validated, error)
	Startup(context.Context, mihomo.Candidate) (string, []string, error)
	Reload(context.Context, mihomo.Candidate) (mihomo.Applied, error)
	Inspect(context.Context, mihomo.Candidate, int) (mihomo.Evidence, error)
	Verify(context.Context, mihomo.Candidate, int, mihomo.Probe) (mihomo.Evidence, error)
	Revocation(context.Context, mihomo.Candidate, mihomo.RevocationProbe) (mihomo.RevocationEvidence, error)
	Connections(context.Context) ([]mihomo.Connection, error)
	CloseConnection(context.Context, mihomo.Candidate, string, string) error
}

type Process interface {
	Start(context.Context, string, []string) (int, error)
	Status(context.Context) (platform.Status, error)
	Stop(context.Context) error
}

var _ Kernel = (*mihomo.Adapter)(nil)

type Options struct {
	NodeID                 domain.ID
	State                  State
	Fetch                  func(context.Context, control.ArtifactKey, int64) ([]byte, error)
	Materials              control.MaterialResolver
	Kernel                 Kernel
	Process                Process
	ProbeFactory           func(context.Context, control.Bundle, mihomo.Candidate) (mihomo.Probe, error)
	RevocationProbeFactory func(context.Context, control.Bundle, mihomo.Candidate) (mihomo.RevocationProbe, error)
	Now                    func() time.Time
	Random                 io.Reader
	OnPending              func()
	OnApplyFailure         func(error)
	ConfigDir              string
	SocketPath             string
	APISecret              string
}

// State persists the complete runtime ledger together with artifacts and evidence.
type State interface {
	control.LocalState
	SaveArtifact(context.Context, control.ArtifactKey, []byte) error
	ReadArtifact(context.Context, control.ArtifactKey) ([]byte, error)
	RestrictionResults(context.Context) (map[domain.ID]control.Result, error)
}

type Runtime struct {
	urgent              atomic.Bool
	pendingNotification chan struct{}
	o                   Options
	ready               chan struct{}
	lifetime            context.Context
	stop                context.CancelFunc
	done                chan struct{}
	runMu               sync.Mutex
	started             bool
	closed              bool
	stateMu             sync.Mutex
	commandGate         chan struct{}
	kernelGate          chan struct{}
	wakeup              chan struct{}
	interruptMu         sync.Mutex
	active              map[uint64]context.CancelCauseFunc
	nextOperation       uint64
	empty               mihomo.Candidate
	messageID           domain.ID
}

const expiryPoll = 250 * time.Millisecond

var (
	ErrUnavailable  = errors.New("AGENT_RUNTIME_UNAVAILABLE")
	ErrVerification = errors.New("AGENT_RUNTIME_VERIFICATION_FAILED")
	errRestricted   = errors.New("AGENT_AUTHORIZATION_CHANGED")
	errUncertain    = errors.New("AGENT_RUNTIME_STATE_UNCERTAIN")
)

func New(o Options) (*Runtime, error) {
	if !o.NodeID.Valid() || o.State == nil || o.Fetch == nil || o.Materials == nil || o.Kernel == nil ||
		o.Process == nil || o.ProbeFactory == nil || o.RevocationProbeFactory == nil || len(o.APISecret) < 32 ||
		!filepath.IsAbs(o.SocketPath) || filepath.Clean(o.SocketPath) != o.SocketPath || len(o.SocketPath) > 100 {
		return nil, ErrUnavailable
	}
	for _, name := range []string{o.ConfigDir, filepath.Dir(o.SocketPath)} {
		d, err := checkpoint.OpenPrivateDir(name)
		if err != nil {
			return nil, err
		}
		d.Close()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Random == nil {
		o.Random = rand.Reader
	}
	var envelopeID [16]byte
	if _, err := io.ReadFull(o.Random, envelopeID[:]); err != nil {
		return nil, ErrUnavailable
	}
	lifetime, stop := context.WithCancel(context.Background())
	r := &Runtime{o: o, ready: make(chan struct{}), lifetime: lifetime, stop: stop, done: make(chan struct{}),
		commandGate: make(chan struct{}, 1), kernelGate: make(chan struct{}, 1), wakeup: make(chan struct{}, 1), pendingNotification: make(chan struct{}, 1),
		active: map[uint64]context.CancelCauseFunc{}, messageID: domain.ID("msg_" + hex.EncodeToString(envelopeID[:]))}
	return r, nil
}

func (r *Runtime) Ready() <-chan struct{} { return r.ready }

func (r *Runtime) available() bool {
	select {
	case <-r.ready:
		return r.lifetime.Err() == nil
	default:
		return false
	}
}

func (r *Runtime) Run(ctx context.Context) error {
	r.runMu.Lock()
	if r.started || r.closed {
		r.runMu.Unlock()
		return ErrUnavailable
	}
	r.started = true
	r.runMu.Unlock()
	defer close(r.done)
	defer r.stop()
	stop := context.AfterFunc(ctx, r.stop)
	defer stop()
	ctx = r.lifetime

	if r.o.OnPending != nil {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-r.pendingNotification:
					r.o.OnPending()
				}
			}
		}()
	}

	empty, err := r.render(ctx, denyBundle(r.o.NodeID), nil)
	if err != nil {
		return errors.Join(err, r.stopKernel())
	}
	r.empty = empty.Candidate
	_, _, err = r.expire(ctx)
	if err != nil {
		return errors.Join(err, r.stopKernel())
	}
	watchDone := make(chan struct{})
	fatal := make(chan error, 1)
	go func() {
		defer close(watchDone)
		tick := time.NewTicker(expiryPoll)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				changed, pending, err := r.expire(ctx)
				if err != nil && ctx.Err() == nil {
					r.interrupt(errRestricted)
					fatal <- errors.Join(err, r.stopKernel())
					r.stop()
					return
				}
				if changed || pending {
					r.signal()
				}
			}
		}
	}()
	defer func() { r.stop(); <-watchDone }()
	// The clock is already active while startup validates and probes.
	for {
		err = r.reconcile(ctx, true)
		if err == nil {
			break
		}
		if ctx.Err() != nil || !errors.Is(err, errRestricted) {
			select {
			case fatalErr := <-fatal:
				return fatalErr
			default:
				return err
			}
		}
	}
	close(r.ready)
	lastAttempt := time.Time{}
	for {
		select {
		case <-ctx.Done():
			select {
			case err := <-fatal:
				return err
			default:
				return nil
			}
		case <-r.wakeup:
			if !r.urgent.Load() && time.Since(lastAttempt) < time.Second {
				continue
			}
			lastAttempt = time.Now()
			if err := r.reconcile(ctx, false); err != nil && errors.Is(err, errUncertain) {
				return err
			}
		}
	}
}

func (r *Runtime) stopKernel() error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return r.o.Process.Stop(ctx)
}

func (r *Runtime) Close() error {
	r.runMu.Lock()
	r.closed = true
	started := r.started
	r.stop()
	r.runMu.Unlock()
	if started {
		<-r.done
	}
	// An active command uses its own serialized shutdown recovery.
	if err := acquire(context.Background(), r.commandGate); err == nil {
		release(r.commandGate)
	}
	return nil
}

func acquire(ctx context.Context, gate chan struct{}) error {
	select {
	case gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			release(gate)
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func release(gate chan struct{}) { <-gate }

func (r *Runtime) operation(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancelCause(ctx)
	stop := context.AfterFunc(r.lifetime, func() { cancel(context.Canceled) })
	r.interruptMu.Lock()
	r.nextOperation++
	id := r.nextOperation
	r.active[id] = cancel
	r.interruptMu.Unlock()
	return ctx, func() {
		stop()
		cancel(context.Canceled)
		r.interruptMu.Lock()
		delete(r.active, id)
		r.interruptMu.Unlock()
	}
}

func (r *Runtime) interrupt(cause error) {
	r.interruptMu.Lock()
	defer r.interruptMu.Unlock()
	for _, cancel := range r.active {
		cancel(cause)
	}
}

func (r *Runtime) signal() {
	select {
	case r.wakeup <- struct{}{}:
	default:
	}
}

func (r *Runtime) Hello(ctx context.Context) (control.Hello, error) {
	if !r.available() {
		return control.Hello{}, ErrUnavailable
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	s, err := r.o.State.Load(ctx)
	if err != nil {
		return control.Hello{}, err
	}
	h := control.Hello{Runtime: true, NodeID: r.o.NodeID, RevocationEpoch: s.RevocationEpoch, PendingResults: []control.Result{}}
	if verified := s.Checkpoint.Verified; verified != nil {
		h.AppliedRevision, h.AppliedDigest = verified.Revision, verified.ArtifactDigest
	}
	// JSON list entries are independently sized. Reserve the full B02 envelope,
	// including the time encoder's upper fractional precision.
	m := control.Message{Version: control.Version, Type: "hello", NodeID: r.o.NodeID, MessageID: r.messageID,
		SentAt: time.Date(9999, 1, 1, 0, 0, 0, 123456789, time.UTC), Hello: &h}
	base, err := json.Marshal(m)
	if err != nil {
		return control.Hello{}, err
	}
	size := len(base)
	for _, result := range s.Outbox {
		b, err := json.Marshal(result)
		if err != nil {
			return control.Hello{}, err
		}
		if len(h.PendingResults) == 100 || size+len(b)+1 > control.MaxMessageBytes {
			if len(h.PendingResults) == 0 {
				return control.Hello{}, ErrUnavailable
			}
			break
		}
		h.PendingResults = append(h.PendingResults, result)
		size += len(b) + 1
	}
	return h, nil
}

func (r *Runtime) Acknowledge(ctx context.Context, receipt control.Receipt) error {
	if !r.available() {
		return ErrUnavailable
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	return r.o.State.Acknowledge(ctx, receipt)
}
