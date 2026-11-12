package storage

import (
	"context"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"testing"
)

func TestB02TokenCertificatePairAndForeignKeys(t *testing.T) {
	s := foundation(t)
	node := testID("node", "a")
	fixtureNode(t, s, node)
	ctx := context.Background()
	q := dbgen.New(s.db)
	for _, id := range []string{"cred_11111111111111111111111111111111", "cred_22222222222222222222222222222222"} {
		if e := q.B02InsertCredential(ctx, dbgen.B02InsertCredentialParams{ID: id, Purpose: "certificate", KeyID: "test-key", Ciphertext: []byte("test-only"), CreatedAt: "2026-11-12T00:00:00Z"}); e != nil {
			t.Fatal(e)
		}
	}
	if e := q.B02InsertToken(ctx, dbgen.B02InsertTokenParams{ID: "cred_11111111111111111111111111111111", NodeID: string(node), Generation: 1, TokenHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ExpiresAt: "2026-11-12T00:15:00.000000000Z"}); e != nil {
		t.Fatal(e)
	}
	rejectSQL(t, s, `UPDATE b02_tokens SET csr_hash='partial'`)
	rejectSQL(t, s, `UPDATE b02_tokens SET certificate=x'01'`)
	if e := q.B02InsertToken(ctx, dbgen.B02InsertTokenParams{ID: "cred_22222222222222222222222222222222", NodeID: string(node), Generation: 1, TokenHash: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", ExpiresAt: "2026-11-12T00:15:00.000000000Z"}); e == nil {
		t.Fatal("duplicate generation accepted")
	}
	rejectSQL(t, s, `DELETE FROM nodes WHERE id=?`, string(node))
}
