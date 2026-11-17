package releases

import (
	"context"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

const redelivery = 10 * time.Second

func (s *Service) dispatch(ctx context.Context) error {
	now := s.options.Now()
	if err := s.sweepTasks(ctx, now); err != nil {
		return err
	}
	if err := s.reconcileRemovals(ctx); err != nil {
		return err
	}
	var ready []dbgen.B04ListReadyRow
	err := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		ready, err = q.B04ListReady(ctx, dbgen.B04ListReadyParams{Now: stamp(now), Tick: now.UnixNano()})
		return err
	})
	if err != nil {
		return err
	}
	for _, row := range ready {
		c, lease, err := s.claim(ctx, row, s.options.Connected(domain.ID(row.NodeID)))
		if err != nil {
			return err
		}
		remaining := c.Deadline.Sub(s.options.Now())
		if lease == 0 || remaining <= 0 {
			continue
		}
		delivery, cancel := context.WithTimeout(ctx, remaining)
		err = s.options.Deliver(delivery, c)
		cancel()
		if err != nil {
			continue
		}
		// A successful transport write is not a result. Keep the target armed,
		// but let the peer finish without receiving duplicate commands. A new
		// peer's first Hello can clear this wait and replay the identical command.
		err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
			return q.B04WaitForResult(ctx, dbgen.B04WaitForResultParams{TaskID: row.TaskID, NodeID: row.NodeID,
				Lease: lease, DeadlineTick: c.Deadline.UnixNano()})
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) claim(ctx context.Context, row dbgen.B04ListReadyRow, connected bool) (control.Command, int64, error) {
	var c control.Command
	var lease int64
	err := s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		now := s.options.Now()
		tick, next := now.UnixNano(), now.Add(redelivery).UnixNano()
		if !connected {
			return q.B04DeferTarget(ctx, dbgen.B04DeferTargetParams{TaskID: row.TaskID, NodeID: row.NodeID, Tick: tick, NextAttempt: next})
		}
		n, err := q.GetNode(ctx, row.NodeID)
		if err != nil {
			return err
		}
		tn, err := q.GetTaskNode(ctx, dbgen.GetTaskNodeParams{TaskID: row.TaskID, NodeID: row.NodeID})
		if err != nil {
			return err
		}
		if n.RemovedAt.Valid || n.RevocationEpoch != tn.RevocationEpoch {
			return finishTask(ctx, q, row.TaskID, domain.TaskFailed, now)
		}
		blocked, err := q.B04DependencyBlocked(ctx, dbgen.B04DependencyBlockedParams{TaskID: row.TaskID, NodeID: row.NodeID, Now: stamp(now)})
		if blocked || err != nil {
			return err
		}
		if err := checkTargetGrants(ctx, q, row.TaskID, row.NodeID, now); err != nil {
			if isProblem(err) {
				return finishTask(ctx, q, row.TaskID, domain.TaskFailed, now)
			}
			return err
		}
		armed, err := q.B04ArmTarget(ctx, dbgen.B04ArmTargetParams{TaskID: row.TaskID, NodeID: row.NodeID, Now: stamp(now), Tick: tick, NextAttempt: next})
		if err != nil || armed == 0 {
			return err
		}
		if err := q.B04SetRunning(ctx, row.TaskID); err != nil {
			return err
		}
		task, err := q.GetTask(ctx, row.TaskID)
		if err != nil {
			return err
		}
		deadline, err := time.Parse(time.RFC3339Nano, task.Deadline)
		if err != nil {
			return err
		}
		deps, err := q.B04Dependencies(ctx, dbgen.B04DependenciesParams{TaskID: row.TaskID, NodeID: row.NodeID})
		if err != nil {
			return err
		}
		c = control.Command{TaskID: domain.ID(row.TaskID), NodeID: domain.ID(row.NodeID), Kind: domain.TaskKind(task.Kind),
			TargetRevision: domain.Revision(tn.TargetRevision), TargetDigest: tn.TargetDigest, Deadline: deadline,
			IdempotencyKey: row.TaskID, RevocationEpoch: domain.Revision(tn.RevocationEpoch), DependsOn: make([]domain.ID, 0, len(deps))}
		lease = next
		for _, dep := range deps {
			c.DependsOn = append(c.DependsOn, domain.ID(dep))
		}
		return nil
	})
	return c, lease, err
}

func (s *Service) sweepTasks(ctx context.Context, now time.Time) error {
	return s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		expired, err := q.B04Expiring(ctx, stamp(now))
		if err != nil {
			return err
		}
		for _, id := range expired {
			if err := finishTask(ctx, q, id, domain.TimedOut, now); err != nil {
				return err
			}
		}
		fenced, err := q.B04FencedTasks(ctx)
		if err != nil {
			return err
		}
		for _, id := range fenced {
			if err := finishTask(ctx, q, id, domain.TaskFailed, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func finishTask(ctx context.Context, q *dbgen.Queries, task string, state domain.TaskState, now time.Time) error {
	n, err := q.B04SetTerminal(ctx, dbgen.B04SetTerminalParams{ID: task, State: string(state)})
	if err != nil || n == 0 {
		return err
	}
	refs, err := q.B04UnsentReferences(ctx, task)
	if err != nil {
		return err
	}
	if err := retireReferences(ctx, q, refs, task, stamp(now)); err != nil {
		return err
	}
	return q.B04HoldUnusedPorts(ctx, task)
}
