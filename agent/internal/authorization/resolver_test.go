package authorization

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func TestScopedMaterialRegistry(t *testing.T) {
	const node domain.ID = "node_00000000000000000000000000000001"
	const foreign domain.ID = "node_00000000000000000000000000000002"
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	materials := filepath.Join(dir, "materials")
	if err := os.Mkdir(materials, 0700); err != nil {
		t.Fatal(err)
	}
	cert := control.LocalMaterial{ID: "cert_00000000000000000000000000000001", Purpose: "tls_certificate"}
	reality := control.LocalMaterial{ID: "key_00000000000000000000000000000001", Purpose: "reality_private_key"}
	v := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	resolver, err := NewResolver(node, dir, []Material{{Ref: cert, Path: "materials/cert.pem"}, {Ref: reality, Value: v}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if got, err := resolver.Resolve(ctx, node, reality); err != nil || got != v {
		t.Fatal("memory-only local key", err)
	}
	if _, err := resolver.Resolve(ctx, foreign, reality); !errors.Is(err, ErrMaterial) {
		t.Fatal("cross-node key access", err)
	}
	wrong := reality
	wrong.Purpose = "tls_private_key"
	if _, err := resolver.Resolve(ctx, node, wrong); !errors.Is(err, ErrMaterial) {
		t.Fatal("cross-purpose key access", err)
	}
	if _, err := NewResolver(node, dir, []Material{{Ref: cert, Path: "materials/../device-key.pem"}}); !errors.Is(err, ErrMaterial) {
		t.Fatal("path traversal", err)
	}
	data := []byte("-----BEGIN CERTIFICATE-----\nAA==\n-----END CERTIFICATE-----\n")
	alias := filepath.Join(materials, "cert.pem")
	path := filepath.Join(materials, "original.pem")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(ctx, node, cert); !errors.Is(err, ErrMaterial) {
		t.Fatal("symlink material", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, alias); err != nil {
		t.Fatal(err)
	}
	if got, err := resolver.Resolve(ctx, node, cert); err != nil || got != alias {
		t.Fatal("registered private TLS path", err)
	}
	if err := os.Chmod(alias, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(ctx, node, cert); !errors.Is(err, ErrMaterial) {
		t.Fatal("public certificate path", err)
	}
}
