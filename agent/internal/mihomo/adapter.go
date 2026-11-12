// Package mihomo controls only an explicitly configured platform kernel.
// It neither imports the kernel nor implements an init/service manager.
package mihomo

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	core "github.com/nicccce/meshstrata/internal/mihomo"
)

var (
	ErrOptions    = errors.New("MIHOMO_INVALID_OPTIONS")
	ErrPath       = errors.New("MIHOMO_UNSAFE_PATH")
	ErrMissing    = errors.New("MIHOMO_CONFIG_MISSING")
	ErrBinary     = errors.New("MIHOMO_BINARY_MISMATCH")
	ErrSyntax     = errors.New("MIHOMO_SYNTAX_FAILED")
	ErrAPI        = errors.New("MIHOMO_API_FAILED")
	ErrLimit      = errors.New("MIHOMO_RESPONSE_LIMIT")
	ErrInspection = errors.New("MIHOMO_INSPECTION_FAILED")
)

type Options struct {
	Binary, BinarySHA256                     string
	WorkDir, StateDir, ConfigDir, SocketPath string
	// TCPAddress is optional, numeric loopback only; Secret must have >=32 bytes.
	// It must match the STARTUP configuration; reload does not change API settings.
	TCPAddress, Secret string
	Timeout            time.Duration
	MaxResponseBytes   int64
}
type Adapter struct {
	o      Options
	client *http.Client
}

func New(o Options) (*Adapter, error) {
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	if o.MaxResponseBytes == 0 {
		o.MaxResponseBytes = 1 << 20
	}
	if o.Timeout <= 0 || o.Timeout > time.Minute || o.MaxResponseBytes < 1024 || o.MaxResponseBytes > 4<<20 {
		return nil, ErrOptions
	}
	h, err := hex.DecodeString(o.BinarySHA256)
	if err != nil || len(h) != 32 {
		return nil, ErrOptions
	}
	for _, p := range []string{o.WorkDir, o.StateDir, o.ConfigDir, filepath.Dir(o.SocketPath)} {
		if privateDir(p) != nil {
			return nil, ErrPath
		}
	}
	if !filepath.IsAbs(o.SocketPath) || len(o.SocketPath) > 100 || filepath.Base(o.SocketPath) == "." {
		return nil, ErrPath
	}
	if !filepath.IsAbs(o.Binary) || noLinks(o.Binary) != nil {
		return nil, ErrPath
	}
	st, err := os.Stat(o.Binary)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0022 != 0 || st.Mode().Perm()&0111 == 0 {
		return nil, ErrPath
	}
	if st, err := os.Lstat(o.SocketPath); err == nil {
		if st.Mode()&os.ModeSocket == 0 || st.Mode()&os.ModeSymlink != 0 {
			return nil, ErrPath
		}
	} else if !os.IsNotExist(err) {
		return nil, ErrPath
	}
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, MaxResponseHeaderBytes: 8192}
	if o.TCPAddress != "" {
		ap, err := netip.ParseAddrPort(o.TCPAddress)
		if err != nil || !ap.Addr().IsLoopback() || ap.Port() == 0 || len(o.Secret) < 32 || strings.ContainsAny(o.Secret, "\r\n") {
			return nil, ErrOptions
		}
		tr.DialContext = (&net.Dialer{Timeout: o.Timeout}).DialContext
	} else {
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			if privateDir(filepath.Dir(o.SocketPath)) != nil || noLinks(o.SocketPath) != nil {
				return nil, ErrPath
			}
			return (&net.Dialer{}).DialContext(ctx, "unix", o.SocketPath)
		}
	}
	a := &Adapter{o: o, client: &http.Client{Transport: tr, Timeout: o.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	return a, nil
}
func noLinks(p string) error {
	if !filepath.IsAbs(p) || filepath.Clean(p) != p {
		return ErrPath
	}
	for q := p; ; q = filepath.Dir(q) {
		s, err := os.Lstat(q)
		if err != nil || s.Mode()&os.ModeSymlink != 0 {
			return ErrPath
		}
		if q == "/" {
			break
		}
	}
	return nil
}
func privateDir(p string) error {
	if noLinks(p) != nil {
		return ErrPath
	}
	s, err := os.Stat(p)
	if err != nil || !s.IsDir() || s.Mode().Perm()&0077 != 0 {
		return ErrPath
	}
	return nil
}
func (a *Adapter) file(p string) ([]byte, error) {
	if privateDir(a.o.ConfigDir) != nil {
		return nil, ErrPath
	}
	rel, err := filepath.Rel(a.o.ConfigDir, p)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || rel == ".." || !filepath.IsAbs(p) {
		return nil, ErrPath
	}
	if _, err := os.Lstat(p); os.IsNotExist(err) {
		return nil, ErrMissing
	}
	if noLinks(p) != nil {
		return nil, ErrPath
	}
	for dir := filepath.Dir(p); ; dir = filepath.Dir(dir) {
		if privateDir(dir) != nil {
			return nil, ErrPath
		}
		if dir == a.o.ConfigDir {
			break
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, ErrPath
	}
	defer f.Close()
	s, err := f.Stat()
	if err != nil || !s.Mode().IsRegular() || s.Mode().Perm()&0077 != 0 {
		return nil, ErrPath
	}
	b, err := io.ReadAll(io.LimitReader(f, a.o.MaxResponseBytes+1))
	if err != nil {
		return nil, ErrPath
	}
	if int64(len(b)) > a.o.MaxResponseBytes {
		return nil, ErrLimit
	}
	return b, nil
}
func (a *Adapter) binary(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(a.o.Binary)
	if err != nil {
		return ErrBinary
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil || hex.EncodeToString(h.Sum(nil)) != a.o.BinarySHA256 {
		return ErrBinary
	}
	return nil
}

type Candidate struct {
	Path, Digest string
	Expected     []core.Expected
}
type Validated struct {
	Digest    string    `json:"digest"`
	CheckedAt time.Time `json:"checked_at"`
}

// Validate never accepts arbitrary kernel YAML. Unknown/duplicate JSON keys,
// general inbounds, TUN, iptables, DNS listeners and unapproved API addresses fail.
func (a *Adapter) Validate(ctx context.Context, c Candidate) (Validated, error) {
	ctx, cancel := context.WithTimeout(ctx, a.o.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Validated{}, err
	}
	b, err := a.file(c.Path)
	if err != nil {
		return Validated{}, err
	}
	if c.Digest != core.Digest(b) {
		return Validated{}, core.ErrInvalid
	}
	if err = a.policy(b, c.Expected); err != nil {
		return Validated{}, err
	}
	if err = a.binary(ctx); err != nil {
		return Validated{}, err
	}
	cmd := exec.CommandContext(ctx, a.o.Binary, "-t", "-d", a.o.WorkDir, "-f", c.Path)
	// Kernel diagnostics can include credentials, paths, and full listener configs.
	// Discard them. Only stable errors may leave this boundary.
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	cmd.WaitDelay = time.Second
	err = cmd.Run()
	if ctx.Err() != nil {
		return Validated{}, ctx.Err()
	}
	if err != nil {
		return Validated{}, ErrSyntax
	}
	return Validated{c.Digest, time.Now().UTC()}, nil
}
func rejectDuplicates(d *json.Decoder) error {
	t, err := d.Token()
	if err != nil {
		return err
	}
	if delim, ok := t.(json.Delim); ok {
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return core.ErrInvalid
				}
				seen[s] = true
				if err = rejectDuplicates(d); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err = rejectDuplicates(d); err != nil {
					return err
				}
			}
		default:
			return core.ErrInvalid
		}
		_, err = d.Token()
		return err
	}
	return nil
}
func (a *Adapter) policy(b []byte, expected []core.Expected) error {
	d := json.NewDecoder(bytes.NewReader(b))
	if rejectDuplicates(d) != nil {
		return core.ErrInvalid
	}
	if _, err := d.Token(); err != io.EOF {
		return core.ErrInvalid
	}
	var c core.Config
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil {
		return core.ErrInvalid
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(b, &keys)
	for _, k := range []string{"port", "socks-port", "mixed-port", "redir-port", "tproxy-port", "tun", "iptables", "dns", "external-controller", "external-controller-tls", "external-controller-unix", "listeners", "rules", "profile"} {
		if _, ok := keys[k]; !ok {
			return core.ErrInvalid
		}
	}
	if c.Port != 0 || c.SocksPort != 0 || c.MixedPort != 0 || c.RedirPort != 0 || c.TProxyPort != 0 || c.Tun.Enable || c.IPTables.Enable || c.AllowLAN || c.DNS.Listen != "" || !c.DNS.Enable || len(c.DNS.Nameserver) == 0 || len(c.DNS.DefaultNameserver) == 0 || c.DNS.EnhancedMode != "normal" || c.Controller != a.o.TCPAddress || c.ControllerTLS != "" || c.Socket != a.o.SocketPath || c.Secret != a.o.Secret || c.Mode != "rule" || c.LogLevel != "silent" || len(c.Rules) == 0 || len(c.Listeners) != len(expected) {
		return core.ErrInvalid
	}
	if len(c.Profile) != 2 {
		return core.ErrInvalid
	}
	if _, ok := c.Profile["store-selected"]; !ok {
		return core.ErrInvalid
	}
	if _, ok := c.Profile["store-fake-ip"]; !ok {
		return core.ErrInvalid
	}
	for _, v := range c.Profile {
		if v {
			return core.ErrInvalid
		}
	}
	// Routing is explicit, never a silent default DIRECT fallback.
	for _, r := range c.Rules {
		parts := strings.Split(r, ",")
		target := parts[len(parts)-1]
		if target != "DIRECT" && target != "REJECT" {
			return core.ErrInvalid
		}
	}
	if !strings.HasPrefix(c.Rules[len(c.Rules)-1], "MATCH,") {
		return core.ErrInvalid
	}
	names := map[string]bool{}
	endpoints := map[string]bool{}
	for i, l := range c.Listeners {
		e := expected[i]
		if e.NodeID == "" || e.ListenerID == "" || l.Name != e.ListenerID || names[l.Name] || l.Listen != e.Address || l.Port != e.Port {
			return core.ErrInvalid
		}
		names[l.Name] = true
		entry := core.Entry{NodeID: e.NodeID, ListenerID: l.Name, IdentityID: "identity", Address: l.Listen, Port: l.Port, Protocol: e.Protocol, BusinessUDP: e.BusinessUDP, Cipher: l.Cipher, Password: l.Password, Certificate: l.Certificate, PrivateKey: l.PrivateKey}
		if e.Protocol == core.Trojan || e.Protocol == core.VLESS {
			if len(l.Users) != 1 {
				return core.ErrInvalid
			}
			entry.IdentityID = l.Users[0].Username
			entry.Password = l.Users[0].Password
			entry.UUID = l.Users[0].UUID
		}
		if e.Protocol == core.Hysteria2 {
			if len(l.HyUsers) != 1 {
				return core.ErrInvalid
			}
			for k, v := range l.HyUsers {
				entry.IdentityID = k
				entry.Password = v
			}
		}
		if l.Reality != nil {
			entry.RealityDestination = l.Reality.Destination
			entry.RealityPrivateKey = l.Reality.PrivateKey
			entry.ServerNames = l.Reality.ServerNames
			entry.ShortIDs = l.Reality.ShortIDs
		}
		rebuilt, err := core.BuildListener(entry)
		if err != nil {
			return err
		}
		x, _ := json.Marshal(l)
		y, _ := json.Marshal(rebuilt)
		if !bytes.Equal(x, y) {
			return core.ErrInvalid
		}
		tcp := e.Protocol != core.Hysteria2
		udp := e.Protocol == core.Hysteria2 || (e.Protocol == core.SS2022 && e.BusinessUDP)
		if e.TCP != tcp || e.UDP != udp {
			return core.ErrInvalid
		}
		for _, transport := range []struct {
			yes  bool
			name string
		}{{tcp, "tcp"}, {udp, "udp"}} {
			if transport.yes {
				key := transport.name + net.JoinHostPort(e.Address, fmtPort(e.Port))
				if endpoints[key] {
					return core.ErrInvalid
				}
				endpoints[key] = true
			}
		}
		if entry.Certificate != "" {
			cert, err := a.file(entry.Certificate)
			if err != nil {
				return err
			}
			key, err := a.file(entry.PrivateKey)
			if err != nil {
				return err
			}
			_, err = tls.X509KeyPair(cert, key)
			clear(key)
			if err != nil {
				return core.ErrInvalid
			}
		}
	}
	return nil
}

// Startup validates before returning an explicit command for B17. It never
// starts/stops services; B05 owns atomic startup config and checkpoint handling.
func (a *Adapter) Startup(ctx context.Context, c Candidate) (string, []string, error) {
	if _, err := a.Validate(ctx, c); err != nil {
		return "", nil, err
	}
	return a.o.Binary, []string{"-d", a.o.WorkDir, "-f", c.Path}, nil
}

type Applied struct {
	Digest      string    `json:"digest"`
	AttemptedAt time.Time `json:"attempted_at"`
	APIAccepted bool      `json:"api_accepted"`
}

func (a *Adapter) Reload(ctx context.Context, c Candidate) (Applied, error) {
	if _, err := a.Validate(ctx, c); err != nil {
		return Applied{}, err
	}
	// B05 must own the config directory and serialize writers throughout this call.
	b, err := a.file(c.Path)
	if err != nil || core.Digest(b) != c.Digest {
		return Applied{}, core.ErrInvalid
	}
	body, _ := json.Marshal(map[string]string{"path": c.Path})
	out := Applied{Digest: c.Digest, AttemptedAt: time.Now().UTC()}
	_, err = a.request(ctx, http.MethodPut, "/configs?force=true", body)
	out.APIAccepted = err == nil
	return out, err
}
func (a *Adapter) request(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, a.o.Timeout)
	defer cancel()
	host := "mihomo"
	if a.o.TCPAddress != "" {
		host = a.o.TCPAddress
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+host+path, bytes.NewReader(body))
	if err != nil {
		return nil, ErrAPI
	}
	req.Header.Set("Content-Type", "application/json")
	if a.o.TCPAddress != "" {
		req.Header.Set("Authorization", "Bearer "+a.o.Secret)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrAPI
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, a.o.MaxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrAPI
	}
	if int64(len(b)) > a.o.MaxResponseBytes {
		return nil, ErrLimit
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ErrAPI
	}
	return b, nil
}
