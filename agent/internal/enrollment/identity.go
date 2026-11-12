// Package enrollment stores the Agent's private management key locally.
package enrollment

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
)

var ErrIdentity = errors.New("local management identity unavailable")

type identityData struct {
	KeyDER         []byte    `json:"key_der"`
	CSRPEM         []byte    `json:"csr_pem"`
	CertificatePEM []byte    `json:"certificate_pem"`
	CAPEM          []byte    `json:"ca_pem"`
	NodeID         domain.ID `json:"node_id"`
}
type Identity struct {
	mu   sync.Mutex
	path string
	data identityData
}

func privateDir(path string) error {
	abs, e := filepath.Abs(path)
	if e != nil {
		return ErrIdentity
	}
	for part := abs; ; part = filepath.Dir(part) {
		info, e := os.Lstat(part)
		if e == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrIdentity
			}
		} else if !os.IsNotExist(e) {
			return ErrIdentity
		}
		if filepath.Dir(part) == part {
			break
		}
	}
	if e = os.MkdirAll(abs, 0700); e != nil {
		return ErrIdentity
	}
	if e = os.Chmod(abs, 0700); e != nil {
		return ErrIdentity
	}
	return nil
}

// Open never replaces malformed, insecure or undecryptable identity files. The
// component's existing lifecycle lock must cover all users of this directory.
func Open(dir string) (*Identity, error) {
	if e := privateDir(dir); e != nil {
		return nil, e
	}
	path := filepath.Join(dir, "management-identity.json")
	id := &Identity{path: path}
	data, e := readPrivate(path)
	if os.IsNotExist(e) {
		if e = createIdentityFile(dir, path); e != nil {
			return nil, e
		}
		data, e = readPrivate(path)
	}
	if e != nil {
		return nil, ErrIdentity
	}
	defer clear(data)
	if json.Unmarshal(data, &id.data) != nil || pki.CheckCSRKey(id.data.CSRPEM, id.data.KeyDER) != nil {
		return nil, ErrIdentity
	}
	if len(id.data.CertificatePEM) > 0 {
		cert, e := pki.ParseCertificate(id.data.CertificatePEM)
		if e != nil {
			return nil, ErrIdentity
		}
		if _, e = pki.ClientCertificate(id.data.CertificatePEM, id.data.KeyDER, id.data.CAPEM, id.data.NodeID, cert.NotBefore.Add(time.Second)); e != nil {
			return nil, ErrIdentity
		}
		actual, e := pki.NodeID(cert)
		if e != nil || actual != id.data.NodeID {
			return nil, ErrIdentity
		}
	}
	return id, nil
}
func readPrivate(path string) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 32<<10 {
		return nil, ErrIdentity
	}
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	actual, e := f.Stat()
	if e != nil || !os.SameFile(info, actual) {
		return nil, ErrIdentity
	}
	raw := make([]byte, info.Size())
	n, e := f.ReadAt(raw, 0)
	if e != nil || n != len(raw) {
		clear(raw)
		return nil, ErrIdentity
	}
	return raw, nil
}
func syncDir(dir string) error {
	f, e := os.Open(dir)
	if e != nil {
		return ErrIdentity
	}
	defer f.Close()
	if f.Sync() != nil {
		return ErrIdentity
	}
	return nil
}
func (i *Identity) save(data identityData) error {
	raw, e := json.Marshal(data)
	if e != nil {
		return ErrIdentity
	}
	defer clear(raw)
	existing, readErr := readPrivate(i.path)
	clear(existing)
	if readErr != nil {
		return ErrIdentity
	}
	tmp, e := os.CreateTemp(filepath.Dir(i.path), ".identity-*")
	if e != nil {
		return ErrIdentity
	}
	defer os.Remove(tmp.Name())
	if _, e = tmp.Write(raw); e != nil {
		tmp.Close()
		return ErrIdentity
	}
	if e = tmp.Sync(); e != nil {
		tmp.Close()
		return ErrIdentity
	}
	if e = tmp.Close(); e != nil {
		return ErrIdentity
	}
	if e = os.Rename(tmp.Name(), i.path); e != nil {
		return ErrIdentity
	}
	if e = syncDir(filepath.Dir(i.path)); e != nil {
		return e
	}
	i.data = data
	return nil
}
func (i *Identity) NodeID() domain.ID { i.mu.Lock(); defer i.mu.Unlock(); return i.data.NodeID }
func (i *Identity) Certificate(now time.Time) (tls.Certificate, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return pki.ClientCertificate(i.data.CertificatePEM, i.data.KeyDER, i.data.CAPEM, i.data.NodeID, now)
}
func (i *Identity) NeedsRenewal(now time.Time, before time.Duration) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	c, e := pki.ParseCertificate(i.data.CertificatePEM)
	return e != nil || c.NotAfter.Sub(now) <= before
}

func createIdentityFile(dir, path string) error {
	key, csr, e := pki.NewKeyCSR()
	if e != nil {
		return ErrIdentity
	}
	defer clear(key)
	raw, e := json.Marshal(identityData{KeyDER: key, CSRPEM: csr})
	if e != nil {
		return ErrIdentity
	}
	defer clear(raw)
	tmp, e := os.CreateTemp(dir, ".identity-*")
	if e != nil {
		return ErrIdentity
	}
	defer os.Remove(tmp.Name())
	if _, e = tmp.Write(raw); e != nil {
		tmp.Close()
		return ErrIdentity
	}
	if e = tmp.Sync(); e != nil {
		tmp.Close()
		return ErrIdentity
	}
	if e = tmp.Close(); e != nil {
		return ErrIdentity
	}
	if e = os.Link(tmp.Name(), path); e != nil && !os.IsExist(e) {
		return ErrIdentity
	}
	return syncDir(dir)
}
