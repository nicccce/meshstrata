package releases

import (
	"context"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

// RecordObservation is the authenticated control adapter. A report never proves
// recovery capability and is accepted only under the current V2 connection.
func (s *Service) RecordObservation(ctx context.Context, node domain.ID, report control.ObservationReportV2) error {
	if control.SessionFromContext(ctx) == "" || s.checkObservationSession(ctx, node) != nil || !report.Valid(node) {
		return problem(domain.RevisionConflict)
	}
	if v := report.Inventory; v != nil {
		r := InventoryReport{NodeID: node, NodeRevision: v.NodeRevision, DNS: v.DNS, Loops: v.Loops, ObservedAt: v.ObservedAt, ExpiresAt: v.ExpiresAt}
		for _, i := range v.Interfaces {
			r.Interfaces = append(r.Interfaces, InterfaceObservation{Name: i.Name, Address: i.Address, Up: i.Up})
		}
		for _, p := range v.Sockets {
			r.Sockets = append(r.Sockets, stage3.PinnedSocketSpec{Protocol: p.Protocol, Pin: p.Pin, Spec: p.Spec, PoolFirst: p.PoolFirst, PoolLast: p.PoolLast})
		}
		for _, c := range v.Capabilities {
			r.Capabilities = append(r.Capabilities, stage3.ProtocolCapability{Available: c.Available, Protocol: c.Protocol, BusinessUDP: c.BusinessUDP})
		}
		return s.RecordInventory(ctx, r)
	}
	v := report.Egress
	return s.RecordEgressObservation(ctx, EgressObservation{NodeID: node, NodeRevision: v.NodeRevision, DNS: v.DNS, Target: v.Target, Interface: v.Interface, SourceIP: v.SourceIP, Probe: v.Probe, ObservedAt: v.ObservedAt, ExpiresAt: v.ExpiresAt, BusinessUDP: v.BusinessUDP})
}

func (s *Service) checkObservationSession(ctx context.Context, node domain.ID) error {
	if id := control.SessionFromContext(ctx); id != "" && s.v2Session(node) != string(id) {
		return problem(domain.RevisionConflict)
	}
	return ctx.Err()
}
