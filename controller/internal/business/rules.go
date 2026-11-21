package business

import (
	"context"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"net/http"
)

type Catalog struct {
	Pin     control.Pin   `json:"pin"`
	Name    string        `json:"name"`
	Source  string        `json:"source,omitempty"`
	Sets    []control.Pin `json:"sets,omitempty"`
	Domains []string      `json:"domains,omitempty"`
}

func (s *Service) listProfiles(_ http.ResponseWriter, r *http.Request) (any, error) {
	after, n, e := pageQuery(r, "profiles")
	if e != nil {
		return nil, e
	}
	out := []Catalog{}
	ids := []string{}
	e = s.DB.Read(r.Context(), func(q *dbgen.Queries) error {
		rows, e := q.I03CListProfiles(r.Context(), dbgen.I03CListProfilesParams{AfterID: after, PageSize: int64(n + 1)})
		for _, v := range rows {
			out = append(out, Catalog{Pin: safePin(v.ID, v.Revision, v.Digest), Name: v.Name})
			ids = append(ids, v.ID)
		}
		return e
	})
	return page(out, ids, n, "profiles"), e
}
func (s *Service) profile(ctx context.Context, key domain.ID) (Catalog, error) {
	var out Catalog
	e := s.DB.Read(ctx, func(q *dbgen.Queries) error {
		v, e := q.I03CProfile(ctx, string(key))
		if e != nil {
			return e
		}
		out = Catalog{Pin: safePin(v.ID, v.Revision, v.Digest), Name: v.Name, Source: v.SourceText, Sets: []control.Pin{}}
		sets, e := q.B07ListProfileSetPins(ctx, dbgen.B07ListProfileSetPinsParams{ProfileID: v.ID, ProfileRevision: v.Revision})
		for _, p := range sets {
			out.Sets = append(out.Sets, safePin(p.DomainSetID, p.DomainSetRevision, p.Digest))
		}
		return e
	})
	return out, e
}
func (s *Service) getProfile(_ http.ResponseWriter, r *http.Request) (any, error) {
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	return s.profile(r.Context(), key)
}

type profileWrite struct {
	domain.Mutation
	Name   string        `json:"name"`
	Source string        `json:"source"`
	Sets   []control.Pin `json:"sets"`
}

func (s *Service) saveProfile(w http.ResponseWriter, r *http.Request) (any, error) {
	var in profileWrite
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	key := domain.ID(r.PathValue("id"))
	if e := mutation(in.Mutation, key == ""); e != nil {
		return nil, e
	}
	if e := s.active(r.Context(), "profile", key); e != nil {
		return nil, e
	}
	for _, p := range in.Sets {
		if e := s.active(r.Context(), "domain_set", p.ID); e != nil {
			return nil, e
		}
	}
	p, problems, e := s.Rules.SaveProfile(r.Context(), in.Mutation, stage3.ProfileInput{ID: key, Name: in.Name, Source: in.Source, Sets: in.Sets})
	if len(problems) > 0 {
		return nil, diagnosticError{problems}
	}
	if e != nil {
		return nil, e
	}
	return map[string]any{"pin": p}, nil
}

type copyRequest struct {
	domain.Mutation
	Name      string      `json:"name"`
	SourcePin control.Pin `json:"source_pin"`
}

func (s *Service) copyProfile(w http.ResponseWriter, r *http.Request) (any, error) {
	var in copyRequest
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	if e := mutation(in.Mutation, true); e != nil {
		return nil, e
	}
	v, e := s.profile(r.Context(), domain.ID(r.PathValue("id")))
	if e != nil {
		return nil, e
	}
	if v.Pin != in.SourcePin {
		return nil, conflict(v.Pin.Revision)
	}
	p, _, e := s.Rules.SaveProfile(r.Context(), in.Mutation, stage3.ProfileInput{Name: in.Name, Source: v.Source, Sets: v.Sets})
	return map[string]any{"pin": p}, e
}
func (s *Service) listSets(_ http.ResponseWriter, r *http.Request) (any, error) {
	after, n, e := pageQuery(r, "sets")
	if e != nil {
		return nil, e
	}
	out := []Catalog{}
	ids := []string{}
	e = s.DB.Read(r.Context(), func(q *dbgen.Queries) error {
		rows, e := q.I03CListSets(r.Context(), dbgen.I03CListSetsParams{AfterID: after, PageSize: int64(n + 1)})
		for _, v := range rows {
			out = append(out, Catalog{Pin: safePin(v.ID, v.Revision, v.Digest), Name: v.Name})
			ids = append(ids, v.ID)
		}
		return e
	})
	return page(out, ids, n, "sets"), e
}
func (s *Service) set(ctx context.Context, key domain.ID) (Catalog, error) {
	var out Catalog
	e := s.DB.Read(ctx, func(q *dbgen.Queries) error {
		v, e := q.I03CSet(ctx, string(key))
		out = Catalog{Pin: safePin(v.ID, v.Revision, v.Digest), Name: v.Name}
		return e
	})
	if e != nil {
		return out, e
	}
	v, e := s.Rules.DomainSet(ctx, out.Pin)
	out.Domains = v.Spec.Domains
	return out, e
}
func (s *Service) getSet(_ http.ResponseWriter, r *http.Request) (any, error) {
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	return s.set(r.Context(), key)
}

type setWrite struct {
	domain.Mutation
	Name    string   `json:"name"`
	Domains []string `json:"domains"`
}

func (s *Service) saveSet(w http.ResponseWriter, r *http.Request) (any, error) {
	var in setWrite
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	key := domain.ID(r.PathValue("id"))
	if e := mutation(in.Mutation, key == ""); e != nil {
		return nil, e
	}
	if e := s.active(r.Context(), "domain_set", key); e != nil {
		return nil, e
	}
	p, e := s.Rules.SaveDomainSet(r.Context(), in.Mutation, key, in.Name, stage3.DomainSetSpec{Domains: in.Domains})
	return map[string]any{"pin": p}, e
}
func (s *Service) copySet(w http.ResponseWriter, r *http.Request) (any, error) {
	var in copyRequest
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	if e := mutation(in.Mutation, true); e != nil {
		return nil, e
	}
	v, e := s.set(r.Context(), domain.ID(r.PathValue("id")))
	if e != nil {
		return nil, e
	}
	if v.Pin != in.SourcePin {
		return nil, conflict(v.Pin.Revision)
	}
	p, e := s.Rules.SaveDomainSet(r.Context(), in.Mutation, "", in.Name, stage3.DomainSetSpec{Domains: v.Domains})
	return map[string]any{"pin": p}, e
}
