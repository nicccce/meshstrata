package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func digest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

type TaskTarget struct {
	NodeID          domain.ID       `json:"node_id"`
	Revision        domain.Revision `json:"revision"`
	Digest          string          `json:"digest"`
	RevocationEpoch domain.Revision `json:"revocation_epoch"`
	DependsOn       []domain.ID     `json:"depends_on"`
}

// TaskRequest contains references/digests only; future private delivery payloads must
// resolve encrypted credentials on the authenticated channel, never in the public DTO.
type TaskRequest struct {
	Kind     domain.TaskKind    `json:"kind"`
	Subject  domain.RevisionRef `json:"subject"`
	Deadline time.Time          `json:"deadline"`
	Targets  []TaskTarget       `json:"targets"`
}

// EnqueueTask atomically persists the task, per-node targets and DAG. The same scoped
// key and canonical request return the original task; changing content returns 409's code.
// It does not execute tasks or report publication success.
func (s *Store) EnqueueTask(ctx context.Context, scope, key string, request TaskRequest, now time.Time) (domain.ID, error) {
	if scope == "" || len(scope) > 128 || key == "" || len(key) > 128 || !request.Kind.Valid() || !request.Subject.ID.Valid() || !request.Subject.Revision.Valid() || now.IsZero() || len(request.Targets) == 0 || len(request.Targets) > 100 {
		return "", &domain.Problem{Code: domain.InvalidArgument}
	}
	// Copy before canonicalizing; callers retain ownership of their slices.
	request.Targets = append([]TaskTarget(nil), request.Targets...)
	request.Deadline = request.Deadline.UTC()
	nodes := make(map[domain.ID]bool)
	for i := range request.Targets {
		t := &request.Targets[i]
		t.DependsOn = append([]domain.ID{}, t.DependsOn...)
		sort.Slice(t.DependsOn, func(i, j int) bool { return t.DependsOn[i] < t.DependsOn[j] })
		if nodes[t.NodeID] {
			return "", &domain.Problem{Code: domain.InvalidArgument}
		}
		nodes[t.NodeID] = true
	}
	sort.Slice(request.Targets, func(i, j int) bool { return request.Targets[i].NodeID < request.Targets[j].NodeID })
	id, err := domain.NewID("task")
	if err != nil {
		return "", err
	}
	messageID, err := domain.NewID("msg")
	if err != nil {
		return "", err
	}
	// Validate shape independently of current expiry so a replay can retrieve a completed task.
	validationTime := request.Deadline.Add(-time.Second)
	dependencies := map[domain.ID][]domain.ID{}
	for _, t := range request.Targets {
		c := control.Command{TaskID: id, NodeID: t.NodeID, Kind: request.Kind, TargetRevision: t.Revision, TargetDigest: t.Digest, IdempotencyKey: key, Deadline: request.Deadline, DependsOn: t.DependsOn, RevocationEpoch: t.RevocationEpoch}
		m := control.Message{Version: control.Version, Type: "command", MessageID: messageID, NodeID: t.NodeID, SentAt: validationTime, Command: &c}
		if err := m.Validate(t.NodeID); err != nil || request.Deadline.IsZero() {
			return "", &domain.Problem{Code: domain.InvalidArgument}
		}
		for _, dependency := range t.DependsOn {
			if !nodes[dependency] {
				return "", &domain.Problem{Code: domain.InvalidArgument}
			}
		}
		dependencies[t.NodeID] = t.DependsOn
	}
	visited, active := map[domain.ID]bool{}, map[domain.ID]bool{}
	var visit func(domain.ID) bool
	visit = func(n domain.ID) bool {
		if active[n] {
			return false
		}
		if visited[n] {
			return true
		}
		active[n] = true
		for _, d := range dependencies[n] {
			if !visit(d) {
				return false
			}
		}
		active[n] = false
		visited[n] = true
		return true
	}
	for n := range dependencies {
		if !visit(n) {
			return "", &domain.Problem{Code: domain.InvalidArgument}
		}
	}
	hash, err := digest(request)
	if err != nil {
		return "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	q := dbgen.New(tx)
	existing, err := q.GetTaskByKey(ctx, dbgen.GetTaskByKeyParams{IdempotencyScope: scope, IdempotencyKey: key})
	if err == nil {
		if existing.RequestDigest != hash {
			return "", &domain.Problem{Code: domain.IdempotencyConflict}
		}
		return domain.ID(existing.ID), tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if !request.Deadline.After(now) {
		return "", &domain.Problem{Code: domain.TaskExpired}
	}
	err = q.InsertTask(ctx, dbgen.InsertTaskParams{ID: string(id), Kind: string(request.Kind), SubjectID: string(request.Subject.ID), TargetRevision: int64(request.Subject.Revision), RequestDigest: hash, IdempotencyScope: scope, IdempotencyKey: key, Deadline: stamp(request.Deadline), CreatedAt: stamp(now)})
	if err != nil {
		return "", err
	}
	for _, t := range request.Targets {
		if err := q.InsertTaskNode(ctx, dbgen.InsertTaskNodeParams{TaskID: string(id), NodeID: string(t.NodeID), TargetRevision: int64(t.Revision), TargetDigest: t.Digest, RevocationEpoch: int64(t.RevocationEpoch)}); err != nil {
			return "", err
		}
	}
	for _, t := range request.Targets {
		for _, dependency := range t.DependsOn {
			if err := q.InsertTaskDependency(ctx, dbgen.InsertTaskDependencyParams{TaskID: string(id), NodeID: string(t.NodeID), RequiresNodeID: string(dependency)}); err != nil {
				return "", err
			}
		}
	}
	return id, tx.Commit()
}

// RecordResult returns true after persisting a new sequence. Send a receipt only
// after this returns nil. Replays are accepted, changed replays and target drift fail.
// B04 owns deadline/phase/dependency checks before advancing any release state.
func (s *Store) RecordResult(ctx context.Context, authenticatedNode domain.ID, result control.Result) (bool, error) {
	messageID, err := domain.NewID("msg")
	if err != nil {
		return false, err
	}
	m := control.Message{Version: control.Version, Type: "result", MessageID: messageID, NodeID: authenticatedNode, SentAt: time.Now().UTC(), Result: &result}
	if err := m.Validate(authenticatedNode); err != nil {
		return false, &domain.Problem{Code: domain.InvalidArgument}
	}
	hash, err := digest(result)
	if err != nil {
		return false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	q := dbgen.New(tx)
	previous, err := q.GetTaskNode(ctx, dbgen.GetTaskNodeParams{TaskID: string(result.TaskID), NodeID: string(result.NodeID)})
	if errors.Is(err, sql.ErrNoRows) {
		return false, &domain.Problem{Code: domain.NotFound}
	}
	if err != nil {
		return false, err
	}
	if previous.TargetRevision != int64(result.TargetRevision) || previous.TargetDigest != result.TargetDigest {
		return false, &domain.Problem{Code: domain.RevisionConflict}
	}
	if previous.ResultSequence > result.Sequence {
		return false, tx.Commit()
	}
	if previous.ResultSequence == result.Sequence {
		if previous.ResultDigest.String != hash {
			return false, &domain.Problem{Code: domain.IdempotencyConflict}
		}
		return false, tx.Commit()
	}
	p := dbgen.RecordTaskResultParams{TaskID: string(result.TaskID), NodeID: string(result.NodeID), TargetRevision: int64(result.TargetRevision), TargetDigest: result.TargetDigest, Verification: string(result.Verification), RollbackState: result.Rollback, CleanupState: result.Cleanup, ResultSequence: result.Sequence, ResultSequence_2: result.Sequence, ResultDigest: sql.NullString{String: hash, Valid: true}}
	if result.Applied {
		p.Applied = 1
	}
	if result.ConfirmedAt != nil {
		p.ConfirmedAt = sql.NullString{String: stamp(*result.ConfirmedAt), Valid: true}
	}
	if result.Issue != nil {
		p.ErrorCode = sql.NullString{String: string(result.Issue.Code), Valid: true}
	}
	n, err := q.RecordTaskResult(ctx, p)
	if err != nil {
		return false, err
	}
	return n == 1, tx.Commit()
}
