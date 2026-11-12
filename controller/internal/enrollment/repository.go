package enrollment

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

type Repository struct{ db storage.Database }

func NewRepository(db storage.Database) *Repository { return &Repository{db: db} }
func stamp(t time.Time) string                      { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }
func problem(c domain.ErrorCode) error              { return &domain.Problem{Code: c} }
func missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return problem(domain.NotFound)
	}
	return err
}
func unauthMissing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return problem(domain.Unauthenticated)
	}
	return err
}
func (r *Repository) loadCA(ctx context.Context) (dbgen.B02GetCARow, bool, error) {
	var row dbgen.B02GetCARow
	var exists int64
	err := r.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		exists, e = q.B02CAExists(ctx)
		if e != nil {
			return e
		}
		if exists == 0 {
			return nil
		}
		row, e = q.B02GetCA(ctx)
		return e
	})
	return row, exists != 0, err
}
func (r *Repository) saveCA(ctx context.Context, cert []byte, ref domain.ID, sealed secretstore.Sealed, now time.Time) error {
	return r.db.Write(ctx, func(q *dbgen.Queries) error {
		n, e := q.B02CAExists(ctx)
		if e != nil {
			return e
		}
		if n != 0 {
			return nil
		}
		if e = q.B02InsertCredential(ctx, dbgen.B02InsertCredentialParams{ID: string(ref), Purpose: string(secretstore.ManagementCA), KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext, CreatedAt: stamp(now)}); e != nil {
			return e
		}
		return q.B02InsertCA(ctx, dbgen.B02InsertCAParams{Certificate: cert, CredentialRef: string(ref)})
	})
}
func (r *Repository) Get(ctx context.Context, id domain.ID) (domain.Node, error) {
	var n dbgen.Node
	err := r.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		n, e = q.GetNode(ctx, string(id))
		if e == nil && n.RemovedAt.Valid {
			return problem(domain.NotFound)
		}
		return missing(e)
	})
	return nodeDTO(n), err
}
func nodeDTO(n dbgen.Node) domain.Node {
	updated, _ := time.Parse(time.RFC3339Nano, n.UpdatedAt)
	out := domain.Node{Entity: domain.Entity{ID: domain.ID(n.ID), Name: n.Name, Revision: domain.Revision(n.Revision), UpdatedAt: updated}, ConnectionMode: n.ConnectionMode, Role: n.Role, ControlAddress: n.ControlAddress, Control: domain.Connection(n.ControlState), Tunnel: domain.Connection(n.TunnelState), Verification: domain.Verification(n.Verification), TargetDNSRevision: domain.Revision(n.TargetDnsRevision)}
	if n.ProxyAddress.Valid {
		v := n.ProxyAddress.String
		out.ProxyAddress = &v
	}
	if n.LastConfirmedAt.Valid {
		t, _ := time.Parse(time.RFC3339Nano, n.LastConfirmedAt.String)
		out.LastConfirmedAt = &t
	}
	return out
}
func (r *Repository) List(ctx context.Context, query domain.ListQuery) (domain.Page[domain.Node], error) {
	out := domain.Page[domain.Node]{Items: []domain.Node{}}
	if e := query.Validate(); e != nil {
		return out, e
	}
	cursor := query.Cursor
	err := r.db.Read(ctx, func(q *dbgen.Queries) error {
		for len(out.Items) <= query.Limit {
			rows, e := q.ListNodes(ctx, dbgen.ListNodesParams{ID: cursor, Limit: int64(query.Limit + 1)})
			if e != nil {
				return e
			}
			for _, n := range rows {
				cursor = n.ID
				if !n.RemovedAt.Valid {
					out.Items = append(out.Items, nodeDTO(n))
					if len(out.Items) > query.Limit {
						break
					}
				}
			}
			if len(rows) < query.Limit+1 {
				break
			}
		}
		return nil
	})
	if len(out.Items) > query.Limit {
		c := string(out.Items[query.Limit-1].ID)
		out.NextCursor = &c
		out.Items = out.Items[:query.Limit]
	}
	return out, err
}

type preparedToken struct {
	id      domain.ID
	hash    string
	sealed  secretstore.Sealed
	expires time.Time
}

func (r *Repository) issue(ctx context.Context, id domain.ID, req IssueRequest, token preparedToken, hash string, now time.Time, create bool) (domain.ID, error) {
	scope := "enrollment:" + string(id)
	if create {
		scope = "node:create"
	}
	var ref domain.ID
	err := r.db.Write(ctx, func(q *dbgen.Queries) error {
		old, e := q.B02GetMutation(ctx, dbgen.B02GetMutationParams{Scope: scope, IdempotencyKey: req.IdempotencyKey})
		if e == nil {
			if old.RequestDigest != hash {
				return problem(domain.IdempotencyConflict)
			}
			ref = domain.ID(old.TokenRef.String)
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		var generation int64
		if create {
			if req.Mode == "reverse_tunnel" {
				relay, e := q.GetNode(ctx, string(req.RelayNodeID))
				if e != nil || relay.RemovedAt.Valid || relay.Role != "both" {
					return problem(domain.InvalidArgument)
				}
			}
			if e = q.CreateNode(ctx, dbgen.CreateNodeParams{ID: string(id), Name: req.Name, ConnectionMode: req.Mode, Role: req.Role, ControlAddress: req.ControlAddress, ProxyAddress: sql.NullString{String: req.ProxyAddress, Valid: req.ProxyAddress != ""}, UpdatedAt: stamp(now)}); e != nil {
				return e
			}
			if e = q.B02InsertEnrollment(ctx, dbgen.B02InsertEnrollmentParams{NodeID: string(id), RelayNodeID: sql.NullString{String: string(req.RelayNodeID), Valid: req.RelayNodeID != ""}}); e != nil {
				return e
			}
		} else {
			n, e := q.GetNode(ctx, string(id))
			if e != nil {
				return missing(e)
			}
			if n.RemovedAt.Valid {
				return problem(domain.NotFound)
			}
			if n.Revision != int64(req.ExpectedRevision) {
				return conflict(n.Revision)
			}
		}
		en, e := q.B02GetEnrollment(ctx, string(id))
		if e != nil {
			return e
		}
		generation = en.Generation + 1
		changed, e := q.B02AdvanceGeneration(ctx, dbgen.B02AdvanceGenerationParams{NodeID: string(id), Generation: en.Generation})
		if e != nil {
			return e
		}
		if changed != 1 {
			return problem(domain.RevisionConflict)
		}
		if e = q.B02RevokeTokens(ctx, dbgen.B02RevokeTokensParams{NodeID: string(id), RevokedAt: sql.NullString{String: stamp(now), Valid: true}}); e != nil {
			return e
		}
		if e = q.B02InsertCredential(ctx, dbgen.B02InsertCredentialParams{ID: string(token.id), Purpose: string(secretstore.Certificate), KeyID: token.sealed.KeyID, Ciphertext: token.sealed.Ciphertext, CreatedAt: stamp(now)}); e != nil {
			return e
		}
		if e = q.B02InsertToken(ctx, dbgen.B02InsertTokenParams{ID: string(token.id), NodeID: string(id), Generation: generation, TokenHash: token.hash, ExpiresAt: stamp(token.expires)}); e != nil {
			return e
		}
		revision := int64(1)
		if !create {
			n, e := q.B02BumpNode(ctx, dbgen.B02BumpNodeParams{ID: string(id), Revision: int64(req.ExpectedRevision), UpdatedAt: stamp(now)})
			if e != nil {
				return e
			}
			if n != 1 {
				return conflict(int64(req.ExpectedRevision))
			}
			revision = int64(req.ExpectedRevision) + 1
		}
		if e = q.B02InsertMutation(ctx, dbgen.B02InsertMutationParams{Scope: scope, IdempotencyKey: req.IdempotencyKey, RequestDigest: hash, NodeID: string(id), Revision: revision, TokenRef: sql.NullString{String: string(token.id), Valid: true}}); e != nil {
			return e
		}
		ref = token.id
		return nil
	})
	return ref, err
}
func conflict(n int64) error {
	v := domain.Revision(n)
	return &domain.Problem{Code: domain.RevisionConflict, CurrentRevision: &v}
}
func (r *Repository) token(ctx context.Context, hash string) (dbgen.B02Token, error) {
	var t dbgen.B02Token
	err := r.db.Read(ctx, func(q *dbgen.Queries) error { var e error; t, e = q.B02GetToken(ctx, hash); return missing(e) })
	return t, err
}
func (r *Repository) tokenByID(ctx context.Context, id domain.ID) (dbgen.B02GetTokenByIDRow, error) {
	var t dbgen.B02GetTokenByIDRow
	err := r.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		t, e = q.B02GetTokenByID(ctx, string(id))
		return missing(e)
	})
	return t, err
}
func (r *Repository) consume(ctx context.Context, hash, csrHash string, certPEM []byte, now func() time.Time) (Identity, bool, error) {
	var out Identity
	committedChange := false
	cert, e := pki.ParseCertificate(certPEM)
	if e != nil {
		return out, false, e
	}
	err := r.db.Write(ctx, func(q *dbgen.Queries) error {
		t, e := q.B02GetToken(ctx, hash)
		if e != nil {
			return unauthMissing(e)
		}
		n, e := q.GetNode(ctx, t.NodeID)
		if e != nil {
			return e
		}
		en, e := q.B02GetEnrollment(ctx, t.NodeID)
		if e != nil {
			return e
		}
		if n.RemovedAt.Valid || t.RevokedAt.Valid || en.Generation != t.Generation {
			return problem(domain.Forbidden)
		}
		if t.CsrHash.Valid {
			if t.CsrHash.String != csrHash {
				return problem(domain.IdempotencyConflict)
			}
			old, e := pki.ParseCertificate(t.Certificate)
			if e != nil {
				return e
			}
			record, e := q.B02GetCertificate(ctx, pki.Fingerprint(old))
			if e != nil {
				return e
			}
			if record.RevokedAt.Valid || !now().Before(old.NotAfter) {
				return problem(domain.Forbidden)
			}
			out = Identity{NodeID: domain.ID(t.NodeID), CertificatePEM: string(t.Certificate)}
			return nil
		}
		expires, e := time.Parse(time.RFC3339Nano, t.ExpiresAt)
		if e != nil {
			return e
		}
		if !now().Before(expires) {
			return problem(domain.TaskExpired)
		}
		changed, e := q.B02ConsumeToken(ctx, dbgen.B02ConsumeTokenParams{ID: t.ID, CsrHash: sql.NullString{String: csrHash, Valid: true}, Certificate: certPEM, ExpiresAt: stamp(now())})
		if e != nil {
			return e
		}
		if changed != 1 {
			return problem(domain.IdempotencyConflict)
		}
		if e = q.B02RevokeCertificates(ctx, dbgen.B02RevokeCertificatesParams{NodeID: t.NodeID, RevokedAt: sql.NullString{String: stamp(now()), Valid: true}}); e != nil {
			return e
		}
		if e = q.B02InsertCertificate(ctx, dbgen.B02InsertCertificateParams{Fingerprint: pki.Fingerprint(cert), NodeID: t.NodeID, Certificate: certPEM, CsrHash: csrHash, NotAfter: stamp(cert.NotAfter)}); e != nil {
			return e
		}
		committedChange = true
		out = Identity{NodeID: domain.ID(t.NodeID), CertificatePEM: string(certPEM)}
		return nil
	})
	return out, committedChange, err
}
func (r *Repository) mutate(ctx context.Context, id domain.ID, expected domain.Revision, key, name string, remove bool, hash string, now time.Time) (domain.Revision, error) {
	var taskID domain.ID
	if remove {
		var e error
		taskID, e = domain.NewID("task")
		if e != nil {
			return 0, e
		}
	}
	scope := "rename:" + string(id)
	if remove {
		scope = "remove:" + string(id)
	}
	var revision domain.Revision
	err := r.db.Write(ctx, func(q *dbgen.Queries) error {
		old, e := q.B02GetMutation(ctx, dbgen.B02GetMutationParams{Scope: scope, IdempotencyKey: key})
		if e == nil {
			if old.RequestDigest != hash {
				return problem(domain.IdempotencyConflict)
			}
			revision = domain.Revision(old.Revision)
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		n, e := q.GetNode(ctx, string(id))
		if e != nil {
			return missing(e)
		}
		if n.RemovedAt.Valid {
			return problem(domain.NotFound)
		}
		if n.Revision != int64(expected) {
			return conflict(n.Revision)
		}
		var changed int64
		if remove {
			changed, e = q.B02RemoveNode(ctx, dbgen.B02RemoveNodeParams{ID: string(id), Revision: int64(expected), RemovedAt: sql.NullString{String: stamp(now), Valid: true}, UpdatedAt: stamp(now)})
			if e != nil {
				return e
			}
			if e = q.B02RevokeTokens(ctx, dbgen.B02RevokeTokensParams{NodeID: string(id), RevokedAt: sql.NullString{String: stamp(now), Valid: true}}); e != nil {
				return e
			}
			if e = q.B02RevokeCertificates(ctx, dbgen.B02RevokeCertificatesParams{NodeID: string(id), RevokedAt: sql.NullString{String: stamp(now), Valid: true}}); e != nil {
				return e
			}
		} else {
			changed, e = q.RenameNode(ctx, dbgen.RenameNodeParams{ID: string(id), Revision: int64(expected), Name: name, UpdatedAt: stamp(now)})
		}
		if e != nil {
			return e
		}
		if changed != 1 {
			return conflict(n.Revision)
		}
		revision = expected + 1
		taskRef := sql.NullString{}
		if remove {
			// Persist the real cleanup intent in the same transaction as identity
			// revocation. B04 reconciles this removal intent; it is never delivered
			// to a revoked node and never treated as Applied/Verified here.
			if e = q.InsertTask(ctx, dbgen.InsertTaskParams{ID: string(taskID), Kind: string(domain.Revoke), SubjectID: string(id), TargetRevision: int64(revision), RequestDigest: hash, IdempotencyScope: scope, IdempotencyKey: key, Deadline: stamp(now.Add(24 * time.Hour)), CreatedAt: stamp(now)}); e != nil {
				return e
			}
			if e = q.InsertTaskNode(ctx, dbgen.InsertTaskNodeParams{TaskID: string(taskID), NodeID: string(id), TargetRevision: int64(revision), TargetDigest: hash, RevocationEpoch: n.RevocationEpoch + 1}); e != nil {
				return e
			}
			if e = q.B02MarkRemovalPending(ctx, dbgen.B02MarkRemovalPendingParams{TaskID: string(taskID), NodeID: string(id)}); e != nil {
				return e
			}
			taskRef = sql.NullString{String: string(taskID), Valid: true}
		}
		return q.B02InsertMutation(ctx, dbgen.B02InsertMutationParams{Scope: scope, IdempotencyKey: key, RequestDigest: hash, NodeID: string(id), Revision: int64(revision), TaskID: taskRef})
	})
	return revision, err
}
