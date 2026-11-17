package runtime

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"golang.org/x/sys/unix"
)

var errProbe = errors.New("AGENT_AUTHENTICATED_PROBE_UNAVAILABLE")

type ProbeOptions struct {
	Binary    string
	Dir       string
	Materials control.MaterialResolver
	Timeout   time.Duration
	Random    io.Reader
}

type Probes struct {
	options  ProbeOptions
	randomMu sync.Mutex
}

func NewProbes(o ProbeOptions) (*Probes, error) {
	if o.Timeout == 0 {
		o.Timeout = 4 * time.Second
	}
	if o.Random == nil {
		o.Random = rand.Reader
	}
	if o.Timeout <= 0 || o.Timeout > 30*time.Second || o.Materials == nil {
		return nil, errProbe
	}
	dir, err := checkpoint.OpenPrivateDir(o.Dir)
	if err != nil {
		return nil, errProbe
	}
	dir.Close()
	f, err := pinnedBinary(o.Binary)
	if err != nil {
		return nil, err
	}
	f.Close()
	return &Probes{options: o}, nil
}

func (p *Probes) random(b []byte) error {
	p.randomMu.Lock()
	defer p.randomMu.Unlock()
	_, err := io.ReadFull(p.options.Random, b)
	return err
}

type retainedProbeKey struct{}

type forwardProbe struct {
	retained  map[domain.ID]bool
	p         *Probes
	listeners map[string]control.BundleListener
}

func (p *Probes) ForwardFactory(ctx context.Context, b control.Bundle, _ mihomo.Candidate) (mihomo.Probe, error) {
	retained, _ := ctx.Value(retainedProbeKey{}).(map[domain.ID]bool)
	out := &forwardProbe{p: p, listeners: map[string]control.BundleListener{}, retained: retained}
	for _, l := range b.Listeners {
		out.listeners[string(l.ListenerID)] = l
	}
	return out, nil
}

func check(status mihomo.Status) mihomo.Check { return mihomo.Check{Status: status} }

func (p *forwardProbe) Forward(ctx context.Context, e core.Expected, network string) mihomo.Check {
	l, ok := p.listeners[e.ListenerID]
	if !ok || string(l.NodeID) != e.NodeID || core.Protocol(l.Protocol) != e.Protocol ||
		l.Address != e.Address || l.Port != e.Port || (network != "tcp" && (network != "udp" || !l.BusinessUDP)) {
		return check(mihomo.Unverified)
	}
	target, closeTarget, err := echoTarget(ctx, network)
	if err != nil {
		return check(mihomo.Unverified)
	}
	defer closeTarget()
	good := unknown
	for range 3 {
		attempt, cancel := context.WithTimeout(ctx, 1800*time.Millisecond)
		good = p.p.observe(attempt, l, target, network, false)
		cancel()
		if good == forwarded || ctx.Err() != nil {
			break
		}
	}
	if good != forwarded {
		return mihomo.Check{Status: mihomo.Unverified, Code: "correct_credential_no_echo"}
	}
	// Use a fresh transport for the negative control. Retry an inconclusive
	// attempt, but fail immediately if any wrong credential forwards data.
	if network == "tcp" && !p.retained[l.Authorization.IdentityID] {
		for range 2 {
			switch p.p.observe(ctx, l, target, network, true) {
			case forwarded:
				return mihomo.Check{Status: mihomo.Failed, Code: "wrong_credential_forwarded"}
			case denied:
				return check(mihomo.Succeeded)
			}
			if ctx.Err() != nil {
				break
			}
		}
		return mihomo.Check{Status: mihomo.Unverified, Code: "wrong_credential_inconclusive"}
	}
	return check(mihomo.Succeeded)
}

func echoTarget(ctx context.Context, network string) (string, func(), error) {
	if network == "udp" {
		pc, err := (&net.ListenConfig{}).ListenPacket(ctx, "udp4", "127.0.0.1:0")
		if err != nil {
			return "", nil, err
		}
		go func() {
			buf := make([]byte, 128)
			for {
				n, addr, err := pc.ReadFrom(buf)
				if err != nil {
					return
				}
				_, _ = pc.WriteTo(buf[:n], addr)
			}
		}()
		return pc.LocalAddr().String(), func() { pc.Close() }, nil
	}
	l, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	lifetime, stop := context.WithCancel(ctx)
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				cancelIO := context.AfterFunc(lifetime, func() { c.Close() })
				defer cancelIO()
				_ = c.SetDeadline(time.Now().Add(30 * time.Second))
				buf := make([]byte, 256)
				for remaining := len(buf); remaining > 0; {
					n, err := c.Read(buf[:remaining])
					if n > 0 {
						if _, writeErr := c.Write(buf[:n]); writeErr != nil {
							return
						}
						remaining -= n
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return l.Addr().String(), func() { stop(); l.Close() }, nil
}

func localEndpoint(l control.BundleListener) (string, error) {
	ip, err := netip.ParseAddr(l.Address)
	if err != nil || ip.Zone() != "" || l.Port == 0 {
		return "", errProbe
	}
	if ip.IsUnspecified() {
		ip = netip.MustParseAddr("::1")
		if l.Address == "0.0.0.0" {
			ip = netip.MustParseAddr("127.0.0.1")
		}
	}
	return netip.AddrPortFrom(ip, l.Port).String(), nil
}

func (p *Probes) proxy(ctx context.Context, l control.BundleListener, wrong bool) (map[string]any, error) {
	addr, err := localEndpoint(l)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(addr)
	password := l.Credential.Password
	uuid := l.Credential.UUID
	if wrong {
		var b [32]byte
		if p.random(b[:]) != nil {
			return nil, errProbe
		}
		n := 32
		if l.Cipher == "2022-blake3-aes-128-gcm" {
			n = 16
		}
		password = base64.StdEncoding.EncodeToString(b[:n])
		b[6] = (b[6] & 15) | 64
		b[8] = (b[8] & 63) | 128
		uuid = fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:16])
		if (l.Credential.Password != "" && password == l.Credential.Password) || uuid == l.Credential.UUID {
			return nil, errProbe
		}
	}
	out := map[string]any{"name": "selected", "server": host, "port": l.Port, "udp": l.BusinessUDP}
	switch core.Protocol(l.Protocol) {
	case core.SS2022:
		out["type"], out["cipher"], out["password"] = "ss", l.Cipher, password
	case core.Trojan, core.Hysteria2:
		if l.Certificate == nil {
			return nil, errProbe
		}
		cert, err := p.options.Materials.Resolve(ctx, l.NodeID, *l.Certificate)
		if err != nil {
			return nil, errProbe
		}
		dir, err := checkpoint.OpenPrivateDir(filepath.Dir(cert))
		if err != nil {
			return nil, errProbe
		}
		data, err := dir.Read(filepath.Base(cert), 256<<10)
		dir.Close()
		block, _ := pem.Decode(data)
		if err != nil || block == nil || block.Type != "CERTIFICATE" {
			return nil, errProbe
		}
		hash := sha256.Sum256(block.Bytes)
		out["type"], out["password"], out["fingerprint"] = l.Protocol, password, hex.EncodeToString(hash[:])
		if cert, err := x509.ParseCertificate(block.Bytes); err == nil && len(cert.DNSNames) > 0 {
			out["sni"] = cert.DNSNames[0]
		} else {
			out["sni"] = host
		}
	case core.VLESS:
		if l.RealityKey == nil || len(l.ServerNames) == 0 || len(l.ShortIDs) == 0 {
			return nil, errProbe
		}
		key, err := p.options.Materials.Resolve(ctx, l.NodeID, *l.RealityKey)
		if err != nil {
			return nil, errProbe
		}
		b, err := base64.RawURLEncoding.DecodeString(key)
		if err != nil {
			return nil, errProbe
		}
		k, err := ecdh.X25519().NewPrivateKey(b)
		if err != nil {
			return nil, errProbe
		}
		out["type"], out["uuid"], out["tls"], out["flow"] = "vless", uuid, true, "xtls-rprx-vision"
		out["servername"], out["client-fingerprint"] = l.ServerNames[0], "chrome"
		out["reality-opts"] = map[string]string{"public-key": base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), "short-id": l.ShortIDs[0]}
	default:
		return nil, errProbe
	}
	return out, nil
}

type probeLog struct {
	mu          sync.Mutex
	hy2Rejected bool
	tail        []byte
}

func (l *probeLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	const marker = "authentication failed, status code: 404"
	buf := append(l.tail, data...)
	l.hy2Rejected = l.hy2Rejected || bytes.Contains(buf, []byte(marker))
	if len(buf) > len(marker) {
		buf = buf[len(buf)-len(marker):]
	}
	l.tail = append(l.tail[:0], buf...)
	return len(data), nil
}
func (l *probeLog) rejected() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.hy2Rejected
}

type probeClient struct {
	done    <-chan struct{}
	address string
	auth    string
	log     *probeLog
	stop    func()
}

func (p *Probes) startClient(ctx context.Context, proxy map[string]any) (*probeClient, error) {
	dir, err := os.MkdirTemp(p.options.Dir, "probe-")
	if err != nil {
		return nil, errProbe
	}
	remove := true
	defer func() {
		if remove {
			os.RemoveAll(dir)
		}
	}()
	binary, err := pinnedBinary(p.options.Binary)
	if err != nil {
		return nil, err
	}
	defer binary.Close()
	var auth [16]byte
	if p.random(auth[:]) != nil {
		return nil, errProbe
	}
	reservation, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, errProbe
	}
	port := reservation.Addr().(*net.TCPAddr).Port
	address := reservation.Addr().String()
	token := hex.EncodeToString(auth[:])
	config := map[string]any{
		"port": 0, "socks-port": port, "allow-lan": false, "bind-address": "127.0.0.1",
		"authentication": []string{"meshstrata:" + token}, "mode": "rule", "log-level": "warning",
		"tun": map[string]bool{"enable": false}, "dns": map[string]bool{"enable": false},
		"profile": map[string]bool{"store-selected": false, "store-fake-ip": false},
		"proxies": []any{proxy}, "rules": []string{"MATCH,selected"},
	}
	path := filepath.Join(dir, "client.json")
	b, err := json.Marshal(config)
	if err == nil {
		err = os.WriteFile(path, b, 0600)
	}
	reservation.Close()
	if err != nil {
		return nil, errProbe
	}
	log := &probeLog{}
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", "-d", dir, "-f", path)
	cmd.Args[0] = p.options.Binary
	cmd.ExtraFiles = []*os.File{binary}
	cmd.Env = []string{"GOMAXPROCS=1"}
	cmd.Dir = dir
	cmd.SysProcAttr = &unix.SysProcAttr{Pdeathsig: unix.SIGKILL}
	cmd.Stdout, cmd.Stderr = log, log
	if cmd.Start() != nil {
		return nil, errProbe
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	c := &probeClient{address: address, auth: token, log: log, done: done, stop: func() {
		_ = cmd.Process.Kill()
		<-done
		os.RemoveAll(dir)
	}}
	remove = false
	for {
		conn, err := c.connect(ctx)
		if err == nil {
			conn.Close()
			return c, nil
		}
		select {
		case <-done:
			c.stop()
			return nil, errProbe
		case <-ctx.Done():
			c.stop()
			return nil, ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
}

type observation int

const (
	unknown observation = iota
	forwarded
	denied
)

func (p *Probes) observe(ctx context.Context, l control.BundleListener, target, network string, wrong bool) observation {
	ctx, cancel := context.WithTimeout(ctx, p.options.Timeout)
	defer cancel()
	proxy, err := p.proxy(ctx, l, wrong)
	if err != nil {
		return unknown
	}
	client, err := p.startClient(ctx, proxy)
	if err != nil {
		return unknown
	}
	defer client.stop()
	conn, err := client.connect(ctx)
	if err != nil {
		return unknown
	}
	defer conn.Close()
	stopIO := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopIO()
	var payload [64]byte
	if p.random(payload[:]) != nil {
		return unknown
	}
	switch network {
	case "udp":
		if probeUDP(ctx, conn, target, payload[:]) {
			return forwarded
		}
	case "tcp":
		rep, _, err := socksRequest(conn, 1, target)
		succeeded := err == nil && rep == 0
		if succeeded {
			buf := make([]byte, len(payload))
			if _, err = conn.Write(payload[:]); err == nil {
				_, err = io.ReadFull(conn, buf)
				if err == nil && bytes.Equal(payload[:], buf) {
					return forwarded
				}
			}
		}
		if core.Protocol(l.Protocol) == core.Hysteria2 {
			// A fresh process and explicit HTTP/3 authentication denial are
			// required. A QUIC timeout or SOCKS failure alone is inconclusive.
			until := time.NewTimer(150 * time.Millisecond)
			defer until.Stop()
			tick := time.NewTicker(10 * time.Millisecond)
			defer tick.Stop()
			for {
				if client.log.rejected() {
					return denied
				}
				select {
				case <-ctx.Done():
					return unknown
				case <-until.C:
					return unknown
				case <-tick.C:
				}
			}
		}
		if succeeded && ctx.Err() == nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET)) {
			select {
			case <-client.done:
				return unknown
			default:
				return denied
			}
		}
	}
	return unknown
}
