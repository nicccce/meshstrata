// Package ports checks the fixed outer bindings reserved in the Controller ledger.
package ports

import (
	"context"
	"net/netip"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
)

type Binding struct {
	Address   netip.Addr
	Port      uint16
	Transport string
}

func (b Binding) Valid() bool {
	return b.Address.IsValid() && b.Address.Zone() == "" && !b.Address.Is4In6() &&
		!b.Address.IsMulticast() && b.Port != 0 && (b.Transport == "tcp" || b.Transport == "udp")
}

func (b Binding) Conflicts(other Binding) bool {
	if b.Port != other.Port || b.Transport != other.Transport {
		return false
	}
	a, c := b.Address, other.Address
	return a == c || a == netip.IPv6Unspecified() || c == netip.IPv6Unspecified() ||
		a.Is4() == c.Is4() && (a.IsUnspecified() || c.IsUnspecified())
}

// Owner permits only an exact reuse of an existing format-1 reservation.
// Legacy writers pass an empty owner when allocating a fresh binding.
type Owner struct {
	Listener string
	Identity string
}

func Parse(address string, port int64, transport string) Binding {
	if port < 1 || port > 65535 {
		return Binding{}
	}
	ip, _ := netip.ParseAddr(address)
	return Binding{Address: ip, Port: uint16(port), Transport: transport}
}

// CheckAvailable must run in the same storage write transaction as allocation.
// Released rows are excluded in SQL. Both ledgers cover active and pending ports.
func CheckAvailable(ctx context.Context, q *dbgen.Queries, node string, want Binding, owner Owner) (bool, error) {
	if !want.Valid() {
		return false, &domain.Problem{Code: domain.InvalidArgument}
	}
	rows, err := q.B04PortCandidates(ctx, dbgen.B04PortCandidatesParams{NodeID: node, Port: int64(want.Port)})
	if err != nil {
		return false, err
	}
	reusable := false
	for _, row := range rows {
		old := Parse(row.Address, int64(want.Port), row.Transport)
		if old.Valid() && !want.Conflicts(old) {
			continue
		}
		if owner.Identity != "" && row.IdentityID == owner.Identity && row.ListenerID == owner.Listener && old == want {
			reusable = true
			continue
		}
		return false, &domain.Problem{Code: domain.PortInUse}
	}
	return reusable, nil
}
