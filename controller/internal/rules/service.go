package rules

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/controller/internal/rules/rulespec"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

const (
	adminScope = "controller_admin" // one authenticated principal; never caller supplied
	saveSet    = "domain_set.save"
	saveProf   = "profile.save"
	internal   = domain.ErrorCode("INTERNAL_ERROR")
)

type Service struct {
	db    storage.Database
	now   func() time.Time
	newID func(string) (domain.ID, error)
}

var _ stage3.Rules = (*Service)(nil)

func NewService(o stage3.RulesOptions) (*Service, error) {
	if o.DB == nil {
		return nil, invalid("/database")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.NewID == nil {
		o.NewID = domain.NewID
	}
	return &Service{db: o.DB, now: o.Now, newID: o.NewID}, nil
}

func problem(code domain.ErrorCode) *domain.Problem { return &domain.Problem{Code: code} }

func invalid(path string) *domain.Problem {
	p := problem(domain.InvalidArgument)
	p.Fields = []domain.FieldError{{Path: path, Code: p.Code}}
	return p
}

func safeResult(ctx context.Context, err error) error {
	if cancel := ctx.Err(); cancel != nil {
		return cancel
	}
	var p *domain.Problem
	switch {
	case err == nil:
		return nil
	case errors.As(err, &p):
		return p
	case errors.Is(err, sql.ErrNoRows):
		return problem(domain.NotFound)
	default:
		return problem(internal)
	}
}

func validMutation(m domain.Mutation, id domain.ID, name string) error {
	if m.ExpectedRevision == 0 && id != "" || m.ExpectedRevision != 0 && (!m.ExpectedRevision.Valid() || !id.Valid()) {
		return invalid("/expected_revision")
	}
	if len(m.IdempotencyKey) < 1 || len(m.IdempotencyKey) > 128 ||
		!utf8.ValidString(m.IdempotencyKey) || strings.ContainsFunc(m.IdempotencyKey, unicode.IsControl) {
		return invalid("/idempotency_key")
	}
	if strings.TrimSpace(name) == "" || len(name) > 255 || !utf8.ValidString(name) || strings.ContainsFunc(name, unicode.IsControl) {
		return invalid("/name")
	}
	return nil
}

func lookup(ctx context.Context, q *dbgen.Queries, op, key, digest string) (control.Pin, error) {
	r, err := q.B07GetMutation(ctx, dbgen.B07GetMutationParams{
		Scope: adminScope, Operation: op, IdempotencyKey: key,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return control.Pin{}, nil
	}
	if err != nil {
		return control.Pin{}, err
	}
	if r.RequestDigest != digest {
		return control.Pin{}, problem(domain.IdempotencyConflict)
	}
	p := control.Pin{ID: domain.ID(r.ResultID), Revision: domain.Revision(r.ResultRevision), Digest: r.ResultDigest}
	if !p.Valid() {
		return control.Pin{}, problem(internal)
	}
	// The ledger is written with the immutable version in the same transaction.
	var stored string
	if op == saveSet {
		stored, err = q.B07GetDomainPin(ctx, dbgen.B07GetDomainPinParams{DomainSetID: r.ResultID, Revision: r.ResultRevision})
	} else {
		marker, e := q.B07GetProfilePin(ctx, dbgen.B07GetProfilePinParams{ProfileID: r.ResultID, Revision: r.ResultRevision})
		stored, err = marker.Digest, e
	}
	if err != nil || stored != p.Digest {
		return control.Pin{}, problem(internal)
	}
	return p, nil
}

func (s *Service) replay(ctx context.Context, op, key, digest string) (p control.Pin, err error) {
	err = s.db.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		p, e = lookup(ctx, q, op, key, digest)
		return e
	})
	if err = safeResult(ctx, err); err != nil {
		return control.Pin{}, err
	}
	return p, nil
}

func (s *Service) preparePin(ctx context.Context, m domain.Mutation, kind string, id domain.ID, spec any) (control.Pin, string, error) {
	if err := ctx.Err(); err != nil {
		return control.Pin{}, "", err
	}
	if m.ExpectedRevision == 0 {
		got, err := s.newID(kind)
		if err != nil || !got.Valid() {
			return control.Pin{}, "", problem(internal)
		}
		id = got
	}
	if m.ExpectedRevision == domain.MaxRevision {
		return control.Pin{}, "", invalid("/expected_revision")
	}
	p, err := stage3.NewPin(id, m.ExpectedRevision+1, spec)
	return p, s.now().UTC().Format(time.RFC3339Nano), err
}

func (s *Service) write(ctx context.Context, op, key, digest string, pin control.Pin, save func(*dbgen.Queries) error) (control.Pin, error) {
	out := pin
	err := s.db.Write(ctx, func(q *dbgen.Queries) error {
		old, err := lookup(ctx, q, op, key, digest)
		if err != nil {
			return err
		}
		if old != (control.Pin{}) {
			out = old
			return nil
		}
		if err := save(q); err != nil {
			return err
		}
		return q.B07InsertMutation(ctx, dbgen.B07InsertMutationParams{
			Scope: adminScope, Operation: op, IdempotencyKey: key, RequestDigest: digest,
			ResultID: string(pin.ID), ResultRevision: int64(pin.Revision), ResultDigest: pin.Digest,
		})
	})
	if err = safeResult(ctx, err); err != nil {
		return control.Pin{}, err
	}
	return out, nil
}

func conflict(revision int64, err error) error {
	if err != nil {
		return err
	}
	cur := domain.Revision(revision)
	return &domain.Problem{Code: domain.RevisionConflict, CurrentRevision: &cur}
}

func (s *Service) SaveDomainSet(ctx context.Context, m domain.Mutation, id domain.ID, name string, input stage3.DomainSetSpec) (control.Pin, error) {
	if err := ctx.Err(); err != nil {
		return control.Pin{}, err
	}
	if err := validMutation(m, id, name); err != nil {
		return control.Pin{}, err
	}
	if len(input.Domains) > rulespec.MaxMembers {
		return control.Pin{}, invalid("/domains")
	}
	for i, d := range input.Domains {
		if len(d) > 1024 || !utf8.ValidString(d) {
			return control.Pin{}, invalid(pathIndex("/domains", i))
		}
	}
	hash, err := control.ComponentDigest(struct {
		ID       domain.ID
		Expected domain.Revision
		Name     string
		Input    stage3.DomainSetSpec
	}{id, m.ExpectedRevision, name, input})
	if err != nil {
		return control.Pin{}, problem(internal)
	}
	if p, err := s.replay(ctx, saveSet, m.IdempotencyKey, hash); err != nil || p.Valid() {
		return p, err
	}
	spec, ok := rulespec.NormalizeDomainSet(input)
	if !ok {
		return control.Pin{}, invalid("/domains")
	}
	pin, at, err := s.preparePin(ctx, m, "set", id, spec)
	if err != nil {
		return control.Pin{}, err
	}
	return s.write(ctx, saveSet, m.IdempotencyKey, hash, pin, func(q *dbgen.Queries) error {
		id, rev := string(pin.ID), int64(pin.Revision)
		if m.ExpectedRevision == 0 {
			if err := q.B07InsertDomainHead(ctx, dbgen.B07InsertDomainHeadParams{ID: id, Identifier: id, Name: name, Revision: rev, UpdatedAt: at}); err != nil {
				return err
			}
		} else {
			changed, err := q.B07UpdateDomainHead(ctx, dbgen.B07UpdateDomainHeadParams{ID: id, Name: name, Revision: rev, UpdatedAt: at, ExpectedRevision: int64(m.ExpectedRevision)})
			if err != nil {
				return err
			}
			if changed != 1 {
				return conflict(q.B07GetDomainHead(ctx, id))
			}
		}
		if err := q.B07InsertDomainRevision(ctx, dbgen.B07InsertDomainRevisionParams{DomainSetID: id, Revision: rev}); err != nil {
			return err
		}
		if err := q.B07InsertDomainPin(ctx, dbgen.B07InsertDomainPinParams{DomainSetID: id, Revision: rev, Digest: pin.Digest}); err != nil {
			return err
		}
		for i, d := range spec.Domains {
			err := q.B07InsertDomainMember(ctx, dbgen.B07InsertDomainMemberParams{DomainSetID: id, Revision: rev, Domain: d, Position: nullable(i)})
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func nullable(i int) sql.NullInt64 { return sql.NullInt64{Int64: int64(i), Valid: true} }

func readSet(ctx context.Context, q *dbgen.Queries, pin control.Pin) (stage3.DomainSetSnapshot, error) {
	id, rev := string(pin.ID), int64(pin.Revision)
	digest, err := q.B07GetDomainPin(ctx, dbgen.B07GetDomainPinParams{DomainSetID: id, Revision: rev})
	if err != nil {
		return stage3.DomainSetSnapshot{}, err
	}
	if pin.Digest != digest {
		return stage3.DomainSetSnapshot{}, problem(domain.NotFound)
	}
	rows, err := q.B07ListDomainMembers(ctx, dbgen.B07ListDomainMembersParams{DomainSetID: id, Revision: rev})
	if err != nil {
		return stage3.DomainSetSnapshot{}, err
	}
	domains := make([]string, len(rows))
	for i, row := range rows {
		if row.Position != nullable(i) {
			return stage3.DomainSetSnapshot{}, problem(internal)
		}
		domains[i] = row.Domain
	}
	return stage3.DomainSetSnapshot{Pin: pin, Spec: stage3.DomainSetSpec{Domains: domains}}, nil
}

// DomainSet reads the exact version; it is available to the I03 admin projection.
func (s *Service) DomainSet(ctx context.Context, pin control.Pin) (stage3.DomainSetSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return stage3.DomainSetSnapshot{}, err
	}
	if !pin.Valid() {
		return stage3.DomainSetSnapshot{}, invalid("/pin")
	}
	var snap stage3.DomainSetSnapshot
	err := s.db.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		snap, err = readSet(ctx, q, pin)
		return err
	})
	if err == nil && !rulespec.ValidateDomainSet(snap) {
		err = problem(domain.NotFound)
	}
	if err = safeResult(ctx, err); err != nil {
		return stage3.DomainSetSnapshot{}, err
	}
	return snap, nil
}

func pathIndex(prefix string, i int) string { return prefix + "/" + strconv.Itoa(i) }
