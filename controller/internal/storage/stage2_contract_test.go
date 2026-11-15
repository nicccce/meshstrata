package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestStage1LedgerNeverReceiptsUnsupportedLocalEvidence(t *testing.T) {
	// nil Store is intentional: unsupported evidence must fail before any SQL or
	// result acknowledgement can discard the payload in a stage-1-only schema.
	var store *Store
	_, err := store.RecordResult(context.Background(), "", control.Result{Restrictions: []control.Restriction{{}}})
	var problem *domain.Problem
	if !errors.As(err, &problem) || problem.Code != domain.NotImplemented {
		t.Fatal(err)
	}
}
