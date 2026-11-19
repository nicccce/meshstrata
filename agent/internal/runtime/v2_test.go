package runtime

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
)

func v2Options(f *fixture) {
	f.opts.EnableV2 = true
	f.opts.Inventory = func(context.Context) (control.LoopInventory, error) {
		return control.LoopInventory{LocalAddresses: []netip.Addr{netip.MustParseAddr("127.0.0.1")}, ProtectedEndpoints: []netip.AddrPort{}, ProtectedNetworks: []netip.Prefix{}, ProtectedInterfaces: []string{}}, nil
	}
	f.opts.ProbeFactoryV2 = func(context.Context, control.BundleV2, mihomo.Candidate) (mihomo.Probe, error) { return noProbe{}, nil }
}
func (f *fixture) v2Command(b control.BundleV2) control.Command {
	f.t.Helper()
	data, key, err := control.EncodeBundleV2(b)
	if err != nil {
		f.t.Fatal(err)
	}
	f.artifacts.Store(key, data)
	return control.Command{TaskID: key.TaskID, NodeID: key.NodeID, Kind: b.Kind, TargetRevision: key.TargetRevision, TargetDigest: key.TargetDigest, RevocationEpoch: b.RevocationEpoch, DependsOn: []domain.ID{}, Deadline: f.clock.now().Add(time.Minute), IdempotencyKey: string(b.TaskID)}
}

func TestV2RuntimeInternalRetirementAndOldCheckpointRecovery(t *testing.T) {
	b := controltest.Chain()[2]
	f := newNodeFixture(t, b.NodeID)
	v2Options(f)
	r := launch(t, f.opts)
	c := f.v2Command(b)
	result, err := r.Execute(context.Background(), c)
	if err != nil || result.Verification != domain.Verified {
		t.Fatal("internal publish", result, err)
	}
	state := f.state()
	if state.Format != 2 || state.Checkpoint.Startup.Format != 2 || len(state.Grants) != 0 || len(state.BindingsV2) != 1 {
		t.Fatal("internal binding not independently durable")
	}
	origin := state.BindingsV2[0].Origin
	r.Close()
	f.reopen()
	f.opts.State = f.store
	r = launch(t, f.opts)
	replay, err := r.Execute(context.Background(), c)
	if err != nil || replay.Sequence != result.Sequence {
		t.Fatal("replay was re-executed", err)
	}
	cleanup := b
	cleanup.TaskID, cleanup.TargetRevision, cleanup.Kind = controltest.ID("cleanup"), 3, domain.Cleanup
	cleanup.Listeners = []control.ListenerV2{}
	cleanup.RetiredLinks = []control.InternalRetirement{{Link: *b.Listeners[0].Internal, Epoch: b.RevocationEpoch}}
	out, err := r.Execute(context.Background(), f.v2Command(cleanup))
	if err != nil || out.Cleanup != "confirmed" || out.Verification != domain.Verified {
		t.Fatal("retirement cleanup", out, err)
	}
	state = f.state()
	if len(state.LinkCleanupsV2) != 1 || len(state.RetiredLinks) != 1 || state.BindingsV2[0].Origin != origin || len(state.Tombstones) != 0 {
		t.Fatal("retirement history lost or fabricated subscription")
	}
	if state.Checkpoint.PreviousGood == nil || state.Checkpoint.PreviousGood.ArtifactDigest != c.TargetDigest {
		t.Fatal("previous good missing")
	}
	r.Close()
	f.reopen()
	f.opts.State = f.store
	r = launch(t, f.opts)
	state = f.state()
	rendered, err := r.checkedRender(context.Background(), state.Checkpoint.Startup)
	if err != nil || len(rendered.Expected) != 0 {
		t.Fatal("retired link resurrected", err)
	}
}

func TestV2RuntimeOfflineExpiryUsesRealOriginAndNoResurrection(t *testing.T) {
	chain := controltest.Chain()
	b := chain[2]
	b.Listeners[0].Internal = nil
	auth := *chain[0].Listeners[0].Entry
	b.Listeners[0].Entry = &auth
	f := newNodeFixture(t, b.NodeID)
	auth.ExpiresAt = f.now.Add(time.Hour)
	v2Options(f)
	r := launch(t, f.opts)
	c := f.v2Command(b)
	out, err := r.Execute(context.Background(), c)
	if err != nil || out.Verification != domain.Verified {
		t.Fatal(out, err)
	}
	f.clock.set(auth.ExpiresAt)
	deadline := time.Now().Add(5 * time.Second)
	for {
		state := f.state()
		if len(state.Tombstones) == 1 && state.Checkpoint.Verified != nil && state.Checkpoint.Verified.EffectiveDigest != out.EffectiveDigest {
			found := false
			for _, result := range state.Outbox {
				if len(result.Restrictions) > 0 {
					found = true
					if result.TaskID != c.TaskID || result.TargetDigest != c.TargetDigest || result.TargetRevision != c.TargetRevision || result.Restrictions[0].IdentityID != auth.IdentityID {
						t.Fatal("expiry fabricated task or target")
					}
				}
			}
			if !found {
				t.Fatal("no durable expiry report")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("v2 expiry did not reconcile")
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.Close()
	f.clock.set(f.now) // clock rollback cannot undo the durable floor or tombstone
	f.reopen()
	f.opts.State = f.store
	r = launch(t, f.opts)
	state := f.state()
	view, err := r.checkedRender(context.Background(), state.Checkpoint.Startup)
	if err != nil || len(view.Expected) != 0 || len(state.BindingsV2) != 1 {
		t.Fatal("old binding resurrected", err)
	}
}

func TestV2RuntimeRequiresLegacyDrainAndPreservesV1Ledger(t *testing.T) {
	chain := controltest.Chain()
	terminal := chain[2]
	f := newNodeFixture(t, terminal.NodeID)
	v2Options(f)
	r := launch(t, f.opts)
	wire := transportV2(terminal.NodeID, terminal.Listeners[0])
	wire.Authorization = *chain[0].Listeners[0].Entry
	wire.Authorization.ExpiresAt = f.now.Add(time.Hour)
	old := bundle(1, 3, domain.Publish, wire)
	old.NodeID = terminal.NodeID
	data, key, err := control.EncodeBundle(old)
	if err != nil {
		t.Fatal(err)
	}
	f.artifacts.Store(key, data)
	command := func(b control.Bundle, k control.ArtifactKey) control.Command {
		return control.Command{TaskID: k.TaskID, NodeID: k.NodeID, Kind: b.Kind, TargetRevision: k.TargetRevision, TargetDigest: k.TargetDigest, RevocationEpoch: b.RevocationEpoch, DependsOn: []domain.ID{}, IdempotencyKey: string(k.TaskID), Deadline: f.now.Add(time.Hour)}
	}
	result, err := r.Execute(context.Background(), command(old, key))
	if err != nil || result.Verification != domain.Verified {
		t.Fatal("legacy setup", result, err)
	}
	// A V2 capability and a well-formed route cannot replace live old grants.
	terminal.TaskID, terminal.TargetRevision = controltest.ID("earlyv2"), 2
	result, err = r.Execute(context.Background(), f.v2Command(terminal))
	if err != nil || result.Issue == nil || result.Issue.Code != domain.RevisionConflict {
		t.Fatal("live V1 grant silently switched", result, err)
	}
	drain := old
	drain.TaskID, drain.Kind, drain.TargetRevision = controltest.ID("legacydrain"), domain.Cleanup, 3
	drain.Listeners = []control.BundleListener{}
	drain.Tombstones = []control.Tombstone{{IdentityID: wire.Authorization.IdentityID, Generation: wire.Authorization.Generation, Epoch: 3, At: f.now, Reason: "revoked"}}
	drainData, drainKey, err := control.EncodeBundle(drain)
	if err != nil {
		t.Fatal(err)
	}
	f.artifacts.Store(drainKey, drainData)
	result, err = r.Execute(context.Background(), command(drain, drainKey))
	if err != nil || result.Cleanup != "confirmed" {
		t.Fatal("legacy drain", result, err)
	}
	switched := terminal
	switched.TaskID, switched.Kind, switched.TargetRevision = controltest.ID("switchv2"), domain.Cleanup, 4
	switched.Listeners, switched.Terminal, switched.Tombstones = []control.ListenerV2{}, []control.TerminalV2{}, drain.Tombstones
	result, err = r.Execute(context.Background(), f.v2Command(switched))
	if err != nil || result.Verification != domain.Verified || result.Cleanup != "confirmed" {
		t.Fatal("empty explicit switch", result, err)
	}
	state := f.state()
	if state.Format != 2 || len(state.Grants) != 1 || state.Grants[0].Origin != key || len(state.Tombstones) != 1 {
		t.Fatal("legacy restrictions or origins lost")
	}
	saved, err := f.store.ReadArtifact(context.Background(), key)
	if err != nil || string(saved) != string(data) {
		t.Fatal("V1 source changed", err)
	}
	r.Close()
	f.reopen()
	f.opts.State = f.store
	r = launch(t, f.opts)
	state = f.state()
	view, err := r.checkedRender(context.Background(), state.Checkpoint.Startup)
	if err != nil || view.v2 == nil || len(view.Expected) != 0 {
		t.Fatal("switch failed to recover", err)
	}
}
