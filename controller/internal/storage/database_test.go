package storage_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
)

func TestDatabaseSharedTransactionBoundary(t *testing.T) {
	ctx := context.Background()
	s, err := storage.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var db storage.Database = s
	put := func(q *dbgen.Queries) error {
		return q.CreateMetadata(ctx, dbgen.CreateMetadataParams{Key: "boundary", Value: "committed"})
	}
	stop := errors.New("abort")
	if err := db.Write(ctx, func(q *dbgen.Queries) error {
		if err := put(q); err != nil {
			return err
		}
		return stop
	}); !errors.Is(err, stop) {
		t.Fatalf("callback error lost: %v", err)
	}
	if err := db.Read(ctx, func(q *dbgen.Queries) error {
		_, err := q.GetMetadata(ctx, "boundary")
		return err
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rollback result: %v", err)
	}
	if err := db.Write(ctx, put); err != nil {
		t.Fatal(err)
	}
	if err := db.Read(ctx, func(q *dbgen.Queries) error {
		got, err := q.GetMetadata(ctx, "boundary")
		if err == nil && got != "committed" {
			t.Fatalf("got %q", got)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	called := false
	if err := db.Write(cancelled, func(*dbgen.Queries) error { called = true; return nil }); !errors.Is(err, context.Canceled) || called {
		t.Fatalf("cancelled callback ran=%v err=%v", called, err)
	}
}
