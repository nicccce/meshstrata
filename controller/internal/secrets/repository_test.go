package secrets

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/storage"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

func newCredentials(t *testing.T, repo *Repository, c secretstore.Cipher) *Credentials {
	t.Helper()
	s, e := NewCredentials(repo, c, CredentialOptions{Clock: func() time.Time { return time.Unix(1800000000, 0) }})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestCredentialPersistenceAndRevocation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db, e := storage.Open(ctx, dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { db.Close() })
	repo := NewRepository(db)
	c := testCipher(t, "v1")
	s := newCredentials(t, repo, c)
	b := binding()
	for i, purpose := range []secretstore.Purpose{secretstore.ProxyEntry, secretstore.InternalProxy, secretstore.DNSAPI, secretstore.LocationAPI, secretstore.ManagementCA, secretstore.Tunnel, secretstore.Certificate} {
		b.Purpose = purpose
		if e = s.Create(ctx, b, []byte("private payload")); e != nil {
			t.Fatal(e)
		}
		sealed, e := repo.load(ctx, b)
		if e != nil || sealed.KeyID != "v1" || bytes.Contains(sealed.Ciphertext, []byte("private payload")) {
			t.Fatal("not stored as ciphertext")
		}
		if e = db.Close(); e != nil {
			t.Fatal(e)
		}
		db, e = storage.Open(ctx, dir)
		if e != nil {
			t.Fatal(e)
		}
		repo = NewRepository(db)
		s = newCredentials(t, repo, c)
		p, e := s.Open(ctx, b)
		if e != nil || string(p) != "private payload" {
			t.Fatal("credential not durable")
		}
		clear(p)
		wrong := b
		wrong.Purpose = "future_purpose"
		if p, e := s.Open(ctx, wrong); p != nil || !errors.Is(e, ErrUnavailable) {
			t.Fatal("wrong purpose returned material")
		}
		if e = s.Revoke(ctx, b); e != nil {
			t.Fatal(e)
		}
		if e = s.Revoke(ctx, b); e != nil {
			t.Fatal("non-idempotent revoke")
		}
		if p, e := s.Open(ctx, b); p != nil || !errors.Is(e, ErrUnavailable) {
			t.Fatal("revoked credential opened")
		}
		if e = s.Rotate(ctx, b); !errors.Is(e, ErrUnavailable) {
			t.Fatal("revoked credential rotated")
		}
		// Use a distinct credential ID for the next purpose.
		b.Ref = domain.ID("credential_11112233445566778899aabbccddeef" + string("0123456"[i]))
	}
	// 002 rejects future purposes until their owner installs an incremental migration.
	b.Purpose = "subscription_download"
	if e = s.Create(ctx, b, []byte("future")); e == nil {
		t.Fatal("unmigrated purpose stored")
	}
}

type hookCipher struct {
	secretstore.Cipher
	beforeSeal func()
	opens      atomic.Int64
}

func (c *hookCipher) Seal(ctx context.Context, b secretstore.Binding, p []byte) (secretstore.Sealed, error) {
	if c.beforeSeal != nil {
		c.beforeSeal()
	}
	return c.Cipher.Seal(ctx, b, p)
}
func (c *hookCipher) Open(ctx context.Context, b secretstore.Binding, sealed secretstore.Sealed) ([]byte, error) {
	c.opens.Add(1)
	return c.Cipher.Open(ctx, b, sealed)
}
func TestRotationCASAndConcurrentRevocation(t *testing.T) {
	ctx := context.Background()
	db, e := storage.Open(ctx, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	repo := NewRepository(db)
	b := binding()
	old := newCredentials(t, repo, testCipher(t, "v1"))
	if e := old.Create(ctx, b, []byte("material")); e != nil {
		t.Fatal(e)
	}
	before, e := repo.load(ctx, b)
	if e != nil {
		t.Fatal(e)
	}
	current := newCredentials(t, repo, testCipher(t, "v2"))
	if e := current.Rotate(ctx, b); e != nil {
		t.Fatal(e)
	}
	after, e := repo.load(ctx, b)
	if e != nil || after.KeyID != "v2" {
		t.Fatal("rotation failed")
	}
	if bytes.Equal(before.Ciphertext, after.Ciphertext) {
		t.Fatal("rotation retained old envelope")
	}
	if e := repo.rotate(ctx, b, before, after); !errors.Is(e, ErrConflict) {
		t.Fatal("stale envelope overwritten")
	}
	p, e := current.Open(ctx, b)
	if e != nil || string(p) != "material" {
		t.Fatal("rotation corrupted plaintext")
	}
	clear(p)
	// Both rotations prepare outside transactions, then race on the same old envelope.
	arrived := make(chan struct{}, 2)
	release := make(chan struct{})
	hook := &hookCipher{Cipher: testCipher(t, "v2"), beforeSeal: func() { arrived <- struct{}{}; <-release }}
	racer := newCredentials(t, repo, hook)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- racer.Rotate(ctx, b) }()
	}
	<-arrived
	<-arrived
	close(release)
	wg.Wait()
	close(results)
	ok, conflict := 0, 0
	for e := range results {
		if e == nil {
			ok++
		} else if errors.Is(e, ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatalf("rotation winners=%d conflicts=%d", ok, conflict)
	}
	// Revoke in the crypto hook. This also detects crypto inside a single-connection
	// transaction: that implementation would deadlock instead of reaching CAS.
	hook = &hookCipher{Cipher: testCipher(t, "v2"), beforeSeal: func() {
		if e := current.Revoke(ctx, b); e != nil {
			t.Error(e)
		}
	}}
	racer = newCredentials(t, repo, hook)
	if e := racer.Rotate(ctx, b); !errors.Is(e, ErrConflict) {
		t.Fatalf("revocation race: %v", e)
	}
	opens := hook.opens.Load()
	if p, e := racer.Open(ctx, b); p != nil || !errors.Is(e, ErrUnavailable) {
		t.Fatal("revoked material available")
	}
	if hook.opens.Load() != opens {
		t.Fatal("revoked row reached decrypt")
	}
}

var rollbackError = errors.New("injected transaction failure")

type rollbackDB struct {
	storage.Database
	fail bool
}

func (d *rollbackDB) Write(ctx context.Context, fn func(*dbgen.Queries) error) error {
	return d.Database.Write(ctx, func(q *dbgen.Queries) error {
		if e := fn(q); e != nil {
			return e
		}
		if d.fail {
			return rollbackError
		}
		return nil
	})
}
func TestCredentialRollback(t *testing.T) {
	ctx := context.Background()
	db, e := storage.Open(ctx, t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	wrapper := &rollbackDB{Database: db, fail: true}
	repo := NewRepository(wrapper)
	s := newCredentials(t, repo, testCipher(t, "v1"))
	b := binding()
	if e := s.Create(ctx, b, []byte("material")); !errors.Is(e, rollbackError) {
		t.Fatal(e)
	}
	if _, e := repo.load(ctx, b); !errors.Is(e, ErrUnavailable) {
		t.Fatal("failed create persisted")
	}
	wrapper.fail = false
	if e := s.Create(ctx, b, []byte("material")); e != nil {
		t.Fatal(e)
	}
	before, e := repo.load(ctx, b)
	if e != nil {
		t.Fatal(e)
	}
	wrapper.fail = true
	current := newCredentials(t, repo, testCipher(t, "v2"))
	if e := current.Rotate(ctx, b); !errors.Is(e, rollbackError) {
		t.Fatal(e)
	}
	after, e := repo.load(ctx, b)
	if e != nil || !bytes.Equal(before.Ciphertext, after.Ciphertext) || before.KeyID != after.KeyID {
		t.Fatal("failed rotation committed")
	}
	if e := s.Revoke(ctx, b); !errors.Is(e, rollbackError) {
		t.Fatal(e)
	}
	if p, e := s.Open(ctx, b); e != nil {
		t.Fatal("failed revoke committed")
	} else {
		clear(p)
	}
}

func TestTypedNilDependenciesFailConstruction(t *testing.T) {
	var store *storage.Store
	if _, e := NewCredentials(NewRepository(store), testCipher(t, "v1"), CredentialOptions{}); e == nil {
		t.Fatal("typed nil database accepted")
	}
	db, e := storage.Open(context.Background(), t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var c *localCipher
	if _, e := NewCredentials(NewRepository(db), c, CredentialOptions{}); e == nil {
		t.Fatal("typed nil cipher accepted")
	}
	var reader *bytes.Reader
	if _, e := New(Options{CurrentKeyID: "v1", Keys: map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)}, Random: reader}); e == nil {
		t.Fatal("typed nil random source accepted")
	}
}
