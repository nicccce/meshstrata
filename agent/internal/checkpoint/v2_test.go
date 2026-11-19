package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
)

func TestV2UpgradeOriginAndDowngradeFence(t *testing.T) {
	ctx := context.Background()
	b := controltest.Chain()[1] // an actual internal link: no entry or expiration
	o := options(t)
	o.NodeID = b.NodeID
	store := openStore(t, o)
	old, err := store.read(stateFile, maxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	data, key, err := control.EncodeBundleV2(b)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveArtifact(ctx, key, data); err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Format, state.RevocationEpoch = 2, b.RevocationEpoch
	state.Executions = []control.Execution{{Command: control.Command{TaskID: b.TaskID, NodeID: b.NodeID, Kind: b.Kind, TargetRevision: b.TargetRevision, TargetDigest: key.TargetDigest, RevocationEpoch: b.RevocationEpoch, Deadline: o.Now().Add(1000000000), IdempotencyKey: "v2-first"}}}
	state.BindingsV2 = []control.BindingV2{{Origin: key, Listener: b.Listeners[0]}}
	bad := state
	bad.BindingsV2 = append([]control.BindingV2{}, state.BindingsV2...)
	bad.BindingsV2[0].Listener.Socket.Port++
	if err = store.Commit(ctx, state.Revision, bad); !errors.Is(err, ErrState) {
		t.Fatal("invented socket accepted", err)
	}
	if err = store.Commit(ctx, state.Revision, state); err != nil {
		t.Fatal(err)
	}
	raw, err := store.read(stateFile, maxStateBytes)
	if err != nil {
		t.Fatal(err)
	}
	var legacy diskState
	if json.Unmarshal(raw, &legacy) != nil || validState(legacy.State, o.NodeID) == nil {
		t.Fatal("old decoder can silently accept v2")
	}
	saved, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Losing a field in a caller cannot truncate permanent bindings or downgrade.
	next := saved
	next.Format, next.BindingsV2 = 0, nil
	if err = store.Commit(ctx, saved.Revision, next); err != nil {
		t.Fatal(err)
	}
	saved, err = store.Load(ctx)
	if err != nil || saved.Format != 2 || !reflect.DeepEqual(saved.BindingsV2, state.BindingsV2) {
		t.Fatal("binding lost", err)
	}
	// A restored old state behind the V2 anchor must fail closed.
	if err = store.write(stateFile, old); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Load(ctx); !errors.Is(err, ErrCorrupt) {
		t.Fatal("downgrade accepted", err)
	}
}

func TestV2StateFirstCrashRepairsVersionedAnchor(t *testing.T) {
	ctx := context.Background()
	o := options(t)
	store := openStore(t, o)
	anchor, err := store.read(anchorFile, 64)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state.Format = 2
	if err = store.Commit(ctx, state.Revision, state); err != nil {
		t.Fatal(err)
	}
	if err = store.write(anchorFile, anchor); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store = openStore(t, o)
	got, err := store.Load(ctx)
	if err != nil || got.Format != 2 || store.format != 2 {
		t.Fatal("crash recovery", err)
	}
	marker, err := store.read(anchorFile, 64)
	if err != nil || string(marker) != "initialized-v2:1" {
		t.Fatal("anchor not repaired", err)
	}
	// The new envelope remains encrypted with the original node/key/purpose AAD.
	store.Close()
	o.Key = append([]byte{}, o.Key...)
	o.Key[0] = 1
	if _, err := Open(o); !errors.Is(err, ErrCorrupt) {
		t.Fatal("wrong key accepted", err)
	}
}
