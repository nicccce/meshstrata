package runtime

import (
	"context"
	"errors"
	"net"
	"syscall"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
)

type revocationKey struct{}

type revocationPlan struct {
	removed, current []control.BundleListener
	kernel           Kernel
	restarted        bool
	currentVerified  bool
	captured         map[string]string
}

func (p *revocationPlan) capture(ctx context.Context, old rendered) error {
	p.captured = map[string]string{}
	listeners := map[string]bool{}
	for _, l := range old.bundle.Listeners {
		listeners[string(l.ListenerID)] = true
	}
	cs, err := p.kernel.Connections(ctx)
	if err != nil {
		return err
	}
	for _, c := range cs {
		if !listeners[c.ListenerID] {
			return ErrVerification
		}
		if p.removes(c.ListenerID) {
			p.captured[c.ID] = c.ListenerID
		}
	}
	return nil
}

func (p *revocationPlan) removes(listener string) bool {
	for _, l := range p.removed {
		if string(l.ListenerID) == listener {
			return true
		}
	}
	return false
}

func (p *revocationPlan) clean(ctx context.Context, old rendered) error {
	// With a restarted, verified managed PID, old process connections cannot
	// survive the stop. The other listeners are checked by fresh forwarding.
	if p.restarted {
		return nil
	}
	for range 4 {
		conns, err := p.kernel.Connections(ctx)
		if err != nil {
			return err
		}
		remaining := false
		for _, c := range conns {
			if !p.removes(c.ListenerID) {
				continue
			}
			remaining = true
			p.captured[c.ID] = c.ListenerID
			if err := p.kernel.CloseConnection(ctx, old.Candidate, c.ListenerID, c.ID); err != nil {
				return err
			}
		}
		if !remaining {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(25 * time.Millisecond):
			}
			conns, err = p.kernel.Connections(ctx)
			if err != nil {
				return err
			}
			stillOpen := false
			for _, c := range conns {
				stillOpen = stillOpen || p.removes(c.ListenerID)
			}
			if !stillOpen {
				return nil
			}
		}
	}
	return ErrVerification
}

type revocationProbe struct {
	p    *Probes
	plan *revocationPlan
}

func (p *Probes) RevocationFactory(ctx context.Context, _ control.Bundle, _ mihomo.Candidate) (mihomo.RevocationProbe, error) {
	plan, ok := ctx.Value(revocationKey{}).(*revocationPlan)
	if !ok || plan == nil || len(plan.removed) == 0 {
		return nil, errProbe
	}
	return &revocationProbe{p: p, plan: plan}, nil
}

func (p *revocationProbe) OldAuthenticationDenied(ctx context.Context) mihomo.Check {
	for _, old := range p.plan.removed {
		var replacement *control.BundleListener
		for i := range p.plan.current {
			if endpointShared(old, p.plan.current[i]) {
				replacement = &p.plan.current[i]
				break
			}
		}
		if replacement == nil {
			// After removal no listener remains to authenticate. Require an
			// explicit TCP/UDP port refusal in addition to the exact socket check.
			if !refusedEndpoint(ctx, old) {
				return check(mihomo.Unverified)
			}
			continue
		}
		if old.Protocol != replacement.Protocol || old.Protocol == "" {
			return check(mihomo.Unverified)
		}
		target, closeTarget, err := echoTarget(ctx, "tcp")
		if err != nil {
			return check(mihomo.Unverified)
		}
		observation := p.p.observe(ctx, old, target, "tcp", false)
		closeTarget()
		switch observation {
		case denied:
		case forwarded:
			return check(mihomo.Failed)
		default:
			return check(mihomo.Unverified)
		}
	}
	return check(mihomo.Succeeded)
}

func refusedEndpoint(ctx context.Context, l control.BundleListener) bool {
	target, err := localEndpoint(l)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer cancel()
	if l.Transport != "udp" {
		c, err := (&net.Dialer{}).DialContext(ctx, "tcp", target)
		if err == nil {
			c.Close()
			return false
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return false
		}
	}
	if l.Transport == "tcp" {
		return true
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "udp", target)
	if err != nil {
		return false
	}
	defer c.Close()
	deadline, _ := ctx.Deadline()
	_ = c.SetDeadline(deadline)
	if _, err = c.Write([]byte{0}); err != nil {
		return errors.Is(err, syscall.ECONNREFUSED)
	}
	var reply [1]byte
	_, err = c.Read(reply[:])
	return errors.Is(err, syscall.ECONNREFUSED)
}

func (p *revocationProbe) ExistingSessionsClosed(ctx context.Context) mihomo.Check {
	cs, err := p.plan.kernel.Connections(ctx)
	if err != nil {
		return check(mihomo.Unverified)
	}
	retained := make(map[string]bool, len(p.plan.current))
	for _, listener := range p.plan.current {
		retained[string(listener.ListenerID)] = true
	}
	for _, c := range cs {
		if p.plan.captured[c.ID] != "" {
			return check(mihomo.Failed)
		}
		if p.plan.removes(c.ListenerID) {
			return check(mihomo.Failed)
		}
		if !retained[c.ListenerID] {
			return check(mihomo.Unverified)
		}
	}
	return check(mihomo.Succeeded)
}

func (p *revocationProbe) OtherIdentityUnaffected(ctx context.Context) mihomo.Check {
	// The current candidate has already passed authenticated forwarding for
	// each retained listener. Session deletion only selected removed listeners.
	if ctx.Err() != nil || !p.plan.currentVerified {
		return check(mihomo.Unverified)
	}
	return check(mihomo.Succeeded)
}
