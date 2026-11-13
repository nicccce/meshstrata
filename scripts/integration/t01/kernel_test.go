// T01 test-only remote-client harness. Helper routines derive from the frozen
// f570207 agent/internal/mihomo/compat_test.go; product implementation is unchanged.
package t01

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
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func mustDir(t *testing.T, p string) {
	t.Helper()
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
}
func freePort(t *testing.T) uint16 {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return uint16(l.Addr().(*net.TCPAddr).Port)
}
func randomPassword(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}
func randomUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func localCertificate(t *testing.T, dir string) (string, string, string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test.local"}, DNSNames: []string{"test.local"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(key)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	private := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb})
	cp, kp := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	_ = os.WriteFile(cp, cert, 0600)
	_ = os.WriteFile(kp, private, 0600)
	return cp, kp, string(cert)
}
func tlsTarget(t *testing.T, cert, key string) (net.Listener, string) {
	t.Helper()
	pair, err := tls.LoadX509KeyPair(cert, key)
	if err != nil {
		t.Fatal(err)
	}
	l, err := tls.Listen("tcp4", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, CurvePreferences: []tls.CurveID{tls.X25519}, NextProtos: []string{"h2", "http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(5 * time.Second))
				_, _ = io.Copy(io.Discard, c)
			}()
		}
	}()
	return l, l.Addr().String()
}
func echoServer(t *testing.T) (net.Listener, string) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	return l, l.Addr().String()
}
func udpEchoServer(t *testing.T) (net.PacketConn, string) {
	t.Helper()
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		b := make([]byte, 1024)
		for {
			n, addr, err := c.ReadFrom(b)
			if err != nil {
				return
			}
			_, _ = c.WriteTo(b[:n], addr)
		}
	}()
	return c, c.LocalAddr().String()
}

var stopped sync.Map

func startKernel(t *testing.T, binary string, args []string, log string) *exec.Cmd {
	t.Helper()
	f, err := os.OpenFile(log, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cmd := exec.Command(binary, args...)
	cmd.Stdout = f
	cmd.Stderr = f
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}
func stopKernel(c *exec.Cmd) {
	if _, loaded := stopped.LoadOrStore(c, true); loaded {
		return
	}
	_ = c.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = c.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = c.Process.Kill()
		<-done
	}
}
func waitTCP(ctx context.Context, addr string) bool {
	for i := 0; i < 60; i++ {
		c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}
func clientProxy(e core.Entry, realityPub []byte, cert string) map[string]any {
	p := map[string]any{"name": "selected", "type": string(e.Protocol), "server": e.Address, "port": e.Port, "udp": true}
	switch e.Protocol {
	case core.SS2022:
		p["type"] = "ss"
		p["cipher"] = e.Cipher
		p["password"] = e.Password
	case core.Trojan:
		p["password"] = e.Password
		p["sni"] = "test.local"

		block, _ := pem.Decode([]byte(cert))
		p["fingerprint"] = core.Digest(block.Bytes)
		p["skip-cert-verify"] = false
	case core.VLESS:
		p["uuid"] = e.UUID
		p["tls"] = true
		p["flow"] = "xtls-rprx-vision"
		p["servername"] = "test.local"
		p["client-fingerprint"] = "chrome"
		p["reality-opts"] = map[string]string{"public-key": base64.RawURLEncoding.EncodeToString(realityPub), "short-id": e.ShortIDs[0]}
	case core.Hysteria2:
		p["password"] = e.Password
		p["sni"] = "test.local"

		block, _ := pem.Decode([]byte(cert))
		p["fingerprint"] = core.Digest(block.Bytes)
		p["skip-cert-verify"] = false
	}
	return p
}
func socksHandshake(addr string) (net.Conn, error) {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		return nil, err
	}
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, err = c.Write([]byte{5, 1, 0})
	if err != nil {
		c.Close()
		return nil, err
	}
	r := make([]byte, 2)
	if _, err = io.ReadFull(c, r); err != nil || r[0] != 5 || r[1] != 0 {
		c.Close()
		return nil, fmt.Errorf("SOCKS greeting failed")
	}
	return c, nil
}
func socksRequest(c net.Conn, command byte, dest string) (string, error) {
	host, ps, err := net.SplitHostPort(dest)
	if err != nil {
		return "", err
	}
	p, _ := net.LookupPort("tcp", ps)
	ip := net.ParseIP(host).To4()
	if ip == nil {
		return "", fmt.Errorf("test target must be IPv4")
	}
	req := append([]byte{5, command, 0, 1}, ip...)
	req = append(req, byte(p>>8), byte(p))
	if _, err = c.Write(req); err != nil {
		return "", err
	}
	r := make([]byte, 4)
	if _, err = io.ReadFull(c, r); err != nil || r[0] != 5 || r[1] != 0 {
		return "", fmt.Errorf("SOCKS request failed")
	}
	n := 4
	if r[3] == 4 {
		n = 16
	} else if r[3] != 1 {
		return "", fmt.Errorf("unexpected SOCKS address")
	}
	b := make([]byte, n+2)
	if _, err = io.ReadFull(c, b); err != nil {
		return "", err
	}
	return net.JoinHostPort(net.IP(b[:n]).String(), fmt.Sprint(binary.BigEndian.Uint16(b[n:]))), nil
}
func socksConnect(proxy, target string) (net.Conn, error) {
	c, err := socksHandshake(proxy)
	if err != nil {
		return nil, err
	}
	if _, err = socksRequest(c, 1, target); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}
func echoRoundtrip(c net.Conn) bool {
	_ = c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	msg := make([]byte, 64)
	_, _ = rand.Read(msg)
	if _, err := c.Write(msg); err != nil {
		return false
	}
	out := make([]byte, len(msg))
	_, err := io.ReadFull(c, out)
	return err == nil && string(msg) == string(out)
}
func tryTCP(proxy, target string) bool {
	c, err := socksConnect(proxy, target)
	if err != nil {
		return false
	}
	defer c.Close()
	return echoRoundtrip(c)
}
func socksUDP(proxy, target string) bool {
	c, err := socksHandshake(proxy)
	if err != nil {
		return false
	}
	defer c.Close()
	relay, err := socksRequest(c, 3, "127.0.0.1:0")
	if err != nil {
		return false
	}
	remote, err := net.ResolveUDPAddr("udp4", relay)
	if err != nil {
		return false
	}
	u, err := net.DialUDP("udp4", nil, remote)
	if err != nil {
		return false
	}
	defer u.Close()
	_ = u.SetDeadline(time.Now().Add(3 * time.Second))
	host, ps, _ := net.SplitHostPort(target)
	p, _ := net.LookupPort("udp", ps)
	msg := make([]byte, 32)
	_, _ = rand.Read(msg)
	packet := append([]byte{0, 0, 0, 1}, net.ParseIP(host).To4()...)
	packet = append(packet, byte(p>>8), byte(p))
	packet = append(packet, msg...)
	if _, err = u.Write(packet); err != nil {
		return false
	}
	b := make([]byte, 1024)
	n, err := u.Read(b)
	if err != nil || n < 10 {
		return false
	}
	return strings.HasSuffix(string(b[:n]), string(msg))
}

func TestRemoteKernel(t *testing.T) {
	role, dir, binary := os.Getenv("T01_ROLE"), os.Getenv("T01_DIR"), os.Getenv("T01_KERNEL")
	if role == "" {
		t.Skip("explicit T01 role required")
	}
	if !filepath.IsAbs(dir) {
		t.Fatal("private absolute directory required")
	}
	data, err := os.ReadFile(binary)
	if err != nil || core.Digest(data) != core.LinuxAMD64SHA256 {
		t.Fatal("kernel checksum")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	if role == "server" {
		cert, key, pubCert := localCertificate(t, dir)
		echo, echoAddr := echoServer(t)
		defer echo.Close()
		udp, udpAddr := udpEchoServer(t)
		defer udp.Close()
		target, targetAddr := tlsTarget(t, cert, key)
		defer target.Close()
		priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
		entries := []core.Entry{}
		proxies := map[string]any{}
		for i, protocol := range []core.Protocol{core.SS2022, core.Trojan, core.VLESS, core.Hysteria2} {
			e := core.Entry{NodeID: "node_t01", ListenerID: "listener_" + string(protocol), IdentityID: "identity_" + string(protocol), Protocol: protocol, Address: "0.0.0.0", Port: uint16(28443 + i), BusinessUDP: true, Cipher: "2022-blake3-aes-128-gcm", Password: randomPassword(t), UUID: randomUUID(t), Certificate: cert, PrivateKey: key, RealityPrivateKey: base64.RawURLEncoding.EncodeToString(priv.Bytes()), RealityDestination: targetAddr, ServerNames: []string{"test.local"}, ShortIDs: []string{"0123456789abcdef"}}
			entries = append(entries, e)
			e.Address = os.Getenv("T01_PUBLIC")
			proxies[string(protocol)] = clientProxy(e, priv.PublicKey().Bytes(), pubCert)
		}
		cfg, _, err := core.Generate(entries, core.DNS{EnhancedMode: "normal", Nameserver: []string{"1.1.1.1"}, DefaultNameserver: []string{"1.1.1.1"}}, filepath.Join(dir, "api.sock"), []string{"MATCH,DIRECT"})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(cfg)
		path := filepath.Join(dir, "server.json")
		os.WriteFile(path, b, 0600)
		server := startKernel(t, binary, []string{"-d", dir, "-f", path}, filepath.Join(dir, "server.log"))
		defer stopKernel(server)
		if !waitTCP(ctx, "127.0.0.1:28443") || !waitTCP(ctx, "127.0.0.1:28444") || !waitTCP(ctx, "127.0.0.1:28445") {
			t.Fatal("server listeners")
		}
		client, _ := json.Marshal(map[string]any{"proxies": proxies, "tcp_target": echoAddr, "udp_target": udpAddr})
		os.WriteFile(filepath.Join(dir, "client-material.json"), client, 0600)
		os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600)
		for {
			if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal("bounded server timeout")
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	if role != "client" {
		t.Fatal("unknown role")
	}
	var material struct {
		Proxies map[string]map[string]any `json:"proxies"`
		TCP     string                    `json:"tcp_target"`
		UDP     string                    `json:"udp_target"`
	}
	b, err := os.ReadFile(filepath.Join(dir, "client-material.json"))
	if err != nil || json.Unmarshal(b, &material) != nil {
		t.Fatal("private materials")
	}
	report := map[string]any{}
	defer func() {
		b, _ := json.MarshalIndent(report, "", "  ")
		os.WriteFile(filepath.Join(dir, "evidence.json"), b, 0600)
	}()
	for _, protocol := range []string{"ss2022", "trojan", "vless", "hysteria2"} {
		t.Run(protocol, func(t *testing.T) {
			result := map[string]any{"checked_at": time.Now().UTC(), "transport": "public direct; no SSH forwarding"}
			report[protocol] = result
			start := func(bad bool) string {
				name := "valid"
				if bad {
					name = "bad"
				}
				cd := filepath.Join(dir, protocol+"-"+name)
				mustDir(t, cd)
				proxy := map[string]any{}
				for k, v := range material.Proxies[protocol] {
					proxy[k] = v
				}
				if bad {
					if protocol == "vless" {
						proxy["uuid"] = randomUUID(t)
					} else {
						proxy["password"] = randomPassword(t)
					}
				}
				port := freePort(t)
				cfg := map[string]any{"socks-port": port, "allow-lan": false, "bind-address": "127.0.0.1", "mode": "rule", "log-level": "warning", "tun": map[string]bool{"enable": false}, "dns": map[string]bool{"enable": false}, "proxies": []any{proxy}, "rules": []string{"MATCH,selected"}}
				b, _ := json.Marshal(cfg)
				path := filepath.Join(cd, "client.json")
				os.WriteFile(path, b, 0600)
				p := startKernel(t, binary, []string{"-d", cd, "-f", path}, filepath.Join(cd, "kernel.log"))
				t.Cleanup(func() { stopKernel(p) })
				addr := net.JoinHostPort("127.0.0.1", fmtPort(port))
				if !waitTCP(ctx, addr) {
					t.Fatal("client readiness")
				}
				return addr
			}
			addr := start(false)
			bad := start(true)
			observation := observeTCP(addr, material.TCP)
			result["tcp"] = observation
			result["business_udp"] = socksUDP(addr, material.UDP)
			badObservation := observeTCP(bad, material.TCP)
			result["bad_credentials"] = badObservation
			denial := badObservation.Reason == "closed" || badObservation.Reason == "socks_failed"
			if protocol == "hysteria2" {
				log, _ := os.ReadFile(filepath.Join(dir, protocol+"-bad", "kernel.log"))
				denial = strings.Contains(string(log), "authentication failed, status code: 404")
				result["explicit_authentication_rejected"] = denial
			}
			result["bad_credential_denial_confirmed"] = denial
			result["status"] = "passed"
			if !observation.Forwarded || result["business_udp"] != true || badObservation.Forwarded || !denial {
				result["status"] = "failed"
				t.Error("remote forwarding or credential check failed")
			}
		})
	}
}
func fmtPort(port uint16) string { return fmt.Sprint(port) }

type tcpObservation struct {
	Forwarded bool   `json:"forwarded"`
	Reason    string `json:"reason"`
}

func observeTCP(proxy, target string) tcpObservation {
	c, err := socksConnect(proxy, target)
	if err != nil {
		return tcpObservation{Reason: "socks_failed"}
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	msg := make([]byte, 64)
	_, _ = rand.Read(msg)
	_, err = c.Write(msg)
	if err == nil {
		out := make([]byte, len(msg))
		_, err = io.ReadFull(c, out)
		if err == nil {
			if string(msg) == string(out) {
				return tcpObservation{true, "echo_matched"}
			}
			return tcpObservation{Reason: "payload_mismatch"}
		}
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return tcpObservation{Reason: "timeout"}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return tcpObservation{Reason: "closed"}
	}
	return tcpObservation{Reason: "transport_error"}
}

// Hold or probe an exact loopback test port; never touches host network rules.
func TestListenerProbe(t *testing.T) {
	dir := os.Getenv("T01_DIR")
	addr := os.Getenv("T01_LISTEN")
	if dir == "" || addr == "" {
		t.Skip("explicit bounded listener probe")
	}
	l, err := net.Listen("tcp4", addr)
	if err != nil {
		t.Fatal("test port bind failed")
	}
	defer l.Close()
	os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600)
	if os.Getenv("T01_HOLD") == "1" {
		deadline := time.Now().Add(45 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("bounded listener hold expired")
	}
}

func TestUnixPermission(t *testing.T) {
	socket := os.Getenv("T01_SOCKET")
	if socket == "" {
		t.Skip("explicit private socket required")
	}
	if err := syscall.Setgid(65534); err != nil {
		t.Fatal("drop gid")
	}
	if err := syscall.Setuid(65534); err != nil {
		t.Fatal("drop uid")
	}
	c, err := net.DialTimeout("unix", socket, time.Second)
	if c != nil {
		c.Close()
		t.Fatal("unauthorized UID connected")
	}
	if !errors.Is(err, syscall.EACCES) {
		t.Fatal("expected EACCES")
	}
	t.Log("uid=65534 gid=65534 Unix connect denied with EACCES")
}

// Node private material is read and used only on its original remote host.
// No private key, certificate body, CSR, token, or response body is printed.
func TestNodeEndpoint(t *testing.T) {
	path := os.Getenv("T01_IDENTITY")
	if path == "" {
		t.Skip("explicit original identity required")
	}
	var identity struct {
		NodeID      string `json:"node_id"`
		KeyDER      []byte `json:"key_der"`
		CSR         []byte `json:"csr_pem"`
		Certificate []byte `json:"certificate_pem"`
	}
	data, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(data, &identity) != nil {
		t.Fatal("read test identity")
	}
	roots := x509.NewCertPool()
	ca, err := os.ReadFile(os.Getenv("T01_CA"))
	if err != nil || !roots.AppendCertsFromPEM(ca) {
		t.Fatal("read CA")
	}
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: identity.KeyDER})
	pair, err := tls.X509KeyPair(identity.Certificate, key)
	if err != nil {
		t.Fatal("identity pair")
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 8 * time.Second}
	url := os.Getenv("T01_URL")
	if os.Getenv("T01_REMOVED") == "1" {
		r, err := client.Get(url + "/control/v1")
		if err != nil {
			t.Log("old certificate transport rejected after removal")
			return
		}
		defer r.Body.Close()
		if r.StatusCode != 401 && r.StatusCode != 403 {
			t.Fatal("removed identity accepted")
		}
		t.Log("old certificate HTTP rejected after removal")
		return
	}
	r, err := client.Get(url + "/control/v1/tasks/task_" + strings.Repeat("1", 32) + "/bundle")
	if err != nil {
		t.Fatal("authenticated artifact transport")
	}
	r.Body.Close()
	if r.StatusCode != 501 {
		t.Fatal("expected authenticated501")
	}
	b, _ := json.Marshal(map[string]string{"csr_pem": string(identity.CSR)})
	r, err = client.Post(url+"/enroll/v1/renew", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal("mTLS renewal transport")
	}
	var response struct {
		NodeID      string `json:"node_id"`
		Certificate string `json:"certificate_pem"`
	}
	err = json.NewDecoder(r.Body).Decode(&response)
	r.Body.Close()
	if err != nil || r.StatusCode != 200 || response.NodeID != identity.NodeID || response.Certificate == string(identity.Certificate) {
		t.Fatal("renewal identity/certificate")
	}
	leaf, _ := pem.Decode([]byte(response.Certificate))
	cert, err := x509.ParseCertificate(leaf.Bytes)
	if err != nil || !bytes.Equal(cert.RawSubjectPublicKeyInfo, pair.Leaf.RawSubjectPublicKeyInfo) {
		t.Fatal("renewal changed public key")
	}
	anonymousTransport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13}}
	defer anonymousTransport.CloseIdleConnections()
	anonymous := &http.Client{Transport: anonymousTransport, Timeout: 8 * time.Second}
	for _, path := range []string{"/control/v1", "/enroll/v1/renew"} {
		req, _ := http.NewRequest("POST", url+path, bytes.NewReader(b))
		if path == "/control/v1" {
			req.Method = "GET"
		}
		req.Header.Set("Content-Type", "application/json")
		r, err := anonymous.Do(req)
		if err == nil {
			r.Body.Close()
			if r.StatusCode != 401 && r.StatusCode != 403 {
				t.Fatal("no-certificate endpoint accepted")
			}
		}
	}
	t.Log("artifact501; mTLS renewal same node/public key and changed certificate; no-certificate control/renew denied")
}
