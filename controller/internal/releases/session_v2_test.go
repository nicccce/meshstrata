package releases

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestV2ObservationPersistenceRechecksSessionInsideTransaction(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for n := range j.reports {
		node = n
	}
	r := j.reports[node]
	v := control.InventoryV2{NodeID: node, NodeRevision: r.NodeRevision, DNS: r.DNS, Loops: r.Loops, ObservedAt: r.ObservedAt, ExpiresAt: r.ExpiresAt}
	for _, i := range r.Interfaces {
		v.Interfaces = append(v.Interfaces, control.InterfaceObservationV2{Name: i.Name, Address: i.Address, Up: i.Up})
	}
	for _, s := range r.Sockets {
		v.Sockets = append(v.Sockets, control.PinnedSocketSpecV2{Protocol: s.Protocol, Pin: s.Pin, Spec: s.Spec, PoolFirst: s.PoolFirst, PoolLast: s.PoolLast})
	}
	for _, c := range r.Capabilities {
		v.Capabilities = append(v.Capabilities, control.ProtocolCapabilityV2{Available: c.Available, Protocol: c.Protocol, BusinessUDP: c.BusinessUDP})
	}
	report := control.ObservationReportV2{ID: idFor(t, "observation"), Inventory: &v}
	current, next := idFor(t, "session"), idFor(t, "session")
	j.s.options.V2Session = func(domain.ID) V2Session { return V2Session{SessionID: string(current), RecoveryFormat: 2} }
	if err := j.s.RecordObservation(j.ctx, node, report); err == nil {
		t.Fatal("report without authenticated context")
	}
	must(t, j.s.RecordObservation(control.WithSession(j.ctx, current), node, report))
	calls := 0
	j.s.options.V2Session = func(domain.ID) V2Session {
		calls++
		if calls > 1 {
			return V2Session{SessionID: string(next), RecoveryFormat: 2}
		}
		return V2Session{SessionID: string(current), RecoveryFormat: 2}
	}
	if err := j.s.RecordObservation(control.WithSession(j.ctx, current), node, report); err == nil || calls < 2 {
		t.Fatal("session changed before commit but observation persisted", err)
	}
}

func TestV2GatewayCurrentSessionRequiredForFetchAndRecoveringOutbox(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for n := range j.reports {
		node = n
	}
	pin, err := j.routes.SaveDraft(j.ctx, domain.Mutation{IdempotencyKey: "session-draft"}, j.draft("session", []domain.ID{node}))
	must(t, err)
	accepted, err := j.routes.PublishDraft(j.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "session-publish"}, pin, j.now.Add(time.Minute))
	must(t, err)
	sent := j.dispatch()
	if len(sent) != 1 {
		t.Fatal("no command")
	}
	c := sent[0]
	current := idFor(t, "session")
	old := idFor(t, "session")
	j.s.options.V2Session = func(domain.ID) V2Session { return V2Session{SessionID: string(current), RecoveryFormat: 2} }
	key, err := j.s.Resolve(control.WithSession(j.ctx, current), node, accepted.TaskID)
	must(t, err)
	if _, err := j.s.Open(control.WithSession(j.ctx, old), node, key); err == nil {
		t.Fatal("stale peer fetched private artifact")
	}
	source, err := j.s.Open(control.WithSession(j.ctx, current), node, key)
	must(t, err)
	source.Close()
	result := j.result(c, 1, true)
	result.EffectiveDigest = strings.Repeat("d", 64)
	// Reconnection has authenticated and negotiated recovery, but has not yet
	// finished Hello. It must still be able to replay its original durable outbox.
	j.s.options.Connected = func(domain.ID) bool { return false }
	fresh, err := j.s.RecordResult(control.WithSession(j.ctx, current), node, result)
	must(t, err)
	if !fresh {
		t.Fatal("recovering peer could not persist pending result")
	}
	current = idFor(t, "session")
	if _, err := j.s.RecordResult(control.WithSession(context.Background(), old), node, result); err == nil {
		t.Fatal("stale peer received a duplicate result receipt")
	}
}
