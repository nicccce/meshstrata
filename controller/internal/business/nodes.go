package business

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/nicccce/meshstrata/controller/internal/egress"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"net/http"
	"reflect"
	"time"
)

type Preparation struct {
	NodeID           domain.ID                    `json:"node_id"`
	Revision         domain.Revision              `json:"revision"`
	PreparedRevision domain.Revision              `json:"prepared_revision"`
	DNS              control.DNSInput             `json:"dns"`
	Sockets          []control.PinnedSocketSpecV2 `json:"sockets"`
	Dependencies     []string                     `json:"dependencies"`
	Observation      *Observation                 `json:"observation"`
}
type Observation struct {
	Revision     domain.Revision                `json:"revision"`
	ObservedAt   string                         `json:"observed_at"`
	ExpiresAt    string                         `json:"expires_at"`
	Current      bool                           `json:"current"`
	Capabilities []control.ProtocolCapabilityV2 `json:"capabilities"`
}

func (s *Service) preparation(ctx context.Context, key domain.ID) (Preparation, error) {
	out := Preparation{NodeID: key, Dependencies: []string{}}
	e := s.DB.Read(ctx, func(q *dbgen.Queries) error {
		n, e := q.GetNode(ctx, string(key))
		if e != nil {
			return e
		}
		if n.RemovedAt.Valid {
			return problem(domain.NotFound)
		}
		out.Revision = domain.Revision(n.Revision)
		p, e := q.I03CPreparation(ctx, string(key))
		if e != nil {
			return e
		}
		out.PreparedRevision = domain.Revision(p.Revision)
		if json.Unmarshal(p.Dns, &out.DNS) != nil || json.Unmarshal(p.Sockets, &out.Sockets) != nil {
			return errors.New("invalid preparation")
		}
		out.Dependencies, e = q.I03CNodeDependencies(ctx, string(key))
		if e != nil {
			return e
		}
		inv, e := q.I03Inventory(ctx, string(key))
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		v := &Observation{Revision: domain.Revision(inv.NodeRevision), ObservedAt: inv.ObservedAt, ExpiresAt: inv.ExpiresAt, Current: inv.NodeRevision == n.Revision && inv.DnsDigest == out.DNS.Pin.Digest && inv.ExpiresAt > time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"), Capabilities: []control.ProtocolCapabilityV2{}}
		socks, e := q.I03Sockets(ctx, string(key))
		for _, sock := range socks {
			v.Capabilities = append(v.Capabilities, control.ProtocolCapabilityV2{Available: true, Protocol: domain.Protocol(sock.Protocol), BusinessUDP: domain.Verification(sock.BusinessUdp)})
		}
		out.Observation = v
		return e
	})
	return out, e
}
func (s *Service) getPreparation(_ http.ResponseWriter, r *http.Request) (any, error) {
	key, e := id(r)
	if e != nil {
		return nil, e
	}
	return s.preparation(r.Context(), key)
}
func (s *Service) prepareNode(w http.ResponseWriter, r *http.Request) (any, error) {
	var in struct {
		domain.Mutation
		DNS     control.DNSInput             `json:"dns"`
		Sockets []control.PinnedSocketSpecV2 `json:"sockets"`
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
	plan := control.PreparationV2{NodeRevision: in.ExpectedRevision + 1, DNS: in.DNS, Sockets: in.Sockets}
	if !plan.Valid() || plan.DNS.IPv6 {
		return nil, problem(domain.InvalidArgument)
	}
	dnsDigest, e := control.ComponentDigest(control.DNSPolicy{Bootstrap: in.DNS.Bootstrap, Resolvers: in.DNS.Resolvers, IPv6: in.DNS.IPv6})
	if e != nil || dnsDigest != in.DNS.Pin.Digest {
		return nil, problem(domain.InvalidArgument)
	}
	seen := map[control.Pin]bool{}
	for _, p := range in.Sockets {
		digest, e := control.ComponentDigest(p.Spec)
		if e != nil || digest != p.Pin.Digest || seen[p.Pin] {
			return nil, problem(domain.InvalidArgument)
		}
		seen[p.Pin] = true
		switch p.Protocol {
		case domain.SS2022, domain.Trojan, domain.VLESS, domain.Hysteria2:
		default:
			return nil, problem(domain.InvalidArgument)
		}
	}
	rawDNS, _ := json.Marshal(in.DNS)
	rawSockets, _ := json.Marshal(in.Sockets)
	digest, _ := control.ComponentDigest(in)
	op := "prepare/" + string(key)
	e = s.DB.Write(r.Context(), func(q *dbgen.Queries) error {
		old, e := q.I03CMutation(r.Context(), dbgen.I03CMutationParams{Operation: op, IdemKey: in.IdempotencyKey})
		if e == nil {
			if old.Digest != digest {
				return problem(domain.IdempotencyConflict)
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		n, e := q.GetNode(r.Context(), string(key))
		if e != nil {
			return e
		}
		if n.RemovedAt.Valid {
			return problem(domain.NotFound)
		}
		if n.Revision != int64(in.ExpectedRevision) {
			return conflict(domain.Revision(n.Revision))
		}
		prior, e := q.I03CPreparation(r.Context(), string(key))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e == nil && !bytes.Equal(prior.Dns, rawDNS) {
			count, e := q.I03CActiveListeners(r.Context(), string(key))
			if e != nil {
				return e
			}
			if count > 0 {
				return problem(domain.DependencyInUse)
			}
		}
		if e == nil {
			var oldSockets []control.PinnedSocketSpecV2
			if json.Unmarshal(prior.Sockets, &oldSockets) != nil {
				return problem(domain.RevisionConflict)
			}
			for _, p := range oldSockets {
				found := false
				for _, next := range in.Sockets {
					if reflect.DeepEqual(p, next) {
						found = true
						break
					}
				}
				if !found {
					count, e := q.I03CActiveListeners(r.Context(), string(key))
					if e != nil {
						return e
					}
					if count > 0 {
						return problem(domain.DependencyInUse)
					}
				}
			}
		}
		rows, e := q.I03CPrepareNode(r.Context(), dbgen.I03CPrepareNodeParams{ID: string(key), Revision: n.Revision, TargetDnsRevision: int64(in.DNS.Pin.Revision), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)})
		if e != nil {
			return e
		}
		if rows != 1 {
			return conflict(domain.Revision(n.Revision))
		}
		if e = q.I03CPutPreparation(r.Context(), dbgen.I03CPutPreparationParams{NodeID: string(key), Revision: n.Revision + 1, Digest: digest, Dns: rawDNS, Sockets: rawSockets}); e != nil {
			return e
		}
		return q.I03CPutMutation(r.Context(), dbgen.I03CPutMutationParams{Operation: op, IdemKey: in.IdempotencyKey, Digest: digest, Result: []byte(`{}`)})
	})
	if e != nil {
		return nil, e
	}
	return s.preparation(r.Context(), key)
}
func (s *Service) requestObservation(ctx context.Context, node domain.ID, target *control.Pin) (any, error) {
	p, e := s.preparation(ctx, node)
	if e != nil {
		return nil, e
	}
	key, e := domain.NewID("observation")
	if e != nil {
		return nil, e
	}
	req := control.ObservationRequestV2{ID: key, Deadline: time.Now().Add(15 * time.Second), Target: target, Preparation: &control.PreparationV2{NodeRevision: p.Revision, DNS: p.DNS, Sockets: p.Sockets}}
	if target != nil {
		spec, e := s.Egress.Target(ctx, *target)
		if e != nil {
			return nil, e
		}
		if spec.NodeID != node {
			return nil, problem(domain.InvalidArgument)
		}
		prepared := &control.PreparedTargetV2{Pin: *target, Spec: spec}
		if spec.CredentialRef != "" {
			auth, e := egress.OpenAuth(ctx, stage3.Runtime{DB: s.DB}, s.Cipher, *target, node)
			if e != nil {
				return nil, e
			}
			prepared.Auth = &auth
		}
		req.Preparation.Target = prepared
	}
	if e = s.Channel.ObserveV2(ctx, node, req); e != nil {
		return nil, problem("OBSERVATION_UNAVAILABLE")
	}
	return map[string]any{"request_id": key, "state": "pending", "expires_at": req.Deadline}, nil
}
func (s *Service) observeNode(w http.ResponseWriter, r *http.Request) (any, error) {
	key, m, e := s.subscriptionAction(w, r)
	if e != nil {
		return nil, e
	}
	p, e := s.preparation(r.Context(), key)
	if e != nil {
		return nil, e
	}
	if p.Revision != m.ExpectedRevision {
		return nil, conflict(p.Revision)
	}
	return s.requestObservation(r.Context(), key, nil)
}
func (s *Service) drainLegacy(w http.ResponseWriter, r *http.Request) (any, error) {
	key, m, e := s.subscriptionAction(w, r)
	if e != nil {
		return nil, e
	}
	return s.Releases.DrainLegacyNode(r.Context(), key, m)
}
func (s *Service) switchV2(w http.ResponseWriter, r *http.Request) (any, error) {
	key, m, e := s.subscriptionAction(w, r)
	if e != nil {
		return nil, e
	}
	return s.Releases.SwitchNodeV2(r.Context(), key, m)
}
