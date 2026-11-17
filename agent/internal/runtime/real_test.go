package runtime

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
)

func TestRealMihomoRuntime(t *testing.T) {
	binary := os.Getenv("B05_MIHOMO_BINARY")
	if binary == "" {
		t.Skip("explicit locked-kernel verification required")
	}
	for _, proto := range []domain.Protocol{domain.SS2022, domain.Trojan, domain.VLESS, domain.Hysteria2} {
		t.Run(string(proto), func(t *testing.T) { realProtocol(t, binary, proto) })
	}
}

func realProtocol(t *testing.T, binary string, proto domain.Protocol) {
	t.Helper()
	f := newFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	cfg := f.opts.ConfigDir
	// Unix socket paths must fit sockaddr_un, including Go's nested test dirs.
	socketDir, err := os.MkdirTemp("", "b05-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	f.opts.SocketPath = filepath.Join(socketDir, "api.sock")
	var secret [32]byte
	randomBytes(t, secret[:])
	f.opts.APISecret = hex.EncodeToString(secret[:])
	randomBytes(t, f.storeOptions.Key)
	f.store.Close()
	// Initialize the real test with a fresh independent key and clean directory.
	f.storeOptions.Dir = privateTestDir(t)
	f.reopen()
	certRef := control.LocalMaterial{ID: testID("cert", 1), Purpose: "tls_certificate"}
	keyRef := control.LocalMaterial{ID: testID("tlskey", 1), Purpose: "tls_private_key"}
	realityRef := control.LocalMaterial{ID: testID("reality", 1), Purpose: "reality_private_key"}
	certificate := makeCertificate(t, cfg)
	realityKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	materials, err := authorization.NewResolver(node, cfg, []authorization.Material{
		{Ref: certRef, Path: "materials/cert.pem"}, {Ref: keyRef, Path: "materials/key.pem"},
		{Ref: realityRef, Value: base64.RawURLEncoding.EncodeToString(realityKey.Bytes())},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.opts.Materials = materials
	target := makeRealityTarget(t, certificate)
	a, err := mihomo.New(mihomo.Options{Binary: binary, BinarySHA256: core.LinuxAMD64SHA256, WorkDir: cfg,
		StateDir: cfg, ConfigDir: cfg, SocketPath: f.opts.SocketPath, Secret: f.opts.APISecret, Timeout: 9 * time.Second})
	if err != nil {
		t.Fatal("adapter", err)
	}
	f.opts.Kernel = a
	po := ProcessOptions{Binary: binary, WorkDir: cfg, ConfigDir: cfg, StateDir: cfg}
	process, err := NewProcess(po)
	if err != nil {
		t.Fatal("process", err)
	}
	f.opts.Process = process
	t.Cleanup(func() {
		cc, done := context.WithTimeout(context.Background(), 8*time.Second)
		defer done()
		if err := process.Stop(cc); err != nil {
			t.Error("managed kernel cleanup failed", err)
		}
		if s, err := process.Status(cc); err != nil || s.Running {
			t.Error("kernel did not stop", err)
		}
	})
	probes, err := NewProbes(ProbeOptions{Binary: binary, Dir: privateTestDir(t), Materials: materials, Timeout: 3500 * time.Millisecond})
	if err != nil {
		t.Fatal("probes", err)
	}
	f.opts.ProbeFactory = func(ctx context.Context, b control.Bundle, c mihomo.Candidate) (mihomo.Probe, error) {
		p, err := probes.ForwardFactory(ctx, b, c)
		return measuredProbe{p: p, t: t}, err
	}
	f.opts.RevocationProbeFactory = probes.RevocationFactory
	makeListener := func(n int, expiry time.Time) control.BundleListener {
		l := listener(n, freeEndpoint(t, proto), expiry)
		l.Protocol = proto
		l.Credential.Password = randomPassword(t)
		switch proto {
		case domain.Trojan, domain.Hysteria2:
			l.Cipher = ""
			l.Certificate, l.PrivateKey = &certRef, &keyRef
			l.Transport = "tcp"
			if proto == domain.Hysteria2 {
				l.Transport = "udp"
			}
		case domain.VLESS:
			l.Cipher, l.Credential.Password = "", ""
			l.Credential.UUID = randomUUID(t)
			l.RealityKey = &realityRef
			l.RealityDestination = target
			l.ServerNames = []string{"test.local"}
			l.ShortIDs = []string{"0123456789abcdef"}
			l.Transport = "tcp"
		}
		return l
	}
	first := makeListener(1, f.now.Add(10*time.Minute))
	second := makeListener(2, f.now.Add(time.Hour))
	for second.Port == first.Port {
		second.Port = freeEndpoint(t, proto)
	}
	c := f.command(bundle(1, 1, domain.Publish, first, second))
	r := launch(t, f.opts)
	res := execute(t, r, c)
	if res.Verification != domain.Verified {
		t.Fatalf("real verification failed (%s, rollback %s)", res.Verification, res.Rollback)
	}
	status, err := process.Status(ctx)
	if err != nil || !status.Running {
		t.Fatal("no supervised process", err)
	}
	t.Logf("verified real TCP, business UDP and wrong credential with supervised PID %d", status.PID)
	r.Close()
	if current, err := process.Status(ctx); err != nil || current != status {
		t.Fatal("agent close stopped platform kernel", err)
	}
	adopted, err := NewProcess(po)
	if err != nil {
		t.Fatal(err)
	}
	if current, err := adopted.Status(ctx); err != nil || current != status {
		t.Fatal("did not adopt kernel launch", err)
	}
	if proto == domain.SS2022 {
		// Reproduce a durable launch intent left before PID publication.
		dir, err := checkpoint.OpenPrivateDir(po.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		record, err := adopted.record(dir)
		if err != nil {
			dir.Close()
			t.Fatal(err)
		}
		record.PID, record.Start = 0, ""
		err = writeRecord(dir, record)
		dir.Close()
		if err != nil {
			t.Fatal(err)
		}
		if current, err := adopted.Status(ctx); err != nil || current != status {
			t.Fatal("crash intent did not adopt the exact PID", err)
		}
		t.Log("recovered managed PID from the interrupted launch intent")
	}
	f.reopen()
	f.opts.Process = adopted
	r = launch(t, f.opts)
	if current, err := adopted.Status(ctx); err != nil || current != status {
		t.Fatal("restore did not preserve managed PID", err)
	}

	echo, done, err := echoTarget(ctx, "tcp")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	hold := func(l control.BundleListener) net.Conn {
		for range 4 {
			clientCtx, done := context.WithTimeout(ctx, 30*time.Second)
			proxy, err := probes.proxy(clientCtx, l, false)
			if err != nil {
				done()
				t.Fatal(err)
			}
			cl, err := probes.startClient(clientCtx, proxy)
			if err != nil {
				done()
				continue
			}
			conn, err := cl.connect(clientCtx)
			if err == nil {
				rep, _, err := socksRequest(conn, 1, echo)
				if err == nil && rep == 0 && roundTrip(conn) {
					t.Cleanup(done)
					t.Cleanup(cl.stop)
					t.Cleanup(func() { conn.Close() })
					return conn
				}
				conn.Close()
			}
			done()
			cl.stop()
		}
		t.Fatal("established session failed")
		return nil
	}
	oldConn, newConn := hold(first), hold(second)
	connections, err := a.Connections(ctx)
	if err != nil {
		t.Fatal("capture held sessions", err)
	}
	oldIDs := map[string]bool{}
	for _, c := range connections {
		if c.ListenerID == string(first.ListenerID) {
			oldIDs[c.ID] = true
		}
	}
	if len(oldIDs) == 0 {
		t.Fatal("expired session was not attributed")
	}
	f.clock.set(f.now.Add(11 * time.Minute))
	waitFor(t, 10*time.Second, func() bool {
		for _, out := range f.state().Outbox {
			if len(out.Restrictions) > 0 && out.Cleanup == "confirmed" && out.TaskID == c.TaskID {
				return true
			}
		}
		return false
	})
	if roundTrip(oldConn) {
		t.Fatal("expired connection can still forward")
	}
	if !roundTrip(newConn) {
		t.Fatal("independent retained connection disrupted")
	}
	connections, err = a.Connections(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range connections {
		if oldIDs[c.ID] {
			t.Fatal("expired session ID remains in kernel")
		}
	}
	if final, err := adopted.Status(ctx); err != nil || final != status {
		t.Fatal("precise expiry restarted kernel", err)
	}
	t.Logf("offline expiry produced confirmed origin evidence; removed %d attributed old sessions with retained session forwarding", len(oldIDs))

	if proto == domain.SS2022 {
		foreign, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer foreign.Close()
		occupied := makeListener(3, f.now.Add(time.Hour))
		occupied.Port = uint16(foreign.Addr().(*net.TCPAddr).Port)
		out := execute(t, r, f.command(bundle(2, 1, domain.Publish, second, occupied)))
		if out.Verification != domain.Failed || !out.Applied || out.Rollback != "confirmed" {
			t.Fatal("port occupied by foreign PID mishandled", out.Verification, out.Rollback)
		}
		t.Log("real foreign-port collision kept publish failed and verified filtered rollback")
	}
}

func freeEndpoint(t *testing.T, proto domain.Protocol) uint16 {
	t.Helper()
	if proto == domain.Hysteria2 {
		l, err := net.ListenPacket("udp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		return uint16(l.LocalAddr().(*net.UDPAddr).Port)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if proto == domain.SS2022 {
		udp, err := net.ListenPacket("udp4", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		udp.Close()
	}
	return uint16(l.Addr().(*net.TCPAddr).Port)
}

func randomBytes(t *testing.T, b []byte) {
	t.Helper()
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
}
func randomPassword(t *testing.T) string {
	var b [16]byte
	randomBytes(t, b[:])
	return base64.StdEncoding.EncodeToString(b[:])
}
func randomUUID(t *testing.T) string {
	var b [16]byte
	randomBytes(t, b[:])
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

func makeCertificate(t *testing.T, cfg string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test.local"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), DNSNames: []string{"test.local"},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, &cert, &cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	materialDir := filepath.Join(cfg, "materials")
	if err = os.Mkdir(materialDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(materialDir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(materialDir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv}), 0600); err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func makeRealityTarget(t *testing.T, cert tls.Certificate) string {
	t.Helper()
	l, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert},
		MinVersion: tls.VersionTLS13})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				_ = conn.(*tls.Conn).Handshake()
			}()
		}
	}()
	return l.Addr().String()
}

func roundTrip(conn net.Conn) bool {
	_ = conn.SetDeadline(time.Now().Add(700 * time.Millisecond))
	b := make([]byte, 64)
	if _, err := rand.Read(b); err != nil {
		return false
	}
	if _, err := conn.Write(b); err != nil {
		return false
	}
	got := make([]byte, len(b))
	_, err := io.ReadFull(conn, got)
	return err == nil && bytes.Equal(b, got)
}

type measuredProbe struct {
	p mihomo.Probe
	t *testing.T
}

func (p measuredProbe) Forward(ctx context.Context, listener core.Expected, network string) mihomo.Check {
	result := p.p.Forward(ctx, listener, network)
	p.t.Logf("probe %s %s: %s %s", listener.Protocol, network, result.Status, result.Code)
	return result
}
