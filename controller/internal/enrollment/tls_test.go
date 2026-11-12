package enrollment

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/nicccce/meshstrata/internal/pki"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRealTLSEnrollmentRenewAndRouteSeparation(t *testing.T) {
	f := setup(t)
	m := issue(t, f, "tls")
	key, csr := csr(t)
	server := httptest.NewUnstartedServer(f.s.NodeHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })))
	config := f.s.TLSConfig()
	pair, e := config.GetCertificate(&tls.ClientHelloInfo{})
	if e != nil {
		t.Fatal(e)
	}
	config.Certificates = []tls.Certificate{*pair}
	server.TLS = config
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.StartTLS()
	defer server.Close()
	trust, e := pki.Trust(f.s.CAPEM(), m.CAFingerprint, "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	trust.Time = f.s.options.Now
	transport := &http.Transport{TLSClientConfig: trust}
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, Timeout: time.Second * 3}
	request := func(path string, payload any) (*http.Response, error) {
		raw, _ := json.Marshal(payload)
		return client.Post(server.URL+path, "application/json", bytes.NewReader(raw))
	}
	resp, e := request("/enroll/v1", map[string]string{"token": m.Token, "csr_pem": string(csr)})
	if e != nil {
		t.Fatal(e)
	}
	var identity Identity
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&identity) != nil {
		t.Fatal("enrollment failed", resp.StatusCode)
	}
	resp.Body.Close()
	resp, e = client.Get(server.URL + "/control/v1")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("no mTLS control accepted", resp.StatusCode)
	}
	resp, e = request("/enroll/v1/renew", map[string]string{"csr_pem": string(csr)})
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("no mTLS renewal accepted")
	}
	cert, e := pki.ClientCertificate([]byte(identity.CertificatePEM), key, f.s.CAPEM(), identity.NodeID, f.s.options.Now())
	if e != nil {
		t.Fatal(e)
	}
	// A valid client may send its full chain, including the trusted root.
	cert.Certificate = append(cert.Certificate, f.s.ca.Certificate.Raw)
	trust = trust.Clone()
	trust.Certificates = []tls.Certificate{cert}
	transport = &http.Transport{TLSClientConfig: trust}
	defer transport.CloseIdleConnections()
	client.Transport = transport
	resp, e = request("/enroll/v1/renew", map[string]string{"csr_pem": string(csr)})
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("renew", resp.StatusCode)
	}
	resp, e = client.Get(server.URL + "/control/v1")
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatal("valid mTLS rejected", resp.StatusCode)
	}
	if _, e = f.s.Remove(context.Background(), m.NodeID, m.Revision, "tls-remove"); e != nil {
		t.Fatal(e)
	}
	resp, e = client.Get(server.URL + "/control/v1")
	if e == nil {
		resp.Body.Close()
		if resp.StatusCode < 400 {
			t.Fatal("revoked reused connection accepted")
		}
	}
}
