package enrollment

import (
	"bytes"
	"context"
	"crypto/x509"
	"database/sql"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
)

func (s *Service) Authenticate(ctx context.Context, cert *x509.Certificate) (domain.ID, error) {
	id, e := pki.NodeID(cert)
	if e != nil || !s.options.Now().Before(cert.NotAfter) || s.options.Now().Before(cert.NotBefore) {
		return "", problem(domain.Unauthenticated)
	}
	pool := x509.NewCertPool()
	pool.AddCert(s.ca.Certificate)
	if _, e = cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: s.options.Now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); e != nil {
		return "", problem(domain.Unauthenticated)
	}
	e = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		record, e := q.B02GetCertificate(ctx, pki.Fingerprint(cert))
		if e != nil {
			return unauthMissing(e)
		}
		n, e := q.GetNode(ctx, string(id))
		if e != nil {
			return unauthMissing(e)
		}
		if record.NodeID != string(id) || record.RevokedAt.Valid || n.RemovedAt.Valid {
			return problem(domain.Forbidden)
		}
		return nil
	})
	return id, e
}
func (s *Service) Renew(ctx context.Context, old *x509.Certificate, csrPEM []byte) (Identity, error) {
	var out Identity
	id, e := s.Authenticate(ctx, old)
	if e != nil {
		return out, e
	}
	csr, e := pki.ParseCSR(csrPEM)
	if e != nil {
		return out, problem(domain.InvalidArgument)
	}
	// Retain the local key across response loss; renewal never rotates checkpoint/node ID.
	if !bytes.Equal(csr.RawSubjectPublicKeyInfo, old.RawSubjectPublicKeyInfo) {
		return out, problem(domain.InvalidArgument)
	}
	certPEM, e := s.ca.SignNode(csrPEM, id, s.options.Now(), s.options.CertificateLifetime)
	if e != nil {
		return out, e
	}
	cert, e := pki.ParseCertificate(certPEM)
	if e != nil {
		return out, e
	}
	e = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		record, e := q.B02GetCertificate(ctx, pki.Fingerprint(old))
		if e != nil {
			return unauthMissing(e)
		}
		n, e := q.GetNode(ctx, string(id))
		if e != nil {
			return e
		}
		if n.RemovedAt.Valid || record.RevokedAt.Valid || !s.options.Now().Before(old.NotAfter) {
			return problem(domain.Forbidden)
		}
		if record.RenewedTo.Valid {
			existing, e := q.B02GetCertificate(ctx, record.RenewedTo.String)
			if e != nil {
				return e
			}
			expiry, e := time.Parse(time.RFC3339Nano, existing.NotAfter)
			if e != nil {
				return e
			}
			if existing.RevokedAt.Valid || existing.CsrHash != pki.CSRHash(csr) || !s.options.Now().Before(expiry) {
				return problem(domain.Forbidden)
			}
			out = Identity{NodeID: id, CertificatePEM: string(existing.Certificate)}
			return nil
		}
		if e = q.B02InsertCertificate(ctx, dbgen.B02InsertCertificateParams{Fingerprint: pki.Fingerprint(cert), NodeID: string(id), Certificate: certPEM, CsrHash: pki.CSRHash(csr), NotAfter: stamp(cert.NotAfter)}); e != nil {
			return e
		}
		changed, e := q.B02RenewCertificate(ctx, dbgen.B02RenewCertificateParams{Fingerprint: record.Fingerprint, RenewedTo: sql.NullString{String: pki.Fingerprint(cert), Valid: true}})
		if e != nil {
			return e
		}
		if changed != 1 {
			return problem(domain.IdempotencyConflict)
		}
		out = Identity{NodeID: id, CertificatePEM: string(certPEM)}
		return nil
	})
	out.CAPEM = string(s.ca.PEM)
	return out, e
}
func (s *Service) Status(ctx context.Context, id domain.ID) (Status, error) {
	out := Status{NodeID: id, State: "pending"}
	_, e := s.repo.Get(ctx, id)
	if e != nil {
		return out, e
	}
	e = s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		en, e := q.B02GetEnrollment(ctx, string(id))
		if e != nil {
			return missing(e)
		}
		if en.RelayNodeID.Valid {
			v := domain.ID(en.RelayNodeID.String)
			out.RelayNodeID = &v
		}
		row, e := q.B02LatestToken(ctx, string(id))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		expires, e := time.Parse(time.RFC3339Nano, row.ExpiresAt)
		if e != nil {
			return e
		}
		out.ExpiresAt = &expires
		switch {
		case row.RevokedAt.Valid:
			out.State = "revoked"
		case row.CsrHash.Valid:
			out.State = "enrolled"
		case !s.options.Now().Before(expires):
			out.State = "expired"
		}
		return nil
	})
	return out, e
}

// RevokeCertificates disables management identity while preserving node ID and
// business checkpoint. An administrator may issue a new enrollment token later.
func (s *Service) RevokeCertificates(ctx context.Context, id domain.ID, expected domain.Revision, key string) (domain.Revision, error) {
	if !id.Valid() || !expected.Valid() || !validKey(key) {
		return 0, problem(domain.InvalidArgument)
	}
	var revision domain.Revision
	committedChange := false
	e := s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		scope := "certificate:revoke:" + string(id)
		hash := requestHash(expected)
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
		node, e := q.GetNode(ctx, string(id))
		if e != nil {
			return missing(e)
		}
		if node.RemovedAt.Valid {
			return problem(domain.NotFound)
		}
		if node.Revision != int64(expected) {
			return conflict(node.Revision)
		}
		changed, e := q.B02BumpNode(ctx, dbgen.B02BumpNodeParams{ID: string(id), Revision: int64(expected), UpdatedAt: stamp(s.options.Now())})
		if e != nil {
			return e
		}
		if changed != 1 {
			return conflict(node.Revision)
		}
		if e = q.B02RevokeCertificates(ctx, dbgen.B02RevokeCertificatesParams{NodeID: string(id), RevokedAt: sql.NullString{String: stamp(s.options.Now()), Valid: true}}); e != nil {
			return e
		}
		if e = q.B02RevokeTokens(ctx, dbgen.B02RevokeTokensParams{NodeID: string(id), RevokedAt: sql.NullString{String: stamp(s.options.Now()), Valid: true}}); e != nil {
			return e
		}
		committedChange = true
		revision = expected + 1
		return q.B02InsertMutation(ctx, dbgen.B02InsertMutationParams{Scope: scope, IdempotencyKey: key, RequestDigest: hash, NodeID: string(id), Revision: int64(revision)})
	})
	if e == nil && committedChange && s.options.OnIdentityChange != nil {
		s.options.OnIdentityChange(id)
	}
	return revision, e
}

// RemoveTask preserves the frozen HTTP 202 contract. Revocation, its idempotency
// record and an actual queued cleanup task commit atomically. B04 must reconcile
// RemovalIntent rather than try to deliver a command to the removed identity.
func (s *Service) RemoveTask(ctx context.Context, id domain.ID, expected domain.Revision, key string) (domain.TaskAccepted, error) {
	if _, e := s.Remove(ctx, id, expected, key); e != nil {
		return domain.TaskAccepted{}, e
	}
	var out domain.TaskAccepted
	e := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		row, e := q.B02GetMutation(ctx, dbgen.B02GetMutationParams{Scope: "remove:" + string(id), IdempotencyKey: key})
		if e != nil {
			return missing(e)
		}
		if !row.TaskID.Valid {
			return problem(domain.NotFound)
		}
		out.TaskID = domain.ID(row.TaskID.String)
		return nil
	})
	return out, e
}

type RemovalIntent struct {
	TaskID   domain.ID
	NodeID   domain.ID
	Revision domain.Revision
}

func (s *Service) RemovalIntent(ctx context.Context, task domain.ID) (RemovalIntent, error) {
	var out RemovalIntent
	e := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		row, e := q.B02GetRemovalIntent(ctx, sql.NullString{String: string(task), Valid: true})
		if e != nil {
			return missing(e)
		}
		out = RemovalIntent{TaskID: task, NodeID: domain.ID(row.NodeID), Revision: domain.Revision(row.Revision)}
		return nil
	})
	return out, e
}
