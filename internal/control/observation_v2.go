package control

import (
	"net/netip"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
)

// Observation requests select locally approved socket/DNS contents. Proxy auth
// is delivered only over the authenticated node channel; probe allowlists stay local.
type ObservationRequestV2 struct {
	Preparation *PreparationV2 `json:"preparation,omitempty"`
	ID          domain.ID      `json:"id"`
	Deadline    time.Time      `json:"deadline"`
	Target      *Pin           `json:"target,omitempty"` // nil requests fresh inventory
}

// PreparationV2 selects only already locally approved DNS and socket contents.
// NodeRevision is a CAS fence, never an observation of host configuration.
type PreparedTargetV2 struct {
	Pin  Pin          `json:"pin"`
	Spec EgressTarget `json:"spec"`
	Auth *EgressAuth  `json:"auth,omitempty"`
}
type PreparationV2 struct {
	Target       *PreparedTargetV2    `json:"target,omitempty"`
	NodeRevision domain.Revision      `json:"node_revision"`
	DNS          DNSInput             `json:"dns"`
	Sockets      []PinnedSocketSpecV2 `json:"sockets"`
}

func (p PreparationV2) Valid() bool {
	if p.Target != nil && (!p.Target.Pin.Valid() || !p.Target.Spec.Valid()) {
		return false
	}
	if !p.NodeRevision.Valid() || !p.DNS.Pin.Valid() || len(p.Sockets) < 1 || len(p.Sockets) > 32 {
		return false
	}
	for _, s := range p.Sockets {
		if !s.Pin.Valid() || s.PoolLast < s.PoolFirst || s.PoolFirst == 0 && s.PoolLast != 0 {
			return false
		}
	}
	return true
}

type ObservationReportV2 struct {
	ID        domain.ID             `json:"id"`
	Inventory *InventoryV2          `json:"inventory,omitempty"`
	Egress    *EgressObservationV2  `json:"egress,omitempty"`
	Failure   *ObservationFailureV2 `json:"failure,omitempty"`
}

type ObservationFailureV2 struct {
	Code string `json:"code"`
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
	return (r.Preparation == nil || r.Preparation.Valid()) && r.ID.Valid() && !r.Deadline.IsZero() && (r.Target == nil || r.Target.Valid())
}
func (r ObservationReportV2) Valid(node domain.ID) bool {
	if r.Failure != nil {
		if !node.Valid() || !r.ID.Valid() || r.Inventory != nil || r.Egress != nil {
			return false
		}
		switch r.Failure.Code {
		case "OBSERVATION_UNAVAILABLE", "OBSERVATION_EXPIRED", "OBSERVATION_CANCELLED":
			return true
		}
		return false
	}
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
