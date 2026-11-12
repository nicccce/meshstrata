package enrollment

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/pki"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func node() domain.ID { return domain.ID("node_" + strings.Repeat("1", 32)) }
func agentTestServer(t *testing.T, ca *pki.Authority, hosts []string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	cert, e := ca.ServerCertificate(hosts, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Token string `json:"token"`
			CSR   string `json:"csr_pem"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			w.WriteHeader(400)
			return
		}
		cert, e := ca.SignNode([]byte(req.CSR), node(), time.Now(), time.Hour)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"node_id": node(), "certificate_pem": string(cert), "ca_pem": string(ca.PEM)})
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}
func TestTrustBeforeTokenLocalPermissionsAndRestart(t *testing.T) {
	ca, raw, e := pki.GenerateCA(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	clear(raw)
	dir := filepath.Join(t.TempDir(), "identity")
	id, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	original := append([]byte(nil), id.data.KeyDER...)
	defer clear(original)
	var calls atomic.Int32
	valid := agentTestServer(t, ca, []string{"127.0.0.1"}, &calls)
	c, e := New(id, Options{ControllerURL: valid.URL, CAPEM: ca.PEM, CAFingerprint: pki.Fingerprint(ca.Certificate), NodeID: node()})
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Enroll(context.Background(), strings.Repeat("a", 43)); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("request missing")
	}
	if _, e = id.Certificate(time.Now()); e != nil {
		t.Fatal(e)
	}
	reopened, e := Open(dir)
	if e != nil || !bytes.Equal(reopened.data.KeyDER, original) || reopened.NodeID() != node() {
		t.Fatal("restart identity changed", e)
	}
	for _, p := range []string{dir, filepath.Join(dir, "management-identity.json")} {
		stat, e := os.Stat(p)
		if e != nil || stat.Mode().Perm()&0077 != 0 {
			t.Fatal("permissions", e)
		}
	}
	rogue, key, e := pki.GenerateCA(time.Now())
	if e != nil {
		t.Fatal(e)
	}
	clear(key)
	var rogueCalls atomic.Int32
	evil := agentTestServer(t, rogue, []string{"127.0.0.1"}, &rogueCalls)
	bad, e := New(reopened, Options{ControllerURL: evil.URL, CAPEM: ca.PEM, NodeID: node()})
	if e != nil {
		t.Fatal(e)
	}
	if bad.Enroll(context.Background(), strings.Repeat("a", 43)) == nil || rogueCalls.Load() != 0 {
		t.Fatal("token sent to rogue CA")
	}
	var wrongCalls atomic.Int32
	wrong := agentTestServer(t, ca, []string{"wrong.invalid"}, &wrongCalls)
	bad, e = New(reopened, Options{ControllerURL: wrong.URL, CAPEM: ca.PEM, NodeID: node()})
	if e != nil {
		t.Fatal(e)
	}
	if bad.Enroll(context.Background(), strings.Repeat("a", 43)) == nil || wrongCalls.Load() != 0 {
		t.Fatal("token sent before SAN validation")
	}
	path := filepath.Join(dir, "management-identity.json")
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(dir); e == nil {
		t.Fatal("insecure key accepted")
	}
	os.Chmod(path, 0600)
	if e = os.WriteFile(path, []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Open(dir); e == nil {
		t.Fatal("corrupt key regenerated")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "corrupt" {
		t.Fatal("corrupt identity overwritten")
	}
}
func TestSymlinkIdentityRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "elsewhere")
	os.WriteFile(target, []byte("preserve"), 0600)
	private := filepath.Join(dir, "private")
	os.Mkdir(private, 0700)
	os.Symlink(target, filepath.Join(private, "management-identity.json"))
	if _, e := Open(private); e == nil {
		t.Fatal("symlink accepted")
	}
	b, _ := os.ReadFile(target)
	if string(b) != "preserve" {
		t.Fatal("target overwritten")
	}
}
