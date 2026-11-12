package enrollment

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/nicccce/meshstrata/internal/domain"
)

func headers(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
func respond(w http.ResponseWriter, status int, value any) {
	headers(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failure(w http.ResponseWriter, err error) {
	p := &domain.Problem{Code: "INTERNAL_ERROR"}
	var known *domain.Problem
	if errors.As(err, &known) {
		p = &domain.Problem{Code: known.Code, CurrentRevision: known.CurrentRevision}
	}
	status := http.StatusInternalServerError
	switch p.Code {
	case domain.InvalidArgument:
		status = 400
	case domain.Unauthenticated:
		status = 401
	case domain.Forbidden:
		status = 403
	case domain.NotFound:
		status = 404
	case domain.RevisionConflict, domain.IdempotencyConflict, domain.TaskExpired:
		status = 409
	case domain.NotImplemented:
		status = 501
	}
	respond(w, status, map[string]any{"error": p})
}
func body(w http.ResponseWriter, r *http.Request, out any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		failure(w, problem(domain.InvalidArgument))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		failure(w, problem(domain.InvalidArgument))
		return false
	}
	return true
}
func (s *Service) ManagementHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/nodes", s.guard.Read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := 50
		var e error
		if raw := r.URL.Query().Get("limit"); raw != "" {
			limit, e = strconv.Atoi(raw)
			if e != nil {
				failure(w, problem(domain.InvalidArgument))
				return
			}
		}
		out, e := s.List(r.Context(), domain.ListQuery{Limit: limit, Cursor: r.URL.Query().Get("cursor")})
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})))
	mux.Handle("POST /api/v1/nodes", s.guard.Write(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req IssueRequest
		if !body(w, r, &req) {
			return
		}
		out, e := s.Issue(r.Context(), "", req)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})))
	mux.Handle("GET /api/v1/nodes/{node_id}", s.guard.Read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, e := s.Get(r.Context(), domain.ID(r.PathValue("node_id")))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})))
	mux.Handle("GET /api/v1/nodes/{node_id}/enrollment", s.guard.Read(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out, e := s.Status(r.Context(), domain.ID(r.PathValue("node_id")))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})))
	mux.Handle("POST /api/v1/nodes/{node_id}/enrollment", s.guard.Write(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req IssueRequest
		if !body(w, r, &req) {
			return
		}
		out, e := s.Issue(r.Context(), domain.ID(r.PathValue("node_id")), req)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})))
	mux.Handle("PATCH /api/v1/nodes/{node_id}", s.guard.Write(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ExpectedRevision domain.Revision `json:"expected_revision"`
			IdempotencyKey   string          `json:"idempotency_key"`
			Name             string          `json:"name"`
		}
		if !body(w, r, &req) {
			return
		}
		out, e := s.Rename(r.Context(), domain.ID(r.PathValue("node_id")), req.ExpectedRevision, req.IdempotencyKey, req.Name)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})))
	mux.Handle("POST /api/v1/nodes/{node_id}/remove", s.guard.Write(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ExpectedRevision domain.Revision `json:"expected_revision"`
			IdempotencyKey   string          `json:"idempotency_key"`
		}
		if !body(w, r, &req) {
			return
		}
		id := domain.ID(r.PathValue("node_id"))
		task, e := s.RemoveTask(r.Context(), id, req.ExpectedRevision, req.IdempotencyKey)
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 202, task)
	})))
	mux.Handle("POST /api/v1/nodes/{node_id}/certificates/revoke", s.guard.Write(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req domain.Mutation
		if !body(w, r, &req) {
			return
		}
		id := domain.ID(r.PathValue("node_id"))
		revision, err := s.RevokeCertificates(r.Context(), id, req.ExpectedRevision, req.IdempotencyKey)
		if err != nil {
			failure(w, err)
			return
		}
		respond(w, 200, map[string]any{"node_id": id, "revision": revision})
	})))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { headers(w); mux.ServeHTTP(w, r) })
}
func (s *Service) EnrollmentHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /enroll/v1", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			failure(w, problem(domain.Unauthenticated))
			return
		}
		var req struct {
			Token  string `json:"token"`
			CSRPEM string `json:"csr_pem"`
		}
		if !body(w, r, &req) {
			return
		}
		out, e := s.Enroll(r.Context(), req.Token, []byte(req.CSRPEM))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	mux.HandleFunc("POST /enroll/v1/renew", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			failure(w, problem(domain.Unauthenticated))
			return
		}
		if _, e := s.Authenticate(r.Context(), r.TLS.PeerCertificates[0]); e != nil {
			failure(w, e)
			return
		}
		var req struct {
			CSRPEM string `json:"csr_pem"`
		}
		if !body(w, r, &req) {
			return
		}
		out, e := s.Renew(r.Context(), r.TLS.PeerCertificates[0], []byte(req.CSRPEM))
		if e != nil {
			failure(w, e)
			return
		}
		respond(w, 200, out)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { headers(w); mux.ServeHTTP(w, r) })
}

// NodeHandler composes the only unauthenticated-by-client-certificate route with
// a separately authenticated control/artifact handler on the 9443 TLS listener.
func (s *Service) NodeHandler(control http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers(w)
		if r.URL.Path == "/enroll/v1" || r.URL.Path == "/enroll/v1/renew" {
			s.EnrollmentHandler().ServeHTTP(w, r)
			return
		}
		if r.TLS == nil || len(r.TLS.PeerCertificates) == 0 {
			failure(w, problem(domain.Unauthenticated))
			return
		}
		if _, e := s.Authenticate(r.Context(), r.TLS.PeerCertificates[0]); e != nil {
			failure(w, e)
			return
		}
		if control == nil {
			failure(w, problem(domain.NotImplemented))
			return
		}
		control.ServeHTTP(w, r)
	})
}
