package runtime

import (
	"context"
	"io"
	"net"
	"net/netip"
	"os"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestV2ExpiryInterruptsStalledObservation(t *testing.T) {
	chain := controltest.Chain()
	b := chain[2]
	b.Listeners[0].Internal = nil
	auth := *chain[0].Listeners[0].Entry
	f := newNodeFixture(t, b.NodeID)
	auth.ExpiresAt = f.now.Add(time.Hour)
	b.Listeners[0].Entry = &auth
	v2Options(f)
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	entered := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var nonce [64]byte
		if _, err := io.ReadFull(conn, nonce[:]); err != nil {
			return
		}
		close(entered)
		_, _ = io.Copy(io.Discard, conn) // intentionally never echoes
	}()
	target := ObservedTargetV2{Spec: control.EgressTarget{NodeID: b.NodeID, Type: "node_default"}}
	target.Pin = controltest.Pin("target", target.Spec)
	plan := ObservationPlanV2{NodeID: b.NodeID, Revision: 1, DNS: b.DNS, Targets: []ObservedTargetV2{target}}
	f.opts.Observations = &ObservationOptionsV2{Plan: func(context.Context) (ObservationPlanV2, error) { return plan, nil }, Probes: []netip.AddrPort{listener.Addr().(*net.TCPAddr).AddrPort()}}
	r := launch(t, f.opts)
	result, err := r.Execute(context.Background(), f.v2Command(b))
	if err != nil || result.Verification != domain.Verified {
		t.Fatal(result, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.ObserveV2(context.Background(), control.ObservationRequestV2{ID: controltest.ID("observation"), Deadline: time.Now().Add(20 * time.Second), Target: &target.Pin})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("observation not started")
	}
	f.clock.set(auth.ExpiresAt)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled probe produced evidence")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("egress probe delayed independent expiry")
	}
	waitFor(t, 5*time.Second, func() bool {
		s := f.state()
		return len(s.Tombstones) == 1 && s.Checkpoint.Verified != nil && s.Checkpoint.Verified.EffectiveDigest != result.EffectiveDigest
	})
}

func TestV2ObservedEgressUsesRealEchoAndAuthentication(t *testing.T) {
	for _, kind := range []string{"node_default", "interface", "http", "socks5"} {
		t.Run(kind, func(t *testing.T) {
			b := controltest.Chain()[2]
			f := newNodeFixture(t, b.NodeID)
			v2Options(f)
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			echo, closeEcho, err := echoTarget(ctx, "tcp")
			if err != nil {
				t.Fatal(err)
			}
			defer closeEcho()
			canary := netip.MustParseAddrPort(echo)
			target := ObservedTargetV2{Spec: control.EgressTarget{NodeID: b.NodeID, Type: kind}}
			if kind == "interface" {
				target.Spec.Interface = "lo"
			}
			if kind == "http" || kind == "socks5" {
				endpoint, _ := egressFixture(t, kind)
				target.Spec.Host, target.Spec.DialIP, target.Spec.Port, target.Spec.CredentialRef = endpoint.Addr().String(), endpoint.Addr().String(), endpoint.Port(), controltest.ID("secret")
			}
			target.Pin = controltest.Pin("target", target.Spec)
			if target.Spec.CredentialRef != "" {
				target.Auth = &control.EgressAuth{Target: target.Pin, Ref: target.Spec.CredentialRef, Audience: b.NodeID, Username: "mesh", Password: "test-secret"}
			}
			plan := ObservationPlanV2{NodeID: b.NodeID, Revision: 1, DNS: b.DNS, Targets: []ObservedTargetV2{target}}
			f.opts.Observations = &ObservationOptionsV2{Plan: func(context.Context) (ObservationPlanV2, error) { return plan, nil }, Probes: []netip.AddrPort{canary}}
			r := launch(t, f.opts)
			req := control.ObservationRequestV2{ID: controltest.ID("observation"), Deadline: time.Now().Add(10 * time.Second), Target: &target.Pin}
			out, err := r.ObserveV2(ctx, req)
			if err != nil || out.Egress == nil || out.Egress.Target != target.Pin || out.Egress.Probe != canary || out.Egress.Interface != "lo" || out.Egress.SourceIP != netip.MustParseAddr("127.0.0.1") || out.Egress.BusinessUDP {
				t.Fatal("missing actual TCP observation", out, err)
			}
			before := f.state()
			if before.Format != 0 || len(before.Grants) != 0 || len(before.BindingsV2) != 0 {
				t.Fatal("observation modified authorization")
			}
			if target.Auth != nil {
				bad := *target.Auth
				bad.Password = "wrong-secret"
				plan.Targets[0].Auth = &bad
				if _, err := r.ObserveV2(ctx, req); err == nil {
					t.Fatal("wrong proxy password produced evidence")
				}
			}
			unprovisioned := controltest.Pin("other", control.EgressTarget{NodeID: b.NodeID, Type: "node_default"})
			req.Target = &unprovisioned
			if _, err := r.ObserveV2(ctx, req); err == nil {
				t.Fatal("wire selected unprovisioned target")
			}
		})
	}
}

func TestV2ObservedInventoryUsesActualInterfaces(t *testing.T) {
	binary := os.Getenv("I03_MIHOMO_BINARY")
	if binary == "" {
		t.Skip("explicit locked-kernel inventory verification required")
	}
	b := controltest.Chain()[2]
	f := newNodeFixture(t, b.NodeID)
	v2Options(f)
	spec := control.SocketSpec{Address: "127.0.0.1", Transport: "tcp", ServerNames: []string{}, ShortIDs: []string{}}
	plan := ObservationPlanV2{NodeID: b.NodeID, Revision: 1, DNS: b.DNS, Sockets: []control.PinnedSocketSpecV2{{Protocol: domain.SS2022, Pin: controltest.Pin("socket", spec), Spec: spec, PoolFirst: 30000, PoolLast: 30100}}}
	f.opts.Observations = &ObservationOptionsV2{Binary: binary, Plan: func(context.Context) (ObservationPlanV2, error) { return plan, nil }}
	r := launch(t, f.opts)
	req := control.ObservationRequestV2{ID: controltest.ID("observation"), Deadline: time.Now().Add(10 * time.Second)}
	out, err := r.ObserveV2(context.Background(), req)
	if err != nil || out.Inventory == nil || len(out.Inventory.Interfaces) == 0 || len(out.Inventory.Loops.LocalAddresses) == 0 || out.Inventory.Capabilities[0].BusinessUDP != domain.NotChecked {
		t.Fatal(out, err)
	}
	f.opts.Observations.Binary = "/unprovisioned/kernel"
	if _, err := r.ObserveV2(context.Background(), req); err == nil {
		t.Fatal("unverified binary advertised capabilities")
	}
}
