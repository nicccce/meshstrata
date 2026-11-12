package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/secretstore"
)

func binding() secretstore.Binding {
	return secretstore.Binding{Ref: domain.ID("credential_00112233445566778899aabbccddeeff"), Purpose: secretstore.DNSAPI}
}
func testCipher(t *testing.T, id string) secretstore.Cipher {
	t.Helper()
	c, e := New(Options{CurrentKeyID: id, Keys: map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32), "v2": bytes.Repeat([]byte{2}, 32)}})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func TestAuthenticatedEnvelope(t *testing.T) {
	ctx := context.Background()
	b := binding()
	c := testCipher(t, "v1")
	plaintext := []byte("private credential material")
	sealed, e := c.Seal(ctx, b, plaintext)
	if e != nil {
		t.Fatal(e)
	}
	again, e := c.Seal(ctx, b, plaintext)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(sealed.Ciphertext, again.Ciphertext) {
		t.Fatal("nonce reused")
	}
	got, e := c.Open(ctx, b, sealed)
	if e != nil || !bytes.Equal(got, plaintext) {
		t.Fatal("round trip failed")
	}
	clear(got)
	newer := testCipher(t, "v2")
	got, e = newer.Open(ctx, b, sealed)
	if e != nil || !bytes.Equal(got, plaintext) {
		t.Fatal("history key not selected")
	}
	clear(got)
	changedID := b
	changedID.Ref = "credential_11112233445566778899aabbccddeeff"
	changedPurpose := b
	changedPurpose.Purpose = secretstore.ProxyEntry
	for _, tc := range []struct {
		name   string
		b      secretstore.Binding
		sealed secretstore.Sealed
	}{
		{"wrong id", changedID, sealed}, {"wrong purpose", changedPurpose, sealed}, {"unknown key", b, secretstore.Sealed{KeyID: "missing", Ciphertext: sealed.Ciphertext}},
		{"key substitution", b, secretstore.Sealed{KeyID: "v2", Ciphertext: sealed.Ciphertext}}, {"empty", b, secretstore.Sealed{KeyID: "v1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, e := c.Open(ctx, tc.b, tc.sealed)
			if p != nil || !errors.Is(e, secretstore.ErrDecrypt) {
				t.Fatalf("not fail closed: %v", e)
			}
		})
	}
	for i := range sealed.Ciphertext {
		corrupt := secretstore.Sealed{KeyID: sealed.KeyID, Ciphertext: bytes.Clone(sealed.Ciphertext)}
		corrupt.Ciphertext[i] ^= 1
		p, e := c.Open(ctx, b, corrupt)
		if p != nil || !errors.Is(e, secretstore.ErrDecrypt) {
			t.Fatalf("corrupt byte %d accepted", i)
		}
	}
	for _, bad := range []secretstore.Binding{{}, {Ref: b.Ref, Purpose: "DNS_API"}, {Ref: "bad", Purpose: b.Purpose}} {
		p, e := c.Open(ctx, bad, sealed)
		if p != nil || !errors.Is(e, secretstore.ErrInvalidBinding) {
			t.Fatal("invalid binding accepted")
		}
		if _, e := c.Seal(ctx, bad, plaintext); !errors.Is(e, secretstore.ErrInvalidBinding) {
			t.Fatal("invalid seal accepted")
		}
	}
	// Even a copied key under a different ID cannot authenticate a substituted KeyID.
	alias, e := New(Options{CurrentKeyID: "alias", Keys: map[string][]byte{"alias": bytes.Repeat([]byte{1}, 32)}})
	if e != nil {
		t.Fatal(e)
	}
	sealed.KeyID = "alias"
	if p, e := alias.Open(ctx, b, sealed); p != nil || !errors.Is(e, secretstore.ErrDecrypt) {
		t.Fatal("key ID not authenticated")
	}
}
func TestKeySourcesFailClosed(t *testing.T) {
	for _, opts := range []Options{{}, {CurrentKeyID: "missing", Keys: map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)}}, {CurrentKeyID: "v1", Keys: map[string][]byte{"v1": []byte("short")}}} {
		if c, e := New(opts); c != nil || !errors.Is(e, secretstore.ErrKeyUnavailable) {
			t.Fatal("invalid keys accepted")
		}
	}
	dir := t.TempDir()
	if e := os.Chmod(dir, 0700); e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(dir, "master.key")
	key := bytes.Repeat([]byte{9}, 32)
	if e := os.WriteFile(path, key, 0600); e != nil {
		t.Fatal(e)
	}
	c, e := New(Options{CurrentKeyID: "local-v1", KeyFiles: map[string]string{"local-v1": path}})
	if e != nil {
		t.Fatal(e)
	}
	sealed, e := c.Seal(context.Background(), binding(), []byte("secret"))
	if e != nil {
		t.Fatal(e)
	}
	if e := os.Remove(path); e != nil {
		t.Fatal(e)
	}
	if _, e := New(Options{CurrentKeyID: "local-v1", KeyFiles: map[string]string{"local-v1": path}}); !errors.Is(e, secretstore.ErrKeyUnavailable) {
		t.Fatal("missing key accepted")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("missing key recreated")
	}
	if e := os.WriteFile(path, key, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := New(Options{CurrentKeyID: "v1", KeyFiles: map[string]string{"v1": path}}); !errors.Is(e, secretstore.ErrKeyUnavailable) {
		t.Fatal("permissive file accepted")
	}
	os.Chmod(path, 0600)
	os.Chmod(dir, 0755)
	if _, e := New(Options{CurrentKeyID: "v1", KeyFiles: map[string]string{"v1": path}}); !errors.Is(e, secretstore.ErrKeyUnavailable) {
		t.Fatal("permissive directory accepted")
	}
	os.Chmod(dir, 0700)
	link := filepath.Join(dir, "link")
	if e := os.Symlink(path, link); e != nil {
		t.Fatal(e)
	}
	if _, e := New(Options{CurrentKeyID: "v1", KeyFiles: map[string]string{"v1": link}}); !errors.Is(e, secretstore.ErrKeyUnavailable) {
		t.Fatal("symlink accepted")
	}
	os.WriteFile(path, []byte("truncated"), 0600)
	if _, e := New(Options{CurrentKeyID: "v1", KeyFiles: map[string]string{"v1": path}}); !errors.Is(e, secretstore.ErrKeyUnavailable) {
		t.Fatal("truncated key accepted")
	}
	if p, e := testCipher(t, "v2").Open(context.Background(), binding(), sealed); p != nil || !errors.Is(e, secretstore.ErrDecrypt) {
		t.Fatal("lost key did not fail closed")
	}
	c, e = New(Options{CurrentKeyID: "v1", Keys: map[string][]byte{"v1": key}, Random: strings.NewReader("")})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Seal(context.Background(), binding(), []byte("secret")); !errors.Is(e, secretstore.ErrKeyUnavailable) {
		t.Fatal("nonce failure ignored")
	}
}
func TestCipherConcurrentUse(t *testing.T) {
	c := testCipher(t, "v1")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := binding()
			s, e := c.Seal(context.Background(), b, []byte("secret"))
			if e != nil {
				t.Error(e)
				return
			}
			p, e := c.Open(context.Background(), b, s)
			if e != nil || string(p) != "secret" {
				t.Error("concurrent cipher failure")
			}
			clear(p)
		}()
	}
	wg.Wait()
}
