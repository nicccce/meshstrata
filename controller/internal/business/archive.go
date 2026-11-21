package business

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/nicccce/meshstrata/controller/internal/routes"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"net/http"
	"time"
)

func references(ctx context.Context, q *dbgen.Queries, kind string, key domain.ID, after string, limit int) ([]string, error) {
	switch kind {
	case "route":
		return q.I03CRouteReferences(ctx, dbgen.I03CRouteReferencesParams{ResourceID: string(key), AfterID: after, PageSize: int64(limit)})
	case "profile":
		return q.I03CProfileReferences(ctx, dbgen.I03CProfileReferencesParams{ResourceID: string(key), AfterID: after, PageSize: int64(limit)})
	case "domain_set":
		return q.I03CSetReferences(ctx, dbgen.I03CSetReferencesParams{ResourceID: string(key), AfterID: after, PageSize: int64(limit)})
	case "target":
		return q.I03CTargetReferences(ctx, dbgen.I03CTargetReferencesParams{ResourceID: string(key), AfterID: after, PageSize: int64(limit)})
	case "policy":
		return q.I03CPolicyReferences(ctx, dbgen.I03CPolicyReferencesParams{ResourceID: string(key), AfterID: after, PageSize: int64(limit)})
	}
	return nil, problem(domain.InvalidArgument)
}
func (s *Service) references(kind string) endpoint {
	return func(_ http.ResponseWriter, r *http.Request) (any, error) {
		key, e := id(r)
		if e != nil {
			return nil, e
		}
		after, limit, e := pageQuery(r, "references/"+kind+"/"+string(key))
		if e != nil {
			return nil, e
		}
		var refs []string
		e = s.DB.Read(r.Context(), func(q *dbgen.Queries) error {
			var e error
			refs, e = references(r.Context(), q, kind, key, after, limit+1)
			return e
		})
		if refs == nil {
			refs = []string{}
		}
		return page(refs, refs, limit, "references/"+kind+"/"+string(key)), e
	}
}
func objectRevision(ctx context.Context, q *dbgen.Queries, kind string, key domain.ID) (int64, error) {
	switch kind {
	case "route":
		return q.B06RouteRevision(ctx, string(key))
	case "profile":
		return q.B07GetProfileHead(ctx, string(key))
	case "domain_set":
		return q.B07GetDomainHead(ctx, string(key))
	default:
		v, e := q.B06EgressObject(ctx, string(key))
		if e == nil && v.Kind != kind {
			return 0, problem(domain.NotFound)
		}
		return v.Revision, e
	}
}

// Archive hides a resource, preserving immutable historical pins and evidence.
// Route heads are retired only after all current entry/cleanup references end.
func (s *Service) archive(kind string) endpoint {
	return func(w http.ResponseWriter, r *http.Request) (any, error) {
		key, m, e := s.subscriptionAction(w, r)
		if e != nil {
			return nil, e
		}
		op := "archive/" + kind + "/" + string(key)
		digest, e := control.ComponentDigest(m)
		if e != nil {
			return nil, e
		}
		out := map[string]any{"id": key, "archived": true, "cleanup": "pending"}
		if kind != "route" {
			out["cleanup"] = "not_required"
		}
		raw, _ := json.Marshal(out)
		e = s.DB.Write(r.Context(), func(q *dbgen.Queries) error {
			old, e := q.I03CMutation(r.Context(), dbgen.I03CMutationParams{Operation: op, IdemKey: m.IdempotencyKey})
			if e == nil {
				if old.Digest != digest {
					return problem(domain.IdempotencyConflict)
				}
				return nil
			}
			if !errors.Is(e, sql.ErrNoRows) {
				return e
			}
			rev, e := objectRevision(r.Context(), q, kind, key)
			if e != nil {
				return e
			}
			if rev != int64(m.ExpectedRevision) {
				return conflict(domain.Revision(rev))
			}
			refs, e := references(r.Context(), q, kind, key, "", 1)
			if e != nil {
				return e
			}
			if len(refs) > 0 {
				return problem(domain.DependencyInUse)
			}
			if kind == "route" {
				busy, e := q.I03CRouteBusy(r.Context(), string(key))
				if e != nil {
					return e
				}
				if busy {
					return problem(domain.DependencyInUse)
				}
				if e = q.I03CDropRouteHead(r.Context(), string(key)); e != nil {
					return e
				}
				refs, e := q.I03CRouteLinkReferences(r.Context(), string(key))
				if e != nil {
					return e
				}
				if len(refs) == 1024 {
					return problem(domain.DependencyInUse)
				}
				for _, ref := range refs {
					if _, e := routes.RetireInternalReference(r.Context(), q, domain.ID(ref), time.Now()); e != nil {
						return e
					}
				}
			}
			archived, e := q.I03CArchived(r.Context(), dbgen.I03CArchivedParams{Kind: kind, ID: string(key)})
			if e != nil {
				return e
			}
			if !archived {
				if e = q.I03CArchive(r.Context(), dbgen.I03CArchiveParams{Kind: kind, ID: string(key), Revision: rev, ArchivedAt: time.Now().UTC().Format(time.RFC3339Nano)}); e != nil {
					return e
				}
			}
			return q.I03CPutMutation(r.Context(), dbgen.I03CPutMutationParams{Operation: op, IdemKey: m.IdempotencyKey, Digest: digest, Result: raw})
		})
		return out, e
	}
}
