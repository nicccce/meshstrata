package runtime

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"golang.org/x/sys/unix"
)

// Trusted local provisioning bounds this plan. Wire requests may select approved
// DNS, sockets and target credentials, but cannot supply topology or probe addresses.
type ObservationPlanV2 struct {
	NodeID   domain.ID
	Revision domain.Revision
	DNS      control.DNSInput
	Sockets  []control.PinnedSocketSpecV2
	Targets  []ObservedTargetV2
}
type ObservedTargetV2 struct {
	Pin  control.Pin
	Spec control.EgressTarget
	Auth *control.EgressAuth
}
type ObservationOptionsV2 struct {
	Binary string
	Plan   func(context.Context) (ObservationPlanV2, error)
	Probes []netip.AddrPort // independent, fixed numeric echo canaries
}

func matchesObservationPin(pin control.Pin, value any) bool {
	b, err := json.Marshal(value)
	if err != nil || !pin.Valid() {
		return false
	}
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:]) == pin.Digest
}

// ObserveV2 reports actual host/owned-process inventory or a bounded TCP echo
// through one exact egress target. TCP success never attests business UDP.
func (r *Runtime) ObserveV2(ctx context.Context, req control.ObservationRequestV2) (control.ObservationReportV2, error) {
	var out control.ObservationReportV2
	o := r.o.Observations
	if !r.available() || !r.o.EnableV2 || o == nil || o.Plan == nil || !req.Valid() || !req.Deadline.After(r.o.Now()) || req.Deadline.After(r.o.Now().Add(30*time.Second)) {
		return out, ErrUnavailable
	}
	ctx, cancel := context.WithDeadline(ctx, req.Deadline)
	defer cancel()
	ctx, finish := r.operation(ctx)
	defer finish()
	plan, err := o.Plan(ctx)
	if err != nil || plan.NodeID != r.o.NodeID || !plan.Revision.Valid() || !matchesObservationPin(plan.DNS.Pin, control.DNSPolicy{Bootstrap: plan.DNS.Bootstrap, Resolvers: plan.DNS.Resolvers, IPv6: plan.DNS.IPv6}) {
		return out, ErrUnavailable
	}
	original := plan
	if p := req.Preparation; p != nil {
		// Pin identity may advance while its content must remain locally approved.
		wantDNS, haveDNS := p.DNS, plan.DNS
		wantDNS.Pin, haveDNS.Pin = control.Pin{}, control.Pin{}
		if !reflect.DeepEqual(wantDNS, haveDNS) || !matchesObservationPin(p.DNS.Pin, control.DNSPolicy{Bootstrap: p.DNS.Bootstrap, Resolvers: p.DNS.Resolvers, IPv6: p.DNS.IPv6}) {
			return out, ErrUnavailable
		}
		for _, requested := range p.Sockets {
			allowed := false
			for _, local := range plan.Sockets {
				if requested.Protocol == local.Protocol && reflect.DeepEqual(requested.Spec, local.Spec) && requested.PoolFirst == local.PoolFirst && requested.PoolLast == local.PoolLast {
					allowed = true
					break
				}
			}
			if !allowed {
				return out, ErrUnavailable
			}
		}
		if selected := p.Target; selected != nil {
			if req.Target == nil || selected.Pin != *req.Target || !matchesObservationPin(selected.Pin, selected.Spec) {
				return out, ErrUnavailable
			}
			allowed := false
			want := selected.Spec
			want.CredentialRef = ""
			for _, local := range original.Targets {
				have := local.Spec
				have.CredentialRef = ""
				if reflect.DeepEqual(want, have) {
					allowed = true
					break
				}
			}
			if !allowed {
				return out, ErrUnavailable
			}
			plan.Targets = []ObservedTargetV2{{Pin: selected.Pin, Spec: selected.Spec, Auth: selected.Auth}}
		}
		plan.Revision, plan.DNS, plan.Sockets = p.NodeRevision, p.DNS, p.Sockets
	}
	// Serialize owned-process observation with apply/recovery. A failed or busy
	// observation never changes the execution ledger or grants capability.
	if err := acquire(ctx, r.kernelGate); err != nil {
		return out, err
	}
	defer release(r.kernelGate)
	if r.urgent.Load() {
		return out, errRestricted
	}
	topology, err := r.loopInventory(ctx)
	if err != nil {
		return out, err
	}
	loops, err := ObserveLoopInventory(ctx, topology)
	if err != nil {
		return out, err
	}
	interfaces, err := observedInterfaces(ctx)
	if err != nil {
		return out, err
	}
	out.ID = req.ID
	if req.Target == nil {
		f, err := pinnedBinary(o.Binary)
		if err != nil {
			return control.ObservationReportV2{}, err
		}
		f.Close()
		if len(plan.Sockets) == 0 || len(plan.Sockets) > 32 {
			return control.ObservationReportV2{}, ErrUnavailable
		}
		caps := []control.ProtocolCapabilityV2{}
		seen := map[domain.Protocol]bool{}
		for _, s := range plan.Sockets {
			if !matchesObservationPin(s.Pin, s.Spec) || s.PoolLast < s.PoolFirst || s.PoolFirst == 0 && s.PoolLast != 0 || r.checkObservedSocket(ctx, s) != nil {
				return control.ObservationReportV2{}, ErrUnavailable
			}
			if !seen[s.Protocol] {
				caps = append(caps, control.ProtocolCapabilityV2{Protocol: s.Protocol, Available: true, BusinessUDP: domain.NotChecked})
				seen[s.Protocol] = true
			}
		}
		now := r.o.Now().UTC()
		out.Inventory = &control.InventoryV2{NodeID: plan.NodeID, NodeRevision: plan.Revision, DNS: plan.DNS, Sockets: plan.Sockets, Capabilities: caps, Loops: loops, Interfaces: interfaces, ObservedAt: now, ExpiresAt: now.Add(time.Minute)}
	} else {
		if len(o.Probes) == 0 || len(o.Probes) > 8 || len(plan.Targets) > control.MaxEgressTargets {
			return control.ObservationReportV2{}, ErrUnavailable
		}
		var selected *ObservedTargetV2
		for i := range plan.Targets {
			if plan.Targets[i].Pin == *req.Target {
				if selected != nil {
					return control.ObservationReportV2{}, ErrUnavailable
				}
				selected = &plan.Targets[i]
			}
		}
		if selected == nil || selected.Spec.NodeID != r.o.NodeID || !selected.Spec.Valid() || !matchesObservationPin(selected.Pin, selected.Spec) || control.CheckEgressTargetLoops(selected.Spec, loops, nil) != nil {
			return control.ObservationReportV2{}, ErrUnavailable
		}
		probe := o.Probes[0]
		if !probe.IsValid() || probe.Port() == 0 || probe.Addr().IsUnspecified() || probe.Addr().IsMulticast() {
			return control.ObservationReportV2{}, ErrUnavailable
		}
		// The canary is checked independently of the proxy endpoint.
		canary := control.EgressTarget{NodeID: plan.NodeID, Type: "http", Host: probe.Addr().String(), DialIP: probe.Addr().String(), Port: probe.Port()}
		if control.CheckEgressTargetLoops(canary, loops, nil) != nil {
			return control.ObservationReportV2{}, ErrUnavailable
		}
		source, err := probeObservedTarget(ctx, *selected, probe)
		if err != nil {
			return control.ObservationReportV2{}, err
		}
		if selected.Auth != nil {
			bad := *selected
			auth := *selected.Auth
			var secret [24]byte
			if _, err := rand.Read(secret[:]); err != nil {
				return control.ObservationReportV2{}, ErrUnavailable
			}
			auth.Password = base64.RawStdEncoding.EncodeToString(secret[:])
			if auth.Password == selected.Auth.Password {
				return control.ObservationReportV2{}, ErrUnavailable
			}
			bad.Auth = &auth
			if _, err := probeObservedTarget(ctx, bad, probe); !errors.Is(err, errObservationDenied) {
				return control.ObservationReportV2{}, ErrUnavailable
			}
		}
		iface := ""
		for _, i := range interfaces {
			if i.Up && i.Address == source && (selected.Spec.Type != "interface" || i.Name == selected.Spec.Interface) {
				iface = i.Name
				break
			}
		}
		if iface == "" || slices.Contains(loops.ProtectedInterfaces, iface) || !slices.Contains(loops.LocalAddresses, source) {
			return control.ObservationReportV2{}, ErrUnavailable
		}
		now := r.o.Now().UTC()
		out.Egress = &control.EgressObservationV2{NodeID: plan.NodeID, NodeRevision: plan.Revision, DNS: plan.DNS.Pin, Target: selected.Pin, Interface: iface, SourceIP: source, Probe: probe, ObservedAt: now, ExpiresAt: now.Add(time.Minute), BusinessUDP: false}
	}
	latest, err := o.Plan(ctx)
	if err != nil || !reflect.DeepEqual(original, latest) || ctx.Err() != nil || !out.Valid(r.o.NodeID) {
		return control.ObservationReportV2{}, ErrUnavailable
	}
	return out, nil
}

func observedInterfaces(ctx context.Context) ([]control.InterfaceObservationV2, error) {
	list, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := []control.InterfaceObservationV2{}
	for _, iface := range list {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		addresses, err := iface.Addrs()
		if err != nil {
			return nil, err
		}
		for _, addr := range addresses {
			p, err := netip.ParsePrefix(addr.String())
			if err != nil {
				return nil, err
			}
			out = append(out, control.InterfaceObservationV2{Name: iface.Name, Address: p.Addr().Unmap(), Up: iface.Flags&net.FlagUp != 0})
		}
	}
	if len(out) == 0 || len(out) > 512 {
		return nil, ErrUnavailable
	}
	return out, nil
}

func (r *Runtime) checkObservedSocket(ctx context.Context, s control.PinnedSocketSpecV2) error {
	// Synthetic values validate kernel syntax only; they never enter a Grant,
	// artifact, listener process, checkpoint or report.
	e := core.Entry{NodeID: string(r.o.NodeID), ListenerID: string(s.Pin.ID), IdentityID: string(s.Pin.ID), Protocol: core.Protocol(s.Protocol), Address: s.Spec.Address, Port: s.Spec.RequestedPort, Cipher: "2022-blake3-aes-256-gcm", Password: base64.StdEncoding.EncodeToString(make([]byte, 32)), UUID: "00000000-0000-4000-8000-000000000001", RealityDestination: s.Spec.RealityDestination, ServerNames: s.Spec.ServerNames, ShortIDs: s.Spec.ShortIDs}
	if e.Port == 0 {
		e.Port = s.PoolFirst
		if e.Port == 0 {
			e.Port = 1
		}
	}
	if s.Protocol == domain.Trojan || s.Protocol == domain.Hysteria2 {
		e.Cipher = ""
		e.UUID = ""
	}
	if s.Protocol == domain.VLESS {
		e.Cipher = ""
		e.Password = ""
	}
	if s.Protocol == domain.SS2022 {
		e.UUID = ""
	}
	for _, item := range []struct {
		ref *control.LocalMaterial
		dst *string
	}{{s.Spec.Certificate, &e.Certificate}, {s.Spec.PrivateKey, &e.PrivateKey}, {s.Spec.RealityKey, &e.RealityPrivateKey}} {
		if item.ref != nil {
			value, err := r.o.Materials.Resolve(ctx, r.o.NodeID, *item.ref)
			if err != nil {
				return err
			}
			*item.dst = value
		}
	}
	if e.Certificate != "" && authorization.CheckTLSLeaf(e.Certificate, e.PrivateKey, s.Spec.TLSLeafSHA256) != nil {
		return ErrUnavailable
	}
	_, err := core.BuildListener(e)
	return err
}

var errObservationDenied = errors.New("AGENT_EGRESS_AUTHENTICATION_DENIED")

func probeObservedTarget(ctx context.Context, target ObservedTargetV2, probe netip.AddrPort) (netip.Addr, error) {
	var none netip.Addr
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dialer := &net.Dialer{}
	if target.Spec.Type == "interface" {
		dialer.Control = func(_, _ string, raw syscall.RawConn) error {
			var err error
			if e := raw.Control(func(fd uintptr) {
				err = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, target.Spec.Interface)
			}); e != nil {
				return e
			}
			return err
		}
	}
	username, password := "", ""
	if target.Spec.CredentialRef != "" {
		a := target.Auth
		if a == nil || a.Target != target.Pin || a.Ref != target.Spec.CredentialRef || a.Audience != target.Spec.NodeID || a.Username == "" || len(a.Username) > 255 || len(a.Password) > 255 || strings.Contains(a.Username, ":") || strings.ContainsAny(a.Username+a.Password, "\r\n\x00") {
			return none, ErrUnavailable
		}
		username, password = a.Username, a.Password
	} else if target.Auth != nil {
		return none, ErrUnavailable
	}
	address := probe.String()
	if target.Spec.Type == "http" || target.Spec.Type == "socks5" {
		address = netip.AddrPortFrom(netip.MustParseAddr(target.Spec.DialIP), target.Spec.Port).String()
	}
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return none, ErrUnavailable
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	source := conn.LocalAddr().(*net.TCPAddr).AddrPort().Addr().Unmap()
	var input io.Reader = conn
	if target.Spec.Type == "http" {
		header := "CONNECT " + probe.String() + " HTTP/1.1\r\nHost: " + probe.String() + "\r\n"
		if target.Auth != nil {
			header += "Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password)) + "\r\n"
		}
		if _, err := io.WriteString(conn, header+"\r\n"); err != nil {
			return none, ErrUnavailable
		}
		reader := bufio.NewReader(io.LimitReader(conn, 8192))
		response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
		if err == nil && response.StatusCode == http.StatusProxyAuthRequired {
			return none, errObservationDenied
		}
		if err != nil || response.StatusCode != 200 {
			return none, ErrUnavailable
		}
		input = reader
	} else if target.Spec.Type == "socks5" {
		method := byte(0)
		if target.Auth != nil {
			method = 2
		}
		if _, err := conn.Write([]byte{5, 1, method}); err != nil {
			return none, ErrUnavailable
		}
		var reply [2]byte
		if _, err := io.ReadFull(conn, reply[:]); err != nil || reply[0] != 5 || reply[1] != method {
			return none, ErrUnavailable
		}
		if method == 2 {
			auth := append([]byte{1, byte(len(username))}, []byte(username)...)
			auth = append(auth, byte(len(password)))
			auth = append(auth, []byte(password)...)
			_, err := conn.Write(auth)
			clear(auth)
			if err != nil {
				return none, ErrUnavailable
			}
			if _, err := io.ReadFull(conn, reply[:]); err != nil || reply[0] != 1 {
				return none, ErrUnavailable
			}
			if reply[1] != 0 {
				return none, errObservationDenied
			}
		}
		request := []byte{5, 1, 0, 1}
		if probe.Addr().Is6() {
			request[3] = 4
		}
		request = append(request, probe.Addr().AsSlice()...)
		request = append(request, byte(probe.Port()>>8), byte(probe.Port()))
		if _, err := conn.Write(request); err != nil {
			return none, ErrUnavailable
		}
		var response [4]byte
		if _, err := io.ReadFull(conn, response[:]); err != nil || response[0] != 5 || response[1] != 0 || response[2] != 0 {
			return none, ErrUnavailable
		}
		if _, err := readSocksAddress(conn, response[3]); err != nil {
			return none, ErrUnavailable
		}
	}
	var nonce [64]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return none, ErrUnavailable
	}
	if _, err := conn.Write(nonce[:]); err != nil {
		return none, ErrUnavailable
	}
	var echo [64]byte
	if _, err := io.ReadFull(input, echo[:]); err != nil || !bytes.Equal(echo[:], nonce[:]) {
		return none, ErrUnavailable
	}
	return source, nil
}
