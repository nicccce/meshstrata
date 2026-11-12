package secrets

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

var (
	ErrUnavailable = errors.New("credential unavailable")
	ErrConflict    = errors.New("credential changed")
)

type Repository struct{ db storage.Database }

func NewRepository(db storage.Database) *Repository { return &Repository{db: db} }
func (r *Repository) insert(ctx context.Context, b secretstore.Binding, sealed secretstore.Sealed, now time.Time) error {
	return r.db.Write(ctx, func(q *dbgen.Queries) error {
		return q.B01InsertCredential(ctx, dbgen.B01InsertCredentialParams{ID: string(b.Ref), Purpose: string(b.Purpose), KeyID: sealed.KeyID, Ciphertext: sealed.Ciphertext, CreatedAt: now.UTC().Format(time.RFC3339Nano)})
	})
}
func (r *Repository) load(ctx context.Context, b secretstore.Binding) (sealed secretstore.Sealed, err error) {
	err = r.db.Read(ctx, func(q *dbgen.Queries) error {
		row, e := q.B01GetCredential(ctx, dbgen.B01GetCredentialParams{ID: string(b.Ref), Purpose: string(b.Purpose)})
		if e != nil {
			return e
		}
		sealed = secretstore.Sealed{KeyID: row.KeyID, Ciphertext: bytes.Clone(row.Ciphertext)}
		return nil
	})
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrUnavailable
	}
	return
}
func (r *Repository) revoke(ctx context.Context, b secretstore.Binding, now time.Time) error {
	return r.db.Write(ctx, func(q *dbgen.Queries) error {
		_, err := q.B01RevokeCredential(ctx, dbgen.B01RevokeCredentialParams{ID: string(b.Ref), Purpose: string(b.Purpose), RevokedAt: sql.NullString{String: now.UTC().Format(time.RFC3339Nano), Valid: true}})
		return err
	})
}
func (r *Repository) rotate(ctx context.Context, b secretstore.Binding, old, new secretstore.Sealed) error {
	return r.db.Write(ctx, func(q *dbgen.Queries) error {
		n, err := q.B01RotateCredential(ctx, dbgen.B01RotateCredentialParams{ID: string(b.Ref), Purpose: string(b.Purpose), NewKeyID: new.KeyID, NewCiphertext: new.Ciphertext, OldKeyID: old.KeyID, OldCiphertext: old.Ciphertext})
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrConflict
		}
		return nil
	})
}

// CredentialOptions and the service deliberately have no HTTP representation.
// Owners choose private, authorized uses; callers clear plaintext returned by Open.
type CredentialOptions struct{ Clock func() time.Time }
type Credentials struct {
	repo   *Repository
	cipher secretstore.Cipher
	clock  func() time.Time
}

func NewCredentials(repo *Repository, cipher secretstore.Cipher, options CredentialOptions) (*Credentials, error) {
	if repo == nil || nilDependency(repo.db) || nilDependency(cipher) {
		return nil, ErrUnavailable
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	return &Credentials{repo: repo, cipher: cipher, clock: options.Clock}, nil
}
func (s *Credentials) Create(ctx context.Context, b secretstore.Binding, plaintext []byte) error {
	if !validBinding(b) {
		return secretstore.ErrInvalidBinding
	}
	sealed, err := s.cipher.Seal(ctx, b, plaintext)
	if err != nil {
		return err
	}
	return s.repo.insert(ctx, b, sealed, s.clock())
}
func (s *Credentials) Open(ctx context.Context, b secretstore.Binding) ([]byte, error) {
	if !validBinding(b) {
		return nil, secretstore.ErrInvalidBinding
	}
	sealed, err := s.repo.load(ctx, b)
	if err != nil {
		return nil, err
	}
	return s.cipher.Open(ctx, b, sealed)
}
func (s *Credentials) Revoke(ctx context.Context, b secretstore.Binding) error {
	if !validBinding(b) {
		return secretstore.ErrInvalidBinding
	}
	return s.repo.revoke(ctx, b, s.clock())
}
func (s *Credentials) Rotate(ctx context.Context, b secretstore.Binding) error {
	if !validBinding(b) {
		return secretstore.ErrInvalidBinding
	}
	old, err := s.repo.load(ctx, b)
	if err != nil {
		return err
	}
	plaintext, err := s.cipher.Open(ctx, b, old)
	if err != nil {
		return err
	}
	defer clear(plaintext)
	sealed, err := s.cipher.Seal(ctx, b, plaintext)
	if err != nil {
		return err
	}
	// No crypto or nested Store calls in this short compare-and-swap transaction.
	return s.repo.rotate(ctx, b, old, sealed)
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	switch reflect.ValueOf(value).Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	}
	return false
}
