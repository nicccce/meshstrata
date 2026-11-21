package business

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"net/http"
	"time"
)

type Target struct {
	NodeID        domain.ID `json:"node_id"`
	Type          string    `json:"type"`
	Interface     string    `json:"interface"`
	Host          string    `json:"host"`
	DialIP        string    `json:"dial_ip"`
	Port          uint16    `json:"port"`
	Authenticated bool      `json:"authenticated"`
}

func target(v control.EgressTarget) Target {
	return Target{v.NodeID, v.Type, v.Interface, v.Host, v.DialIP, v.Port, v.CredentialRef != ""}
}
func (v Target) private() control.EgressTarget {
	return control.EgressTarget{NodeID: v.NodeID, Type: v.Type, Interface: v.Interface, Host: v.Host, DialIP: v.DialIP, Port: v.Port}
}

type EgressObservation struct {
	Scope        string              `json:"scope"`
	BusinessUDP  domain.Verification `json:"business_udp"`
	NodeRevision domain.Revision     `json:"node_revision"`
	DNS          control.Pin         `json:"dns"`
	Target       control.Pin         `json:"target"`
	Interface    string              `json:"interface"`
	SourceIP     string              `json:"source_ip"`
	ObservedAt   string              `json:"observed_at"`
	ExpiresAt    string              `json:"expires_at"`
	Current      bool                `json:"current"`
}
type Egress struct {
	Observation  *EgressObservation   `json:"observation,omitempty"`
	Pin          control.Pin          `json:"pin"`
	Name         string               `json:"name"`
	NodeID       domain.ID            `json:"node_id"`
	Target       *Target              `json:"target,omitempty"`
	DNS          *control.Pin         `json:"dns,omitempty"`
	Targets      []control.Pin        `json:"targets,omitempty"`
	Rules        []control.EgressRule `json:"rules,omitempty"`
	Verification domain.Verification  `json:"verification"`
}

func (s *Service) listEgress(kind string) endpoint {
	return func(_ http.ResponseWriter, r *http.Request) (any, error) {
		after, n, e := pageQuery(r, kind)
		if e != nil {
			return nil, e
		}
		node := r.URL.Query().Get("node_id")
		if node != "" && !domain.ID(node).Valid() {
			return nil, problem(domain.InvalidArgument)
		}
		out := []Egress{}
		ids := []string{}
		e = s.DB.Read(r.Context(), func(q *dbgen.Queries) error {
			rows, e := q.I03CListEgress(r.Context(), dbgen.I03CListEgressParams{AfterID: after, PageSize: int64(n + 1), Kind: kind, NodeID: node})
			for _, v := range rows {
				out = append(out, Egress{Pin: safePin(v.ID, v.Revision, v.Digest), Name: v.Name, NodeID: domain.ID(v.NodeID), Verification: domain.NotChecked})
				ids = append(ids, v.ID)
			}
			return e
		})
		return page(out, ids, n, kind), e
	}
}
func (s *Service) egress(ctx context.Context, key domain.ID, kind string) (Egress, error) {
	var out Egress
	e := s.DB.Read(ctx, func(q *dbgen.Queries) error {
		v, e := q.I03CEgress(ctx, string(key))
		if e != nil {
			return e
		}
		if v.Kind != kind {
			return problem(domain.NotFound)
		}
		out = Egress{Pin: safePin(v.ID, v.Revision, v.Digest), Name: v.Name, NodeID: domain.ID(v.NodeID), Verification: domain.NotChecked}
		if kind == "target" {
			var t control.EgressTarget
			if e := json.Unmarshal(v.Spec, &t); e != nil {
				return e
			}
			p := target(t)
			out.Target = &p
			n, e := q.GetNode(ctx, v.NodeID)
			if e != nil {
				return e
			}
			prepared, e := q.I03CPreparation(ctx, v.NodeID)
			if e == nil {
				var dns control.DNSInput
				if e = json.Unmarshal(prepared.Dns, &dns); e != nil {
					return e
				}
				observation, e := q.I03Observation(ctx, dbgen.I03ObservationParams{NodeID: v.NodeID, NodeRevision: n.Revision, TargetDigest: v.Digest, DnsDigest: dns.Pin.Digest})
				if e == nil {
					current := observation.ExpiresAt > time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
					out.Observation = &EgressObservation{Scope: "tcp_canary", BusinessUDP: domain.NotChecked, NodeRevision: domain.Revision(n.Revision), DNS: dns.Pin, Target: out.Pin, Interface: observation.InterfaceName, SourceIP: observation.SourceIp, ObservedAt: observation.ObservedAt, ExpiresAt: observation.ExpiresAt, Current: current}
					if current {
						out.Verification = domain.Verified
					}
				} else if !errors.Is(e, sql.ErrNoRows) {
					return e
				}
			} else if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
		} else {
			var p control.EgressSpec
			if e := json.Unmarshal(v.Spec, &p); e != nil {
				return e
			}
			out.DNS = &p.DNS
			out.Rules = p.Rules
			for _, t := range p.Targets {
				out.Targets = append(out.Targets, t.Pin)
			}
		}
		return nil
	})
	return out, e
}
func (s *Service) getEgress(kind string) endpoint {
	return func(_ http.ResponseWriter, r *http.Request) (any, error) {
		key, e := id(r)
		if e != nil {
			return nil, e
		}
		return s.egress(r.Context(), key, kind)
	}
}

type targetWrite struct {
	domain.Mutation
	Name   string `json:"name"`
	Target Target `json:"target"`
}
type policyWrite struct {
	domain.Mutation
	Name    string               `json:"name"`
	NodeID  domain.ID            `json:"node_id"`
	DNS     control.Pin          `json:"dns"`
	Targets []control.Pin        `json:"targets"`
	Rules   []control.EgressRule `json:"rules"`
}

func (s *Service) saveEgress(kind string) endpoint {
	return func(w http.ResponseWriter, r *http.Request) (any, error) {
		key := domain.ID(r.PathValue("id"))
		if e := s.active(r.Context(), kind, key); e != nil {
			return nil, e
		}
		if kind == "target" {
			var in targetWrite
			if e := decode(w, r, &in); e != nil {
				return nil, e
			}
			if e := mutation(in.Mutation, key == ""); e != nil {
				return nil, e
			}
			if in.Target.Authenticated {
				return nil, problem(domain.InvalidArgument)
			}
			t := in.Target.private()
			if key != "" {
				old, e := s.egress(r.Context(), key, kind)
				if e != nil {
					return nil, e
				}
				prior, e := s.Egress.Target(r.Context(), old.Pin)
				if e != nil {
					return nil, e
				}
				t.CredentialRef = prior.CredentialRef
			}
			p, e := s.Egress.SaveTarget(r.Context(), in.Mutation, stage3.EgressTargetInput{ID: key, Name: in.Name, Spec: t})
			return map[string]any{"pin": p}, e
		}
		var in policyWrite
		if e := decode(w, r, &in); e != nil {
			return nil, e
		}
		if e := mutation(in.Mutation, key == ""); e != nil {
			return nil, e
		}
		if len(in.Targets) > control.MaxEgressTargets {
			return nil, problem(domain.InvalidArgument)
		}
		p := control.EgressSpec{NodeID: in.NodeID, DNS: in.DNS, Rules: in.Rules}
		for _, pin := range in.Targets {
			if e := s.active(r.Context(), "target", pin.ID); e != nil {
				return nil, e
			}
			t, e := s.Egress.Target(r.Context(), pin)
			if e != nil {
				return nil, e
			}
			p.Targets = append(p.Targets, control.PinnedEgressTarget{Pin: pin, Spec: t})
		}
		pin, e := s.Egress.SavePolicy(r.Context(), in.Mutation, stage3.EgressDraft{ID: key, Name: in.Name, Spec: p})
		return map[string]any{"pin": pin}, e
	}
}
func (s *Service) targetCredentials(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		domain.Mutation
		Pin      control.Pin `json:"pin"`
		Username string      `json:"username"`
		Password string      `json:"password"`
	}
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	if e := mutation(in.Mutation, false); e != nil {
		return nil, e
	}
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	if e = s.active(r.Context(), "target", key); e != nil {
		return nil, e
	}
	if in.Pin.ID != key {
		return nil, problem(domain.InvalidArgument)
	}
	p, e := s.Egress.ReplaceTargetAuth(r.Context(), in.Mutation, in.Pin, stage3.ProxyAuth{Username: in.Username, Password: in.Password})
	in.Password = ""
	return map[string]any{"pin": p}, e
}
func (s *Service) checkTarget(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		domain.Mutation
		Pin control.Pin `json:"pin"`
	}
	if e := decode(w, r, &in); e != nil {
		return nil, e
	}
	if e := mutation(in.Mutation, false); e != nil {
		return nil, e
	}
	v, e := s.egress(r.Context(), domain.ID(r.PathValue("id")), "target")
	if e != nil {
		return nil, e
	}
	if v.Pin != in.Pin || v.Pin.Revision != in.ExpectedRevision {
		return nil, conflict(v.Pin.Revision)
	}
	return s.requestObservation(r.Context(), v.NodeID, &v.Pin)
}
