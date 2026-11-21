// Package business owns explicit administrative DTOs and ordinary stage-three wiring.
// Private compiler plans, credentials and full node artifacts never cross this boundary.
package business

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	channel "github.com/nicccce/meshstrata/controller/internal/control"
	"github.com/nicccce/meshstrata/controller/internal/egress"
	"github.com/nicccce/meshstrata/controller/internal/releases"
	"github.com/nicccce/meshstrata/controller/internal/routes"
	"github.com/nicccce/meshstrata/controller/internal/rules"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/controller/internal/subscriptions"
	"github.com/nicccce/meshstrata/internal/adminauth"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/httpx"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

type Service struct {
	Cipher        secretstore.Cipher
	DB            storage.Database
	Guard         adminauth.Guard
	Routes        *routes.Service
	Rules         *rules.Service
	Egress        *egress.Service
	Subscriptions *subscriptions.Service
	Client        stage3.ClientBuilder
	Releases      *releases.Service
	Channel       *channel.Server
}

func problem(code domain.ErrorCode) error { return &domain.Problem{Code: code} }
func conflict(r domain.Revision) error {
	return &domain.Problem{Code: domain.RevisionConflict, CurrentRevision: &r}
}
func writeError(w http.ResponseWriter, err error) {
	if d, ok := err.(diagnosticError); ok {
		httpx.JSON(w, 400, map[string]any{"error": domain.Problem{Code: domain.InvalidArgument}, "issues": issues(d.issues)})
		return
	}
	status := http.StatusInternalServerError
	p := domain.Problem{Code: "INTERNAL_ERROR"}
	var v *domain.Problem
	if errors.Is(err, sql.ErrNoRows) {
		err = problem(domain.NotFound)
	}
	if errors.As(err, &v) {
		p.Code, p.CurrentRevision = v.Code, v.CurrentRevision
		for _, f := range v.Fields {
			p.Fields = append(p.Fields, domain.FieldError{Path: f.Path, Code: f.Code})
		}
		status = http.StatusConflict
		switch p.Code {
		case "INTERNAL", "INTERNAL_ERROR":
			status = http.StatusInternalServerError
		case domain.InvalidArgument:
			status = 400
		case domain.NotFound:
			status = 404
		case domain.Unauthenticated:
			status = 401
		case domain.Forbidden:
			status = 403
		case domain.NotImplemented:
			status = 501
		}
	}
	httpx.JSON(w, status, domain.ErrorResponse{Error: p})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 256<<10))
	defer clear(raw)
	if r.Header.Get("Content-Type") != "application/json" || e != nil || len(raw) == 0 || raw[0] != '{' || !control.UniqueJSON(raw) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return problem(domain.InvalidArgument)
	}
	d := json.NewDecoder(strings.NewReader(string(raw)))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		return problem(domain.InvalidArgument)
	}
	return nil
}
func mutation(m domain.Mutation, create bool) error {
	if len(m.IdempotencyKey) < 1 || len(m.IdempotencyKey) > 128 || strings.IndexFunc(m.IdempotencyKey, func(r rune) bool { return r < '!' || r > '~' }) >= 0 || create && m.ExpectedRevision != 0 || !create && (!m.ExpectedRevision.Valid() || m.ExpectedRevision == domain.MaxRevision) {
		return problem(domain.InvalidArgument)
	}
	return nil
}

type diagnosticError struct{ issues []stage3.Issue }

func (e diagnosticError) Error() string { return "INVALID_ARGUMENT" }

type endpoint func(http.ResponseWriter, *http.Request) (any, error)

func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	add := func(pattern string, write bool, fn endpoint) {
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			out, e := fn(w, r)
			if e != nil {
				writeError(w, e)
				return
			}
			status := 200
			if write {
				status = 202
			}
			httpx.JSON(w, status, out)
		})
		var guarded http.Handler = s.Guard.Read(h)
		if write {
			guarded = s.Guard.Write(h)
		}
		mux.Handle(pattern, guarded)
	}
	add("GET /api/v1/routes", false, s.listRoutes)
	add("GET /api/v1/routes/{id}", false, s.getRoute)
	add("GET /api/v1/routes/{id}/history", false, s.routeHistory)
	add("POST /api/v1/routes", true, s.saveRoute)
	add("PATCH /api/v1/routes/{id}", true, s.saveRoute)
	add("POST /api/v1/routes/{id}/validate", true, s.validateRoute)
	add("POST /api/v1/routes/{id}/publish", true, s.publishRoute)
	add("GET /api/v1/rule-profiles", false, s.listProfiles)
	add("GET /api/v1/rule-profiles/{id}", false, s.getProfile)
	add("POST /api/v1/rule-profiles", true, s.saveProfile)
	add("PATCH /api/v1/rule-profiles/{id}", true, s.saveProfile)
	add("POST /api/v1/rule-profiles/{id}/copy", true, s.copyProfile)
	add("GET /api/v1/domain-sets", false, s.listSets)
	add("GET /api/v1/domain-sets/{id}", false, s.getSet)
	add("POST /api/v1/domain-sets", true, s.saveSet)
	add("PATCH /api/v1/domain-sets/{id}", true, s.saveSet)
	add("POST /api/v1/domain-sets/{id}/copy", true, s.copySet)
	for _, kind := range []string{"target", "policy"} {
		plural := "egress-" + kind + "s"
		if kind == "policy" {
			plural = "egress-policies"
		}
		add("GET /api/v1/"+plural, false, s.listEgress(kind))
		add("GET /api/v1/"+plural+"/{id}", false, s.getEgress(kind))
		add("POST /api/v1/"+plural, true, s.saveEgress(kind))
		add("PATCH /api/v1/"+plural+"/{id}", true, s.saveEgress(kind))
	}
	add("POST /api/v1/egress-targets/{id}/credentials", true, s.targetCredentials)
	add("POST /api/v1/egress-targets/{id}/validate", true, s.checkTarget)
	add("GET /api/v1/subscriptions", false, s.listSubscriptions)
	add("GET /api/v1/subscriptions/{id}", false, s.getSubscription)
	add("POST /api/v1/subscriptions", true, s.saveSubscription)
	add("PATCH /api/v1/subscriptions/{id}", true, s.saveSubscription)
	add("POST /api/v1/subscriptions/{id}/preview", true, s.previewSubscription)
	add("POST /api/v1/subscriptions/{id}/renew", true, s.renewSubscription)
	add("POST /api/v1/subscriptions/{id}/reset-token", true, s.resetToken)
	add("DELETE /api/v1/subscriptions/{id}", true, s.deleteSubscription)
	add("POST /api/v1/subscriptions/{id}/rule-profile", true, s.updateSubscriptionProfile)
	add("GET /api/v1/nodes/{id}/preparation", false, s.getPreparation)
	add("PUT /api/v1/nodes/{id}/preparation", true, s.prepareNode)
	add("POST /api/v1/nodes/{id}/observe", true, s.observeNode)
	add("POST /api/v1/nodes/{id}/drain-legacy", true, s.drainLegacy)
	add("POST /api/v1/nodes/{id}/switch-v2", true, s.switchV2)
	for resource, kind := range map[string]string{"routes": "route", "rule-profiles": "profile", "domain-sets": "domain_set", "egress-targets": "target", "egress-policies": "policy"} {
		add("GET /api/v1/"+resource+"/{id}/references", false, s.references(kind))
		add("DELETE /api/v1/"+resource+"/{id}", true, s.archive(kind))
	}
	return mux
}
func id(r *http.Request) (domain.ID, error) {
	v := domain.ID(r.PathValue("id"))
	if !v.Valid() {
		return "", problem(domain.InvalidArgument)
	}
	return v, nil
}
func pageQuery(r *http.Request, scope string) (string, int, error) {
	q := r.URL.Query()
	if q.Has("node_id") && scope != "target" && scope != "policy" {
		return "", 0, problem(domain.InvalidArgument)
	}
	for k, v := range q {
		if (k != "cursor" && k != "limit" && k != "node_id") || len(v) != 1 {
			return "", 0, problem(domain.InvalidArgument)
		}
	}
	limit := 50
	var e error
	if q.Has("limit") {
		limit, e = strconv.Atoi(q.Get("limit"))
	}
	if e != nil || limit < 1 || limit > 200 {
		return "", 0, problem(domain.InvalidArgument)
	}
	after := ""
	if cursor := q.Get("cursor"); cursor != "" {
		b, e := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if e != nil || !strings.HasPrefix(string(b), scope+"|") {
			return "", 0, problem(domain.InvalidArgument)
		}
		after = strings.TrimPrefix(string(b), scope+"|")
		if !domain.ID(after).Valid() {
			return "", 0, problem(domain.InvalidArgument)
		}
	}
	return after, limit, nil
}
func page[T any](items []T, ids []string, limit int, scope string) domain.Page[T] {
	out := domain.Page[T]{Items: items}
	if out.Items == nil {
		out.Items = []T{}
	}
	if len(items) > limit {
		out.Items = items[:limit]
		c := base64.RawURLEncoding.EncodeToString([]byte(scope + "|" + ids[limit-1]))
		out.NextCursor = &c
	}
	return out
}
func safePin(id string, rev int64, digest string) control.Pin {
	return control.Pin{ID: domain.ID(id), Revision: domain.Revision(rev), Digest: digest}
}
func (s *Service) active(ctx context.Context, kind string, id domain.ID) error {
	if id == "" {
		return nil
	}
	return s.DB.Read(ctx, func(q *dbgen.Queries) error {
		a, e := q.I03CArchived(ctx, dbgen.I03CArchivedParams{Kind: kind, ID: string(id)})
		if e == nil && a {
			return problem(domain.NotFound)
		}
		return e
	})
}
