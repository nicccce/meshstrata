package routes

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nicccce/meshstrata/controller/internal/egress"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"testing"
)

func TestPolicyPreviewDoesNotMutateBaseAndSorts(t *testing.T) {
	a := control.TerminalV2{Pin: control.Pin{ID: idAt("policy", 2), Revision: 1}}
	b := control.TerminalV2{Pin: control.Pin{ID: idAt("policy", 1), Revision: 1}}
	backing := make([]control.TerminalV2, 1, 4)
	backing[0] = a
	base := control.BundleV2{Terminal: backing}
	for i := 0; i < 3; i++ {
		candidate := base
		if e := addPolicy(&candidate, b); e != nil {
			t.Fatal(e)
		}
		if candidate.Terminal[0].Pin != b.Pin || candidate.Terminal[1].Pin != a.Pin {
			t.Fatal("not sorted")
		}
		if base.Terminal[0].Pin != a.Pin {
			t.Fatal("preview mutated the base")
		}
	}
}

// Fail at the commit boundary after all statements have executed. A retry must
// see the original version, with neither credential nor replay result committed.
type abortCredentialCommit struct{ storage.Database }

func (d abortCredentialCommit) Write(ctx context.Context, fn func(*dbgen.Queries) error) error {
	return d.Database.Write(ctx, func(q *dbgen.Queries) error {
		if e := fn(q); e != nil {
			return e
		}
		return errors.New("injected pre-commit failure")
	})
}
func TestCredentialReplacementAtomicReplayAndCAS(t *testing.T) {
	f := setup(t, 1)
	source, e := f.e.SaveTarget(f.ctx, domain.Mutation{IdempotencyKey: "target"}, stage3.EgressTargetInput{Name: "proxy", Spec: control.EgressTarget{NodeID: f.hops[0].NodeID, Type: "http", Host: "proxy.invalid", DialIP: "198.51.100.20", Port: 8080}})
	testOK(t, e)
	m := domain.Mutation{ExpectedRevision: source.Revision, IdempotencyKey: "replace"}
	auth := stage3.ProxyAuth{Username: "user", Password: "private-fixture-auth"}
	brokenRuntime := f.r
	brokenRuntime.DB = abortCredentialCommit{f.r.DB}
	broken, e := egress.NewService(stage3.EgressOptions{Runtime: brokenRuntime, Cipher: f.cipher})
	testOK(t, e)
	if _, e = broken.ReplaceTargetAuth(f.ctx, m, source, auth); e == nil {
		t.Fatal("commit fault ignored")
	}
	testOK(t, f.r.DB.Read(f.ctx, func(q *dbgen.Queries) error {
		v, e := q.B06EgressObject(f.ctx, string(source.ID))
		if e != nil {
			return e
		}
		if v.Revision != 1 {
			t.Fatal("partial target version")
		}
		_, e = q.B06Mutation(f.ctx, dbgen.B06MutationParams{Operation: "egress.target.credentials", IdemKey: m.IdempotencyKey})
		if !errors.Is(e, sql.ErrNoRows) {
			t.Fatal("partial replay record", e)
		}
		return nil
	}))
	p, e := f.e.ReplaceTargetAuth(f.ctx, m, source, auth)
	testOK(t, e)
	private, e := egress.OpenAuth(f.ctx, f.r, f.cipher, p, f.hops[0].NodeID)
	testOK(t, e)
	if private.Password != auth.Password {
		t.Fatal("credential not committed")
	}
	spec, e := f.e.Target(f.ctx, p)
	testOK(t, e)
	_, e = f.e.SaveTarget(f.ctx, domain.Mutation{ExpectedRevision: p.Revision, IdempotencyKey: "rename"}, stage3.EgressTargetInput{ID: p.ID, Name: "new name", Spec: spec})
	testOK(t, e)
	again, e := f.e.ReplaceTargetAuth(f.ctx, m, source, auth)
	testOK(t, e)
	if again != p {
		t.Fatal("replay changed after head advanced")
	}
	auth.Password = "different"
	_, e = f.e.ReplaceTargetAuth(f.ctx, m, source, auth)
	testCode(t, e, domain.IdempotencyConflict)
	_, e = f.e.ReplaceTargetAuth(f.ctx, domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "stale"}, source, auth)
	testCode(t, e, domain.RevisionConflict)
}
