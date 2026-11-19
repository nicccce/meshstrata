package runtime

import (
	"context"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"sort"

	"github.com/nicccce/meshstrata/internal/control"
)

// ObserveLoopInventory adds actual host interfaces to trusted topology. The
// caller must provide protected control/mesh/API endpoints; an omitted topology
// is an error, not permission to treat an empty inventory as production evidence.
func ObserveLoopInventory(ctx context.Context, topology control.LoopInventory) (control.LoopInventory, error) {
	if topology.ProtectedEndpoints == nil || topology.ProtectedNetworks == nil || topology.ProtectedInterfaces == nil {
		return control.LoopInventory{}, ErrUnavailable
	}
	out := control.LoopInventory{LocalAddresses: []netip.Addr{}, ProtectedEndpoints: append([]netip.AddrPort{}, topology.ProtectedEndpoints...), ProtectedNetworks: append([]netip.Prefix{}, topology.ProtectedNetworks...), ProtectedInterfaces: append([]string{}, topology.ProtectedInterfaces...)}
	interfaces, err := net.Interfaces()
	if err != nil {
		return control.LoopInventory{}, ErrUnavailable
	}
	for _, i := range interfaces {
		if err := ctx.Err(); err != nil {
			return control.LoopInventory{}, err
		}
		addresses, err := i.Addrs()
		if err != nil {
			return control.LoopInventory{}, ErrUnavailable
		}
		tun := i.Flags&net.FlagPointToPoint != 0
		if _, err := os.Stat(filepath.Join("/sys/class/net", i.Name, "tun_flags")); err == nil {
			tun = true
		} else if !os.IsNotExist(err) {
			return control.LoopInventory{}, ErrUnavailable
		}
		if tun {
			out.ProtectedInterfaces = append(out.ProtectedInterfaces, i.Name)
		}
		for _, a := range addresses {
			prefix, err := netip.ParsePrefix(a.String())
			if err != nil {
				return control.LoopInventory{}, ErrUnavailable
			}
			ip := prefix.Addr().Unmap()
			if !ip.IsUnspecified() && !ip.IsMulticast() {
				out.LocalAddresses = append(out.LocalAddresses, ip)
			}
			if tun {
				out.ProtectedNetworks = append(out.ProtectedNetworks, prefix.Masked())
			}
		}
	}
	if len(out.LocalAddresses) == 0 {
		return control.LoopInventory{}, ErrUnavailable
	}
	sort.Slice(out.LocalAddresses, func(i, j int) bool { return out.LocalAddresses[i].Less(out.LocalAddresses[j]) })
	return out, nil
}

func (r *Runtime) loopInventory(ctx context.Context) (control.LoopInventory, error) {
	inv, err := r.o.Inventory(ctx)
	if err != nil {
		return control.LoopInventory{}, err
	}
	status, err := r.o.Process.Status(ctx)
	if err != nil {
		return control.LoopInventory{}, err
	}
	if status.Running {
		// Inspect uses the owned PID, executable, boot/start identity and /proc FD
		// inventory. Empty expected listeners still returns all actual owned sockets.
		ev, err := r.o.Kernel.Inspect(ctx, r.empty, status.PID)
		if err != nil || r.apiOwned(ctx, status.PID) != nil {
			return control.LoopInventory{}, ErrVerification
		}
		for _, s := range ev.OwnedSockets {
			ip, err := netip.ParseAddr(s.Address)
			if err != nil {
				return control.LoopInventory{}, ErrVerification
			}
			if !ip.IsUnspecified() {
				inv.ProtectedEndpoints = append(inv.ProtectedEndpoints, netip.AddrPortFrom(ip.Unmap(), s.Port))
			} else {
				inv.ProtectedEndpoints = append(inv.ProtectedEndpoints, netip.AddrPortFrom(ip, s.Port))
				// Unknown IPV6_V6ONLY must conservatively protect IPv4 aliases too.
				if ip.Is6() && (s.IPv6Only == nil || !*s.IPv6Only) {
					inv.ProtectedEndpoints = append(inv.ProtectedEndpoints, netip.AddrPortFrom(netip.IPv4Unspecified(), s.Port))
				}
			}
		}
	}
	return inv, nil
}
