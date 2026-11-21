package business

import (
	"context"
	"database/sql"
	"errors"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"log/slog"
	"time"
)

type retry struct {
	failures uint
	next     time.Time
	code     domain.ErrorCode
}

// Run is the only stage-three business worker. B04 continues to own dispatch.
// Cursors always advance past individual failures and reset at the end of a sweep.
func (s *Service) Run(ctx context.Context) error {
	var intentAfter, pubAfter, obsAfter, linkAfter domain.ID
	var targetAfter string
	failures := map[string]retry{}
	attempt := func(key string, fn func(context.Context) error) {
		now := time.Now()
		prior := failures[key]
		if prior.next.After(now) {
			return
		}
		local, cancel := context.WithTimeout(ctx, 5*time.Second)
		e := fn(local)
		cancel()
		if e == nil {
			delete(failures, key)
			return
		}
		prior.failures = min(prior.failures+1, 5)
		code := domain.ErrorCode("INTERNAL_ERROR")
		var p *domain.Problem
		if errors.As(e, &p) {
			code = p.Code
		}
		if errors.Is(e, context.DeadlineExceeded) {
			code = "DEADLINE_EXCEEDED"
		}
		if prior.failures == 1 || prior.code != code {
			slog.Warn("Business operation remains pending", "operation", key, "code", code)
		}
		prior.code = code
		prior.next = now.Add(time.Second * time.Duration(1<<prior.failures))
		// Bounded retry metadata; persistent operations remain the source of truth.
		if len(failures) >= 2048 {
			for k, v := range failures {
				if !v.next.After(now) {
					delete(failures, k)
				}
			}
		}
		if len(failures) < 2048 {
			failures[key] = prior
		}
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		ids, e := s.Subscriptions.PendingIntents(ctx, intentAfter, 32)
		if e != nil {
			return e
		}
		for _, key := range ids {
			if ctx.Err() != nil {
				return nil
			}
			intentAfter = key
			attempt("entry/"+string(key), func(c context.Context) error { return s.Subscriptions.Reconcile(c, key) })
		}
		if len(ids) < 32 {
			intentAfter = ""
		}
		var pubs []dbgen.I03CPublicationWorkRow
		e = s.DB.Read(ctx, func(q *dbgen.Queries) error {
			var e error
			pubs, e = q.I03CPublicationWork(ctx, dbgen.I03CPublicationWorkParams{AfterID: string(pubAfter), PageSize: 32})
			return e
		})
		if e != nil {
			return e
		}
		for _, p := range pubs {
			if ctx.Err() != nil {
				return nil
			}
			pubAfter = domain.ID(p.ID)
			attempt("publication/"+p.ID, func(c context.Context) error {
				if p.TaskID.Valid {
					_, e := s.Routes.ReconcilePublication(c, domain.ID(p.TaskID.String))
					return e
				}
				_, e := s.Routes.ResumePublication(c, domain.ID(p.ID))
				return e
			})
		}
		if len(pubs) < 32 {
			pubAfter = ""
		}
		var nodes []dbgen.I03CObservationWorkRow
		e = s.DB.Read(ctx, func(q *dbgen.Queries) error {
			var e error
			nodes, e = q.I03CObservationWork(ctx, dbgen.I03CObservationWorkParams{AfterID: string(obsAfter), PageSize: 16})
			return e
		})
		if e != nil {
			return e
		}
		for _, n := range nodes {
			if ctx.Err() != nil {
				return nil
			}
			obsAfter = domain.ID(n.ID)
			attempt("observe/"+n.ID, func(c context.Context) error {
				p, e := s.preparation(c, domain.ID(n.ID))
				if e != nil {
					return e
				}
				if v := p.Observation; v != nil && v.Current {
					until, e := time.Parse(time.RFC3339Nano, v.ExpiresAt)
					if e == nil && until.After(time.Now().Add(20*time.Second)) {
						return nil
					}
				}
				_, e = s.requestObservation(c, domain.ID(n.ID), nil)
				return e
			})
		}
		if len(nodes) < 16 {
			obsAfter = ""
		}
		var targets []dbgen.I03CTargetObservationWorkRow
		e = s.DB.Read(ctx, func(q *dbgen.Queries) error {
			var e error
			targets, e = q.I03CTargetObservationWork(ctx, dbgen.I03CTargetObservationWorkParams{AfterKey: targetAfter, PageSize: 16})
			return e
		})
		if e != nil {
			return e
		}
		for _, t := range targets {
			if ctx.Err() != nil {
				return nil
			}
			targetAfter = t.CursorKey
			attempt("egress/"+t.CursorKey, func(c context.Context) error {
				p, e := s.preparation(c, domain.ID(t.NodeID))
				if e != nil {
					return e
				}
				if p.Observation == nil || !p.Observation.Current {
					return nil
				}
				var evidence dbgen.I03EgressObservation
				e = s.DB.Read(c, func(q *dbgen.Queries) error {
					var e error
					evidence, e = q.I03Observation(c, dbgen.I03ObservationParams{NodeID: t.NodeID, NodeRevision: int64(p.Revision), TargetDigest: t.Digest, DnsDigest: p.DNS.Pin.Digest})
					return e
				})
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				if e == nil {
					until, e := time.Parse(time.RFC3339Nano, evidence.ExpiresAt)
					if e == nil && until.After(time.Now().Add(20*time.Second)) {
						return nil
					}
				}
				pin := safePin(t.ID, t.Revision, t.Digest)
				_, e = s.requestObservation(c, domain.ID(t.NodeID), &pin)
				return e
			})
		}
		if len(targets) < 16 {
			targetAfter = ""
		}
		var links []dbgen.I03CRetiringLinksRow
		e = s.DB.Read(ctx, func(q *dbgen.Queries) error {
			var e error
			links, e = q.I03CRetiringLinks(ctx, dbgen.I03CRetiringLinksParams{AfterID: string(linkAfter), PageSize: 16})
			return e
		})
		if e != nil {
			return e
		}
		for _, l := range links {
			if ctx.Err() != nil {
				return nil
			}
			linkAfter = domain.ID(l.ID)
			attempt("retire/"+l.ID, func(c context.Context) error {
				pin := safePin(l.ID, l.Revision, l.SuffixDigest)
				var prior dbgen.I03CInternalTaskRow
				e := s.DB.Read(c, func(q *dbgen.Queries) error { var e error; prior, e = q.I03CInternalTask(c, l.ID); return e })
				if e == nil {
					switch prior.State {
					case "failed", "timed_out", "cancelled":
						_, e = s.Releases.RetryTask(c, domain.ID(prior.ID), domain.Mutation{ExpectedRevision: domain.Revision(prior.Revision), IdempotencyKey: "worker-retire-retry/" + prior.ID})
						return e
					default:
						return nil
					}
				}
				if !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				_, e = s.Releases.RetireInternal(c, domain.Mutation{ExpectedRevision: pin.Revision, IdempotencyKey: "worker-retire/" + l.ID}, control.Pin(pin))
				return e
			})
		}
		if len(links) < 16 {
			linkAfter = ""
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
	return nil
}
