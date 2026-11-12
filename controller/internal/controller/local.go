package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/pki"
	"github.com/nicccce/meshstrata/internal/privatefile"
)

// PrepareLocal creates a NEW private provisioning directory only. Existing
// material is never regenerated. It supplies a short-lived local HTTPS identity
// for explicit trust testing; imported production TLS files remain configurable.
func PrepareLocal(ctx context.Context, cfg config.Controller, _ io.Reader) error {
	if cfg.Secure == nil {
		return errors.New("secure configuration required")
	}
	s := cfg.Secure
	keyPath := s.KeyFiles[s.CurrentKeyID]
	dir := filepath.Dir(keyPath)
	if !filepath.IsAbs(dir) || keyPath != filepath.Join(dir, "master.key") || s.BootstrapFile != filepath.Join(dir, "bootstrap.json") || s.TLSCertFile != filepath.Join(dir, "web-cert.pem") || s.TLSKeyFile != filepath.Join(dir, "web-key.pem") || s.WebCAFile != filepath.Join(dir, "web-ca.pem") {
		return errors.New("prepare-local requires canonical files in one new absolute private directory")
	}
	if e := os.Mkdir(dir, 0700); e != nil {
		return errors.New("local provisioning directory must be new")
	}
	write := func(name string, data []byte) error {
		f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(data)
		if e == nil {
			e = f.Sync()
		}
		ce := f.Close()
		if e != nil {
			return e
		}
		return ce
	}
	key := make([]byte, 32)
	if _, e := rand.Read(key); e != nil {
		return e
	}
	defer clear(key)
	if e := write("master.key", key); e != nil {
		return e
	}
	entropy := make([]byte, 32)
	if _, e := rand.Read(entropy); e != nil {
		return e
	}
	defer clear(entropy)
	proof := []byte(base64.RawURLEncoding.EncodeToString(entropy))
	defer clear(proof)
	hash := sha256.Sum256(proof)
	b, _ := json.Marshal(bootstrapMaterial{SHA256: hex.EncodeToString(hash[:]), ExpiresAt: time.Now().Add(30 * time.Minute)})
	if e := write("bootstrap.json", b); e != nil {
		return e
	}
	if e := write("bootstrap-proof", proof); e != nil {
		return e
	}
	ca, raw, e := pki.GenerateCA(time.Now())
	if e != nil {
		return e
	}
	clear(raw)
	cert, e := ca.ServerCertificate(s.ServerNames, time.Now())
	if e != nil {
		return e
	}
	der, e := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if e != nil {
		return e
	}
	defer clear(der)
	if e = write("web-ca.pem", ca.PEM); e != nil {
		return e
	}
	if e = write("web-cert.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})); e != nil {
		return e
	}
	if e = write("web-key.pem", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})); e != nil {
		return e
	}
	d, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

// InitializeAdmin uses verified HTTPS and stdin JSON {username,password}. Neither
// proof nor password is placed in argv or printed. No local database bypass.
func InitializeAdmin(ctx context.Context, cfg config.Controller, input io.Reader) error {
	if cfg.Secure == nil || cfg.Secure.BootstrapFile == "" || len(cfg.Secure.AllowedOrigins) == 0 {
		return errors.New("bootstrap configuration required")
	}
	s := cfg.Secure
	proof, e := privatefile.Read(filepath.Join(filepath.Dir(s.BootstrapFile), "bootstrap-proof"), 128)
	if e != nil {
		return e
	}
	defer clear(proof)
	ca, e := privatefile.Read(s.WebCAFile, 32<<10)
	if e != nil {
		return e
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return errors.New("web CA unavailable")
	}
	raw, e := io.ReadAll(io.LimitReader(input, 4097))
	if e != nil || len(raw) > 4096 {
		return errors.New("invalid administrator input")
	}
	defer clear(raw)
	req, e := http.NewRequestWithContext(ctx, "POST", s.AllowedOrigins[0]+"/api/v1/auth/initialize", bytes.NewReader(raw))
	if e != nil || req.URL.Scheme != "https" || req.URL.User != nil || req.URL.RawQuery != "" || req.URL.Fragment != "" {
		return errors.New("invalid bootstrap endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", s.AllowedOrigins[0])
	req.Header.Set("X-Meshstrata-Bootstrap", string(proof))
	transport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return errors.New("administrator HTTPS initialization failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 {
		return errors.New("administrator initialization rejected")
	}
	return nil
}
