// Package pki implements the separate management CA trust domain.
package pki

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
)

var ErrIdentity = errors.New("management identity unavailable")

type Authority struct {
	Certificate *x509.Certificate
	key         ed25519.PrivateKey
	PEM         []byte
}

func serial() (*big.Int, error) { return rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128)) }
func GenerateCA(now time.Time) (*Authority, []byte, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, ErrIdentity
	}
	s, err := serial()
	if err != nil {
		return nil, nil, ErrIdentity
	}
	t := &x509.Certificate{SerialNumber: s, Subject: pkix.Name{CommonName: "Meshstrata management CA"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, t, t, pub, key)
	if err != nil {
		return nil, nil, ErrIdentity
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, ErrIdentity
	}
	a, err := LoadCA(certPEM, raw, now)
	return a, raw, err
}
func ParseCertificate(data []byte) (*x509.Certificate, error) {
	b, rest := pem.Decode(data)
	if b == nil || b.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, ErrIdentity
	}
	c, e := x509.ParseCertificate(b.Bytes)
	if e != nil {
		return nil, ErrIdentity
	}
	return c, nil
}
func LoadCA(certPEM, keyDER []byte, now time.Time) (*Authority, error) {
	c, e := ParseCertificate(certPEM)
	if e != nil || !c.IsCA || !c.BasicConstraintsValid || c.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(c.NotBefore) || !now.Before(c.NotAfter) || c.CheckSignatureFrom(c) != nil {
		return nil, ErrIdentity
	}
	k, e := x509.ParsePKCS8PrivateKey(keyDER)
	if e != nil {
		return nil, ErrIdentity
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok {
		return nil, ErrIdentity
	}
	pub, ok := c.PublicKey.(ed25519.PublicKey)
	if !ok || !bytes.Equal(pub, key.Public().(ed25519.PublicKey)) {
		return nil, ErrIdentity
	}
	return &Authority{c, append(ed25519.PrivateKey(nil), key...), append([]byte(nil), certPEM...)}, nil
}
func NewKeyCSR() ([]byte, []byte, error) {
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return nil, nil, ErrIdentity
	}
	raw, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		return nil, nil, ErrIdentity
	}
	csr, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{}, key)
	if e != nil {
		return nil, nil, ErrIdentity
	}
	return raw, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csr}), nil
}
func ParseCSR(data []byte) (*x509.CertificateRequest, error) {
	b, rest := pem.Decode(data)
	if b == nil || b.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 || len(b.Bytes) > 8192 {
		return nil, ErrIdentity
	}
	c, e := x509.ParseCertificateRequest(b.Bytes)
	if e != nil || c.CheckSignature() != nil {
		return nil, ErrIdentity
	}
	if _, ok := c.PublicKey.(ed25519.PublicKey); !ok {
		return nil, ErrIdentity
	}
	return c, nil
}
func CheckCSRKey(csrPEM, keyDER []byte) error {
	c, e := ParseCSR(csrPEM)
	if e != nil {
		return e
	}
	k, e := x509.ParsePKCS8PrivateKey(keyDER)
	if e != nil {
		return ErrIdentity
	}
	key, ok := k.(ed25519.PrivateKey)
	if !ok || !bytes.Equal(c.PublicKey.(ed25519.PublicKey), key.Public().(ed25519.PublicKey)) {
		return ErrIdentity
	}
	return nil
}
func Fingerprint(c *x509.Certificate) string {
	h := sha256.Sum256(c.Raw)
	return hex.EncodeToString(h[:])
}
func CSRHash(csr *x509.CertificateRequest) string {
	h := sha256.Sum256(csr.Raw)
	return hex.EncodeToString(h[:])
}
func (a *Authority) SignNode(csrPEM []byte, id domain.ID, now time.Time, lifetime time.Duration) ([]byte, error) {
	if !id.Valid() || !strings.HasPrefix(string(id), "node_") {
		return nil, ErrIdentity
	}
	csr, e := ParseCSR(csrPEM)
	if e != nil {
		return nil, e
	}
	s, e := serial()
	if e != nil {
		return nil, ErrIdentity
	}
	u := &url.URL{Scheme: "meshstrata", Host: "node", Path: "/" + string(id)}
	t := &x509.Certificate{SerialNumber: s, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(lifetime), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, URIs: []*url.URL{u}}
	if lifetime <= 0 || t.NotAfter.After(a.Certificate.NotAfter) {
		return nil, ErrIdentity
	}
	der, e := x509.CreateCertificate(rand.Reader, t, a.Certificate, csr.PublicKey, a.key)
	if e != nil {
		return nil, ErrIdentity
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
func NodeID(c *x509.Certificate) (domain.ID, error) {
	if c == nil || c.IsCA || len(c.URIs) != 1 || len(c.DNSNames) != 0 || len(c.IPAddresses) != 0 || len(c.ExtKeyUsage) != 1 || c.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth || c.KeyUsage != x509.KeyUsageDigitalSignature {
		return "", ErrIdentity
	}
	u := c.URIs[0]
	id := domain.ID(strings.TrimPrefix(u.Path, "/"))
	if u.Scheme != "meshstrata" || u.Host != "node" || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Opaque != "" || !id.Valid() || !strings.HasPrefix(string(id), "node_") || u.Path != "/"+string(id) {
		return "", ErrIdentity
	}
	return id, nil
}
func (a *Authority) ServerCertificate(hosts []string, now time.Time) (tls.Certificate, error) {
	if len(hosts) == 0 {
		return tls.Certificate{}, ErrIdentity
	}
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return tls.Certificate{}, ErrIdentity
	}
	s, e := serial()
	if e != nil {
		return tls.Certificate{}, ErrIdentity
	}
	until := now.Add(24 * time.Hour)
	if until.After(a.Certificate.NotAfter) {
		until = a.Certificate.NotAfter
	}
	t := &x509.Certificate{SerialNumber: s, NotBefore: now.Add(-time.Minute), NotAfter: until, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	for _, h := range hosts {
		if h == "" || strings.ContainsAny(h, "/: ") && net.ParseIP(h) == nil {
			return tls.Certificate{}, ErrIdentity
		}
		if ip := net.ParseIP(h); ip != nil {
			t.IPAddresses = append(t.IPAddresses, ip)
		} else {
			t.DNSNames = append(t.DNSNames, h)
		}
	}
	der, e := x509.CreateCertificate(rand.Reader, t, a.Certificate, pub, a.key)
	if e != nil {
		return tls.Certificate{}, ErrIdentity
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
}

// Trust requires a supplied management CA. An optional out-of-band SHA256 pin
// authenticates that CA; normal Go chain and SAN validation always remain enabled.
func Trust(caPEM []byte, pin, serverName string) (*tls.Config, error) {
	ca, e := ParseCertificate(caPEM)
	if e != nil || !ca.IsCA || ca.CheckSignatureFrom(ca) != nil || serverName == "" || (pin != "" && pin != Fingerprint(ca)) {
		return nil, ErrIdentity
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool, ServerName: serverName, NextProtos: []string{"http/1.1"}}, nil
}
func ClientCertificate(certPEM, keyDER, caPEM []byte, id domain.ID, now time.Time) (tls.Certificate, error) {
	cert, e := ParseCertificate(certPEM)
	if e != nil {
		return tls.Certificate{}, e
	}
	if !now.Before(cert.NotAfter) || now.Before(cert.NotBefore) {
		return tls.Certificate{}, ErrIdentity
	}
	actual, e := NodeID(cert)
	if e != nil || actual != id {
		return tls.Certificate{}, ErrIdentity
	}
	ca, e := ParseCertificate(caPEM)
	if e != nil {
		return tls.Certificate{}, e
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, e = cert.Verify(x509.VerifyOptions{Roots: pool, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); e != nil {
		return tls.Certificate{}, ErrIdentity
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	defer clear(keyPEM)
	pair, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil {
		return tls.Certificate{}, ErrIdentity
	}
	return pair, nil
}
