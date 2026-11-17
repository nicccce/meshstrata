package mihomo

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"golang.org/x/sys/unix"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	core "github.com/nicccce/meshstrata/internal/mihomo"
)

type Status string

const (
	Succeeded  Status = "success"
	Failed     Status = "failure"
	Unverified Status = "unverified"
)

type Check struct {
	Status Status `json:"status"`
	Code   string `json:"code,omitempty"`
}
type Endpoint struct {
	Address   string `json:"address"`
	Port      uint16 `json:"port"`
	Transport string `json:"transport"`
	// Nil if IPV6_V6ONLY was not observed on this socket.
	IPv6Only *bool `json:"ipv6_only,omitempty"`
}

// Matches keeps the bind scope exact, apart from an IPv6 wildcard with
// positive per-socket proof that it also binds the IPv4 wildcard.
func (p Endpoint) Matches(address string, port uint16, transport string) bool {
	got, err := netip.ParseAddr(p.Address)
	want, wantErr := netip.ParseAddr(address)
	if err != nil || wantErr != nil || got.Is4In6() || want.Is4In6() ||
		got.Zone() != "" || want.Zone() != "" || p.Port != port || p.Transport != transport {
		return false
	}
	return got == want || p.IPv6Only != nil && !*p.IPv6Only && got == netip.IPv6Unspecified() && want == netip.IPv4Unspecified()
}

type ListenerEvidence struct {
	Expected          core.Expected `json:"expected"`
	Listening         Check         `json:"listening"`
	AuthenticationTCP Check         `json:"authentication_tcp"`
	BusinessUDP       Check         `json:"business_udp"`
}
type Evidence struct {
	Digest       string             `json:"digest"`
	CheckedAt    time.Time          `json:"checked_at"`
	PID          int                `json:"pid"`
	ProcessStart string             `json:"process_start,omitempty"`
	Listeners    []ListenerEvidence `json:"listeners"`
	OwnedSockets []Endpoint         `json:"owned_sockets"`
	Verified     bool               `json:"verified"`
}

func fmtPort(p uint16) string { return strconv.Itoa(int(p)) }
func procStart(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", ErrInspection
	}
	idx := strings.LastIndex(string(b), ")")
	if idx < 0 {
		return "", ErrInspection
	}
	f := strings.Fields(string(b[idx+1:]))
	if len(f) < 20 {
		return "", ErrInspection
	}
	return f[19], nil
}

// Inspect reads sockets owned by the selected kernel PID, not mere host port
// occupancy. Permission/OS errors fail closed. Linux /proc is required; Alpine
// uses the same interface, but real Alpine acceptance belongs to T01.
func (a *Adapter) Inspect(ctx context.Context, c Candidate, pid int) (Evidence, error) {
	out := Evidence{Digest: c.Digest, CheckedAt: time.Now().UTC(), PID: pid}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if pid <= 0 {
		return out, ErrInspection
	}
	b, err := a.file(c.Path)
	if err != nil {
		return out, err
	}
	if core.Digest(b) != c.Digest {
		return out, core.ErrInvalid
	}
	if err = a.policy(b, c.Expected); err != nil {
		return out, err
	}
	pidfd, err := unix.PidfdOpen(pid, 0)
	if err != nil {
		return out, ErrInspection
	}
	defer unix.Close(pidfd)
	base := filepath.Join("/proc", strconv.Itoa(pid))
	exe, err := os.Readlink(filepath.Join(base, "exe"))
	if err != nil || exe != a.o.Binary {
		return out, ErrInspection
	}
	cmd, err := os.ReadFile(filepath.Join(base, "cmdline"))
	if err != nil {
		return out, ErrInspection
	}
	args := strings.Split(string(cmd), "\x00")
	owned := false
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-d" && args[i+1] == a.o.WorkDir {
			owned = true
		}
	}
	if !owned {
		return out, ErrInspection
	}
	start, err := procStart(pid)
	if err != nil {
		return out, err
	}
	out.ProcessStart = start
	out.OwnedSockets, err = inspectSockets(ctx, base, c.Expected)
	end, endErr := procStart(pid)
	if err != nil || endErr != nil || start != end || unix.PidfdSendSignal(pidfd, 0, nil, 0) != nil {
		return out, ErrInspection
	}
	for _, e := range c.Expected {
		check := Check{Status: Succeeded}
		for _, transport := range []string{"tcp", "udp"} {
			if (transport == "tcp" && !e.TCP) || (transport == "udp" && !e.UDP) {
				continue
			}
			found, unknown := false, false
			for _, p := range out.OwnedSockets {
				found = found || p.Matches(e.Address, e.Port, transport)
				unknown = unknown || e.Address == "0.0.0.0" && p.Address == "::" &&
					p.IPv6Only == nil && p.Port == e.Port && p.Transport == transport
			}
			if !found {
				if !unknown {
					check = Check{Status: Failed, Code: "OWNED_LISTENER_MISSING"}
					break
				}
				check = Check{Status: Unverified, Code: "OWNED_IPV4_COVERAGE_UNKNOWN"}
			}
		}
		out.Listeners = append(out.Listeners, ListenerEvidence{e, check, Check{Status: Unverified}, Check{Status: Unverified}})
	}
	return out, nil
}

func inspectSockets(ctx context.Context, base string, expected []core.Expected) ([]Endpoint, error) {
	fds, err := os.ReadDir(filepath.Join(base, "fd"))
	if err != nil {
		return nil, ErrInspection
	}
	inodes := map[string]string{}
	for _, fd := range fds {
		path := filepath.Join(base, "fd", fd.Name())
		v, err := os.Readlink(path)
		if errors.Is(err, os.ErrNotExist) {
			continue // an FD closed between enumeration and readlink
		}
		if err != nil {
			return nil, ErrInspection
		}
		if strings.HasPrefix(v, "socket:[") && strings.HasSuffix(v, "]") {
			inodes[v[8:len(v)-1]] = path
		}
	}
	type owned struct {
		ep    Endpoint
		inode string
		table string
	}
	var sockets []owned
	diag := map[string]map[diagSocket]diagFacts{}
	for _, table := range []string{"tcp", "tcp6", "udp", "udp6"} {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		f, err := os.Open(filepath.Join(base, "net", table))
		if err != nil {
			return nil, ErrInspection
		}
		reader := &io.LimitedReader{R: f, N: 16 << 20}
		s := bufio.NewScanner(reader)
		s.Scan()
		for s.Scan() {
			fields := strings.Fields(s.Text())
			if len(fields) < 10 || inodes[fields[9]] == "" {
				continue
			}
			transport := "tcp"
			if strings.HasPrefix(table, "udp") {
				transport = "udp"
			}
			if transport == "tcp" && fields[3] != "0A" {
				continue
			}
			if transport == "udp" {
				remote, ok := parseEndpoint(fields[2], transport)
				if !ok || fields[3] != "07" || remote.Port != 0 ||
					(remote.Address != "::" && remote.Address != "0.0.0.0") {
					continue
				}
			}
			ep, ok := parseEndpoint(fields[1], transport)
			if !ok || ep.Port == 0 {
				f.Close()
				return nil, ErrInspection
			}
			sockets = append(sockets, owned{ep, fields[9], table})
			for _, want := range expected {
				if want.Port != ep.Port || !(transport == "udp" && want.UDP ||
					transport == "tcp" && want.TCP && ep.Address == "::" && want.Address == "0.0.0.0") {
					continue
				}
				if key, ok := socketKey(ep, fields[9]); ok {
					if diag[table] == nil {
						diag[table] = map[diagSocket]diagFacts{}
					}
					diag[table][key] = diagFacts{}
				}
				break
			}
		}
		err = s.Err()
		f.Close()
		if err != nil || reader.N <= 0 {
			return nil, ErrInspection
		}
	}
	sameNS := len(diag) > 0 && sameNetNS(base)
	for table, wanted := range diag {
		if !sameNS || socketFacts(ctx, table, wanted) != nil {
			if strings.HasPrefix(table, "udp") {
				return nil, ErrInspection
			}
			// Unavailable or incomplete diagnostics keep IPv4 coverage unknown.
			delete(diag, table)
		}
	}
	var out []Endpoint
	for _, socket := range sockets {
		ep := socket.ep
		key, _ := socketKey(ep, socket.inode)
		facts := diag[socket.table][key]
		if ep.Transport == "udp" && !facts.found {
			// Connected, auto-bound and unrelated UDP sockets cannot
			// prove ownership of a configured fixed listener.
			continue
		}
		// Require the FD to still reference the same inode after both reads.
		if link, err := os.Readlink(inodes[socket.inode]); err != nil || link != "socket:["+socket.inode+"]" {
			// A partial inventory cannot prove there is no unexpected TCP listener.
			return nil, ErrInspection
		}
		ep.IPv6Only = facts.v6only
		out = append(out, ep)
	}
	return out, nil
}
func parseEndpoint(raw, transport string) (Endpoint, bool) {
	parts := strings.Split(raw, ":")
	if len(parts) != 2 {
		return Endpoint{}, false
	}
	b, err := hex.DecodeString(parts[0])
	if err != nil || len(b) != 4 && len(b) != 16 {
		return Endpoint{}, false
	}
	// Linux prints address words in native byte order.
	for i := 0; i < len(b); i += 4 {
		v := binary.NativeEndian.Uint32(b[i : i+4])
		binary.BigEndian.PutUint32(b[i:i+4], v)
	}
	addr, ok := netip.AddrFromSlice(b)
	p, err := strconv.ParseUint(parts[1], 16, 16)
	if !ok || err != nil {
		return Endpoint{}, false
	}
	return Endpoint{Address: addr.String(), Port: uint16(p), Transport: transport}, true
}

// Probe performs bounded, authenticated forwarding with a separately pinned
// external client. It must compare returned payloads from a controlled target;
// connection establishment or an API/port check alone is not success.
// Implementations MUST honor ctx; B05 owns client provisioning and credentials.
type Probe interface {
	Forward(context.Context, core.Expected, string) Check
}

func clean(c Check) Check {
	switch c.Status {
	case Succeeded, Failed, Unverified:
		return Check{Status: c.Status}
	default:
		return Check{Status: Unverified}
	}
}
func (a *Adapter) Verify(ctx context.Context, c Candidate, pid int, p Probe) (Evidence, error) {
	out, err := a.Inspect(ctx, c, pid)
	if err != nil {
		return out, err
	}
	for i := range out.Listeners {
		e := &out.Listeners[i]
		if e.Listening.Status != Succeeded || p == nil {
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, a.o.Timeout)
		e.AuthenticationTCP = clean(p.Forward(probeCtx, e.Expected, "tcp"))
		if probeCtx.Err() != nil {
			e.AuthenticationTCP = Check{Status: Unverified}
		}
		cancel()
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if e.Expected.BusinessUDP {
			probeCtx, cancel = context.WithTimeout(ctx, a.o.Timeout)
			e.BusinessUDP = clean(p.Forward(probeCtx, e.Expected, "udp"))
			if probeCtx.Err() != nil {
				e.BusinessUDP = Check{Status: Unverified}
			}
			cancel()
		}
	}
	out.Verified = len(out.Listeners) > 0
	for _, e := range out.Listeners {
		if e.Listening.Status != Succeeded || e.AuthenticationTCP.Status != Succeeded || (e.Expected.BusinessUDP && e.BusinessUDP.Status != Succeeded) {
			out.Verified = false
		}
	}
	return out, ctx.Err()
}

// Connection contains only attribution needed for precise cleanup. Raw API
// metadata (destinations, credentials, process names) never leaves the adapter.
type Connection struct {
	ID         string `json:"id"`
	ListenerID string `json:"listener_id"`
}

func (a *Adapter) Connections(ctx context.Context) ([]Connection, error) {
	b, err := a.request(ctx, "GET", "/connections", nil)
	if err != nil {
		return nil, err
	}
	var wire struct {
		Connections []struct {
			ID       string `json:"id"`
			Metadata struct {
				InboundName string `json:"inboundName"`
			} `json:"metadata"`
		} `json:"connections"`
	}
	if json.Unmarshal(b, &wire) != nil {
		return nil, ErrAPI
	}
	out := make([]Connection, 0, len(wire.Connections))
	for _, c := range wire.Connections {
		if !safeConnectionID(c.ID) || c.Metadata.InboundName == "" {
			return nil, ErrInspection
		}
		out = append(out, Connection{c.ID, c.Metadata.InboundName})
	}
	return out, nil
}
func safeConnectionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	return err == nil && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-'
}

// CloseConnection rechecks attribution and only closes a caller-selected session
// belonging to an explicit platform listener. Never use DELETE /connections.
func (a *Adapter) CloseConnection(ctx context.Context, c Candidate, listener, id string) error {
	b, err := a.file(c.Path)
	if err != nil {
		return err
	}
	if core.Digest(b) != c.Digest {
		return core.ErrInvalid
	}
	if err = a.policy(b, c.Expected); err != nil {
		return err
	}
	allowed := false
	for _, e := range c.Expected {
		if e.ListenerID == listener {
			allowed = true
		}
	}
	if !allowed || !safeConnectionID(id) {
		return core.ErrInvalid
	}
	conns, err := a.Connections(ctx)
	if err != nil {
		return err
	}
	for _, c := range conns {
		if c.ID == id {
			if c.ListenerID != listener {
				return core.ErrInvalid
			}
			_, err = a.request(ctx, "DELETE", "/connections/"+id, nil)
			return err
		}
	}
	return nil
}

type RevocationProbe interface {
	// A timeout or generic transport error is UNVERIFIED, not proof of denial.
	OldAuthenticationDenied(context.Context) Check
	ExistingSessionsClosed(context.Context) Check
	OtherIdentityUnaffected(context.Context) Check
}
type RevocationEvidence struct {
	Digest                  string    `json:"digest"`
	CheckedAt               time.Time `json:"checked_at"`
	OldAuthenticationDenied Check     `json:"old_authentication_denied"`
	ExistingSessionsClosed  Check     `json:"existing_sessions_closed"`
	OtherIdentityUnaffected Check     `json:"other_identity_unaffected"`
	Verified                bool      `json:"verified"`
}

func (a *Adapter) Revocation(ctx context.Context, c Candidate, p RevocationProbe) (RevocationEvidence, error) {
	out := RevocationEvidence{Digest: c.Digest, CheckedAt: time.Now().UTC(), OldAuthenticationDenied: Check{Status: Unverified}, ExistingSessionsClosed: Check{Status: Unverified}, OtherIdentityUnaffected: Check{Status: Unverified}}
	b, err := a.file(c.Path)
	if err != nil {
		return out, err
	}
	if core.Digest(b) != c.Digest {
		return out, core.ErrInvalid
	}
	if err = a.policy(b, c.Expected); err != nil {
		return out, err
	}
	if p == nil {
		return out, nil
	}
	for _, f := range []struct {
		dst *Check
		fn  func(context.Context) Check
	}{{&out.OldAuthenticationDenied, p.OldAuthenticationDenied}, {&out.ExistingSessionsClosed, p.ExistingSessionsClosed}, {&out.OtherIdentityUnaffected, p.OtherIdentityUnaffected}} {
		probeCtx, cancel := context.WithTimeout(ctx, a.o.Timeout)
		*f.dst = clean(f.fn(probeCtx))
		if probeCtx.Err() != nil {
			*f.dst = Check{Status: Unverified}
		}
		cancel()
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
	}
	out.Verified = out.OldAuthenticationDenied.Status == Succeeded && out.ExistingSessionsClosed.Status == Succeeded && out.OtherIdentityUnaffected.Status == Succeeded
	return out, nil
}
