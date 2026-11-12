package mihomo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	core "github.com/nicccce/meshstrata/internal/mihomo"
)

func fixture(t *testing.T) (*Adapter, Candidate) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "kernel")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(binary)
	a, err := New(Options{Binary: binary, BinarySHA256: core.Digest(b), WorkDir: root, StateDir: root, ConfigDir: root, SocketPath: filepath.Join(root, "api.sock"), Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	c, exp, err := core.Generate(nil, core.DNS{Enable: true, EnhancedMode: "normal", Nameserver: []string{"1.1.1.1"}, DefaultNameserver: []string{"1.1.1.1"}}, a.o.SocketPath, []string{"MATCH,REJECT"})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(c)
	p := filepath.Join(root, "candidate.json")
	_ = os.WriteFile(p, data, 0600)
	return a, Candidate{p, core.Digest(data), exp}
}
func TestValidationBoundaries(t *testing.T) {
	a, c := fixture(t)
	if _, err := a.Validate(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Validate(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(c.Path)
	cases := []struct {
		name string
		data []byte
	}{
		{"unknown", []byte(strings.TrimSuffix(string(original), "}") + `,"proxy-providers":{}}`)},
		{"general", []byte(strings.Replace(string(original), `"mixed-port":0`, `"mixed-port":7890`, 1))},
		{"duplicate", []byte(strings.TrimSuffix(string(original), "}") + `,"mixed-port":0}`)},
		{"tun", []byte(strings.Replace(string(original), `"tun":{"enable":false}`, `"tun":{"enable":true}`, 1))},
		{"dns", []byte(strings.Replace(string(original), `"listen":""`, `"listen":"0.0.0.0:53"`, 1))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_ = os.WriteFile(c.Path, tc.data, 0600)
			x := c
			x.Digest = core.Digest(tc.data)
			if _, err := a.Validate(context.Background(), x); err == nil {
				t.Fatal("accepted unsafe config")
			}
		})
	}
	_ = os.WriteFile(c.Path, original, 0600)
	_ = os.Chmod(c.Path, 0644)
	if _, err := a.Validate(context.Background(), c); !errors.Is(err, ErrPath) {
		t.Fatal(err)
	}
	_ = os.Chmod(c.Path, 0600)
	x := c
	x.Path = filepath.Join(a.o.ConfigDir, "missing.json")
	if _, err := a.Validate(context.Background(), x); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
	if _, err := os.Stat(x.Path); !os.IsNotExist(err) {
		t.Fatal("generated missing config")
	}
	x = c
	x.Path = filepath.Join(filepath.Dir(a.o.ConfigDir), "outside.json")
	if _, err := a.Validate(context.Background(), x); !errors.Is(err, ErrPath) {
		t.Fatal(err)
	}
	x = c
	x.Path = filepath.Join(a.o.ConfigDir, "link.json")
	_ = os.Symlink(c.Path, x.Path)
	if _, err := a.Validate(context.Background(), x); !errors.Is(err, ErrPath) {
		t.Fatal(err)
	}
	_ = os.Chmod(a.o.ConfigDir, 0755)
	if _, err := a.Validate(context.Background(), c); !errors.Is(err, ErrPath) {
		t.Fatal(err)
	}
}
func TestTimeoutAndRedaction(t *testing.T) {
	a, c := fixture(t)
	b := []byte("#!/bin/sh\necho PRIVATE_CREDENTIAL >&2\nexec sleep 10\n")
	_ = os.WriteFile(a.o.Binary, b, 0700)
	a.o.BinarySHA256 = core.Digest(b)
	if _, err := a.Validate(context.Background(), c); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
func TestAPIBoundsAndNoVerificationFrom200(t *testing.T) {
	a, c := fixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("x", 32) {
			t.Error("missing secret")
		}
		if r.Method != "PUT" || r.URL.RequestURI() != "/configs?force=true" {
			t.Error("incorrect API")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	a.o.TCPAddress = strings.TrimPrefix(server.URL, "http://")
	a.o.Secret = strings.Repeat("x", 32)
	a.client = server.Client()
	var conf core.Config
	b, _ := os.ReadFile(c.Path)
	_ = json.Unmarshal(b, &conf)
	conf.Controller = a.o.TCPAddress
	conf.Secret = a.o.Secret
	b, _ = json.Marshal(conf)
	_ = os.WriteFile(c.Path, b, 0600)
	c.Digest = core.Digest(b)
	result, err := a.Reload(context.Background(), c)
	if err != nil || !result.APIAccepted {
		t.Fatal(result, err)
	}
	if _, err = a.Verify(context.Background(), c, os.Getpid(), nil); !errors.Is(err, ErrInspection) {
		t.Fatal("wrong process accepted", err)
	}
}
func TestAPIResponseLimit(t *testing.T) {
	a, _ := fixture(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("PRIVATE", 1024))) }))
	defer s.Close()
	a.client = s.Client()
	a.o.TCPAddress = strings.TrimPrefix(s.URL, "http://")
	a.o.MaxResponseBytes = 1024
	if _, err := a.request(context.Background(), "GET", "/version", nil); !errors.Is(err, ErrLimit) {
		t.Fatal(err)
	}
}
func TestEndpointParsing(t *testing.T) {
	for raw, want := range map[string]string{"0100007F:4E20": "127.0.0.1", "00000000000000000000000001000000:4E20": "::1"} {
		e, ok := parseEndpoint(raw, "tcp")
		if !ok || e.Address != want || e.Port != 20000 {
			t.Fatal(raw, e)
		}
	}
}

func TestOptionalTCPAndRestrictedDirectory(t *testing.T) {
	a, _ := fixture(t)
	for _, tc := range []struct{ address, secret string }{{"0.0.0.0:12345", strings.Repeat("x", 32)}, {"localhost:12345", strings.Repeat("x", 32)}, {"127.0.0.1:0", strings.Repeat("x", 32)}, {"127.0.0.1:12345", "short"}} {
		o := a.o
		o.TCPAddress = tc.address
		o.Secret = tc.secret
		if _, err := New(o); !errors.Is(err, ErrOptions) {
			t.Fatal("unsafe TCP option accepted", err)
		}
	}
	o := a.o
	o.TCPAddress = "127.0.0.1:12345"
	o.Secret = strings.Repeat("x", 32)
	if _, err := New(o); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(a.o.ConfigDir, 0755)
	if _, err := New(a.o); !errors.Is(err, ErrPath) {
		t.Fatal("public Unix parent accepted", err)
	}
}
func TestAPIErrorRedactionAndCancellation(t *testing.T) {
	a, _ := fixture(t)
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		http.Error(w, "PRIVATE_CREDENTIAL", 400)
	}))
	defer s.Close()
	a.client = s.Client()
	a.o.TCPAddress = strings.TrimPrefix(s.URL, "http://")
	if _, err := a.request(context.Background(), "GET", "/error", nil); !errors.Is(err, ErrAPI) || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatal("unsafe API error", err)
	}
	if _, err := a.request(context.Background(), "GET", "/slow", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("unbounded API", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.request(ctx, "GET", "/error", nil); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
