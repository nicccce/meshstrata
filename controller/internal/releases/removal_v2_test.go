package releases

import (
	"database/sql"
	"slices"
	"strings"
	"testing"

	"github.com/nicccce/meshstrata/controller/internal/enrollment"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestV2RemovalRevokesAffectedEntryAndWaitsForEachPhysicalCleanup(t *testing.T) {
	j := jointFor(t, 3)
	nodes := []domain.ID{}
	for n := range j.reports {
		nodes = append(nodes, n)
	}
	slices.Sort(nodes)
	ingress, gone, terminal := nodes[0], nodes[1], nodes[2]
	route := j.publish(j.draft("remove-three", nodes))
	keepRoute := j.publish(j.draft("keep-local", []domain.ID{ingress}))
	bad := j.subscription("affected", route)
	keep := j.subscription("unrelated", keepRoute)
	badLink, err := j.subs.DownloadLink(j.ctx, bad.Entity.ID)
	must(t, err)
	keepLink, err := j.subs.DownloadLink(j.ctx, keep.Entity.ID)
	must(t, err)
	token := func(url string) string { return url[strings.LastIndex(url, "/")+1:] }
	auth, err := enrollment.New(enrollment.NewRepository(j.store), j.cipher, testGuard{}, enrollment.Options{ControllerURL: "https://controller.test", ServerNames: []string{"controller.test"}, Now: j.s.options.Now, Context: j.ctx})
	must(t, err)
	j.s.options.RemovalIntent = auth.RemovalIntent
	var revision domain.Revision
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
		n, e := q.GetNode(j.ctx, string(gone))
		revision = domain.Revision(n.Revision)
		return e
	}))
	removed, err := auth.RemoveTask(j.ctx, gone, revision, "remove-modern")
	must(t, err)
	// Authorization closes immediately, before any removal/cleanup worker tick.
	if _, err := j.subs.DownloadFull(j.ctx, token(badLink.DownloadURL)); err == nil {
		t.Fatal("removed hop still downloadable")
	}
	if _, err := j.subs.DownloadFull(j.ctx, token(keepLink.DownloadURL)); err != nil {
		t.Fatal("unrelated subscription denied", err)
	}
	j.connections[gone], j.connections[terminal] = false, false
	delete(j.reports, gone)
	j.now = j.now.Add(redelivery)
	j.dispatch()
	var intent dbgen.B08Intent
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error {
		rows, e := q.I03RemovalIntents(j.ctx, string(gone))
		if e == nil {
			intent = rows[0]
		}
		return e
	}))
	if !intent.RevokedAt.Valid || intent.CleanupState != "pending" {
		t.Fatal("no permanent entry revocation")
	}
	var liveRefs int
	must(t, j.raw().QueryRow("SELECT count(*) FROM b06_entry_link_references WHERE identity_id=? AND released_at IS NULL", intent.IdentityKey).Scan(&liveRefs))
	if liveRefs != 2 {
		t.Fatal("removed hop released entry references before cleanup")
	}
	j.refresh()
	must(t, j.subs.Reconcile(j.ctx, domain.ID(intent.ID)))
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error { var e error; intent, e = q.B08Intent(j.ctx, intent.ID); return e }))
	if !intent.CleanupTaskID.Valid {
		t.Fatal("missing independent ingress cleanup")
	}
	j.finish(domain.ID(intent.CleanupTaskID.String), true)
	must(t, j.subs.Reconcile(j.ctx, domain.ID(intent.ID)))
	j.now = j.now.Add(redelivery)
	j.dispatch()
	var links []dbgen.B06InternalLink
	must(t, j.store.Read(j.ctx, func(q *dbgen.Queries) error { var e error; links, e = q.I03RemovalLinks(j.ctx, string(gone)); return e }))
	if len(links) != 2 {
		t.Fatal("lost internal history")
	}
	for _, l := range links {
		if l.State != "retiring" {
			t.Fatal("unreferenced link not retired", l.State)
		}
	}
	var child sql.NullString
	must(t, j.raw().QueryRow("SELECT cleanup_task_id FROM b04_removal_links WHERE removal_task_id=? AND node_id=?", removed.TaskID, terminal).Scan(&child))
	if child.Valid {
		t.Fatal("offline terminal received cleanup")
	}
	j.connections[terminal] = true
	j.now = j.now.Add(redelivery)
	j.refresh()
	sent := j.dispatch()
	if len(sent) != 1 || sent[0].NodeID != terminal || sent[0].Kind != domain.Cleanup {
		t.Fatal("missing terminal cleanup", sent)
	}
	c := sent[0]
	key, err := j.s.Resolve(j.ctx, terminal, c.TaskID)
	must(t, err)
	source, err := j.s.Open(j.ctx, terminal, key)
	must(t, err)
	source.Close()
	r := j.result(c, 1, true)
	r.Cleanup = "pending"
	r.EffectiveDigest = strings.Repeat("e", 64)
	_, err = j.s.RecordResult(j.ctx, terminal, r)
	must(t, err)
	j.dispatch()
	var state string
	must(t, j.raw().QueryRow("SELECT state FROM b06_internal_links WHERE node_id=?", terminal).Scan(&state))
	if state != "retiring" {
		t.Fatal("non-confirming receipt released link")
	}
	r.Sequence, r.Cleanup = 2, "confirmed"
	_, err = j.s.RecordResult(j.ctx, terminal, r)
	must(t, err)
	j.dispatch()
	must(t, j.raw().QueryRow("SELECT state FROM b06_internal_links WHERE node_id=?", terminal).Scan(&state))
	if state != "released" {
		t.Fatal("real terminal cleanup did not settle link")
	}
	var pending int
	must(t, j.raw().QueryRow("SELECT count(*) FROM port_reservations WHERE node_id=? AND state='pending_cleanup'", gone).Scan(&pending))
	if pending == 0 || j.row(removed.TaskID).State != "queued" {
		t.Fatal("peer receipt forged removed-node cleanup")
	}
	snap, err := j.s.NodeSnapshot(j.ctx, ingress)
	must(t, err)
	if len(snap.Extended.Listeners) != 1 || snap.Extended.Listeners[0].Entry.IdentityID != keep.Entries[0].IdentityID || len(snap.Extended.Tombstones) != 1 || snap.Extended.Tombstones[0].IdentityID != bad.Entries[0].IdentityID {
		t.Fatal("cleanup changed unrelated identity or lost permanent tombstone")
	}
	var count int
	must(t, j.raw().QueryRow("SELECT count(*) FROM b04_grants").Scan(&count))
	if count != 2 {
		t.Fatal("internal removal manufactured entry grant")
	}
	// Repeating recovery preserves the child and original entry origins.
	j.now = j.now.Add(redelivery)
	j.dispatch()
	if _, err := j.subs.DownloadFull(j.ctx, token(keepLink.DownloadURL)); err != nil {
		t.Fatal(err)
	}
}
