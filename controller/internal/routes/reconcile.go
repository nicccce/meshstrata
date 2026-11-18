package routes

import (
	"context"
	"database/sql"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func (s *Service) ReconcilePublication(ctx context.Context, task domain.ID) (stage3.ReleaseState, error) {
	state, err := s.reconcile(ctx, task)
	if err = safe(ctx, err); err != nil {
		return stage3.ReleaseState{}, err
	}
	return state, nil
}
func (s *Service) reconcile(ctx context.Context, task domain.ID) (stage3.ReleaseState, error) {
	none := stage3.ReleaseState{}
	if !task.Valid() {
		return none, problem(domain.InvalidArgument, "/task")
	}
	var p dbgen.B06Publication
	var subj control.Pin
	var nodes []dbgen.B06PublicationNode
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		p, e = q.B06TaskPublication(ctx, sql.NullString{String: string(task), Valid: true})
		if e != nil {
			return e
		}
		v, e := q.B06RouteVersion(ctx, dbgen.B06RouteVersionParams{RouteID: p.RouteID, Revision: p.Revision})
		if e != nil {
			return e
		}
		subj = control.Pin{ID: domain.ID(v.RouteID), Revision: domain.Revision(v.Revision), Digest: v.DraftDigest}
		nodes, e = q.B06PublicationNodes(ctx, p.ID)
		return e
	})
	if err != nil {
		return none, err
	}
	state, err := s.opts.Publisher.ReleaseState(ctx, task)
	if err != nil {
		return none, dependency(ctx, err, "/task")
	}
	if state.TaskID != task || state.Subject != subj || state.EntryOperationID != "" || !validReleaseState(state.State) {
		return none, problem(domain.GrantOriginUncertain, "/task")
	}
	if state.State == "published" {
		deadline, err := time.Parse(time.RFC3339Nano, p.Deadline)
		if err != nil {
			return none, err
		}
		plan, err := s.restore(ctx, p, subj, deadline, nodes)
		if err != nil {
			return none, err
		}
		if err = confirmed(plan, state); err != nil {
			return none, err
		}
	}
	// The gateway may return evidence from its transport or probes. Expose only
	// allowed codes; leave its issue details outside this boundary.
	state.Nodes = append([]domain.NodeResult(nil), state.Nodes...)
	for i, n := range state.Nodes {
		if n.ConfirmedAt != nil {
			at := *n.ConfirmedAt
			state.Nodes[i].ConfirmedAt = &at
		}
		if n.Issue != nil {
			state.Nodes[i].Issue = problem(issueFor(dependency(ctx, n.Issue, ""), "").Code, "")
		}
	}
	err = s.opts.DB.Write(ctx, func(q *dbgen.Queries) error {
		row, err := q.B06TaskPublication(ctx, sql.NullString{String: string(task), Valid: true})
		if err != nil {
			return err
		}
		if row.ID != p.ID {
			return problem(domain.GrantOriginUncertain, "/task")
		}
		if row.State == state.State {
			return nil
		}
		if !validTransition(row.State, state.State) {
			return problem(domain.RevisionConflict, "/task/state")
		}
		if state.State == "published" {
			n, err := q.B06ConfirmVersion(ctx, p.ID)
			if err != nil {
				return err
			}
			if n != 1 {
				return problem(domain.GrantOriginUncertain, "/publication")
			}
			if err = q.B06AdvanceHead(ctx, dbgen.B06AdvanceHeadParams{RouteID: p.RouteID, Revision: p.Revision}); err != nil {
				return err
			}
		}
		_, err = q.B06SetPublicationState(ctx, dbgen.B06SetPublicationStateParams{ID: row.ID, OldState: row.State, NewState: state.State})
		return err
	})
	return state, err
}

func confirmed(plan stage3.PublishPlan, state stage3.ReleaseState) error {
	if len(plan.Nodes) != len(state.Nodes) {
		return problem(domain.GrantOriginUncertain, "/task/nodes")
	}
	results := map[domain.ID]domain.NodeResult{}
	for _, r := range state.Nodes {
		results[r.NodeID] = r
	}
	for _, n := range plan.Nodes {
		b := n.Template
		b.TaskID = state.TaskID // actual task ID returned by B04
		data, key, err := control.EncodeBundleV2(b)
		clear(data)
		r, ok := results[n.NodeID]
		if err != nil || !ok || !r.Applied || r.TargetRevision != key.TargetRevision || r.TargetDigest != key.TargetDigest ||
			r.ConfirmedAt == nil || !utc(*r.ConfirmedAt) || r.ConfirmedAt.After(plan.Deadline) || (r.Rollback != "" && r.Rollback != "none") {
			return problem(domain.GrantOriginUncertain, "/task/nodes")
		}
	}
	return nil
}
func validReleaseState(state string) bool {
	switch state {
	case "queued", "running", "published", "failed", "cancelled", "timed_out", "rolled_back", "rollback_pending", "rollback_failed":
		return true
	}
	return false
}
func validTransition(old, next string) bool {
	switch old {
	case "queued":
		return true
	case "running":
		return next != "queued"
	case "failed", "timed_out", "cancelled":
		return next == "rollback_pending" || next == "rollback_failed" || next == "rolled_back"
	case "rollback_pending", "rollback_failed":
		return next == "rolled_back" || next == "rollback_failed" || next == "rollback_pending"
	}
	return false
}

// PublishedHead is a safe Controller projection for discovering the fixed pin
// after task confirmation. Historical lookups use PublishedRoute directly.
func (s *Service) PublishedHead(ctx context.Context, id domain.ID) (stage3.PublishedRoute, error) {
	if !id.Valid() {
		return stage3.PublishedRoute{}, problem(domain.InvalidArgument, "/route")
	}
	var pin control.Pin
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		rev, err := q.B06RouteHead(ctx, string(id))
		if err != nil {
			return err
		}
		p, err := q.B06PublishedVersion(ctx, dbgen.B06PublishedVersionParams{RouteID: string(id), Revision: rev})
		pin = control.Pin{ID: id, Revision: domain.Revision(rev), Digest: p.RouteDigest}
		return err
	})
	if err = safe(ctx, err); err != nil {
		return stage3.PublishedRoute{}, err
	}
	return s.PublishedRoute(ctx, pin)
}
