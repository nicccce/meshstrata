// Package egress owns fixed terminal policy and target versions, and the
// purpose-bound storage for external proxy authentication.
package egress

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

type Service struct{ opts stage3.EgressOptions }

var _ stage3.Egress = (*Service)(nil)

func NewService(opts stage3.EgressOptions) (*Service, error) {
	if opts.DB == nil || opts.Cipher == nil {
		return nil, problem(domain.InvalidArgument, "")
	}
	if opts.NewID == nil {
		opts.NewID = domain.NewID
	}
	return &Service{opts}, nil
}

func problem(code domain.ErrorCode, path string) *domain.Problem {
	p := &domain.Problem{Code: code}
	if path != "" {
		p.Fields = []domain.FieldError{{Path: path, Code: code}}
	}
	return p
}

func safe(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	var p *domain.Problem
	if errors.As(err, &p) {
		return p
	}
	if errors.Is(err, sql.ErrNoRows) {
		return problem(domain.NotFound, "")
	}
	return problem(domain.ErrorCode("INTERNAL"), "")
}

func textOK(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func validateWrite(m domain.Mutation, id domain.ID, name string) error {
	if (m.ExpectedRevision == 0 && id != "") || (m.ExpectedRevision != 0 && (!id.Valid() || !m.ExpectedRevision.Valid() || m.ExpectedRevision == domain.MaxRevision)) {
		return problem(domain.InvalidArgument, "/expected_revision")
	}
	if strings.TrimSpace(name) == "" || !textOK(name, 128) {
		return problem(domain.InvalidArgument, "/name")
	}
	if m.IdempotencyKey == "" || !textOK(m.IdempotencyKey, 128) {
		return problem(domain.InvalidArgument, "/idempotency_key")
	}
	return nil
}

func getReplay(ctx context.Context, q *dbgen.Queries, op, key, hash string) (control.Pin, bool, error) {
	r, err := q.B06Mutation(ctx, dbgen.B06MutationParams{Operation: op, IdemKey: key})
	if errors.Is(err, sql.ErrNoRows) {
		return control.Pin{}, false, nil
	}
	if err != nil {
		return control.Pin{}, false, err
	}
	if r.RequestDigest != hash {
		return control.Pin{}, false, problem(domain.IdempotencyConflict, "")
	}
	var pin control.Pin
	err = json.Unmarshal(r.Result, &pin)
	return pin, true, err
}

func (s *Service) readReplay(ctx context.Context, op, key, hash string) (pin control.Pin, found bool, err error) {
	err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		pin, found, e = getReplay(ctx, q, op, key, hash)
		return e
	})
	return
}

func version(ctx context.Context, q *dbgen.Queries, pin control.Pin, kind string, node domain.ID) (dbgen.B06EgressVersionRow, error) {
	if !pin.Valid() {
		return dbgen.B06EgressVersionRow{}, problem(domain.InvalidArgument, "/pin")
	}
	r, err := q.B06EgressVersion(ctx, dbgen.B06EgressVersionParams{ID: string(pin.ID), Revision: int64(pin.Revision)})
	if err != nil {
		return r, err
	}
	if r.Digest != pin.Digest || r.Kind != kind || node != "" && r.NodeID != string(node) {
		return r, problem(domain.NotFound, "/pin")
	}
	return r, nil
}

func update(ctx context.Context, q *dbgen.Queries, m domain.Mutation, id, node domain.ID, kind, name string) error {
	n, err := q.GetNode(ctx, string(node))
	if err != nil {
		return err
	}
	if n.RemovedAt.Valid {
		return problem(domain.NotFound, "/node_id")
	}
	if m.ExpectedRevision == 0 {
		return q.B06InsertEgressObject(ctx, dbgen.B06InsertEgressObjectParams{ID: string(id), NodeID: string(node), Kind: kind, Name: name})
	}
	obj, err := q.B06EgressObject(ctx, string(id))
	if err != nil {
		return err
	}
	if obj.Kind != kind || obj.NodeID != string(node) {
		return problem(domain.NotFound, "/id")
	}
	if domain.Revision(obj.Revision) != m.ExpectedRevision {
		rev := domain.Revision(obj.Revision)
		return &domain.Problem{Code: domain.RevisionConflict, CurrentRevision: &rev}
	}
	_, err = q.B06UpdateEgressObject(ctx, dbgen.B06UpdateEgressObjectParams{ID: string(id), Name: name, Revision: obj.Revision})
	return err
}

func persist(ctx context.Context, q *dbgen.Queries, m domain.Mutation, op, hash string, pin control.Pin, spec []byte) error {
	if err := q.B06InsertEgressVersion(ctx, dbgen.B06InsertEgressVersionParams{ID: string(pin.ID), Revision: int64(pin.Revision), Digest: pin.Digest, Spec: spec}); err != nil {
		return err
	}
	b, _ := json.Marshal(pin)
	return q.B06InsertMutation(ctx, dbgen.B06InsertMutationParams{Operation: op, IdemKey: m.IdempotencyKey, RequestDigest: hash, Result: b})
}

func (s *Service) SaveTarget(ctx context.Context, m domain.Mutation, in stage3.EgressTargetInput) (control.Pin, error) {
	pin, err := s.saveTarget(ctx, m, in)
	if err = safe(ctx, err); err != nil {
		return control.Pin{}, err
	}
	return pin, nil
}

func (s *Service) saveTarget(ctx context.Context, m domain.Mutation, in stage3.EgressTargetInput) (control.Pin, error) {
	const op = "egress.target.save"
	if err := validateWrite(m, in.ID, in.Name); err != nil {
		return control.Pin{}, err
	}
	t := in.Spec
	if !t.Valid() {
		return control.Pin{}, problem(domain.InvalidArgument, "/spec")
	}
	if in.Auth != nil && ((t.Type != "http" && t.Type != "socks5") || t.CredentialRef != "" || !validAuth(*in.Auth)) {
		return control.Pin{}, problem(domain.InvalidArgument, "/auth")
	}
	hash, err := control.ComponentDigest(struct {
		Revision domain.Revision
		ID       domain.ID
		Name     string
		Target   control.EgressTarget
		Replace  bool
	}{m.ExpectedRevision, in.ID, in.Name, t, in.Auth != nil})
	if err != nil {
		return control.Pin{}, err
	}
	replay := func(pin control.Pin) (control.Pin, error) {
		if in.Auth != nil {
			got, err := OpenAuth(ctx, s.opts.Runtime, s.opts.Cipher, pin, "")
			if err != nil {
				return control.Pin{}, err
			}
			if subtle.ConstantTimeCompare([]byte(got.Username), []byte(in.Auth.Username)) != 1 || subtle.ConstantTimeCompare([]byte(got.Password), []byte(in.Auth.Password)) != 1 {
				return control.Pin{}, problem(domain.IdempotencyConflict, "")
			}
		}
		return pin, nil
	}
	pin, found, err := s.readReplay(ctx, op, m.IdempotencyKey, hash)
	if err != nil {
		return control.Pin{}, err
	}
	if found {
		return replay(pin)
	}
	id := in.ID
	if id == "" {
		id, err = s.opts.NewID("target")
		if err != nil {
			return control.Pin{}, err
		}
	}
	var sealed secretstore.Sealed
	if in.Auth != nil {
		t.CredentialRef, err = s.opts.NewID("credential")
		if err != nil {
			return control.Pin{}, err
		}
		plain, _ := json.Marshal(in.Auth)
		sealed, err = s.opts.Cipher.Seal(ctx, secretstore.Binding{Ref: t.CredentialRef, Purpose: secretstore.EgressProxy}, plain)
		clear(plain)
		if err != nil {
			return control.Pin{}, err
		}
	}
	pin, err = stage3.NewPin(id, m.ExpectedRevision+1, t)
	if err != nil {
		return control.Pin{}, err
	}
	spec, _ := json.Marshal(t)
	var duplicate bool
	err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		p, f, e := getReplay(ctx, q, op, m.IdempotencyKey, hash)
		if e != nil {
			return e
		}
		if f {
			pin, duplicate = p, true
			return nil
		}
		if err := stage3.RequireActive(ctx, q, "target", id); err != nil {
			return err
		}
		if err := update(ctx, q, m, id, t.NodeID, "target", in.Name); err != nil {
			return err
		}
		if in.Auth == nil && t.CredentialRef != "" {
			old, err := q.B06EgressCredential(ctx, dbgen.B06EgressCredentialParams{ID: string(t.CredentialRef), TargetID: string(id)})
			if err != nil {
				return err
			}
			origin, err := q.B06EgressVersion(ctx, dbgen.B06EgressVersionParams{ID: string(id), Revision: old.OriginRevision})
			if err != nil {
				return err
			}
			// Exact pinned bytes of the origin must agree; explicitly replacing auth
			// is required when switching proxy destinations.
			if string(origin.Spec) != string(spec) {
				return problem(domain.InvalidArgument, "/spec/credential_ref")
			}
		}
		if err := persist(ctx, q, m, op, hash, pin, spec); err != nil {
			return err
		}
		if in.Auth == nil {
			return nil
		}
		return q.B06InsertEgressCredential(ctx, dbgen.B06InsertEgressCredentialParams{ID: string(t.CredentialRef), TargetID: string(id), OriginRevision: int64(pin.Revision), KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext})
	})
	if err == nil && duplicate {
		return replay(pin)
	}
	return pin, err
}

// ReplaceTargetAuth atomically persists a new credential, exact target version
// and replay result. The HTTP boundary never needs a second transaction.
func (s *Service) ReplaceTargetAuth(ctx context.Context, m domain.Mutation, source control.Pin, auth stage3.ProxyAuth) (control.Pin, error) {
	pin, err := s.replaceTargetAuth(ctx, m, source, auth)
	if err = safe(ctx, err); err != nil {
		return control.Pin{}, err
	}
	return pin, nil
}
func (s *Service) replaceTargetAuth(ctx context.Context, m domain.Mutation, source control.Pin, auth stage3.ProxyAuth) (control.Pin, error) {
	const op = "egress.target.credentials"
	if !source.Valid() || source.Revision != m.ExpectedRevision || !validAuth(auth) {
		return control.Pin{}, problem(domain.InvalidArgument, "/pin")
	}
	if err := validateWrite(m, source.ID, "credentials"); err != nil {
		return control.Pin{}, err
	}
	hash, err := control.ComponentDigest(source)
	if err != nil {
		return control.Pin{}, err
	}
	replay := func(pin control.Pin) (control.Pin, error) {
		got, err := OpenAuth(ctx, s.opts.Runtime, s.opts.Cipher, pin, "")
		if err != nil {
			return control.Pin{}, err
		}
		if subtle.ConstantTimeCompare([]byte(got.Username), []byte(auth.Username)) != 1 || subtle.ConstantTimeCompare([]byte(got.Password), []byte(auth.Password)) != 1 {
			return control.Pin{}, problem(domain.IdempotencyConflict, "")
		}
		return pin, nil
	}
	pin, found, err := s.readReplay(ctx, op, m.IdempotencyKey, hash)
	if err != nil {
		return control.Pin{}, err
	}
	if found {
		return replay(pin)
	}
	var target control.EgressTarget
	err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		v, err := version(ctx, q, source, "target", "")
		if err != nil {
			return err
		}
		return json.Unmarshal(v.Spec, &target)
	})
	if err != nil {
		return control.Pin{}, err
	}
	if target.Type != "http" && target.Type != "socks5" {
		return control.Pin{}, problem(domain.InvalidArgument, "/target")
	}
	target.CredentialRef, err = s.opts.NewID("credential")
	if err != nil {
		return control.Pin{}, err
	}
	plain, _ := json.Marshal(auth)
	sealed, err := s.opts.Cipher.Seal(ctx, secretstore.Binding{Ref: target.CredentialRef, Purpose: secretstore.EgressProxy}, plain)
	clear(plain)
	if err != nil {
		return control.Pin{}, err
	}
	pin, err = stage3.NewPin(source.ID, source.Revision+1, target)
	if err != nil {
		return control.Pin{}, err
	}
	raw, _ := json.Marshal(target)
	duplicate := false
	err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		prior, found, err := getReplay(ctx, q, op, m.IdempotencyKey, hash)
		if err != nil {
			return err
		}
		if found {
			pin = prior
			duplicate = true
			return nil
		}
		if err = stage3.RequireActive(ctx, q, "target", source.ID); err != nil {
			return err
		}
		current, err := q.B06EgressObject(ctx, string(source.ID))
		if err != nil {
			return err
		}
		if err = update(ctx, q, m, source.ID, target.NodeID, "target", current.Name); err != nil {
			return err
		}
		if err = persist(ctx, q, m, op, hash, pin, raw); err != nil {
			return err
		}
		return q.B06InsertEgressCredential(ctx, dbgen.B06InsertEgressCredentialParams{ID: string(target.CredentialRef), TargetID: string(source.ID), OriginRevision: int64(pin.Revision), KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext})
	})
	if err == nil && duplicate {
		return replay(pin)
	}
	return pin, err
}

func validAuth(a stage3.ProxyAuth) bool {
	return a.Username != "" && textOK(a.Username, 255) && textOK(a.Password, 255)
}

// OpenAuth is the B06 compiler's private adapter for the separate EgressProxy
// table. It checks ownership and binds the result to the terminal node.
func OpenAuth(ctx context.Context, runtime stage3.Runtime, cipher secretstore.Cipher, pin control.Pin, node domain.ID) (control.EgressAuth, error) {
	var r dbgen.B06EgressCredential
	var target control.EgressTarget
	err := runtime.DB.Read(ctx, func(q *dbgen.Queries) error {
		v, err := version(ctx, q, pin, "target", node)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(v.Spec, &target); err != nil {
			return err
		}
		r, err = q.B06EgressCredential(ctx, dbgen.B06EgressCredentialParams{ID: string(target.CredentialRef), TargetID: string(pin.ID)})
		return err
	})
	if err != nil {
		return control.EgressAuth{}, safe(ctx, err)
	}
	exact, err := stage3.NewPin(pin.ID, pin.Revision, target)
	if err != nil || exact != pin || !target.Valid() {
		return control.EgressAuth{}, problem(domain.NotFound, "/target")
	}
	b, err := cipher.Open(ctx, secretstore.Binding{Ref: target.CredentialRef, Purpose: secretstore.EgressProxy}, secretstore.Sealed{KeyID: r.KeyID, Ciphertext: r.Ciphertext})
	defer clear(b)
	if err != nil {
		return control.EgressAuth{}, safe(ctx, problem(domain.NotFound, "/auth"))
	}
	var a stage3.ProxyAuth
	if json.Unmarshal(b, &a) != nil || !validAuth(a) {
		return control.EgressAuth{}, problem(domain.NotFound, "/auth")
	}
	if ctx.Err() != nil {
		return control.EgressAuth{}, ctx.Err()
	}
	return control.EgressAuth{Target: pin, Ref: target.CredentialRef, Audience: target.NodeID, Username: a.Username, Password: a.Password}, nil
}

func normalize(spec control.EgressSpec) (control.EgressSpec, error) {
	spec.Targets = append([]control.PinnedEgressTarget{}, spec.Targets...)
	spec.Rules = append([]control.EgressRule{}, spec.Rules...)
	if len(spec.Rules) > control.MaxEgressRules || len(spec.Targets) > control.MaxEgressTargets {
		return spec, problem(domain.InvalidArgument, "/spec")
	}
	slices.SortFunc(spec.Targets, func(a, b control.PinnedEgressTarget) int { return strings.Compare(string(a.Pin.ID), string(b.Pin.ID)) })
	match := false
	for i := range spec.Rules {
		r := &spec.Rules[i]
		if r.Target != nil {
			p := *r.Target
			r.Target = &p
		}
		if r.Destination == "MATCH" {
			match = true
			continue
		}
		if ip, e := netip.ParseAddr(r.Destination); e == nil && ip.Zone() == "" && !ip.Is4In6() {
			r.Destination = netip.PrefixFrom(ip, ip.BitLen()).String()
		} else if p, e := netip.ParsePrefix(r.Destination); e == nil && !p.Addr().Is4In6() {
			r.Destination = p.Masked().String()
		} else {
			return spec, problem(domain.InvalidArgument, "/spec/rules")
		}
	}
	if !match {
		spec.Rules = append(spec.Rules, control.EgressRule{Destination: "MATCH"})
	}
	return spec, nil
}

func (s *Service) SavePolicy(ctx context.Context, m domain.Mutation, in stage3.EgressDraft) (control.Pin, error) {
	pin, err := s.savePolicy(ctx, m, in)
	if err = safe(ctx, err); err != nil {
		return control.Pin{}, err
	}
	return pin, nil
}

func (s *Service) savePolicy(ctx context.Context, m domain.Mutation, in stage3.EgressDraft) (control.Pin, error) {
	const op = "egress.policy.save"
	if err := validateWrite(m, in.ID, in.Name); err != nil {
		return control.Pin{}, err
	}
	// Hash the actual request before normalization. Only the resulting policy
	// snapshot uses normalized rules and target ordering.
	if len(in.Spec.Rules) > control.MaxEgressRules || len(in.Spec.Targets) > control.MaxEgressTargets {
		return control.Pin{}, problem(domain.InvalidArgument, "/spec")
	}
	hash, err := control.ComponentDigest(struct {
		Revision domain.Revision
		Draft    stage3.EgressDraft
	}{m.ExpectedRevision, in})
	if err != nil {
		return control.Pin{}, problem(domain.InvalidArgument, "/spec")
	}
	spec, err := normalize(in.Spec)
	if err != nil {
		return control.Pin{}, err
	}
	p, found, err := s.readReplay(ctx, op, m.IdempotencyKey, hash)
	if err != nil || found {
		return p, err
	}
	id := in.ID
	if id == "" {
		id, err = s.opts.NewID("policy")
		if err != nil {
			return p, err
		}
	}
	pin, err := stage3.NewPin(id, m.ExpectedRevision+1, spec)
	if err != nil || spec.Validate(pin, true) != nil {
		return p, problem(domain.InvalidArgument, "/spec")
	}
	data, _ := json.Marshal(spec)
	err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		p, f, e := getReplay(ctx, q, op, m.IdempotencyKey, hash)
		if e != nil {
			return e
		}
		if f {
			pin = p
			return nil
		}
		if err := stage3.RequireActive(ctx, q, "policy", id); err != nil {
			return err
		}
		for _, t := range spec.Targets {
			if err := stage3.RequireActive(ctx, q, "target", t.Pin.ID); err != nil {
				return err
			}
		}
		if err := update(ctx, q, m, id, spec.NodeID, "policy", in.Name); err != nil {
			return err
		}
		for _, t := range spec.Targets {
			v, err := version(ctx, q, t.Pin, "target", spec.NodeID)
			if err != nil {
				return err
			}
			expect, _ := json.Marshal(t.Spec)
			if string(v.Spec) != string(expect) {
				return problem(domain.NotFound, "/spec/targets")
			}
		}
		if err := persist(ctx, q, m, op, hash, pin, data); err != nil {
			return err
		}
		for _, t := range spec.Targets {
			err := q.B06InsertPolicyTarget(ctx, dbgen.B06InsertPolicyTargetParams{PolicyID: string(pin.ID), PolicyRevision: int64(pin.Revision), TargetID: string(t.Pin.ID), TargetRevision: int64(t.Pin.Revision)})
			if err != nil {
				return err
			}
		}
		return nil
	})
	return pin, err
}

func (s *Service) Policy(ctx context.Context, pin control.Pin) (control.EgressSpec, error) {
	var row dbgen.B06EgressVersionRow
	var targets []dbgen.B06PolicyTargetsRow
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		row, e = version(ctx, q, pin, "policy", "")
		if e != nil {
			return e
		}
		targets, e = q.B06PolicyTargets(ctx, dbgen.B06PolicyTargetsParams{PolicyID: string(pin.ID), PolicyRevision: int64(pin.Revision)})
		return e
	})
	if err = safe(ctx, err); err != nil {
		return control.EgressSpec{}, err
	}
	var spec control.EgressSpec
	if json.Unmarshal(row.Spec, &spec) != nil || spec.Validate(pin, true) != nil || len(spec.Targets) != len(targets) || string(spec.NodeID) != row.NodeID {
		return control.EgressSpec{}, problem(domain.NotFound, "/pin")
	}
	for i, target := range targets {
		wanted := spec.Targets[i]
		data, _ := json.Marshal(wanted.Spec)
		if target.TargetID != string(wanted.Pin.ID) || target.TargetRevision != int64(wanted.Pin.Revision) ||
			target.Digest != wanted.Pin.Digest || target.Kind != "target" || target.NodeID != row.NodeID ||
			string(data) != string(target.Spec) {
			return control.EgressSpec{}, problem(domain.NotFound, "/targets")
		}
	}
	if ctx.Err() != nil {
		return control.EgressSpec{}, ctx.Err()
	}
	return spec, nil
}

// Select evaluates the fixed, already validated rule sequence against the
// supplied destination IP. It never retries a later rule on availability
// failure. DNS resolution and availability are the caller's responsibility.
func Select(spec control.EgressSpec, ip netip.Addr) *control.Pin {
	for _, rule := range spec.Rules {
		p, _ := netip.ParsePrefix(rule.Destination)
		if rule.Destination == "MATCH" || p.Contains(ip) {
			if rule.Target == nil {
				return nil
			}
			pin := *rule.Target
			return &pin
		}
	}
	return nil
}

// Target returns a private Controller snapshot for building a policy with an
// exact target pin. Ordinary HTTP projections must omit the credential ref.
func (s *Service) Target(ctx context.Context, pin control.Pin) (control.EgressTarget, error) {
	var row dbgen.B06EgressVersionRow
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		row, e = version(ctx, q, pin, "target", "")
		return e
	})
	if err = safe(ctx, err); err != nil {
		return control.EgressTarget{}, err
	}
	var t control.EgressTarget
	if json.Unmarshal(row.Spec, &t) != nil || !t.Valid() || string(t.NodeID) != row.NodeID {
		return control.EgressTarget{}, problem(domain.NotFound, "/target")
	}
	exact, err := stage3.NewPin(pin.ID, pin.Revision, t)
	if err != nil || exact != pin {
		return control.EgressTarget{}, problem(domain.NotFound, "/target")
	}
	if ctx.Err() != nil {
		return control.EgressTarget{}, ctx.Err()
	}
	return t, nil
}
