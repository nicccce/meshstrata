package control

import (
	"net/netip"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
)

// Observation requests carry references only. Socket/DNS configuration, private
// proxy credentials and numeric probe allowlists come from local provisioning.
type ObservationRequestV2 struct {
	ID       domain.ID `json:"id"`
	Deadline time.Time `json:"deadline"`
	Target   *Pin      `json:"target,omitempty"` // nil requests fresh inventory
}
type ObservationReportV2 struct {
	ID        domain.ID            `json:"id"`
	Inventory *InventoryV2         `json:"inventory,omitempty"`
	Egress    *EgressObservationV2 `json:"egress,omitempty"`
}
type InterfaceObservationV2 struct {
	Name    string     `json:"name"`
	Address netip.Addr `json:"address"`
	Up      bool       `json:"up"`
}
type PinnedSocketSpecV2 struct {
	Protocol  domain.Protocol `json:"protocol"`
	Pin       Pin             `json:"pin"`
	Spec      SocketSpec      `json:"spec"`
	PoolFirst uint16          `json:"pool_first"`
	PoolLast  uint16          `json:"pool_last"`
}
type ProtocolCapabilityV2 struct {
	Available   bool                `json:"available"`
	Protocol    domain.Protocol     `json:"protocol"`
	BusinessUDP domain.Verification `json:"business_udp"`
}
type InventoryV2 struct {
	Interfaces   []InterfaceObservationV2 `json:"interfaces"`
	NodeID       domain.ID                `json:"node_id"`
	NodeRevision domain.Revision          `json:"node_revision"`
	DNS          DNSInput                 `json:"dns"`
	Loops        LoopInventory            `json:"loops"`
	Sockets      []PinnedSocketSpecV2     `json:"sockets"`
	Capabilities []ProtocolCapabilityV2   `json:"capabilities"`
	ObservedAt   time.Time                `json:"observed_at"`
	ExpiresAt    time.Time                `json:"expires_at"`
}
type EgressObservationV2 struct {
	NodeID       domain.ID       `json:"node_id"`
	NodeRevision domain.Revision `json:"node_revision"`
	DNS          Pin             `json:"dns"`
	Target       Pin             `json:"target"`
	Interface    string          `json:"interface"`
	SourceIP     netip.Addr      `json:"source_ip"`
	Probe        netip.AddrPort  `json:"probe"`
	ObservedAt   time.Time       `json:"observed_at"`
	ExpiresAt    time.Time       `json:"expires_at"`
	BusinessUDP  bool            `json:"business_udp"`
}

func (r ObservationRequestV2) Valid() bool {
	return r.ID.Valid() && !r.Deadline.IsZero() && (r.Target == nil || r.Target.Valid())
}
func (r ObservationReportV2) Valid(node domain.ID) bool {
	if !r.ID.Valid() || (r.Inventory == nil) == (r.Egress == nil) {
		return false
	}
	if i := r.Inventory; i != nil {
		for _, c := range i.Capabilities {
			if c.BusinessUDP != domain.NotChecked {
				return false
			}
		}
		return i.NodeID == node && i.NodeRevision.Valid() && i.DNS.Pin.Valid() && len(i.Interfaces) > 0 && len(i.Interfaces) <= 512 && len(i.Sockets) > 0 && len(i.Sockets) <= 32 && len(i.Capabilities) > 0 && len(i.Capabilities) <= 4 && len(i.Loops.LocalAddresses) > 0 && len(i.Loops.LocalAddresses) <= 512 && i.Loops.ProtectedEndpoints != nil && len(i.Loops.ProtectedEndpoints) <= 1024 && i.Loops.ProtectedNetworks != nil && len(i.Loops.ProtectedNetworks) <= 256 && i.Loops.ProtectedInterfaces != nil && len(i.Loops.ProtectedInterfaces) <= 128 && !i.ObservedAt.IsZero() && i.ExpiresAt.After(i.ObservedAt)
	}
	e := r.Egress
	return e.NodeID == node && e.NodeRevision.Valid() && e.DNS.Valid() && e.Target.Valid() && e.Interface != "" && len(e.Interface) <= 15 && e.SourceIP.IsValid() && e.Probe.IsValid() && e.Probe.Port() != 0 && !e.ObservedAt.IsZero() && e.ExpiresAt.After(e.ObservedAt) && !e.BusinessUDP
}
