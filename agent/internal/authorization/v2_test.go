package authorization

import (
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestV2LedgerRetiresLinksWithoutEntryOrExpiry(t *testing.T) {
	b := controltest.Chain()[1]
	now := time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)
	state := control.DurableState{RevocationEpoch: 1, ClockFloor: now}
	_, key, err := control.EncodeBundleV2(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := MergeV2(&state, b, key, now); err != nil {
		t.Fatal(err)
	}
	if len(state.Grants) != 0 || len(state.BindingsV2) != 1 || state.BindingsV2[0].Listener.Entry != nil || !NextExpiry(state).IsZero() {
		t.Fatal("internal link projected into expiring entry")
	}
	Expire(&state, now.AddDate(10, 0, 0))
	if len(state.Tombstones) != 0 || len(ActiveV2(state, b, state.ClockFloor)) != 1 {
		t.Fatal("link expired")
	}
	cleanup := b
	cleanup.Kind, cleanup.TargetRevision = domain.Cleanup, 3
	cleanup.Listeners = []control.ListenerV2{}
	cleanup.RetiredLinks = []control.InternalRetirement{{Link: *b.Listeners[0].Internal, Epoch: 3}}
	_, cleanupKey, err := control.EncodeBundleV2(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	if err := MergeV2(&state, cleanup, cleanupKey, state.ClockFloor); err != nil {
		t.Fatal(err)
	}
	if len(ActiveV2(state, b, state.ClockFloor)) != 0 || len(state.RetiredLinks) != 1 {
		t.Fatal("old checkpoint resurrected retired link")
	}
	if err := MergeV2(&state, b, key, state.ClockFloor); err != nil {
		t.Fatal(err)
	}
	if len(ActiveV2(state, b, state.ClockFloor)) != 0 {
		t.Fatal("late source resurrected retired link")
	}
}

func TestV2LedgerExpiresOnlyRealEntriesAndPreservesFirstOrigin(t *testing.T) {
	b := controltest.Chain()[0]
	now := b.Listeners[0].Entry.ExpiresAt.Add(-time.Hour)
	state := control.DurableState{RevocationEpoch: 1, ClockFloor: now}
	_, key, err := control.EncodeBundleV2(b)
	if err != nil {
		t.Fatal(err)
	}
	if err = MergeV2(&state, b, key, now); err != nil {
		t.Fatal(err)
	}
	next := b
	next.TaskID, next.TargetRevision = controltest.ID("nexttask"), 3
	_, nextKey, err := control.EncodeBundleV2(next)
	if err != nil {
		t.Fatal(err)
	}
	if err = MergeV2(&state, next, nextKey, now); err != nil {
		t.Fatal(err)
	}
	if len(state.BindingsV2) != 1 || state.BindingsV2[0].Origin != key {
		t.Fatal("real first origin changed")
	}
	if !Due(state, b.Listeners[0].Entry.ExpiresAt) {
		t.Fatal("v2 expiry not scheduled")
	}
	Expire(&state, b.Listeners[0].Entry.ExpiresAt)
	if len(state.Tombstones) != 1 || len(ActiveV2(state, b, now)) != 0 {
		t.Fatal("rollback clock reactivated expired identity")
	}
	bad := next
	bad.Listeners = append([]control.ListenerV2{}, next.Listeners...)
	bad.Listeners[0].Socket.Credential.Password = "AQEBAQEBAQEBAQEBAQEBAQ=="
	_, badKey, err := control.EncodeBundleV2(bad)
	if err != nil {
		t.Fatal(err)
	}
	if err := MergeV2(&state, bad, badKey, now); err == nil {
		t.Fatal("known private binding mutated")
	}
}
