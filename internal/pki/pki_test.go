package pki

import (
	"crypto/x509"
	"github.com/nicccce/meshstrata/internal/domain"
	"strings"
	"testing"
	"time"
)

func TestPurposesKeysSANAndPin(t *testing.T) {
	now := time.Now()
	ca, key, e := GenerateCA(now)
	if e != nil {
		t.Fatal(e)
	}
	defer clear(key)
	raw, csr, e := NewKeyCSR()
	if e != nil {
		t.Fatal(e)
	}
	other, _, _ := NewKeyCSR()
	if CheckCSRKey(csr, other) == nil {
		t.Fatal("mismatched CSR key")
	}
	id := domain.ID("node_" + strings.Repeat("1", 32))
	cert, e := ca.SignNode(csr, id, now, time.Hour)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = ClientCertificate(cert, raw, ca.PEM, id, now); e != nil {
		t.Fatal(e)
	}
	if _, e = ClientCertificate(cert, other, ca.PEM, id, now); e == nil {
		t.Fatal("mismatch")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.Certificate)
	c, _ := ParseCertificate(cert)
	if _, e = c.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); e == nil {
		t.Fatal("client used as server")
	}
	server, e := ca.ServerCertificate([]string{"controller.test", "127.0.0.1"}, now)
	if e != nil {
		t.Fatal(e)
	}
	s, e := x509.ParseCertificate(server.Certificate[0])
	if e != nil {
		t.Fatal(e)
	}
	if s.VerifyHostname("controller.test") != nil || s.VerifyHostname("127.0.0.1") != nil || s.VerifyHostname("evil.test") == nil {
		t.Fatal("SAN")
	}
	if _, e = NodeID(s); e == nil {
		t.Fatal("server used as client")
	}
	if _, e = Trust(ca.PEM, strings.Repeat("0", 64), "controller.test"); e == nil {
		t.Fatal("pin ignored")
	}
	if _, e = LoadCA(ca.PEM, other, now); e == nil {
		t.Fatal("wrong CA key")
	}
}
