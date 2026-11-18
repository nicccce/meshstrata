package rules

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type failingCommit struct {
	storage.Database
	fail bool
}

func (f *failingCommit) Write(ctx context.Context, write func(*dbgen.Queries) error) error {
	return f.Database.Write(ctx, func(q *dbgen.Queries) error {
		if err := write(q); err != nil {
			return err
		}
		if f.fail {
			return errors.New("sensitive underlying failure")
		}
		return nil
	})
}

func assertCode(t *testing.T, err error, code domain.ErrorCode) *domain.Problem {
	t.Helper()
	var p *domain.Problem
	if !errors.As(err, &p) || p.Code != code {
		t.Fatalf("expected %s, got %v", code, err)
	}
	return p
}

func TestPinnedCatalogIdempotencyCASAndRollback(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, err := storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	fault := &failingCommit{Database: db}
	var calls atomic.Int64
	clock := time.Date(2026, 1, 1, 5, 0, 0, 0, time.FixedZone("test", 28800))
	r, err := NewService(stage3.RulesOptions{Runtime: stage3.Runtime{DB: fault, Now: func() time.Time { return clock }, NewID: func(kind string) (domain.ID, error) {
		return domain.ID(fmt.Sprintf("%s_%032x", kind, calls.Add(1))), nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	create := domain.Mutation{IdempotencyKey: "create"}
	values := stage3.DomainSetSpec{Domains: []string{"A.example", "例子.cn", "a.example", "B.example"}}
	first, err := r.SaveDomainSet(ctx, create, "", "A set", values)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := r.SaveDomainSet(ctx, create, "", "A set", values)
	if replay != first || err != nil || calls.Load() != 1 {
		t.Fatalf("create replay=%v; err=%v; allocations=%d", replay, err, calls.Load())
	}
	_, err = r.SaveDomainSet(ctx, create, "", "changed", values)
	assertCode(t, err, domain.IdempotencyConflict)
	edit := domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "set edit"}
	second, err := r.SaveDomainSet(ctx, edit, first.ID, "new name", stage3.DomainSetSpec{Domains: []string{"new.example"}})
	if err != nil || second.Revision != 2 {
		t.Fatalf("set edit: %v", err)
	}
	stale := domain.Mutation{ExpectedRevision: 1, IdempotencyKey: "stale"}
	_, err = r.SaveDomainSet(ctx, stale, first.ID, "new name", values)
	if current := assertCode(t, err, domain.RevisionConflict).CurrentRevision; current == nil || *current != 2 {
		t.Fatal("missing current revision")
	}
	old, err := r.DomainSet(ctx, first)
	if err != nil || !reflect.DeepEqual(old.Spec.Domains, []string{"a.example", "xn--fsqu00a.cn", "b.example"}) {
		t.Fatalf("old domain order or normalization lost: %v %v", old, err)
	}
	in := stage3.ProfileInput{Name: "profile", Sets: []control.Pin{first},
		Source: "MATCH,All-Proxies\nRULE-SET," + string(first.ID) + ",All-Proxies"}
	p1, issues, err := r.SaveProfile(ctx, create, in) // same key, different operation
	if err != nil || len(issues) != 0 {
		t.Fatalf("profile: %v %+v", err, issues)
	}
	view, err := r.Profile(ctx, p1)
	if err != nil || view.Spec.Sets[0].Pin != first || *view.Spec.Rules[0].DomainSet != first {
		t.Fatalf("profile did not bind old pin: %v", err)
	}
	view.Spec.Sets[0].Spec.Domains[0] = "changed"
	*view.Spec.Rules[0].DomainSet = second
	again, err := r.Profile(ctx, p1)
	if err != nil || again.Spec.Sets[0].Spec.Domains[0] != "a.example" || *again.Spec.Rules[0].DomainSet != first {
		t.Fatal("caller altered stored snapshot")
	}
	in.ID, in.Sets = p1.ID, []control.Pin{second}
	up := domain.Mutation{ExpectedRevision: p1.Revision, IdempotencyKey: "update"}
	fault.fail = true
	p2, _, err := r.SaveProfile(ctx, up, in)
	assertCode(t, err, internal)
	if p2.Valid() {
		t.Fatal("a rolled back write returned a pin")
	}
	fault.fail = false
	p2, _, err = r.SaveProfile(ctx, up, in)
	if err != nil || p2.Revision != 2 {
		t.Fatalf("retry did not recover from transaction failure: %v", err)
	}
	_, _, err = r.SaveProfile(ctx, stale, in)
	assertCode(t, err, domain.RevisionConflict)
	obsolete, _, err := r.SaveProfile(ctx, create, stage3.ProfileInput{Name: in.Name, Source: in.Source, Sets: []control.Pin{first}})
	if err != nil || obsolete != p1 {
		t.Fatal("profile replay did not return original pin after edit")
	}
	bad := p1
	bad.Digest = strings.Repeat("a", 64)
	_, err = r.Profile(ctx, bad)
	assertCode(t, err, domain.NotFound)
	bad = first
	bad.Digest = strings.Repeat("a", 64)
	_, _, err = r.SaveProfile(ctx, domain.Mutation{ExpectedRevision: 2, IdempotencyKey: "bad set"}, stage3.ProfileInput{ID: p2.ID, Name: in.Name, Source: in.Source, Sets: []control.Pin{bad}})
	assertCode(t, err, domain.NotFound)
	if _, err := r.Profile(ctx, p2); err != nil {
		t.Fatal("failed save damaged previous revision")
	}
	done, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := r.Profile(done, p1); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled read appeared successful")
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	tamper, err := sql.Open("sqlite", filepath.Join(dir, "controller.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer tamper.Close()
	if _, err := tamper.ExecContext(ctx, "UPDATE domain_set_members SET domain = 'tampered.example' WHERE domain_set_id = ? AND revision = 1 AND position = 0", first.ID); err != nil {
		t.Fatal(err)
	}
	if err := tamper.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r, err = NewService(stage3.RulesOptions{Runtime: stage3.Runtime{DB: db}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Profile(ctx, p1)
	assertCode(t, err, domain.NotFound)
	if _, err := r.Profile(ctx, p2); err != nil {
		t.Fatal("tampering old revision made independent revision unreadable")
	}
}
