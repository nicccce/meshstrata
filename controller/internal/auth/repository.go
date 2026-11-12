// Package auth implements single-administrator browser authentication.
package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
)

var (
	ErrUnauthenticated = errors.New("authentication required")
	ErrForbidden       = errors.New("request forbidden")
	ErrConflict        = errors.New("authentication state conflict")
	ErrRateLimited     = errors.New("authentication rate limited")
	ErrInvalid         = errors.New("invalid authentication input")
)

type Repository struct{ db storage.Database }

func NewRepository(db storage.Database) *Repository { return &Repository{db: db} }

type administrator struct {
	username, password string
	revision           int64
}

func (r *Repository) administrator(ctx context.Context) (a administrator, err error) {
	err = r.db.Read(ctx, func(q *dbgen.Queries) error {
		row, e := q.B01GetAdministrator(ctx)
		if e != nil {
			return e
		}
		a = administrator{row.Username, row.PasswordHash, row.Revision}
		return nil
	})
	return
}
func (r *Repository) initialize(ctx context.Context, username, hash string, now time.Time) error {
	return r.db.Write(ctx, func(q *dbgen.Queries) error {
		n, e := q.B01ConsumeBootstrap(ctx)
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrConflict
		}
		return q.B01CreateAdministrator(ctx, dbgen.B01CreateAdministratorParams{Username: username, PasswordHash: hash, CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
	})
}
func (r *Repository) attempt(ctx context.Context, bucket string, now time.Time, window time.Duration, max int64) error {
	limited := false
	err := r.db.Write(ctx, func(q *dbgen.Queries) error {
		row, e := q.B01GetAttempt(ctx, bucket)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		start, count := now.Unix(), int64(1)
		if e == nil && now.Unix()-row.WindowStart < int64(window.Seconds()) {
			if row.Attempts >= max {
				limited = true
				return nil
			}
			start, count = row.WindowStart, row.Attempts+1
		}
		return q.B01SetAttempt(ctx, dbgen.B01SetAttemptParams{Bucket: bucket, WindowStart: start, Attempts: count})
	})
	if err != nil {
		return err
	}
	if limited {
		return ErrRateLimited
	}
	return nil
}
func (r *Repository) login(ctx context.Context, a administrator, hash, previous []byte, now, expiry time.Time) error {
	return r.db.Write(ctx, func(q *dbgen.Queries) error {
		current, e := q.B01GetAdministrator(ctx)
		if e != nil {
			return e
		}
		if current.Revision != a.revision || current.PasswordHash != a.password {
			return ErrUnauthenticated
		}
		if e = q.B01DeleteExpiredSessions(ctx, now.Unix()); e != nil {
			return e
		}
		if e = q.B01DeleteSession(ctx, previous); e != nil {
			return e
		}
		return q.B01CreateSession(ctx, dbgen.B01CreateSessionParams{TokenHash: hash, AdminRevision: a.revision, CreatedAt: now.Unix(), ExpiresAt: expiry.Unix()})
	})
}

type session struct {
	username string
	revision int64
	expires  time.Time
	hash     []byte
	token    string
}

func (r *Repository) session(ctx context.Context, hash []byte, now time.Time) (s session, err error) {
	err = r.db.Read(ctx, func(q *dbgen.Queries) error {
		row, e := q.B01GetSession(ctx, dbgen.B01GetSessionParams{TokenHash: hash, ExpiresAt: now.Unix()})
		if e != nil {
			return e
		}
		s = session{username: row.Username, revision: row.Revision, expires: time.Unix(row.ExpiresAt, 0).UTC(), hash: row.TokenHash}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUnauthenticated
	}
	return
}
func (r *Repository) logout(ctx context.Context, s session) error {
	return r.db.Write(ctx, func(q *dbgen.Queries) error { return q.B01DeleteSession(ctx, s.hash) })
}
func (r *Repository) changePassword(ctx context.Context, s session, a administrator, hash string, now time.Time) error {
	return r.db.Write(ctx, func(q *dbgen.Queries) error {
		// Revalidate inside the mutation transaction; logout/password races cannot authorize this change.
		_, e := q.B01GetSession(ctx, dbgen.B01GetSessionParams{TokenHash: s.hash, ExpiresAt: now.Unix()})
		if errors.Is(e, sql.ErrNoRows) {
			return ErrUnauthenticated
		}
		if e != nil {
			return e
		}
		n, e := q.B01ChangePassword(ctx, dbgen.B01ChangePasswordParams{NewHash: hash, Now: now.Unix(), ExpectedRevision: a.revision, OldHash: a.password})
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrConflict
		}
		return q.B01DeleteAllSessions(ctx)
	})
}
