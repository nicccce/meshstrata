package releases

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/httpx"
)

func (s *Service) ManagementHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/tasks", s.guard.Read(http.HandlerFunc(s.listHTTP)))
	mux.Handle("GET /api/v1/tasks/{task_id}", s.guard.Read(http.HandlerFunc(s.getHTTP)))
	for _, name := range []string{"cancel", "retry", "rollback"} {
		mux.Handle("POST /api/v1/tasks/{task_id}/"+name, s.guard.Write(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var m domain.Mutation
			d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
			d.DisallowUnknownFields()
			if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF || r.URL.RawQuery != "" {
				writeProblem(w, problem(domain.InvalidArgument))
				return
			}
			out, err := s.action(r.Context(), domain.ID(r.PathValue("task_id")), name, m)
			if err != nil {
				writeProblem(w, err)
				return
			}
			httpx.JSON(w, http.StatusAccepted, out)
		})))
	}
	return mux
}

func (s *Service) listHTTP(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	limit := 50
	var err error
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
	}
	cursor := query.Get("cursor")
	list := domain.ListQuery{Cursor: cursor, Limit: limit}
	if err != nil || list.Validate() != nil || cursor != "" && !domain.ID(cursor).Valid() {
		writeProblem(w, problem(domain.InvalidArgument))
		return
	}
	out := domain.Page[domain.Task]{Items: make([]domain.Task, 0, limit)}
	err = s.repo.db.Read(r.Context(), func(q *dbgen.Queries) error {
		rows, err := q.ListTasks(r.Context(), dbgen.ListTasksParams{ID: cursor, Limit: int64(limit + 1)})
		if err != nil {
			return err
		}
		for i, row := range rows {
			if i == limit {
				last := rows[i-1].ID
				out.NextCursor = &last
				break
			}
			task, err := taskDTO(r.Context(), q, row)
			if err != nil {
				return err
			}
			out.Items = append(out.Items, task)
		}
		return nil
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Service) getHTTP(w http.ResponseWriter, r *http.Request) {
	id := domain.ID(r.PathValue("task_id"))
	if !id.Valid() {
		writeProblem(w, problem(domain.InvalidArgument))
		return
	}
	var out domain.Task
	err := s.repo.db.Read(r.Context(), func(q *dbgen.Queries) error {
		row, err := q.GetTask(r.Context(), string(id))
		if err != nil {
			return missing(err)
		}
		out, err = taskDTO(r.Context(), q, row)
		return err
	})
	if err != nil {
		writeProblem(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func taskDTO(ctx context.Context, q *dbgen.Queries, t dbgen.Task) (domain.Task, error) {
	deadline, err := time.Parse(time.RFC3339Nano, t.Deadline)
	if err != nil {
		return domain.Task{}, err
	}
	created, err := time.Parse(time.RFC3339Nano, t.CreatedAt)
	if err != nil {
		return domain.Task{}, err
	}
	targets, err := q.B04TaskNodes(ctx, t.ID)
	if err != nil {
		return domain.Task{}, err
	}
	out := domain.Task{ID: domain.ID(t.ID), Revision: domain.Revision(t.Revision), Kind: domain.TaskKind(t.Kind),
		Subject: domain.RevisionRef{ID: domain.ID(t.SubjectID), Revision: domain.Revision(t.TargetRevision)}, State: domain.TaskState(t.State),
		Deadline: deadline, CreatedAt: created, Nodes: make([]domain.NodeResult, 0, len(targets))}
	for _, n := range targets {
		item := domain.NodeResult{NodeID: domain.ID(n.NodeID), TargetRevision: domain.Revision(n.TargetRevision), TargetDigest: n.TargetDigest,
			Applied: n.Applied == 1, Verification: domain.Verification(n.Verification), Rollback: n.RollbackState, Cleanup: n.CleanupState, Issue: resultIssue(n.ErrorCode)}
		if n.ConfirmedAt.Valid {
			at, err := time.Parse(time.RFC3339Nano, n.ConfirmedAt.String)
			if err != nil {
				return domain.Task{}, err
			}
			item.ConfirmedAt = &at
		}
		out.Nodes = append(out.Nodes, item)
	}
	return out, nil
}

func writeProblem(w http.ResponseWriter, err error) {
	var p *domain.Problem
	if !errors.As(err, &p) {
		httpx.Error(w, http.StatusInternalServerError, "INTERNAL_ERROR")
		return
	}
	status := http.StatusConflict
	switch p.Code {
	case domain.InvalidArgument:
		status = http.StatusBadRequest
	case domain.NotFound:
		status = http.StatusNotFound
	case domain.Unauthenticated:
		status = http.StatusUnauthorized
	case domain.Forbidden:
		status = http.StatusForbidden
	case domain.NotImplemented:
		status = http.StatusNotImplemented
	}
	httpx.JSON(w, status, domain.ErrorResponse{Error: domain.Problem{Code: p.Code, CurrentRevision: p.CurrentRevision}})
}
