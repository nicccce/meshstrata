package checkpoint

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func testID(kind string, n int) domain.ID { return domain.ID(fmt.Sprintf("%s_%032x", kind, n)) }

func options(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return Options{Dir: dir, NodeID: testID("node", 1), KeyID: "key-1", Key: make([]byte, 32),
		Now: func() time.Time { return time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC) }}
}
func openStore(t *testing.T, o Options) *Store {
	t.Helper()
	s, err := Open(o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestConcurrentCASAndRestart(t *testing.T) {
	ctx := context.Background()
	o := options(t)
	store := openStore(t, o)
	if _, err := Open(o); !errors.Is(err, ErrConflict) {
		t.Fatal("second writer", err)
	}
	state, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			next := state
			next.ClockFloor = next.ClockFloor.Add(time.Hour)
			results <- store.Commit(ctx, state.Revision, next)
		}()
	}
	wg.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal(successes, conflicts)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openStore(t, o)
	got, err := reopened.Load(ctx)
	if err != nil || got.Revision != 1 || !got.ClockFloor.Equal(state.ClockFloor.Add(time.Hour)) {
		t.Fatal("durable CAS or floor lost", err)
	}
}

func TestBindingAndLossNeverInitializeOverState(t *testing.T) {
	for _, what := range []string{"wrong key", "wrong key id", "wrong node", "unknown version", "damaged", "missing", "wrong purpose", "missing key"} {
		t.Run(what, func(t *testing.T) {
			o := options(t)
			s := openStore(t, o)
			s.Close()
			name := filepath.Join(o.Dir, stateFile)
			b, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			switch what {
			case "wrong key":
				o.Key[0] = 1
			case "wrong key id":
				o.KeyID = "key-2"
			case "wrong node":
				o.NodeID = testID("node", 2)
			case "unknown version":
				b[0] = 'x'
			case "damaged":
				b[len(b)-1] ^= 1
			case "missing key":
				o.Key = nil
			case "wrong purpose":
				b, err = os.ReadFile(filepath.Join(o.Dir, anchorFile))
				if err != nil {
					t.Fatal(err)
				}
			}
			if what == "missing" {
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			} else if err := os.WriteFile(name, b, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(o); err == nil {
				t.Fatal("invalid checkpoint opened")
			}
			got, err := os.ReadFile(name)
			if what == "missing" {
				if !errors.Is(err, os.ErrNotExist) {
					t.Fatal("missing file replaced")
				}
			} else if err != nil || !reflect.DeepEqual(b, got) {
				t.Fatal("checkpoint overwritten")
			}
		})
	}
}

func TestPathAndAbandonedTemporaryWrite(t *testing.T) {
	o := options(t)
	store := openStore(t, o)
	ctx := context.Background()
	st, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Commit(ctx, st.Revision, st); err != nil {
		t.Fatal(err)
	}
	store.Close()
	// Simulate a writer interrupted after writing but before renaming.
	if err := os.WriteFile(filepath.Join(o.Dir, ".write-abandoned"), []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	store = openStore(t, o)
	st, err = store.Load(ctx)
	if err != nil || st.Revision != 1 {
		t.Fatal("temporary write changed CAS", err)
	}
	store.Close()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(o.Dir, alias); err != nil {
		t.Fatal(err)
	}
	aliasOptions := o
	aliasOptions.Dir = alias
	if _, err := Open(aliasOptions); !errors.Is(err, ErrUnsafe) {
		t.Fatal("symlink", err)
	}
	if err := os.Chmod(o.Dir, 0750); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(o); !errors.Is(err, ErrUnsafe) {
		t.Fatal("public directory", err)
	}
	if err := os.Chmod(o.Dir, 0700); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(o.Dir, stateFile)
	copyPath := filepath.Join(t.TempDir(), stateFile)
	if err := os.Rename(statePath, copyPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(copyPath, statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(o); err == nil {
		t.Fatal("symlink state accepted")
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(copyPath, statePath); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(o); err == nil {
		t.Fatal("hardlink state accepted")
	}
}

func TestIntermediateSnapshotsLedgerAndExactReceipts(t *testing.T) {
	ctx := context.Background()
	o := options(t)
	store := openStore(t, o)
	s, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	cmd := control.Command{TaskID: testID("task", 1), NodeID: o.NodeID, Kind: domain.Publish,
		TargetRevision: 2, TargetDigest: digest, IdempotencyKey: "one", Deadline: o.Now().Add(time.Hour),
		DependsOn: []domain.ID{}, RevocationEpoch: 1}
	result := control.Result{TaskID: cmd.TaskID, Sequence: 1, NodeResult: domain.NodeResult{NodeID: o.NodeID,
		TargetRevision: cmd.TargetRevision, TargetDigest: digest, Verification: domain.Failed, Rollback: "none", Cleanup: "none"}}
	grant := control.Grant{Origin: control.ArtifactKey{TaskID: cmd.TaskID, NodeID: o.NodeID, TargetRevision: 2, TargetDigest: digest},
		Listener: control.BundleListener{ListenerID: testID("listener", 1), NodeID: o.NodeID,
			Authorization: control.BundleAuthorization{EntryID: testID("entry", 1), IdentityID: testID("identity", 1),
				Generation: 1, ExpiresAt: o.Now()}, Credential: control.PrivateCredential{Audience: o.NodeID}}}
	s.Grants = []control.Grant{grant}
	tomb := control.Tombstone{IdentityID: grant.Listener.Authorization.IdentityID, Generation: 1, Epoch: 1, At: o.Now(), Reason: "expired"}
	s.Tombstones = []control.Tombstone{tomb}
	local := result
	local.Sequence = 2
	local.Cleanup = "pending"
	local.Restrictions = []control.Restriction{{IdentityID: tomb.IdentityID, ListenerID: grant.Listener.ListenerID,
		Generation: 1, Reason: "expired", ObservedAt: o.Now(), Epoch: 1, EffectiveDigest: digest}}
	s.Executions = []control.Execution{{Command: cmd, Sequence: 2, LastResult: &result}}
	s.Outbox = []control.Result{result, local}
	snap := control.Snapshot{TaskID: cmd.TaskID, Revision: 2, ArtifactDigest: digest, EffectiveDigest: digest, Epoch: 1}
	s.Checkpoint.Candidate = &snap
	if err := store.Commit(ctx, 0, s); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store = openStore(t, o)
	s, err = store.Load(ctx)
	if err != nil || s.Checkpoint.Candidate == nil || s.Checkpoint.Applied != nil || s.Checkpoint.Verified != nil {
		t.Fatal("phase recovery", err)
	}
	receipt := control.Receipt{TaskID: cmd.TaskID, NodeID: o.NodeID, Sequence: 2}
	if err := store.Acknowledge(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if err := store.Acknowledge(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	s, err = store.Load(ctx)
	if err != nil || len(s.Outbox) != 1 || s.Outbox[0].Sequence != 1 || s.Executions[0].Sequence != 2 || s.Executions[0].LastResult.Sequence != 1 {
		t.Fatal("receipt removed another result or high water", err)
	}
	history, err := store.RestrictionResults(ctx)
	if err != nil || history[tomb.IdentityID].Sequence != 2 {
		t.Fatal("lost pending evidence", err)
	}
	s.Outbox = []control.Result{local}
	if err := store.Commit(ctx, s.Revision, s); !errors.Is(err, ErrState) {
		t.Fatal("acknowledged sequence reused", err)
	}
	s.Outbox, s.Grants, s.Tombstones, s.Executions = nil, nil, nil, nil
	s.ClockFloor = s.ClockFloor.Add(-time.Hour)
	if err := store.Commit(ctx, s.Revision, s); err != nil {
		t.Fatal(err)
	}
	s, err = store.Load(ctx)
	if err != nil || len(s.Outbox) != 1 || len(s.Grants) != 1 || len(s.Tombstones) != 1 || len(s.Executions) != 1 || !s.ClockFloor.Equal(o.Now()) {
		t.Fatal("monotonic merge lost data", err)
	}
}

func TestAuthenticatedWatermarkRejectsOldStateAndRepairsInterruptedAdvance(t *testing.T) {
	ctx := context.Background()
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprint(interrupted), func(t *testing.T) {
			o := options(t)
			store := openStore(t, o)
			name := stateFile
			if interrupted {
				name = anchorFile
			}
			path := filepath.Join(o.Dir, name)
			older, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s, err := store.Load(ctx)
			if err != nil || store.Commit(ctx, s.Revision, s) != nil {
				t.Fatal("first commit", err)
			}
			store.Close()
			if err := os.WriteFile(path, older, 0600); err != nil {
				t.Fatal(err)
			}
			if !interrupted {
				if _, err := Open(o); !errors.Is(err, ErrCorrupt) {
					t.Fatal("old state was restored", err)
				}
				return
			}
			store = openStore(t, o)
			got, err := store.Load(ctx)
			if err != nil || got.Revision != 1 || store.watermark != 1 {
				t.Fatal("marker did not catch up after interrupted commit", err)
			}
		})
	}
}
