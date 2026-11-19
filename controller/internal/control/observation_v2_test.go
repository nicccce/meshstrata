package control

import (
	"context"
	"net/netip"
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
