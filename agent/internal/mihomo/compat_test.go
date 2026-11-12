package mihomo

import (
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
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	core "github.com/nicccce/meshstrata/internal/mihomo"
)

// Enabled only by the explicit, locked compatibility runner. All credentials,
// private configs, and kernel logs live under B03_ARTIFACT_DIR, never in source.
func TestRealKernelCompatibility(t *testing.T) {
	binary := os.Getenv("B03_MIHOMO_BINARY")
	if binary == "" {
		t.Skip("explicit pinned-kernel compatibility run required")
	}
	root := os.Getenv("B03_ARTIFACT_DIR")
	if !filepath.IsAbs(root) {
		t.Fatal("private artifact directory required")
	}
	if err := privateDir(root); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(binary)
	if err != nil || core.Digest(b) != core.LinuxAMD64SHA256 {
		t.Fatal("pinned binary mismatch")
	}
	report := map[string]any{"kernel_version": core.Version, "binary_sha256": core.Digest(b), "architecture": "linux/amd64", "started_at": time.Now().UTC(), "protocols": map[string]any{}}
	defer func() {
		report["finished_at"] = time.Now().UTC()
		data, _ := json.MarshalIndent(report, "", "  ")
		_ = os.WriteFile(filepath.Join(root, "evidence.json"), data, 0600)
	}()
	for _, protocol := range []core.Protocol{core.SS2022, core.Trojan, core.VLESS, core.Hysteria2} {
		t.Run(string(protocol), func(t *testing.T) {
			result := map[string]any{"status": "failure", "listener_sharing": "disabled", "business_udp": "unverified"}
			report["protocols"].(map[string]any)[string(protocol)] = result
			dir := filepath.Join(root, string(protocol))
			mustDir(t, dir)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			cert, key, pubCert := localCertificate(t, dir)
			echo, echoAddr := echoServer(t)
			defer echo.Close()
			udp, udpAddr := udpEchoServer(t)
			defer udp.Close()
			realityTarget, targetAddr := tlsTarget(t, cert, key)
			defer realityTarget.Close()
			priv, _ := ecdh.X25519().GenerateKey(rand.Reader)
			entry := func(id string) core.Entry {
				return core.Entry{NodeID: "node_local", ListenerID: "listener_" + id, IdentityID: "identity_" + id, Protocol: protocol, Address: "127.0.0.1", Port: freePort(t), BusinessUDP: true, Cipher: "2022-blake3-aes-128-gcm", Password: randomPassword(t), UUID: randomUUID(t), Certificate: cert, PrivateKey: key, RealityPrivateKey: base64.RawURLEncoding.EncodeToString(priv.Bytes()), RealityDestination: targetAddr, ServerNames: []string{"test.local"}, ShortIDs: []string{"0123456789abcdef"}}
			}
			first, other := entry("first"), entry("other")
			a, err := New(Options{Binary: binary, BinarySHA256: core.LinuxAMD64SHA256, WorkDir: dir, StateDir: dir, ConfigDir: dir, SocketPath: filepath.Join(dir, "api.sock"), Timeout: 5 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			c := writeCandidate(t, a, []core.Entry{first, other}, "startup.json")
			exe, args, err := a.Startup(ctx, c)
			if err != nil {
				t.Fatal("server syntax", err)
			}
			server := startKernel(t, exe, args, filepath.Join(dir, "server.log"))
			defer stopKernel(server)
			if !waitAPI(ctx, a) {
				t.Fatal("server API readiness")
			}
			mode, err := os.Stat(a.o.SocketPath)
			if err != nil {
				t.Fatal(err)
			}
			result["unix_socket_mode"] = fmt.Sprintf("%04o", mode.Mode().Perm())
			result["unix_parent_mode"] = "0700"
			ev, err := a.Inspect(ctx, c, server.Process.Pid)
			for attempt := 0; attempt < 100 && err == nil; attempt++ {
				ready := true
				for _, l := range ev.Listeners {
					ready = ready && l.Listening.Status == Succeeded
				}
				if ready {
					break
				}
				time.Sleep(50 * time.Millisecond)
				ev, err = a.Inspect(ctx, c, server.Process.Pid)
			}
			if err != nil {
				t.Fatal("inspect", err)
			}
			result["initial_inspection"] = ev
			for _, l := range ev.Listeners {
				if l.Listening.Status != Succeeded {
					t.Fatal("missing owned listener")
				}
			}
			for _, ep := range ev.OwnedSockets {
				wanted := false
				for _, expected := range c.Expected {
					if ep.Address == expected.Address && ep.Port == expected.Port && ((ep.Transport == "tcp" && expected.TCP) || (ep.Transport == "udp" && expected.UDP)) {
						wanted = true
					}
				}
				if !wanted {
					t.Fatal("unexpected owned listening socket")
				}
			}
			result["no_general_inbounds"] = "success"
			// Clients are separate fixed-version processes and use only private loopback
			// SOCKS listeners. The production Agent has no kernel dependency.
			clientLogs := map[string]string{}
			client := func(e core.Entry, label string) string {
				cd := filepath.Join(dir, label)
				mustDir(t, cd)
				port := freePort(t)
				proxy := clientProxy(e, priv.PublicKey().Bytes(), pubCert)
				cfg := map[string]any{"port": 0, "socks-port": port, "mixed-port": 0, "redir-port": 0, "tproxy-port": 0, "allow-lan": false, "bind-address": "127.0.0.1", "mode": "rule", "log-level": "silent", "tun": map[string]bool{"enable": false}, "dns": map[string]bool{"enable": false}, "external-controller": "", "external-controller-tls": "", "external-controller-unix": "", "proxies": []any{proxy}, "rules": []string{"MATCH,selected"}}
				if protocol == core.Hysteria2 {
					cfg["log-level"] = "warning"
				}
				clientLogs[label] = filepath.Join(cd, "kernel.log")
				p := filepath.Join(cd, "config.json")
				data, _ := json.Marshal(cfg)
				_ = os.WriteFile(p, data, 0600)
				cmd := startKernel(t, binary, []string{"-d", cd, "-f", p}, filepath.Join(cd, "kernel.log"))
				t.Cleanup(func() { stopKernel(cmd) })
				addr := net.JoinHostPort("127.0.0.1", fmtPort(port))
				if !waitTCP(ctx, addr) {
					t.Fatal("client readiness")
				}
				return addr
			}
			firstAddr := client(first, "first-client")
			otherAddr := client(other, "other-client")
			bad := first
			bad.Password = randomPassword(t)
			bad.UUID = randomUUID(t)
			badAddr := client(bad, "bad-client")
			held, err := socksConnect(firstAddr, echoAddr)
			if err != nil {
				t.Fatal("first authentication", err)
			}
			defer held.Close()
			heldOther, err := socksConnect(otherAddr, echoAddr)
			if err != nil {
				t.Fatal("other authentication", err)
			}
			defer heldOther.Close()
			if !echoRoundtrip(held) || !echoRoundtrip(heldOther) {
				t.Fatal("TCP payload mismatch")
			}
			result["tcp_forward"] = "success"
			result["independent_identity"] = "success"
			verified, verifyErr := a.Verify(ctx, c, server.Process.Pid, compatForwardProbe{map[string]string{first.ListenerID: firstAddr, other.ListenerID: otherAddr}, echoAddr, udpAddr})
			if verifyErr != nil || !verified.Verified {
				t.Fatal("authenticated verification", verifyErr)
			}
			result["forward_verification"] = verified
			badObservation := observeTCP(badAddr, echoAddr)
			result["bad_credential_observation"] = badObservation
			if badObservation.Forwarded {
				t.Fatal("bad credential forwarded")
			}
			result["bad_credentials_no_forward"] = "success"
			if protocol == core.Hysteria2 {
				rejected := hy2Rejected(clientLogs["bad-client"], 0)
				result["bad_authentication_explicitly_rejected"] = rejected
				if !rejected {
					t.Error("no explicit Hy2 authentication rejection")
				}
			}
			if socksUDP(firstAddr, udpAddr) {
				result["business_udp"] = "success"
			} else {
				result["business_udp"] = "failure"
				t.Error("business UDP payload not verified")
			}
			// A changed credential keeps the listener/port occupied. Old-client failure
			// therefore cannot be explained by removing the listening endpoint.
			beforeConnections, err := a.Connections(ctx)
			if err != nil {
				t.Fatal(err)
			}
			revokedIDs := map[string]bool{}
			for _, connection := range beforeConnections {
				if connection.ListenerID == first.ListenerID {
					revokedIDs[connection.ID] = true
				}
			}
			if len(revokedIDs) == 0 {
				t.Fatal("no attributed established session before rotation")
			}
			oldLogOffset := int64(0)
			if st, err := os.Stat(clientLogs["first-client"]); err == nil {
				oldLogOffset = st.Size()
			}
			rotated := first
			rotated.Password = randomPassword(t)
			rotated.UUID = randomUUID(t)
			next := writeCandidate(t, a, []core.Entry{rotated, other}, "rotated.json")
			app, err := a.Reload(ctx, next)
			if err != nil {
				t.Fatal("reload", err)
			}
			result["rotation_apply"] = app
			oldAuthAddr := firstAddr
			oldAuthLog := clientLogs["first-client"]
			if protocol == core.Hysteria2 {
				// A fresh process forces fresh QUIC authentication; reusing the
				// old client's outer QUIC connection tests a different property.
				oldAuthAddr = client(first, "revoked-client")
				oldAuthLog = clientLogs["revoked-client"]
				oldLogOffset = 0
			}
			oldObservation := observeTCP(oldAuthAddr, echoAddr)
			result["old_credential_observation"] = oldObservation
			if oldObservation.Forwarded {
				t.Error("old credentials still forward")
				result["old_credentials_no_forward"] = "failure"
			} else {
				result["old_credentials_no_forward"] = "success"
			}
			rotatedAddr := client(rotated, "rotated-client")
			if !waitForward(ctx, rotatedAddr, echoAddr) {
				t.Fatal("rotated credential fails")
			}
			if !tryTCP(otherAddr, echoAddr) {
				t.Fatal("other identity new connection fails")
			}
			result["other_new_connection"] = "success"
			otherSurvived := echoRoundtrip(heldOther)
			result["other_existing_survived"] = otherSurvived
			if !otherSurvived {
				t.Error("independent listener session disrupted")
			}
			survived := echoRoundtrip(held)
			result["revoked_existing_survived_reload"] = survived
			// Use exact inbound attribution to close existing revoked sessions. No global
			// connection deletion and no claim that reload itself closes all sessions.
			conns, err := a.Connections(ctx)
			if err != nil {
				t.Fatal("connection attribution", err)
			}
			closed := 0
			for _, conn := range conns {
				if conn.ListenerID == first.ListenerID && revokedIDs[conn.ID] {
					if err = a.CloseConnection(ctx, next, first.ListenerID, conn.ID); err != nil {
						t.Fatal("close attributed session", err)
					}
					closed++
				}
			}
			result["explicit_connection_close_requests"] = closed
			result["captured_revoked_server_sessions"] = len(revokedIDs)
			remaining := echoRoundtrip(held)
			result["revoked_existing_after_cleanup_no_forward"] = !remaining
			if remaining {
				t.Error("revoked established connection remains")
			}
			result["other_existing_after_cleanup_survived"] = echoRoundtrip(heldOther)
			revocation, revErr := a.Revocation(ctx, next, compatRevocationProbe{old: oldAuthAddr, current: rotatedAddr, other: otherAddr, target: echoAddr, revokedConn: held, otherConn: heldOther, adapter: a, revokedIDs: revokedIDs, oldClientLog: oldAuthLog, oldLogOffset: oldLogOffset, hy2: protocol == core.Hysteria2})
			if revErr != nil {
				t.Fatal("revocation evidence", revErr)
			}
			result["revocation_verification"] = revocation
			result["existing_session_verification_scope"] = "captured server-tracked sessions absent after exact-ID cleanup; obsolete client channel does not forward"
			if !revocation.Verified {
				t.Error("incomplete revocation evidence")
			}
			// Reproduce API success while a third-party process owns the requested port.
			occupied, err := net.Listen("tcp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer occupied.Close()
			port := uint16(occupied.Addr().(*net.TCPAddr).Port)
			if protocol == core.Hysteria2 {
				occupied.Close()
				u, err := net.ListenPacket("udp4", net.JoinHostPort("127.0.0.1", fmtPort(port)))
				if err != nil {
					t.Fatal(err)
				}
				defer u.Close()
			}
			conflict := rotated
			conflict.ListenerID = "listener_conflict"
			conflict.IdentityID = "identity_conflict"
			conflict.Port = port
			conflicted := writeCandidate(t, a, []core.Entry{rotated, other, conflict}, "conflict.json")
			app, err = a.Reload(ctx, conflicted)
			result["conflict_api"] = app
			if err != nil {
				t.Fatal("expected kernel false success", err)
			}
			ev, err = a.Inspect(ctx, conflicted, server.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			result["conflict_inspection"] = ev
			if ev.Listeners[2].Listening.Status != Failed {
				t.Fatal("foreign port mistaken for owned listener")
			}
			if t.Failed() {
				result["status"] = "failure"
			} else {
				result["status"] = "success"
			}
			result["checked_at"] = time.Now().UTC()
			result["reality_target"] = "controlled local TLS 1.3 target; external target not tested"
		})
	}
}
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
func waitAPI(ctx context.Context, a *Adapter) bool {
	for i := 0; i < 60; i++ {
		if _, err := a.request(ctx, "GET", "/version", nil); err == nil {
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
func writeCandidate(t *testing.T, a *Adapter, entries []core.Entry, name string) Candidate {
	t.Helper()
	conf, exp, err := core.Generate(entries, core.DNS{Enable: true, EnhancedMode: "normal", Nameserver: []string{"1.1.1.1"}, DefaultNameserver: []string{"1.1.1.1"}}, a.o.SocketPath, []string{"MATCH,DIRECT"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(conf)
	p := filepath.Join(a.o.ConfigDir, name)
	if err = os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
	return Candidate{p, core.Digest(b), exp}
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

// The opt-in harness supplies the external-client probe. Production B05 must
// provision a probe with the same controlled-payload semantics.
type compatForwardProbe struct {
	clients              map[string]string
	tcpTarget, udpTarget string
}

func (p compatForwardProbe) Forward(ctx context.Context, e core.Expected, network string) Check {
	if ctx.Err() != nil {
		return Check{Status: Unverified}
	}
	ok := false
	if network == "tcp" {
		ok = tryTCP(p.clients[e.ListenerID], p.tcpTarget)
	} else if network == "udp" {
		ok = socksUDP(p.clients[e.ListenerID], p.udpTarget)
	}
	if ctx.Err() != nil {
		return Check{Status: Unverified}
	}
	if ok {
		return Check{Status: Succeeded}
	}
	return Check{Status: Failed}
}

// Record generic timeout separately from a closed data path. No raw network
// error text or credential is included in evidence.
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

type compatRevocationProbe struct {
	old, current, other, target string
	revokedConn, otherConn      net.Conn
	adapter                     *Adapter
	revokedIDs                  map[string]bool
	oldClientLog                string
	oldLogOffset                int64
	hy2                         bool
}

func (p compatRevocationProbe) OldAuthenticationDenied(ctx context.Context) Check {
	if ctx.Err() != nil || !waitForward(ctx, p.current, p.target) {
		return Check{Status: Unverified}
	}
	// The pinned client can leave one failed SOCKS attempt pending during
	// reconnect. Retry only inconclusive observations, never forwarded traffic;
	// a timeout never becomes authentication-denial evidence.
	for attempt := 0; attempt < 3; attempt++ {
		out := observeTCP(p.old, p.target)
		if ctx.Err() != nil {
			return Check{Status: Unverified}
		}
		if out.Forwarded {
			return Check{Status: Failed}
		}
		if out.Reason == "closed" || (p.hy2 && hy2Rejected(p.oldClientLog, p.oldLogOffset)) {
			return Check{Status: Succeeded}
		}
		select {
		case <-ctx.Done():
			return Check{Status: Unverified}
		case <-time.After(25 * time.Millisecond):
		}
	}
	return Check{Status: Unverified}
}
func (p compatRevocationProbe) ExistingSessionsClosed(ctx context.Context) Check {
	if ctx.Err() != nil || len(p.revokedIDs) == 0 {
		return Check{Status: Unverified}
	}
	for i := 0; i < 20; i++ {
		connections, err := p.adapter.Connections(ctx)
		if err != nil {
			return Check{Status: Unverified}
		}
		present := false
		for _, connection := range connections {
			if p.revokedIDs[connection.ID] {
				present = true
			}
		}
		if !present {
			if echoRoundtrip(p.revokedConn) {
				return Check{Status: Failed}
			}
			return Check{Status: Succeeded}
		}
		select {
		case <-ctx.Done():
			return Check{Status: Unverified}
		case <-time.After(25 * time.Millisecond):
		}
	}
	return Check{Status: Unverified}
}

func (p compatRevocationProbe) OtherIdentityUnaffected(ctx context.Context) Check {
	if ctx.Err() != nil {
		return Check{Status: Unverified}
	}
	if echoRoundtrip(p.otherConn) && tryTCP(p.other, p.target) {
		return Check{Status: Succeeded}
	}
	return Check{Status: Failed}
}

// v1.19.33's client retries a denied Hy2 request while leaving its local SOCKS
// stream open. Timeout is not denial evidence; this exact pinned-client auth
// response is. Only a boolean from the private, bounded log leaves the harness.
func hy2Rejected(path string, offset int64) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return false
	}
	return strings.Contains(string(b), "authentication failed, status code: 404")
}

func waitForward(ctx context.Context, proxy, target string) bool {
	for i := 0; i < 3; i++ {
		if ctx.Err() != nil {
			return false
		}
		if tryTCP(proxy, target) {
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
