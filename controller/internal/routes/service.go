// Package routes compiles fixed route and entry intents into complete V2 node
// plans. The release gateway, not the compiler, owns task acceptance and arm.
package routes

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type Service struct {
	opts    stage3.RoutesOptions
	entropy sync.Mutex
}

var _ stage3.Routes = (*Service)(nil)

func NewService(opts stage3.RoutesOptions) (*Service, error) {
	if opts.DB == nil || opts.Publisher == nil || opts.Infra == nil || opts.Cipher == nil || opts.Secrets == nil || opts.Egress == nil {
		return nil, problem(domain.InvalidArgument, "")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.NewID == nil {
		opts.NewID = domain.NewID
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	return &Service{opts: opts}, nil
}

func problem(code domain.ErrorCode, path string) *domain.Problem {
	p := &domain.Problem{Code: code}
	if path != "" {
		p.Fields = []domain.FieldError{{Path: path, Code: code}}
	}
	return p
}
func revisionConflict(rev int64) error {
	r := domain.Revision(rev)
	return &domain.Problem{Code: domain.RevisionConflict, CurrentRevision: &r}
}
func codeIs(err error, code domain.ErrorCode) bool {
	var p *domain.Problem
	return errors.As(err, &p) && p.Code == code
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

// Gateways may provide their own structured details. Only allowlisted codes
// cross into this service, with locally generated field paths.
func dependency(ctx context.Context, err error, path string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, code := range []domain.ErrorCode{domain.InvalidArgument, domain.NotFound, domain.RevisionConflict, domain.IdempotencyConflict, domain.PortInUse, domain.DependencyInUse, domain.NotImplemented, domain.TaskExpired, domain.GrantOriginUncertain} {
		if codeIs(err, code) {
			return problem(code, path)
		}
	}
	return problem(domain.ErrorCode("INTERNAL"), path)
}

func hopPath(i int, suffix string) string { return fmt.Sprintf("/hops/%d%s", i, suffix) }
func goodText(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
func validKey(key string) bool    { return key != "" && goodText(key, 128) }
func (s *Service) now() time.Time { return s.opts.Now().UTC() }
func utc(t time.Time) bool {
	return !t.IsZero() && t.Location() == time.UTC && t.Year() >= 1 && t.Year() <= 9999
}
func timeText(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func routeDraftShape(d stage3.RouteDraft) error {
	if strings.TrimSpace(d.Name) == "" || !goodText(d.Name, 128) {
		return problem(domain.InvalidArgument, "/name")
	}
	if len(d.Hops) == 0 || len(d.Hops) > control.MaxRouteHops {
		return problem(domain.InvalidArgument, "/hops")
	}
	if !d.Egress.Valid() {
		return problem(domain.InvalidArgument, "/egress")
	}
	seen := map[domain.ID]bool{}
	for i, h := range d.Hops {
		if !h.NodeID.Valid() || seen[h.NodeID] {
			return problem(domain.InvalidArgument, hopPath(i, "/node_id"))
		}
		seen[h.NodeID] = true
		if !h.NodeConfig.Valid() || !h.DNS.Valid() {
			return problem(domain.InvalidArgument, hopPath(i, "/pins"))
		}
		ep := h.Endpoint
		if ep.Port == 0 {
			ep.Port = 1
		}
		if !ep.Valid() {
			return problem(domain.InvalidArgument, hopPath(i, "/endpoint"))
		}
		ip, err := netip.ParseAddr(h.DialIP)
		literal, lerr := netip.ParseAddr(ep.Host)
		if err != nil || ip.Is4In6() || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() ||
			ip.String() != h.DialIP || lerr == nil && literal != ip {
			return problem(domain.InvalidArgument, hopPath(i, "/dial_ip"))
		}
		if d.BusinessUDP && ep.Protocol != domain.SS2022 && ep.Protocol != domain.Hysteria2 {
			return problem(domain.InvalidArgument, hopPath(i, "/endpoint/protocol"))
		}
	}
	return nil
}

func replay(ctx context.Context, q *dbgen.Queries, op, key, digest string, out any) (bool, error) {
	r, err := q.B06Mutation(ctx, dbgen.B06MutationParams{Operation: op, IdemKey: key})
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if r.RequestDigest != digest {
		return false, problem(domain.IdempotencyConflict, "")
	}
	return true, json.Unmarshal(r.Result, out)
}

func remember(ctx context.Context, q *dbgen.Queries, op, key, digest string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return q.B06InsertMutation(ctx, dbgen.B06InsertMutationParams{Operation: op, IdemKey: key, RequestDigest: digest, Result: b})
}

func (s *Service) SaveDraft(ctx context.Context, m domain.Mutation, d stage3.RouteDraft) (control.Pin, error) {
	p, err := s.saveDraft(ctx, m, d)
	if err = safe(ctx, err); err != nil {
		return control.Pin{}, err
	}
	return p, nil
}

func (s *Service) saveDraft(ctx context.Context, m domain.Mutation, d stage3.RouteDraft) (control.Pin, error) {
	const op = "routes.draft.save"
	if !validKey(m.IdempotencyKey) {
		return control.Pin{}, problem(domain.InvalidArgument, "/idempotency_key")
	}
	if m.ExpectedRevision == 0 && d.ID != "" || m.ExpectedRevision != 0 && (!d.ID.Valid() || !m.ExpectedRevision.Valid() || m.ExpectedRevision == domain.MaxRevision) {
		return control.Pin{}, problem(domain.InvalidArgument, "/expected_revision")
	}
	d.Hops = append([]stage3.HopDraft(nil), d.Hops...)
	if err := routeDraftShape(d); err != nil {
		return control.Pin{}, err
	}
	digest, err := control.ComponentDigest(struct {
		Revision domain.Revision
		Draft    stage3.RouteDraft
	}{m.ExpectedRevision, d})
	if err != nil {
		return control.Pin{}, problem(domain.InvalidArgument, "/draft")
	}
	var p control.Pin
	var found bool
	err = s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		found, err = replay(ctx, q, op, m.IdempotencyKey, digest, &p)
		return err
	})
	if err != nil || found {
		return p, err
	}
	if d.ID == "" {
		d.ID, err = s.opts.NewID("route")
		if err != nil {
			return p, err
		}
	}
	p, err = stage3.NewPin(d.ID, m.ExpectedRevision+1, d)
	if err != nil {
		return p, err
	}
	parts := make([]dbgen.B06InsertRouteHopParams, len(d.Hops))
	for i, h := range d.Hops {
		n, _ := json.Marshal(h.NodeConfig)
		dns, _ := json.Marshal(h.DNS)
		e, _ := json.Marshal(h.Endpoint)
		parts[i] = dbgen.B06InsertRouteHopParams{RouteID: string(p.ID), Revision: int64(p.Revision), Position: int64(i), NodeID: string(h.NodeID), NodeConfig: n, Dns: dns, Endpoint: e, DialIp: h.DialIP}
	}
	touched := timeText(s.now())
	err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		if found, err := replay(ctx, q, op, m.IdempotencyKey, digest, &p); found || err != nil {
			return err
		}
		if err := stage3.RequireActive(ctx, q, "route", p.ID); err != nil {
			return err
		}
		if err := stage3.RequireActive(ctx, q, "policy", d.Egress.ID); err != nil {
			return err
		}
		if m.ExpectedRevision == 0 {
			if err := q.B06CreateRoute(ctx, dbgen.B06CreateRouteParams{ID: string(p.ID), Name: d.Name, UpdatedAt: touched}); err != nil {
				return err
			}
		} else {
			current, err := q.B06RouteRevision(ctx, string(p.ID))
			if err != nil {
				return err
			}
			if current != int64(m.ExpectedRevision) {
				return revisionConflict(current)
			}
			if _, err = q.B06RouteVersion(ctx, dbgen.B06RouteVersionParams{RouteID: string(p.ID), Revision: current}); err != nil {
				return err
			}

			n, err := q.B06UpdateRoute(ctx, dbgen.B06UpdateRouteParams{ID: string(p.ID), Revision: current, Name: d.Name, UpdatedAt: touched})
			if err != nil {
				return err
			}
			if n != 1 {
				return problem(domain.NotFound, "/id")
			}
		}
		if err := q.B06InsertRouteVersion(ctx, dbgen.B06InsertRouteVersionParams{RouteID: string(p.ID), Revision: int64(p.Revision), Name: d.Name, DraftDigest: p.Digest,
			EgressID: string(d.Egress.ID), EgressRevision: int64(d.Egress.Revision), EgressDigest: d.Egress.Digest, NodeDefault: flag(d.EgressIsDefault), BusinessUdp: flag(d.BusinessUDP)}); err != nil {
			return err
		}
		for _, h := range parts {
			n, err := q.GetNode(ctx, h.NodeID)
			if err != nil {
				return err
			}
			if n.RemovedAt.Valid {
				return problem(domain.NotFound, hopPath(int(h.Position), "/node_id"))
			}
			if err = q.B06InsertRouteHop(ctx, h); err != nil {
				return err
			}
		}
		return remember(ctx, q, op, m.IdempotencyKey, digest, p)
	})
	return p, err
}

func flag(v bool) int64 {
	if v {
		return 1
	}
	return 0
}

func draftRows(ctx context.Context, q *dbgen.Queries, id domain.ID, rev domain.Revision) (dbgen.B06RouteVersion, []dbgen.B06RouteHop, error) {
	v, err := q.B06RouteVersion(ctx, dbgen.B06RouteVersionParams{RouteID: string(id), Revision: int64(rev)})
	if err != nil {
		return v, nil, err
	}
	rows, err := q.B06RouteHops(ctx, dbgen.B06RouteHopsParams{RouteID: string(id), Revision: int64(rev)})
	return v, rows, err
}
func decodeDraft(v dbgen.B06RouteVersion, rows []dbgen.B06RouteHop) (stage3.RouteDraft, error) {
	d := stage3.RouteDraft{ID: domain.ID(v.RouteID), Name: v.Name, Egress: control.Pin{ID: domain.ID(v.EgressID), Revision: domain.Revision(v.EgressRevision), Digest: v.EgressDigest},
		EgressIsDefault: v.NodeDefault == 1, BusinessUDP: v.BusinessUdp == 1, Hops: make([]stage3.HopDraft, len(rows))}
	for i, r := range rows {
		h := &d.Hops[i]
		if r.Position != int64(i) || json.Unmarshal(r.NodeConfig, &h.NodeConfig) != nil || json.Unmarshal(r.Dns, &h.DNS) != nil || json.Unmarshal(r.Endpoint, &h.Endpoint) != nil {
			return stage3.RouteDraft{}, problem(domain.NotFound, "/draft")
		}
		h.NodeID, h.DialIP = domain.ID(r.NodeID), r.DialIp
	}
	p, err := stage3.NewPin(d.ID, domain.Revision(v.Revision), d)
	if err != nil || p.Digest != v.DraftDigest || routeDraftShape(d) != nil {
		return stage3.RouteDraft{}, problem(domain.NotFound, "/draft")
	}
	return d, nil
}
func (s *Service) draft(ctx context.Context, p control.Pin) (stage3.RouteDraft, error) {
	if !p.Valid() {
		return stage3.RouteDraft{}, problem(domain.InvalidArgument, "/draft")
	}
	var v dbgen.B06RouteVersion
	var rows []dbgen.B06RouteHop
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		v, rows, err = draftRows(ctx, q, p.ID, p.Revision)
		return err
	})
	if err != nil {
		return stage3.RouteDraft{}, err
	}
	if p.Digest != v.DraftDigest {
		return stage3.RouteDraft{}, problem(domain.NotFound, "/draft")
	}
	return decodeDraft(v, rows)
}
func tailFor(d stage3.RouteDraft, ports []uint16) control.RouteTail {
	t := control.RouteTail{Egress: d.Egress, BusinessUDP: d.BusinessUDP, Hops: make([]control.HopSpec, len(d.Hops))}
	for i, h := range d.Hops {
		ep := h.Endpoint
		ep.Port = ports[i]
		t.Hops[i] = control.HopSpec{NodeID: h.NodeID, NodeConfig: h.NodeConfig, DNS: h.DNS, Endpoint: ep, DialIP: h.DialIP}
	}
	return t
}

func (s *Service) PublishedRoute(ctx context.Context, pin control.Pin) (stage3.PublishedRoute, error) {
	if !pin.Valid() {
		return stage3.PublishedRoute{}, problem(domain.InvalidArgument, "/route")
	}
	var v dbgen.B06RouteVersion
	var hops []dbgen.B06RouteHop
	var ports []int64
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		p, err := q.B06PublishedVersion(ctx, dbgen.B06PublishedVersionParams{RouteID: string(pin.ID), Revision: int64(pin.Revision)})
		if err != nil {
			return err
		}
		if p.RouteDigest != pin.Digest {
			return problem(domain.NotFound, "/route")
		}
		v, hops, err = draftRows(ctx, q, pin.ID, pin.Revision)
		if err != nil {
			return err
		}
		ports, err = q.B06PublishedHops(ctx, dbgen.B06PublishedHopsParams{RouteID: string(pin.ID), Revision: int64(pin.Revision)})
		return err
	})
	if err = safe(ctx, err); err != nil {
		return stage3.PublishedRoute{}, err
	}
	d, err := decodeDraft(v, hops)
	if err != nil || len(ports) != len(hops) {
		return stage3.PublishedRoute{}, problem(domain.NotFound, "/route")
	}
	pp := make([]uint16, len(ports))
	for i, p := range ports {
		pp[i] = uint16(p)
	}
	r := stage3.PublishedRoute{Pin: pin, Name: d.Name, Tail: tailFor(d, pp), EgressIsDefault: d.EgressIsDefault, Verification: domain.NotChecked}
	digest, err := stage3.RouteDigest(r)
	if err != nil || digest != pin.Digest {
		return stage3.PublishedRoute{}, problem(domain.NotFound, "/route")
	}
	if ctx.Err() != nil {
		return stage3.PublishedRoute{}, ctx.Err()
	}
	return r, nil
}

// Draft reads the exact immutable draft for the administrative projection.
func (s *Service) Draft(ctx context.Context, pin control.Pin) (stage3.RouteDraft, error) {
	d, err := s.draft(ctx, pin)
	return d, safe(ctx, err)
}
