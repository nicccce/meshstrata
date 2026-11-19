package authorization

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"path/filepath"
	"strings"

	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

var ErrMaterial = errors.New("AGENT_LOCAL_MATERIAL_UNAVAILABLE")

type Material struct {
	Ref control.LocalMaterial
	// Path is provisioned locally and relative to the private config directory.
	Path string
	// Value is the local REALITY private key. It is never written by the resolver.
	Value string
}

type Resolver struct {
	node      domain.ID
	root      string
	materials map[control.LocalMaterial]Material
}

func NewResolver(node domain.ID, configDir string, materials []Material) (*Resolver, error) {
	d, err := checkpoint.OpenPrivateDir(configDir)
	if err != nil || !node.Valid() {
		if d != nil {
			d.Close()
		}
		return nil, ErrMaterial
	}
	d.Close()
	r := &Resolver{node: node, root: configDir, materials: map[control.LocalMaterial]Material{}}
	for _, item := range materials {
		if _, exists := r.materials[item.Ref]; exists || !item.Ref.ID.Valid() {
			return nil, ErrMaterial
		}
		switch item.Ref.Purpose {
		case "tls_certificate", "tls_private_key":
			if item.Value != "" || !filepath.IsLocal(item.Path) || filepath.Clean(item.Path) != item.Path || item.Path == "." {
				return nil, ErrMaterial
			}
			// Keep certificate inputs separate from generated configs and the
			// socket. Only explicit files under a dedicated private subtree.
			if !strings.HasPrefix(item.Path, "materials"+string(filepath.Separator)) {
				return nil, ErrMaterial
			}
		case "reality_private_key":
			key, err := base64.RawURLEncoding.DecodeString(item.Value)
			if item.Path != "" || err != nil || len(key) != 32 {
				return nil, ErrMaterial
			}
		default:
			return nil, ErrMaterial
		}
		r.materials[item.Ref] = item
	}
	return r, nil
}

func (r *Resolver) Resolve(ctx context.Context, node domain.ID, ref control.LocalMaterial) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m, ok := r.materials[ref]
	if node != r.node || !ok {
		return "", ErrMaterial
	}
	if ref.Purpose == "reality_private_key" {
		return m.Value, nil
	}
	path := filepath.Join(r.root, m.Path)
	for parent := filepath.Dir(path); parent != r.root; parent = filepath.Dir(parent) {
		d, err := checkpoint.OpenPrivateDir(parent)
		if err != nil {
			return "", ErrMaterial
		}
		d.Close()
	}
	d, err := checkpoint.OpenPrivateDir(filepath.Dir(path))
	if err != nil {
		return "", ErrMaterial
	}
	defer d.Close()
	b, err := d.Read(filepath.Base(path), 256<<10)
	if err != nil {
		return "", ErrMaterial
	}
	block, _ := pem.Decode(b)
	if block == nil || (ref.Purpose == "tls_certificate" && block.Type != "CERTIFICATE") {
		return "", ErrMaterial
	}
	if ref.Purpose == "tls_private_key" {
		// Parse the key when rendering with its paired certificate. Reject
		// non-key files here, including management material under other paths.
		switch block.Type {
		case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY":
		default:
			return "", ErrMaterial
		}
	}
	return path, nil
}

func CheckTLS(cert, key string) error {
	if _, err := tls.LoadX509KeyPair(cert, key); err != nil {
		return ErrMaterial
	}
	return nil
}

var _ control.MaterialResolver = (*Resolver)(nil)

// CheckTLSLeaf binds the locally provisioned leaf to the reviewed public pin.
// It retains CheckTLS's private-file and key-pair checks before hashing the leaf.
func CheckTLSLeaf(cert, key string, pin control.TLSLeafSHA256) error {
	if err := CheckTLS(cert, key); err != nil {
		return err
	}
	if pin == "" {
		return nil
	}
	dir, err := checkpoint.OpenPrivateDir(filepath.Dir(cert))
	if err != nil {
		return ErrMaterial
	}
	data, err := dir.Read(filepath.Base(cert), 256<<10)
	dir.Close()
	if err != nil {
		return ErrMaterial
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return ErrMaterial
	}
	digest := sha256.Sum256(block.Bytes)
	if !pin.Valid() || hex.EncodeToString(digest[:]) != string(pin) {
		return ErrMaterial
	}
	return nil
}
