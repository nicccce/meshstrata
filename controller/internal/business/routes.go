package business

import (
	"context"
	"encoding/base64"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Hop struct {
	NodeID     domain.ID             `json:"node_id"`
	NodeConfig control.Pin           `json:"node_config"`
	DNS        control.Pin           `json:"dns"`
	Endpoint   control.ProxyEndpoint `json:"endpoint"`
	DialIP     string                `json:"dial_ip"`
}
type Route struct {
	Pin             control.Pin  `json:"pin"`
	Name            string       `json:"name"`
	Hops            []Hop        `json:"hops,omitempty"`
	Egress          control.Pin  `json:"egress"`
	EgressIsDefault bool         `json:"egress_is_default"`
	BusinessUDP     bool         `json:"business_udp"`
	Published       *control.Pin `json:"published"`
}
type routeWrite struct {
	domain.Mutation
	Name            string      `json:"name"`
	Hops            []Hop       `json:"hops"`
	Egress          control.Pin `json:"egress"`
	EgressIsDefault bool        `json:"egress_is_default"`
	BusinessUDP     bool        `json:"business_udp"`
}

func projectDraft(p control.Pin, d stage3.RouteDraft) Route {
	v := Route{Pin: p, Name: d.Name, Egress: d.Egress, EgressIsDefault: d.EgressIsDefault, BusinessUDP: d.BusinessUDP, Hops: []Hop{}}
	for _, h := range d.Hops {
		v.Hops = append(v.Hops, Hop{h.NodeID, h.NodeConfig, h.DNS, h.Endpoint, h.DialIP})
	}
	return v
}
func (s *Service) listRoutes(_ http.ResponseWriter, r *http.Request) (any, error) {
	after, n, e := pageQuery(r, "routes")
	if e != nil {
		return nil, e
	}
	items := []Route{}
	ids := []string{}
	e = s.DB.Read(r.Context(), func(q *dbgen.Queries) error {
		rows, e := q.I03CListRoutes(r.Context(), dbgen.I03CListRoutesParams{AfterID: after, PageSize: int64(n + 1)})
		for _, v := range rows {
			d := Route{Pin: safePin(v.ID, v.Revision, v.DraftDigest), Name: v.Name, Egress: safePin(v.EgressID, v.EgressRevision, v.EgressDigest), EgressIsDefault: v.NodeDefault == 1, BusinessUDP: v.BusinessUdp == 1}
			if v.ConfirmedRevision.Valid {
				p := safePin(v.ID, v.ConfirmedRevision.Int64, v.RouteDigest.String)
				d.Published = &p
			}
			items = append(items, d)
			ids = append(ids, v.ID)
		}
		return e
	})
	return page(items, ids, n, "routes"), e
}
func (s *Service) route(ctx context.Context, p control.Pin) (Route, error) {
	d, e := s.Routes.Draft(ctx, p)
	if e != nil {
		return Route{}, e
	}
	v := projectDraft(p, d)
	h, e := s.Routes.PublishedHead(ctx, p.ID)
	if e == nil {
		v.Published = &h.Pin
	}
	return v, nil
}
func (s *Service) getRoute(_ http.ResponseWriter, r *http.Request) (any, error) {
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	var p control.Pin
	e = s.DB.Read(r.Context(), func(q *dbgen.Queries) error {
		rev, e := q.B06RouteRevision(r.Context(), string(key))
		if e != nil {
			return e
		}
		v, e := q.B06RouteVersion(r.Context(), dbgen.B06RouteVersionParams{RouteID: string(key), Revision: rev})
		p = safePin(v.RouteID, v.Revision, v.DraftDigest)
		return e
	})
	if e != nil {
		return nil, e
	}
	return s.route(r.Context(), p)
}

type routeVersion struct {
	Draft     control.Pin  `json:"draft"`
	Published *control.Pin `json:"published"`
	State     string       `json:"state"`
	TaskID    *string      `json:"task_id"`
}

func (s *Service) routeHistory(_ http.ResponseWriter, r *http.Request) (any, error) {
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	limit := 50
	before := int64(domain.MaxRevision) + 1
	scope := "history/" + string(key) + "|"
	query := r.URL.Query()
	for k, v := range query {
		if (k != "limit" && k != "cursor") || len(v) != 1 {
			return nil, problem(domain.InvalidArgument)
		}
	}
	if query.Has("limit") {
		n, e := strconv.Atoi(query.Get("limit"))
		if e != nil || n < 1 || n > 200 {
			return nil, problem(domain.InvalidArgument)
		}
		limit = n
	}
	if c := query.Get("cursor"); c != "" {
		b, e := base64.RawURLEncoding.Strict().DecodeString(c)
		if e != nil || !strings.HasPrefix(string(b), scope) {
			return nil, problem(domain.InvalidArgument)
		}
		rev, e := strconv.ParseInt(strings.TrimPrefix(string(b), scope), 10, 64)
		if e != nil || !domain.Revision(rev).Valid() {
			return nil, problem(domain.InvalidArgument)
		}
		before = rev
	}
	out := []routeVersion{}
	e = s.DB.Read(r.Context(), func(q *dbgen.Queries) error {
		rows, e := q.I03CRouteVersions(r.Context(), dbgen.I03CRouteVersionsParams{RouteID: string(key), BeforeRevision: before, PageSize: int64(limit + 1)})
		for _, v := range rows {
			d := routeVersion{Draft: safePin(v.RouteID, v.Revision, v.DraftDigest), State: v.State.String}
			if v.Confirmed.Int64 == 1 {
				p := safePin(v.RouteID, v.Revision, v.RouteDigest.String)
				d.Published = &p
			}
			if v.TaskID.Valid {
				t := v.TaskID.String
				d.TaskID = &t
			}
			out = append(out, d)
		}
		return e
	})
	result := domain.Page[routeVersion]{Items: out}
	if len(out) > limit {
		result.Items = out[:limit]
		cursor := base64.RawURLEncoding.EncodeToString([]byte(scope + strconv.FormatInt(int64(out[limit-1].Draft.Revision), 10)))
		result.NextCursor = &cursor
	}
	return result, e
}
func (s *Service) saveRoute(w http.ResponseWriter, r *http.Request) (any, error) {
	var in routeWrite
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	key := domain.ID(r.PathValue("id"))
	if e := mutation(in.Mutation, key == ""); e != nil {
		return nil, e
	}
	if e := s.active(r.Context(), "route", key); e != nil {
		return nil, e
	}
	if !in.EgressIsDefault {
		if e := s.active(r.Context(), "policy", in.Egress.ID); e != nil {
			return nil, e
		}
	}
	d := stage3.RouteDraft{ID: key, Name: in.Name, Egress: in.Egress, EgressIsDefault: in.EgressIsDefault, BusinessUDP: in.BusinessUDP}
	for _, h := range in.Hops {
		d.Hops = append(d.Hops, stage3.HopDraft{NodeID: h.NodeID, NodeConfig: h.NodeConfig, DNS: h.DNS, Endpoint: h.Endpoint, DialIP: h.DialIP})
	}
	p, e := s.Routes.SaveDraft(r.Context(), in.Mutation, d)
	if e != nil {
		return nil, e
	}
	return s.route(r.Context(), p)
}

type pinAction struct {
	domain.Mutation
	Pin      control.Pin `json:"pin"`
	Deadline time.Time   `json:"deadline,omitempty"`
}
type issue struct {
	Path   string           `json:"path"`
	Line   int              `json:"line"`
	Column int              `json:"column"`
	Code   domain.ErrorCode `json:"code"`
}

func issues(in []stage3.Issue) []issue {
	out := []issue{}
	for _, v := range in {
		out = append(out, issue{v.Path, v.Line, v.Column, v.Code})
	}
	return out
}
func (s *Service) routeAction(w http.ResponseWriter, r *http.Request) (pinAction, error) {
	var in pinAction
	e := decode(w, r, &in)
	if e == nil {
		e = mutation(in.Mutation, false)
	}
	if e == nil && (in.Pin.ID != domain.ID(r.PathValue("id")) || in.Pin.Revision != in.ExpectedRevision || !in.Pin.Valid()) {
		e = problem(domain.InvalidArgument)
	}
	if e == nil {
		e = s.active(r.Context(), "route", in.Pin.ID)
	}
	return in, e
}
func (s *Service) validateRoute(w http.ResponseWriter, r *http.Request) (any, error) {
	in, e := s.routeAction(w, r)
	if e != nil {
		return nil, e
	}
	v, e := s.Routes.ValidateDraft(r.Context(), in.Pin)
	if e != nil {
		return nil, e
	}
	return map[string]any{"pin": v.Draft, "nodes": v.Nodes, "blocking": issues(v.Blocking), "warnings": issues(v.Warnings)}, nil
}
func (s *Service) publishRoute(w http.ResponseWriter, r *http.Request) (any, error) {
	in, e := s.routeAction(w, r)
	if e != nil {
		return nil, e
	}
	if in.Deadline.IsZero() || in.Deadline.After(time.Now().Add(15*time.Minute)) {
		return nil, problem(domain.InvalidArgument)
	}
	return s.Routes.PublishDraft(r.Context(), in.Mutation, in.Pin, in.Deadline)
}
