package releases

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

func artifactKey(a dbgen.B04Artifact) control.ArtifactKey {
	return control.ArtifactKey{TaskID: domain.ID(a.TaskID), NodeID: domain.ID(a.NodeID), TargetRevision: domain.Revision(a.TargetRevision), TargetDigest: a.TargetDigest}
}

func (s *Service) Resolve(ctx context.Context, node, task domain.ID) (control.ArtifactKey, error) {
	if !node.Valid() || !task.Valid() {
		return control.ArtifactKey{}, problem(domain.InvalidArgument)
	}
	var key control.ArtifactKey
	err := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		artifact, e := authorizedArtifact(ctx, q, node, task, s.options.Now())
		key = artifactKey(artifact)
		if e == nil {
			e = s.checkV2Target(ctx, q, string(task), string(node), s.options.Now())
		}
		return e
	})
	return key, err
}

func (s *Service) Open(ctx context.Context, node domain.ID, key control.ArtifactKey) (io.ReadCloser, error) {
	if !node.Valid() || node != key.NodeID || !key.TaskID.Valid() {
		return nil, problem(domain.Forbidden)
	}
	var a dbgen.B04Artifact
	err := s.repo.db.Read(ctx, func(q *dbgen.Queries) error {
		var err error
		a, err = authorizedArtifact(ctx, q, node, key.TaskID, s.options.Now())
		if err == nil {
			err = s.checkV2Target(ctx, q, string(key.TaskID), string(node), s.options.Now())
		}
		if err == nil && artifactKey(a) != key {
			return problem(domain.RevisionConflict)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	data, err := s.openBytes(ctx, a)
	if err != nil {
		return nil, err
	}
	err = s.repo.db.Write(ctx, func(q *dbgen.Queries) error {
		current, err := authorizedArtifact(ctx, q, node, key.TaskID, s.options.Now())
		if err != nil {
			return err
		}
		if artifactKey(current) != key || current.ID != a.ID {
			return problem(domain.RevisionConflict)
		}
		if err = s.checkV2Target(ctx, q, a.TaskID, a.NodeID, s.options.Now()); err != nil {
			return err
		}
		if a.Format == 2 {
			if err = q.I03PinOrigins(ctx, dbgen.I03PinOriginsParams{TaskID: a.TaskID, NodeID: a.NodeID}); err != nil {
				return err
			}
		}
		params := dbgen.B04PinGrantOriginsParams{TaskID: nullable(a.TaskID), NodeID: a.NodeID}
		if err := q.B04PinGrantOrigins(ctx, params); err != nil {
			return err
		}
		return q.B04MarkFetched(ctx, dbgen.B04MarkFetchedParams{TaskID: a.TaskID, NodeID: a.NodeID})
	})
	if err != nil {
		clear(data)
		return nil, err
	}
	return &artifactReader{Reader: *bytes.NewReader(data), data: data}, nil
}

func authorizedArtifact(ctx context.Context, q *dbgen.Queries, node, task domain.ID, now time.Time) (dbgen.B04Artifact, error) {
	var empty dbgen.B04Artifact
	n, err := q.GetNode(ctx, string(node))
	if err != nil {
		return empty, missing(err)
	}
	if n.RemovedAt.Valid {
		return empty, problem(domain.Forbidden)
	}
	t, err := q.GetTask(ctx, string(task))
	if err != nil {
		return empty, missing(err)
	}
	if t.Deadline <= stamp(now) {
		return empty, problem(domain.TaskExpired)
	}
	b, err := q.B04GetTarget(ctx, dbgen.B04GetTargetParams{TaskID: t.ID, NodeID: n.ID})
	if err != nil {
		return empty, missing(err)
	}
	if !active(t.State) || b.Phase != "armed" {
		return empty, problem(domain.Forbidden)
	}
	tn, err := q.GetTaskNode(ctx, dbgen.GetTaskNodeParams{TaskID: t.ID, NodeID: n.ID})
	if err != nil {
		return empty, err
	}
	if n.RevocationEpoch != tn.RevocationEpoch {
		return empty, conflict(n.Revision)
	}
	blocked, err := q.B04DependencyBlocked(ctx, dbgen.B04DependencyBlockedParams{TaskID: t.ID, NodeID: n.ID, Now: stamp(now)})
	if err != nil {
		return empty, err
	}
	if blocked {
		return empty, problem(domain.DependencyInUse)
	}
	if err := checkTargetGrants(ctx, q, t.ID, n.ID, now); err != nil {
		return empty, err
	}
	a, err := q.B04GetArtifact(ctx, dbgen.B04GetArtifactParams{TaskID: t.ID, NodeID: n.ID})
	if err == nil && (a.TargetRevision != tn.TargetRevision || a.TargetDigest != tn.TargetDigest) {
		return empty, problem(domain.RevisionConflict)
	}
	return a, missing(err)
}

func checkTargetGrants(ctx context.Context, q *dbgen.Queries, task, node string, now time.Time) error {
	grants, err := q.B04TargetGrants(ctx, dbgen.B04TargetGrantsParams{TaskID: task, NodeID: node})
	if err != nil {
		return err
	}
	mentioned := make(map[string]bool, len(grants))
	for _, g := range grants {
		mentioned[g.IdentityID] = true
		if g.Action != "include" {
			continue
		}
		if g.ExpiresAt <= stamp(now) || g.RestrictedAt.Valid {
			return problem(domain.TaskExpired)
		}
		if g.OriginTaskID.Valid && g.OriginTaskID.String != task {
			origin, err := q.B04GetTarget(ctx, dbgen.B04GetTargetParams{TaskID: g.OriginTaskID.String, NodeID: node})
			if err != nil {
				return err
			}
			// A fetch may have reached the agent even if the transport did not
			// deliver a result. Until confirmed, the first origin is ambiguous.
			if origin.Phase != "confirmed" {
				return problem(domain.GrantOriginUncertain)
			}
		}
	}
	live, err := q.B04PossibleLiveGrants(ctx, node)
	if err != nil {
		return err
	}
	for _, g := range live {
		if !mentioned[g.IdentityID] {
			return problem(domain.DependencyInUse)
		}
	}
	return nil
}

func (s *Service) openBytes(ctx context.Context, a dbgen.B04Artifact) ([]byte, error) {
	b, err := s.cipher.Open(ctx, binding(domain.ID(a.ID)), secretstore.Sealed{KeyID: a.KeyID, Ciphertext: a.Ciphertext})
	if err != nil || len(b) > control.MaxBundleBytes || control.DigestBytes(b) != a.TargetDigest {
		clear(b)
		return nil, errors.New("release artifact unavailable")
	}
	return b, nil
}

func (s *Service) openTemplate(ctx context.Context, a dbgen.B04Artifact) (control.Bundle, error) {
	if a.Format != 1 {
		return control.Bundle{}, problem(domain.NotImplemented)
	}
	data, err := s.openBytes(ctx, a)
	if err != nil {
		return control.Bundle{}, err
	}
	defer clear(data)
	return control.DecodeBundle(data, artifactKey(a), domain.ID(a.NodeID))
}

type artifactReader struct {
	bytes.Reader
	data []byte
}

func (r *artifactReader) Close() error {
	clear(r.data)
	r.data = nil
	r.Reset(nil)
	return nil
}

func isProblem(err error) bool {
	var p *domain.Problem
	return errors.As(err, &p)
}

func legitimatelyAbsent(err error) bool { return errors.Is(err, sql.ErrNoRows) }
