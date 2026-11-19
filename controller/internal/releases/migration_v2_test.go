package releases

import (
	"strings"
	"testing"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestExplicitV1DrainThenV2SwitchPreservesOriginalBytesAndTombstones(t *testing.T) {
	j := jointFor(t, 1)
	var node domain.ID
	for id := range j.reports {
		node = id
	}
	listener := j.listener(node, 31000, "0.0.0.0")
	publish := j.submit("legacy-before-switch", j.plan(j.target(node, 1, listener)))
	sent := j.dispatch()
	if len(sent) != 1 {
		t.Fatal("no v1 command")
	}
	original := j.artifact(sent[0])
	result := j.result(sent[0], 1, true)
	result.EffectiveDigest = strings.Repeat("a", 64)
	j.record(result)
	j.refresh()
	_, err := j.s.SwitchNodeV2(j.ctx, node, mutation("premature-switch", 2))
	code(t, err, domain.DependencyInUse)
	drain, err := j.s.DrainLegacyNode(j.ctx, node, mutation("drain", 2))
	must(t, err)
	replay, err := j.s.DrainLegacyNode(j.ctx, node, mutation("drain", 2))
	must(t, err)
	if replay.TaskID != drain.TaskID {
		t.Fatal("drain not idempotent")
	}
	_, err = j.s.SwitchNodeV2(j.ctx, node, mutation("still-premature", 3))
	if err == nil {
		t.Fatal("switch accepted before real cleanup receipt")
	}
	sent = j.dispatch()
	if len(sent) != 1 || sent[0].TaskID != drain.TaskID {
		t.Fatal("no drain command")
	}
	data := j.artifact(sent[0])
	key := control.ArtifactKey{TaskID: sent[0].TaskID, NodeID: node, TargetRevision: sent[0].TargetRevision, TargetDigest: sent[0].TargetDigest}
	b, err := control.DecodeBundle(data, key, node)
	must(t, err)
	if len(b.Listeners) != 0 || len(b.Tombstones) != 1 || b.Tombstones[0].IdentityID != listener.Authorization.IdentityID {
		t.Fatal("drain did not preserve restriction")
	}
	result = j.result(sent[0], 1, true)
	result.Cleanup, result.EffectiveDigest = "confirmed", strings.Repeat("b", 64)
	j.record(result)
	j.refresh()
	switched, err := j.s.SwitchNodeV2(j.ctx, node, mutation("switch", 3))
	must(t, err)
	j.finish(switched.TaskID, true)
	replay, err = j.s.SwitchNodeV2(j.ctx, node, mutation("switch", 3))
	must(t, err)
	if replay.TaskID != switched.TaskID {
		t.Fatal("switch replay changed task")
	}
	snapshot, err := j.s.NodeSnapshot(j.ctx, node)
	must(t, err)
	if snapshot.Legacy != nil || snapshot.Extended == nil || len(snapshot.Extended.Listeners) != 0 || len(snapshot.Extended.Tombstones) != 1 {
		t.Fatal("switch did not establish explicit restricted v2 baseline")
	}
	var artifact []byte
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
		a, err := q.B04GetArtifact(j.ctx, dbgen.B04GetArtifactParams{TaskID: string(publish), NodeID: string(node)})
		if err != nil {
			return err
		}
		artifact, err = j.s.openBytes(j.ctx, a)
		return err
	}))
	if string(artifact) != string(original) {
		t.Fatal("migration re-encoded old artifact")
	}
}
