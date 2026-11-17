// Package releases owns the durable Controller publication ledger.
package releases

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"time"

	channel "github.com/nicccce/meshstrata/controller/internal/control"
	"github.com/nicccce/meshstrata/controller/internal/enrollment"
	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/internal/adminauth"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

type Repository struct{ db storage.Database }

func NewRepository(db storage.Database) *Repository { return &Repository{db: db} }

type Options struct {
	Now           func() time.Time
	NewID         func(string) (domain.ID, error)
	Deliver       func(context.Context, control.Command) error
	Connected     func(domain.ID) bool
	RemovalIntent func(context.Context, domain.ID) (enrollment.RemovalIntent, error)
}

type Service struct {
	repo    *Repository
	cipher  secretstore.Cipher
	guard   adminauth.Guard
	options Options

	wake          chan struct{}
	stop          chan struct{}
	done          chan struct{}
	lifecycle     sync.Mutex
	started       bool
	nextRemoval   time.Time
	removalCursor string
}

var (
	_ channel.ResultStore      = (*Service)(nil)
	_ channel.Recovery         = (*Service)(nil)
	_ channel.ArtifactResolver = (*Service)(nil)
	_ control.ArtifactSource   = (*Service)(nil)
)

func New(repo *Repository, cipher secretstore.Cipher, guard adminauth.Guard, options Options) (*Service, error) {
	if repo == nil || nilDependency(repo.db) || nilDependency(cipher) || nilDependency(guard) || options.Now == nil ||
		options.NewID == nil || options.Deliver == nil || options.Connected == nil || options.RemovalIntent == nil {
		return nil, errors.New("missing release dependency")
	}
	return &Service{repo: repo, cipher: cipher, guard: guard, options: options,
		wake: make(chan struct{}, 1), stop: make(chan struct{}), done: make(chan struct{})}, nil
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func (s *Service) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func problem(code domain.ErrorCode) error { return &domain.Problem{Code: code} }

func missing(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return problem(domain.NotFound)
	}
	return err
}

func conflict(current int64) error {
	revision := domain.Revision(current)
	return &domain.Problem{Code: domain.RevisionConflict, CurrentRevision: &revision}
}

func stamp(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func hashPayload(payload any) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", problem(domain.InvalidArgument)
	}
	defer clear(data)
	return control.DigestBytes(data), nil
}

func nullable(value string) sql.NullString { return sql.NullString{String: value, Valid: true} }

func nullTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: stamp(*t), Valid: true}
}

func mutationValid(m domain.Mutation) bool {
	return m.ExpectedRevision.Valid() && len(m.IdempotencyKey) > 0 && len(m.IdempotencyKey) <= 128
}

func active(state string) bool {
	return state == string(domain.Queued) || state == string(domain.Running)
}

func (s *Service) newID(kind string) (domain.ID, error) {
	id, err := s.options.NewID(kind)
	if err != nil || !id.Valid() {
		return "", errors.New("release ID unavailable")
	}
	return id, nil
}

func (s *Service) Run(ctx context.Context) error {
	s.lifecycle.Lock()
	select {
	case <-s.stop:
		s.lifecycle.Unlock()
		return errors.New("release scheduler closed")
	default:
	}
	if s.started {
		s.lifecycle.Unlock()
		return errors.New("release scheduler already started")
	}
	s.started = true
	s.lifecycle.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-s.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer close(s.done)
	defer cancel()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for ctx.Err() == nil {
		if err := s.dispatch(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		select {
		case <-ctx.Done():
		case <-s.wake:
		case <-tick.C:
		}
	}
	return nil
}

func (s *Service) Close() error {
	s.lifecycle.Lock()
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	started := s.started
	s.lifecycle.Unlock()
	if started {
		<-s.done
	}
	return nil
}
