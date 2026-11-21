package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"golang.org/x/sys/unix"
)

type application struct {
	attempted bool
	verified  bool
	removed   []control.BundleListener
}

func (r *Runtime) apiOwned(ctx context.Context, pid int) error {
	if _, production := r.o.Kernel.(*mihomo.Adapter); !production {
		return nil
	}
	state, err := r.o.Process.Status(ctx)
	if err != nil || !state.Running || state.PID != pid {
		return ErrVerification
	}
	c, err := (&net.Dialer{}).DialContext(ctx, "unix", r.o.SocketPath)
	if err != nil {
		return ErrVerification
	}
	defer c.Close()
	raw, err := c.(*net.UnixConn).SyscallConn()
	if err != nil {
		return ErrVerification
	}
	var cred *unix.Ucred
	var sockErr error
	err = raw.Control(func(fd uintptr) {
		cred, sockErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	})
	if err != nil || sockErr != nil || cred == nil || int(cred.Pid) != pid {
		return ErrVerification
	}
	return nil
}

func (r *Runtime) liveRender(ctx context.Context, s control.DurableState) (rendered, error) {
	status, err := r.o.Process.Status(ctx)
	if err != nil || !status.Running {
		return rendered{}, err
	}
	cp := s.Checkpoint
	// A crash after a kernel mutation but before Applied is ambiguous.
	// Recovery stops the managed instance before using another snapshot.
	if cp.Applied == nil || cp.Candidate == nil || *cp.Applied != *cp.Candidate {
		return rendered{}, errUncertain
	}
	return r.checkedRender(ctx, cp.Applied)
}

func removals(old, next []control.BundleListener, pending []restriction) []control.BundleListener {
	active := map[domain.ID]bool{}
	for _, l := range next {
		active[physicalID(l)] = true
	}
	var removed []control.BundleListener
	add := func(l control.BundleListener) {
		id := physicalID(l)
		if !active[id] {
			removed = append(removed, l)
			active[id] = true
		}
	}
	for _, l := range old {
		add(l)
	}
	for _, item := range pending {
		add(item.listener())
	}
	return removed
}

func (r *Runtime) apply(ctx context.Context, snap *control.Snapshot, old, next rendered, pending []restriction) (application, error) {
	out := application{removed: removals(old.listeners(), next.listeners(), pending)}
	if _, err := r.o.Kernel.Validate(ctx, next.Candidate); err != nil {
		return out, errors.Join(ErrVerification, err)
	}
	if next.v2 != nil {
		inv, err := r.loopInventory(ctx)
		if err != nil || next.v2.CheckEgressLoops(inv) != nil {
			return out, ErrVerification
		}
	}
	state, err := r.o.Process.Status(ctx)
	if err != nil {
		return out, err
	}
	plan := &revocationPlan{removed: out.removed, current: next.listeners(), kernel: r.o.Kernel}
	var proof mihomo.RevocationProbe
	if len(out.removed) > 0 {
		proof, err = r.o.RevocationProbeFactory(context.WithValue(ctx, revocationKey{}, plan), next.bundle, next.Candidate)
		if err != nil || proof == nil {
			return out, ErrVerification
		}
	}
	if state.Running {
		if err = r.apiOwned(ctx, state.PID); err != nil {
			return out, err
		}
		forceStop := old.Path == ""
		if len(out.removed) > 0 && !forceStop {
			if err = plan.capture(ctx, old); err != nil {
				forceStop = true
			}
			for _, l := range out.removed {
				for _, n := range next.listeners() {
					if l.ListenerID == n.ListenerID {
						forceStop = true
					}
				}
			}
		}
		if forceStop {
			if err = r.stopKernel(); err != nil {
				return out, errors.Join(errUncertain, err)
			}
			state.Running = false
			plan.restarted = true
		}
	}
	if err := r.save(ctx, func(s *control.DurableState) error {
		now := authorization.Clock(*s, r.o.Now())
		if authorization.Due(*s, now) {
			return errRestricted
		}
		if snap != nil && s.RevocationEpoch != snap.Epoch {
			return errRestricted
		}
		s.Checkpoint.Verified = nil
		s.Checkpoint.Candidate = snap
		return nil
	}); err != nil {
		return out, err
	}
	out.attempted = true
	var attemptErr error
	if state.Running {
		var applied mihomo.Applied
		applied, attemptErr = r.o.Kernel.Reload(ctx, next.Candidate)
		if attemptErr == nil && (!applied.APIAccepted || applied.Digest != next.Digest) {
			attemptErr = ErrVerification
		}
	} else {
		plan.restarted = true
		var executable string
		var args []string
		executable, args, attemptErr = r.o.Kernel.Startup(ctx, next.Candidate)
		if attemptErr == nil {
			state.PID, attemptErr = r.o.Process.Start(ctx, executable, args)
		}
	}
	if err := r.save(ctx, func(s *control.DurableState) error {
		s.Checkpoint.Applied = snap
		return nil
	}); err != nil {
		return out, err
	}
	if attemptErr != nil {
		return out, errors.Join(ErrVerification, attemptErr)
	}
	if err = r.waitInspection(ctx, next.Candidate, state.PID); err != nil {
		return out, err
	}
	if len(out.removed) > 0 {
		if err = plan.clean(ctx, old); err != nil {
			return out, err
		}
	}
	if len(next.Expected) > 0 {
		if err := r.verifyActive(ctx, next, state.PID); err != nil {
			return out, err
		}
		out.verified = true
	}
	if len(out.removed) > 0 {
		plan.currentVerified = out.verified || len(next.Expected) == 0
		evidence, err := r.o.Kernel.Revocation(ctx, next.Candidate, proof)
		if err != nil || !evidence.Verified || evidence.Digest != next.Digest || evidence.CheckedAt.IsZero() {
			out.verified = false
			return out, ErrVerification
		}
		// A zero-listener configuration is confirmed from real revocation
		// and owned-socket evidence; Verify intentionally requires listeners.
		out.verified = true
	}
	return out, nil
}

// Retained identities already passed an independent negative control when
// published. Require fresh positive traffic after filtering, and separately
// prove every removed identity denied on a new transport.
func retainedVerification(ctx context.Context, previous, current rendered) context.Context {
	prior := make(map[domain.ID]control.BundleListener, len(previous.listeners()))
	for _, l := range previous.listeners() {
		prior[physicalID(l)] = l
	}
	retained := make(map[domain.ID]bool)
	for _, l := range current.listeners() {
		id := physicalID(l)
		if found, ok := prior[id]; ok && control.CheckGrant(control.Grant{Listener: found}, l) == nil {
			retained[id] = true
		}
	}
	return context.WithValue(ctx, retainedProbeKey{}, retained)
}

func (r *Runtime) verifyActive(ctx context.Context, next rendered, pid int) error {
	var probe mihomo.Probe
	var err error
	if next.v2 != nil {
		if r.o.ProbeFactoryV2 == nil {
			return ErrVerification
		}
		if observation := r.o.Observations; observation != nil {
			inventory, e := r.loopInventory(ctx)
			if e != nil {
				return ErrVerification
			}
			sockets := make([]control.SocketV2, 0, len(next.v2.Listeners))
			for _, listener := range next.v2.Listeners {
				sockets = append(sockets, listener.Socket)
			}
			for _, canary := range observation.Probes {
				target := control.EgressTarget{NodeID: r.o.NodeID, Type: "http", Host: canary.Addr().String(), DialIP: canary.Addr().String(), Port: canary.Port()}
				if control.CheckEgressTargetLoops(target, inventory, sockets) != nil {
					return ErrVerification
				}
			}
		}
		probe, err = r.o.ProbeFactoryV2(ctx, *next.v2, next.Candidate)
	} else {
		probe, err = r.o.ProbeFactory(ctx, next.bundle, next.Candidate)
	}
	if err != nil || probe == nil {
		return ErrVerification
	}
	ev, err := r.o.Kernel.Verify(ctx, next.Candidate, pid, probe)
	if err == nil && ev.Verified && ev.Digest == next.Digest && ev.PID == pid && r.apiOwned(ctx, pid) == nil {
		return nil
	}
	for _, listener := range ev.Listeners {
		if listener.Listening.Status != mihomo.Succeeded ||
			listener.AuthenticationTCP.Status != mihomo.Succeeded ||
			(listener.Expected.BusinessUDP && listener.BusinessUDP.Status != mihomo.Succeeded) {
			return fmt.Errorf("%w: %s listener check (%s, tcp=%s, udp=%s)", ErrVerification,
				listener.Expected.Protocol, listener.Listening.Status, listener.AuthenticationTCP.Status, listener.BusinessUDP.Status)
		}
	}
	return errors.Join(ErrVerification, err)
}

func (r *Runtime) waitInspection(ctx context.Context, c mihomo.Candidate, pid int) error {
	ctx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	defer cancel()
	for {
		code := "OWNED_API_UNAVAILABLE"
		if r.apiOwned(ctx, pid) == nil {
			code = "OWNED_SOCKET_INSPECTION_FAILED"
			ev, err := r.o.Kernel.Inspect(ctx, c, pid)
			if err == nil {
				if exactSockets(ev, c, pid) && r.apiOwned(ctx, pid) == nil {
					return nil
				}
				code = "OWNED_SOCKET_SET_MISMATCH"
				for _, listener := range ev.Listeners {
					switch listener.Listening.Code {
					case "OWNED_LISTENER_MISSING", "OWNED_IPV4_COVERAGE_UNKNOWN":
						code = listener.Listening.Code
					}
					if code != "OWNED_SOCKET_SET_MISMATCH" {
						break
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %s", ErrVerification, code)
		case <-time.After(30 * time.Millisecond):
		}
	}
}

func exactSockets(e mihomo.Evidence, c mihomo.Candidate, pid int) bool {
	if e.Digest != c.Digest || e.PID != pid || len(e.Listeners) != len(c.Expected) {
		return false
	}
	for i, l := range e.Listeners {
		if l.Expected != c.Expected[i] || l.Listening.Status != mihomo.Succeeded {
			return false
		}
	}
	for _, socket := range e.OwnedSockets {
		known := false
		for _, l := range c.Expected {
			if l.TCP && socket.Matches(l.Address, l.Port, "tcp") || l.UDP && socket.Matches(l.Address, l.Port, "udp") {
				known = true
				break
			}
		}
		// /proc labels both bound UDP listeners and unconnected egress
		// relay sockets as UDP 07. Retired UDP entrances are checked
		// independently after exact-ID cleanup with an explicit refusal.
		if !known && socket.Transport == "tcp" {
			return false
		}
	}
	return true
}

func (r *Runtime) reconcile(ctx context.Context, boot bool) error {
	wait := ctx
	var stopWait context.CancelFunc
	if !boot {
		wait, stopWait = context.WithTimeout(ctx, 500*time.Millisecond)
		defer stopWait()
	}
	if err := acquire(wait, r.kernelGate); err != nil {
		if ctx.Err() == nil && r.urgent.Load() {
			// A canceled candidate should promptly release the activation gate.
			// If it does not, stop only the supervised instance.
			if stopErr := r.stopKernel(); stopErr != nil {
				return errors.Join(errUncertain, stopErr)
			}
		}
		return err
	}
	defer release(r.kernelGate)
	r.urgent.Store(false)
	ctx, done := r.operation(ctx)
	defer done()
	s, pending, err := r.snapshotState(ctx)
	if err != nil {
		return errors.Join(errUncertain, err, r.stopKernel())
	}
	if !boot && len(pending) == 0 {
		return nil
	}
	return r.restore(ctx, s.Checkpoint.Startup, boot, false, false)
}

func (r *Runtime) restore(ctx context.Context, start *control.Snapshot, boot, rollback, discardAttempt bool) error {
	s, pending, err := r.snapshotState(ctx)
	if err != nil {
		return r.failedRestore(err)
	}
	var previous *control.Snapshot
	if s.Checkpoint.PreviousGood != nil && len(pending) > 0 {
		saved := *s.Checkpoint.PreviousGood
		filtered, err := r.filteredSnapshot(ctx, saved, s)
		if err != nil {
			return r.failedRestore(errors.Join(errUncertain, err))
		}
		saved.EffectiveDigest, saved.Epoch = filtered.Digest, s.RevocationEpoch
		previous = &saved
	}
	var next, startupRender rendered
	var snap *control.Snapshot
	if start == nil {
		if len(pending) == 0 {
			if err = r.stopKernel(); err != nil {
				return errors.Join(errUncertain, err)
			}
			if err := r.clearApplied(ctx); err != nil {
				return err
			}
			if rollback {
				return ErrVerification
			}
			return nil
		}
		next = rendered{Candidate: r.empty, bundle: denyBundle(r.o.NodeID)}
	} else {
		// The startup pointer must still identify the exact previously
		// rendered bytes. Rebuild from the authenticated original artifact.
		if startupRender, err = r.checkedRender(ctx, start); err != nil {
			return r.failedRestore(errors.Join(errUncertain, err))
		}
		next, err = r.filteredSnapshot(ctx, *start, s)
		if err != nil {
			return r.failedRestore(err)
		}
		value := *start
		value.Epoch, value.EffectiveDigest = s.RevocationEpoch, next.Digest
		snap = &value
	}
	if discardAttempt {
		if err := r.stopKernel(); err != nil {
			return errors.Join(errUncertain, err)
		}
	}
	old, err := r.liveRender(ctx, s)
	if err != nil {
		if stopErr := r.stopKernel(); stopErr != nil {
			return errors.Join(errUncertain, err, stopErr)
		}
		if boot && !errors.Is(err, errUncertain) {
			return r.failedRestore(errors.Join(errUncertain, err))
		}
	}
	// On normal Agent restart the checkpoint and still supervised process
	// can agree on the unchanged render. Inspect and probe that process
	// directly, preserving its live listeners and sessions.
	retained := len(pending) == 0 && snap != nil && s.Checkpoint.Verified != nil && *s.Checkpoint.Verified == *snap
	if boot && retained && old.Digest == next.Digest {
		status, err := r.o.Process.Status(ctx)
		if err == nil && status.Running && r.waitInspection(ctx, next.Candidate, status.PID) == nil &&
			(len(next.Expected) == 0 || r.verifyActive(retainedVerification(ctx, startupRender, next), next, status.PID) == nil) {
			return r.confirmRestrictions(ctx, snap, previous, nil, next.Digest, true)
		}
	}
	verifyCtx := ctx
	if len(pending) > 0 {
		verifyCtx = retainedVerification(ctx, startupRender, next)
	}
	app, err := r.apply(verifyCtx, snap, old, next, pending)
	if err != nil {
		if errors.Is(context.Cause(ctx), errRestricted) {
			err = errRestricted
		}
		return r.failedRestore(err)
	}
	if snap != nil && len(next.Expected) == 0 && startupRender.Digest == next.Digest {
		// Startup is a previously confirmed snapshot even when an attempted
		// candidate cleared Verified. An unchanged empty render has no
		// forwarding probe; apply already inspected it and checked any pending
		// revocations.
		app.verified = true
	}
	if !app.verified && (rollback || len(next.Expected) > 0) {
		return r.failedRestore(ErrVerification)
	}
	if err := r.confirmRestrictions(ctx, snap, previous, pending, next.Digest, app.verified); err != nil {
		return r.failedRestore(err)
	}
	if len(pending) > 0 {
		r.notify()
	}
	return nil
}

func (r *Runtime) clearApplied(ctx context.Context) error {
	return r.save(ctx, func(s *control.DurableState) error {
		s.Checkpoint.Verified, s.Checkpoint.Applied, s.Checkpoint.Candidate = nil, nil, nil
		return nil
	})
}

func (r *Runtime) failedRestore(cause error) error {
	stopErr := r.stopKernel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	clearErr := r.clearApplied(ctx)
	if stopErr != nil || clearErr != nil {
		return errors.Join(errUncertain, cause, stopErr, clearErr)
	}
	return cause
}

func endpointShared(old, new control.BundleListener) bool {
	if old.Port != new.Port {
		return false
	}
	toIP := func(raw string) netip.Addr { ip, _ := netip.ParseAddr(raw); return ip }
	a, b := toIP(old.Address), toIP(new.Address)
	address := a == b || old.Address == "::" || new.Address == "::" || a.Is4() == b.Is4() && (a.IsUnspecified() || b.IsUnspecified())
	return address && (old.Transport == new.Transport || old.Transport == "tcp_udp" || new.Transport == "tcp_udp")
}
