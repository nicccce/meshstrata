package control

import (
	"context"
	"errors"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	protocol "github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
)

type observationSink struct{ calls chan domain.ID }

func (s *observationSink) RecordObservation(ctx context.Context, _ domain.ID, _ protocol.ObservationReportV2) error {
	s.calls <- protocol.SessionFromContext(ctx)
	return nil
}

func TestV2ObservationsRequireOutstandingRequestAndCurrentSession(t *testing.T) {
	f := wsSetup(t)
	f.control.o.EnableV2, f.control.o.RequireRuntime = true, true
	sink := &observationSink{calls: make(chan domain.ID, 2)}
	f.control.o.Observations = sink
	c, session := f.dialV2(t)
	hello := f.v2Envelope("hello", session)
	hello.RecoveryFormat = 2
	hello.Hello = &protocol.Hello{NodeID: f.materials.NodeID, Runtime: true, RevocationEpoch: 1, PendingResults: []protocol.Result{}}
	if err := c.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !f.control.RuntimeConnected(f.materials.NodeID) {
		if time.Now().After(deadline) {
			t.Fatal("not ready")
		}
		time.Sleep(time.Millisecond)
	}
	target := controltest.Pin("target", "fixed-target")
	req := protocol.ObservationRequestV2{ID: controltest.ID("observation"), Deadline: time.Now().Add(10 * time.Second), Target: &target}
	if err := f.control.ObserveV2(context.Background(), f.materials.NodeID, req); err != nil {
		t.Fatal(err)
	}
	if err := f.control.ObserveV2(context.Background(), f.materials.NodeID, req); err == nil {
		t.Fatal("unbounded outstanding probes")
	}
	_, raw, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	request, err := protocol.DecodeSessionV2(raw, f.materials.NodeID)
	if err != nil || request.ObservationRequest == nil || request.ObservationRequest.ID != req.ID {
		t.Fatal("missing correlated request", err)
	}
	now := time.Now().UTC()
	report := f.v2Envelope("observation", session)
	report.ObservationReport = &protocol.ObservationReportV2{ID: req.ID, Egress: &protocol.EgressObservationV2{NodeID: f.materials.NodeID, NodeRevision: 1, DNS: controltest.Chain()[2].DNS.Pin, Target: target, Interface: "lo", SourceIP: netip.MustParseAddr("127.0.0.1"), Probe: netip.MustParseAddrPort("127.0.0.1:12345"), ObservedAt: now, ExpiresAt: now.Add(time.Minute)}}
	if err := c.WriteJSON(report); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-sink.calls:
		if got != session {
			t.Fatal("lost authentication context")
		}
	case <-time.After(time.Second):
		t.Fatal("report not recorded")
	}
	// Consumed requests cannot be replayed, even with another envelope ID.
	if err := c.WriteJSON(report); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ReadMessage(); err == nil {
		t.Fatal("replayed observation accepted")
	}
	select {
	case <-sink.calls:
		t.Fatal("replay reached storage")
	default:
	}
}

// The clock is the external timing boundary; both reports still traverse real
// enrolled mTLS WebSockets. Expiry/replacement must not erase the new slot.
func TestV2LateObservationCannotConsumeReplacement(t *testing.T) {
	f := wsSetup(t)
	f.control.o.EnableV2, f.control.o.RequireRuntime = true, true
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	f.control.o.Now = func() time.Time { return time.Unix(0, clock.Load()) }
	sink := &observationSink{calls: make(chan domain.ID, 2)}
	f.control.o.Observations = sink
	c, session := f.dialV2(t)
	hello := f.v2Envelope("hello", session)
	hello.RecoveryFormat = 2
	hello.Hello = &protocol.Hello{NodeID: f.materials.NodeID, Runtime: true, RevocationEpoch: 1, PendingResults: []protocol.Result{}}
	if err := c.WriteJSON(hello); err != nil {
		t.Fatal(err)
	}
	limit := time.Now().Add(time.Second)
	for !f.control.RuntimeConnected(f.materials.NodeID) {
		if time.Now().After(limit) {
			t.Fatal("not ready")
		}
		time.Sleep(time.Millisecond)
	}
	target := controltest.Pin("target", "approved")
	old := protocol.ObservationRequestV2{ID: controltest.ID("observation"), Deadline: f.control.o.Now().Add(time.Second), Target: &target}
	if err := f.control.ObserveV2(context.Background(), f.materials.NodeID, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	clock.Add(int64(2 * time.Second))
	next := old
	next.ID, _ = domain.NewID("observation")
	next.Deadline = f.control.o.Now().Add(10 * time.Second)
	if err := f.control.ObserveV2(context.Background(), f.materials.NodeID, next); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	now := f.control.o.Now()
	report := f.v2Envelope("observation", session)
	report.ObservationReport = &protocol.ObservationReportV2{ID: old.ID, Egress: &protocol.EgressObservationV2{NodeID: f.materials.NodeID, NodeRevision: 1, DNS: controltest.Chain()[2].DNS.Pin, Target: target, Interface: "lo", SourceIP: netip.MustParseAddr("127.0.0.1"), Probe: netip.MustParseAddrPort("127.0.0.1:12345"), ObservedAt: now, ExpiresAt: now.Add(time.Minute)}}
	if err := c.WriteJSON(report); err != nil {
		t.Fatal(err)
	}
	report.ObservationReport.ID = next.ID
	if err := c.WriteJSON(report); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-sink.calls:
		if got != session {
			t.Fatal("wrong session")
		}
	case <-time.After(time.Second):
		t.Fatal("late reply consumed replacement or disconnected session")
	}
	if err := f.control.ObserveV2(context.Background(), f.materials.NodeID, next); err != nil {
		t.Fatal("replacement slot not released", err)
	}
	if _, _, err := c.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if err := f.control.ObserveV2(context.Background(), f.materials.NodeID, old); err == nil {
		t.Fatal("overlap not bounded", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.control.ObserveV2(cancelled, f.materials.NodeID, old); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled scheduler submitted work", err)
	}
}

func TestV2ObservationFailureRequiresNegotiationAndReleasesSlot(t *testing.T) {
	for _, selected := range []string{protocol.SubprotocolV2, protocol.SubprotocolV2ObservationStatus} {
		t.Run(selected, func(t *testing.T) {
			f := wsSetup(t)
			f.control.o.EnableV2, f.control.o.RequireRuntime = true, true
			sink := &observationSink{calls: make(chan domain.ID, 2)}
			f.control.o.Observations = sink
			c, session := f.dialV2Protocol(t, selected)
			hello := f.v2Envelope("hello", session)
			hello.RecoveryFormat = 2
			hello.Hello = &protocol.Hello{NodeID: f.materials.NodeID, Runtime: true, RevocationEpoch: 1, PendingResults: []protocol.Result{}}
			if err := c.WriteJSON(hello); err != nil {
				t.Fatal(err)
			}
			limit := time.Now().Add(time.Second)
			for !f.control.RuntimeConnected(f.materials.NodeID) {
				if time.Now().After(limit) {
					t.Fatal("not ready")
				}
				time.Sleep(time.Millisecond)
			}
			target := controltest.Pin("target", "failed-credentials")
			req := protocol.ObservationRequestV2{ID: controltest.ID("observation"), Deadline: time.Now().Add(10 * time.Second), Target: &target}
			if err := f.control.ObserveV2(context.Background(), f.materials.NodeID, req); err != nil {
				t.Fatal(err)
			}
			if _, _, err := c.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			failure := f.v2Envelope("observation", session)
			failure.ObservationReport = &protocol.ObservationReportV2{ID: req.ID, Failure: &protocol.ObservationFailureV2{Code: "OBSERVATION_UNAVAILABLE"}}
			if err := c.WriteJSON(failure); err != nil {
				t.Fatal(err)
			}
			if selected == protocol.SubprotocolV2 {
				if _, _, err := c.ReadMessage(); err == nil {
					t.Fatal("base V2 accepted extension")
				}
				return
			}
			// Retry the same failed binding only after bounded backoff. A different
			// inventory/target binding remains immediately schedulable.
			for {
				err := f.control.ObserveV2(context.Background(), f.materials.NodeID, req)
				if errors.Is(err, ErrObservationBackoff) {
					break
				}
				if !errors.Is(err, ErrObservationBusy) || time.Now().After(limit) {
					t.Fatal("failed binding not backed off", err)
				}
				time.Sleep(time.Millisecond)
			}
			// The failure needs neither a disconnect nor the original 15-second wait.
			next := req
			next.Target = nil
			next.ID, _ = domain.NewID("observation")
			for {
				err := f.control.ObserveV2(context.Background(), f.materials.NodeID, next)
				if err == nil {
					break
				}
				if !errors.Is(err, ErrObservationBusy) || time.Now().After(limit) {
					t.Fatal("failure retained slot", err)
				}
				time.Sleep(time.Millisecond)
			}
			if _, _, err := c.ReadMessage(); err != nil {
				t.Fatal("failure disconnected session", err)
			}
			// A stale failure is not permission to release its replacement.
			if err := c.WriteJSON(failure); err != nil {
				t.Fatal(err)
			}
			// A following exact report establishes processing order on the real wire.
			failure.ObservationReport.ID = next.ID
			if err := c.WriteJSON(failure); err != nil {
				t.Fatal(err)
			}
			final := req
			currentTarget := controltest.Pin("target", "new-credentials")
			final.Target = &currentTarget
			final.ID, _ = domain.NewID("observation")
			for {
				err := f.control.ObserveV2(context.Background(), f.materials.NodeID, final)
				if err == nil {
					break
				}
				if !errors.Is(err, ErrObservationBusy) || time.Now().After(limit) {
					t.Fatal(err)
				}
				time.Sleep(time.Millisecond)
			}
			if _, _, err := c.ReadMessage(); err != nil {
				t.Fatal(err)
			}
			select {
			case <-sink.calls:
				t.Fatal("failed observation reached evidence store")
			default:
			}
		})
	}
}
