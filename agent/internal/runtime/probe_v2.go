package runtime

import (
	"context"

	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
)

// ForwardFactoryV2 uses real authenticated end-to-end traffic through the
// compiled route. A policy that does not permit the canary stays unverified;
// network failure is never reported as successful REJECT or egress evidence.
func (p *Probes) ForwardFactoryV2(ctx context.Context, b control.BundleV2, _ mihomo.Candidate) (mihomo.Probe, error) {
	if b.Validate() != nil {
		return nil, errProbe
	}
	out := &forwardProbe{p: p, canaries: p.options.V2Canaries, listeners: map[string]control.BundleListener{}}
	for _, l := range b.Listeners {
		out.listeners[string(l.Socket.ListenerID)] = transportV2(b.NodeID, l)
	}
	return out, nil
}
