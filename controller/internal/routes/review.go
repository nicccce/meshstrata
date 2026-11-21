package routes

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strconv"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/egress"
	"github.com/nicccce/meshstrata/controller/internal/ports"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/controller/internal/storage/dbgen"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type occupiedPort struct {
	Address, Transport, Listener string
	Port                         int64
}
type nodeInput struct {
	snapshot stage3.NodeSnapshot
	sock     stage3.PinnedSocketSpec
	base     control.BundleV2
	baseKey  control.ArtifactKey
	ports    []occupiedPort
}
type reviewed struct {
	draft    stage3.RouteDraft
	inputs   []nodeInput
	terminal control.TerminalV2
}

func issueFor(err error, path string) stage3.Issue {
	var p *domain.Problem
	if errors.As(err, &p) {
		if len(p.Fields) != 0 {
			path = p.Fields[0].Path
		}
		return stage3.Issue{Path: path, Code: p.Code}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return stage3.Issue{Path: path, Code: domain.NotFound}
	}
	return stage3.Issue{Path: path, Code: "INTERNAL"}
}

func (s *Service) ValidateDraft(ctx context.Context, pin control.Pin) (stage3.DraftReview, error) {
	d, err := s.draft(ctx, pin)
	if err != nil {
		return stage3.DraftReview{}, safe(ctx, err)
	}
	review := stage3.DraftReview{Draft: pin, Nodes: make([]domain.ID, len(d.Hops)), Blocking: []stage3.Issue{}, Warnings: []stage3.Issue{}}
	for i, h := range d.Hops {
		review.Nodes[i] = h.NodeID
	}
	r, issues := s.review(ctx, d)
	review.Blocking = append(review.Blocking, issues...)
	if len(issues) == 0 {
		actual, _, e := s.allocate(ctx, r, "", pin, time.Time{})
		if e != nil {
			review.Blocking = append(review.Blocking, issueFor(e, "/hops"))
		} else {
			// Run the same topology checks for the preview without acquiring any lease.
			for i := range r.inputs {
				b := r.inputs[i].base
				if i == len(r.inputs)-1 {
					if e = addPolicy(&b, r.terminal); e != nil {
						review.Blocking = append(review.Blocking, issueFor(e, "/egress"))
						break
					}
				}
				if e = s.checkBundle(ctx, b, r.inputs[i], actual, s.now()); e != nil {
					review.Blocking = append(review.Blocking, issueFor(e, hopPath(i, "")))
					break
				}
			}
		}
		if !d.EgressIsDefault {
			review.Warnings = append(review.Warnings, stage3.Issue{Path: "/egress", Code: "EGRESS_NOT_CHECKED"})
		}
	}
	if ctx.Err() != nil {
		return stage3.DraftReview{}, ctx.Err()
	}
	return review, nil
}

func (s *Service) review(ctx context.Context, d stage3.RouteDraft) (reviewed, []stage3.Issue) {
	r := reviewed{draft: d, inputs: make([]nodeInput, len(d.Hops))}
	var issues []stage3.Issue
	for i, h := range d.Hops {
		in, err := s.node(ctx, h, d.BusinessUDP, true, i)
		if err != nil {
			issues = append(issues, issueFor(err, hopPath(i, "")))
			continue
		}
		r.inputs[i] = in
	}
	terminal := len(d.Hops) - 1
	if r.inputs[terminal].snapshot.Revision.Valid() {
		term, err := s.policy(ctx, d, r.inputs[terminal])
		if err != nil {
			issues = append(issues, issueFor(err, "/egress"))
		} else {
			r.terminal = term
		}
	}
	return r, issues
}

func (s *Service) node(ctx context.Context, h stage3.HopDraft, udp, checkPort bool, i int) (nodeInput, error) {
	var in nodeInput
	var n dbgen.Node
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		n, e = q.GetNode(ctx, string(h.NodeID))
		if e != nil {
			return e
		}
		if checkPort {
			legacy, err := q.B06PortsForNode(ctx, string(h.NodeID))
			if err != nil {
				return err
			}
			modern, err := q.B06ModernPortsForNode(ctx, string(h.NodeID))
			if err != nil {
				return err
			}
			for _, p := range legacy {
				in.ports = append(in.ports, occupiedPort{p.Address, p.Transport, p.Listener.String, p.Port})
			}
			for _, p := range modern {
				in.ports = append(in.ports, occupiedPort{p.Address, p.Transport, p.Listener, p.Port})
			}
		}
		return e
	})
	if errors.Is(err, sql.ErrNoRows) || n.RemovedAt.Valid {
		return in, problem(domain.NotFound, hopPath(i, "/node_id"))
	}
	if err != nil {
		return in, err
	}
	if n.ConnectionMode != "direct" {
		return in, problem(domain.NotImplemented, hopPath(i, "/node_id"))
	}
	snap, err := s.opts.Infra.NodeSnapshot(ctx, h.NodeID)
	if err != nil {
		return in, dependency(ctx, err, hopPath(i, "/node_id"))
	}
	if snap.Revision != domain.Revision(n.Revision) || !snap.Revision.Valid() || snap.Revision >= domain.MaxRevision {
		return in, problem(domain.RevisionConflict, hopPath(i, "/node_id"))
	}
	if snap.DNS.Pin != h.DNS || h.DNS.Revision != domain.Revision(n.TargetDnsRevision) {
		return in, problem(domain.RevisionConflict, hopPath(i, "/dns"))
	}
	ip, _ := netip.ParseAddr(h.DialIP)
	if ip.Is6() && !snap.DNS.IPv6 {
		return in, problem(domain.InvalidArgument, hopPath(i, "/dial_ip"))
	}
	capCount := 0
	for _, c := range snap.Capabilities {
		if c.Protocol != h.Endpoint.Protocol {
			continue
		}
		if !c.Available || udp && c.BusinessUDP != domain.Verified {
			return in, problem(domain.DependencyInUse, hopPath(i, "/endpoint/protocol"))
		}
		capCount++
	}
	if capCount != 1 {
		return in, problem(domain.NotFound, hopPath(i, "/endpoint/protocol"))
	}
	count := 0
	for _, c := range snap.Sockets {
		if c.Pin != h.NodeConfig {
			continue
		}
		p, err := stage3.NewPin(c.Pin.ID, c.Pin.Revision, c.Spec)
		if err != nil || p != h.NodeConfig {
			return in, problem(domain.NotFound, hopPath(i, "/node_config"))
		}
		// Clone the material and list fields returned by the gateway.
		buf, _ := json.Marshal(c.Spec)
		var spec control.SocketSpec
		if json.Unmarshal(buf, &spec) != nil {
			return in, problem(domain.InvalidArgument, hopPath(i, "/node_config"))
		}
		c.Spec = spec
		in.sock = c
		count++
	}
	if count != 1 || !socketValid(in.sock.Spec, h.Endpoint, udp) {
		return in, problem(domain.NotFound, hopPath(i, "/node_config"))
	}
	if checkPort {
		if fixed := in.sock.Spec.RequestedPort; fixed != 0 && h.Endpoint.Port != fixed {
			return in, problem(domain.InvalidArgument, hopPath(i, "/endpoint/port"))
		}
		if h.Endpoint.Port == 0 && (in.sock.PoolFirst == 0 || in.sock.PoolLast < in.sock.PoolFirst) {
			return in, problem(domain.NotFound, hopPath(i, "/endpoint/port"))
		}
	}
	base, err := baseBundle(snap, h.NodeID, domain.Revision(n.RevocationEpoch))
	if err != nil {
		return in, dependency(ctx, err, hopPath(i, "/node_id"))
	}
	in.snapshot = snap
	inventory := snap.Loops
	if inventory.LocalAddresses != nil {
		inventory.LocalAddresses = append([]netip.Addr{}, inventory.LocalAddresses...)
	}
	if inventory.ProtectedEndpoints != nil {
		inventory.ProtectedEndpoints = append([]netip.AddrPort{}, inventory.ProtectedEndpoints...)
	}
	if inventory.ProtectedNetworks != nil {
		inventory.ProtectedNetworks = append([]netip.Prefix{}, inventory.ProtectedNetworks...)
	}
	if inventory.ProtectedInterfaces != nil {
		inventory.ProtectedInterfaces = append([]string{}, inventory.ProtectedInterfaces...)
	}
	in.snapshot.Loops = inventory
	if base.TaskID != "" {
		payload, key, err := control.EncodeBundleV2(base)
		clear(payload)
		if err != nil {
			return nodeInput{}, problem(domain.InvalidArgument, hopPath(i, "/bundle"))
		}
		in.baseKey = key
	}
	restrictBase(&base, snap.PendingTombstones)
	base.TaskID = ""
	base.Kind = domain.Publish

	in.base = base
	return in, nil
}

func (s *Service) removalNode(ctx context.Context, id domain.ID) (nodeInput, error) {
	var node dbgen.Node
	err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
		var e error
		node, e = q.GetNode(ctx, string(id))
		return e
	})
	if err != nil {
		return nodeInput{}, err
	}
	if node.RemovedAt.Valid {
		return nodeInput{}, problem(domain.NotFound, "/entry/node")
	}
	if node.ConnectionMode != "direct" {
		return nodeInput{}, problem(domain.NotImplemented, "/entry/node")
	}
	snap, err := s.opts.Infra.NodeSnapshot(ctx, id)
	if err != nil {
		return nodeInput{}, dependency(ctx, err, "/entry/node")
	}
	if snap.Revision != domain.Revision(node.Revision) || !snap.Revision.Valid() || snap.Revision == domain.MaxRevision ||
		snap.DNS.Pin.Revision != domain.Revision(node.TargetDnsRevision) {
		return nodeInput{}, revisionConflict(node.Revision)
	}
	base, err := baseBundle(snap, id, domain.Revision(node.RevocationEpoch))
	if err != nil {
		return nodeInput{}, err
	}
	restrictBase(&base, snap.PendingTombstones)
	base.TaskID = ""
	return nodeInput{snapshot: snap, base: base}, nil
}

func (s *Service) confirmReceiver(ctx context.Context, in nodeInput) error {
	key := in.baseKey
	if !key.TaskID.Valid() {
		return problem(domain.GrantOriginUncertain, "/link/confirmed")
	}
	state, err := s.opts.Publisher.ReleaseState(ctx, key.TaskID)
	if err != nil {
		return dependency(ctx, err, "/link/confirmed")
	}
	if state.TaskID == key.TaskID && state.Subject.Valid() && state.State == "published" {
		hits := 0
		for _, r := range state.Nodes {
			if r.NodeID != key.NodeID {
				continue
			}
			if !r.Applied || r.TargetRevision != key.TargetRevision || r.TargetDigest != key.TargetDigest ||
				r.ConfirmedAt == nil || !utc(*r.ConfirmedAt) || (r.Rollback != "" && r.Rollback != "none") {
				return problem(domain.GrantOriginUncertain, "/link/confirmed")
			}
			hits++
		}
		if hits == 1 {
			return nil
		}
	}
	return problem(domain.GrantOriginUncertain, "/link/confirmed")
}

func socketValid(s control.SocketSpec, ep control.ProxyEndpoint, udp bool) bool {
	ip, err := netip.ParseAddr(s.Address)
	if s.TLSLeafSHA256 != ep.TLSLeafSHA256 || err != nil || ip.String() != s.Address || ip.Is4In6() || ip.IsMulticast() || ip.Zone() != "" || s.ServerNames == nil || s.ShortIDs == nil {
		return false
	}
	noReality := s.RealityKey == nil && s.RealityDestination == "" && len(s.ServerNames) == 0 && len(s.ShortIDs) == 0
	noTLS := s.Certificate == nil && s.PrivateKey == nil
	material := func(m *control.LocalMaterial, p string) bool { return m != nil && m.ID.Valid() && m.Purpose == p }
	switch ep.Protocol {
	case domain.SS2022:
		return noReality && noTLS && (!udp && s.Transport == "tcp" || udp && s.Transport == "tcp_udp")
	case domain.Trojan, domain.Hysteria2:
		transport := "tcp"
		if ep.Protocol == domain.Hysteria2 {
			transport = "udp"
		}
		return s.Transport == transport && noReality && material(s.Certificate, "tls_certificate") && material(s.PrivateKey, "tls_private_key")
	case domain.VLESS:
		return s.Transport == "tcp" && noTLS && material(s.RealityKey, "reality_private_key") && len(s.ServerNames) <= 8 && len(s.ShortIDs) <= 8 &&
			slices.Contains(s.ServerNames, ep.ServerName) && slices.Contains(s.ShortIDs, ep.ShortID) && validReality(s.RealityDestination)
	}
	return false
}
func validReality(s string) bool {
	ap, err := netip.ParseAddrPort(s)
	if err == nil {
		return ap.Port() != 0 && ap.Addr().Zone() == ""
	}
	i := len(s) - 1
	for i >= 0 && s[i] != ':' {
		i--
	}
	if i < 1 {
		return false
	}
	p, e := strconv.ParseUint(s[i+1:], 10, 16)
	return e == nil && p != 0 && control.DomainName(s[:i])
}

func baseBundle(s stage3.NodeSnapshot, node domain.ID, epoch domain.Revision) (control.BundleV2, error) {
	if (s.Legacy == nil) == (s.Extended == nil) {
		return control.BundleV2{}, problem(domain.InvalidArgument, "")
	}
	var b control.BundleV2
	if s.Legacy != nil {
		l := s.Legacy
		if l.Validate() != nil || l.NodeID != node || l.TargetRevision > s.Revision || l.RevocationEpoch != epoch || l.DNS.Pin != s.DNS.Pin {
			return b, problem(domain.RevisionConflict, "")
		}
		if len(l.Listeners) > 0 || len(l.Resources) > 0 {
			return b, problem(domain.NotImplemented, "")
		}
		b = control.BundleV2{Format: control.BundleVersion2, Kind: domain.Publish, NodeID: node, TargetRevision: s.Revision, RevocationEpoch: epoch, DNS: l.DNS,
			Listeners: []control.ListenerV2{}, Terminal: []control.TerminalV2{}, Tombstones: append([]control.Tombstone{}, l.Tombstones...), RetiredLinks: []control.InternalRetirement{}}
	} else {
		if s.Extended.NodeID != node || s.Extended.TargetRevision > s.Revision || s.Extended.RevocationEpoch != epoch || s.Extended.DNS.Pin != s.DNS.Pin {
			return b, problem(domain.RevisionConflict, "")
		}
		data, err := json.Marshal(s.Extended)
		defer clear(data)
		if err != nil || len(data) > control.MaxBundleBytes || json.Unmarshal(data, &b) != nil {
			return control.BundleV2{}, problem(domain.InvalidArgument, "")
		}
	}
	if b.DNS.Pin != s.DNS.Pin || !slices.Equal(s.DNS.Bootstrap, b.DNS.Bootstrap) || !slices.Equal(s.DNS.Resolvers, b.DNS.Resolvers) || s.DNS.IPv6 != b.DNS.IPv6 || structural(b) != nil {
		return control.BundleV2{}, problem(domain.InvalidArgument, "")
	}
	return b, nil
}

// The frozen codec requires a valid TaskID even for its pure checks. Use the
// real node ID as a local validation-only value, never in the outgoing template.
func structural(b control.BundleV2) error {
	if b.TaskID == "" {
		b.TaskID = b.NodeID
	}
	return b.Validate()
}

func (s *Service) policy(ctx context.Context, d stage3.RouteDraft, in nodeInput) (control.TerminalV2, error) {
	spec, err := s.opts.Egress.Policy(ctx, d.Egress)
	if err != nil {
		return control.TerminalV2{}, dependency(ctx, err, "/egress")
	}
	// The reader is injected. Keep a private copy before building templates.
	data, err := json.Marshal(spec)
	if err != nil {
		return control.TerminalV2{}, problem(domain.InvalidArgument, "/egress")
	}
	var cloned control.EgressSpec
	if json.Unmarshal(data, &cloned) != nil || cloned.NodeID != d.Hops[len(d.Hops)-1].NodeID ||
		cloned.DNS != in.base.DNS.Pin || cloned.Validate(d.Egress, in.base.DNS.IPv6) != nil {
		return control.TerminalV2{}, problem(domain.NotFound, "/egress")
	}
	isDefault := len(cloned.Targets) == 1 && len(cloned.Rules) == 1 && cloned.Targets[0].Spec.Type == "node_default" && cloned.Rules[0].Target != nil
	if d.EgressIsDefault && !isDefault {
		return control.TerminalV2{}, problem(domain.InvalidArgument, "/egress_is_default")
	}
	term := control.TerminalV2{Pin: d.Egress, Spec: cloned, Auth: []control.EgressAuth{}}
	for _, target := range cloned.Targets {
		if target.Spec.CredentialRef != "" {
			auth, err := egress.OpenAuth(ctx, s.opts.Runtime, s.opts.Cipher, target.Pin, cloned.NodeID)
			if err != nil {
				return control.TerminalV2{}, dependency(ctx, err, "/egress/auth")
			}
			term.Auth = append(term.Auth, auth)
		}
	}
	if err := s.checkPolicyEvidence(ctx, term, in, d.BusinessUDP, s.now()); err != nil {
		return control.TerminalV2{}, err
	}
	return term, nil
}

func (s *Service) checkPolicyEvidence(ctx context.Context, t control.TerminalV2, in nodeInput, udp bool, now time.Time) error {
	for _, target := range t.Spec.Targets {
		if target.Spec.Type == "node_default" && !udp {
			continue
		}
		if target.Spec.Type == "http" && udp {
			return problem(domain.DependencyInUse, "/egress/business_udp")
		}
		var row dbgen.B06EgressEvidence
		err := s.opts.DB.Read(ctx, func(q *dbgen.Queries) error {
			var e error
			row, e = q.B06EgressEvidence(ctx, dbgen.B06EgressEvidenceParams{NodeID: string(t.Spec.NodeID), NodeRevision: int64(in.snapshot.Revision), TargetDigest: target.Pin.Digest, DnsDigest: t.Spec.DNS.Digest})
			return e
		})
		if errors.Is(err, sql.ErrNoRows) {
			return problem(domain.NotFound, "/egress/evidence")
		}
		if err != nil {
			return err
		}
		until, err := time.Parse(time.RFC3339Nano, row.ExpiresAt)
		if err != nil || !until.After(now) {
			return problem(domain.TaskExpired, "/egress/evidence")
		}
		if udp && row.BusinessUdp != 1 {
			return problem(domain.DependencyInUse, "/egress/business_udp")
		}
	}
	return nil
}

func addPolicy(b *control.BundleV2, t control.TerminalV2) error {
	for _, existing := range b.Terminal {
		if existing.Pin == t.Pin {
			a, _ := json.Marshal(existing)
			c, _ := json.Marshal(t)
			same := bytes.Equal(a, c)
			clear(a)
			clear(c)
			if !same {
				return problem(domain.RevisionConflict, "/egress")
			}
			return nil
		}
		if existing.Pin.ID == t.Pin.ID && existing.Pin.Revision == t.Pin.Revision {
			return problem(domain.RevisionConflict, "/egress")
		}
	}
	b.Terminal = append(slices.Clone(b.Terminal), t)
	sortBundle(b)
	return nil
}

func (s *Service) checkBundle(ctx context.Context, b control.BundleV2, in nodeInput, path control.RouteTail, now time.Time) error {
	if structural(b) != nil {
		return problem(domain.InvalidArgument, "/bundle")
	}
	inv := in.snapshot.Loops
	allDefault := true
	for _, t := range b.Terminal {
		udp := path.BusinessUDP && t.Pin == path.Egress && t.Spec.NodeID == path.Hops[len(path.Hops)-1].NodeID
		for _, l := range b.Listeners {
			udp = udp || l.Tail.BusinessUDP && l.Forward.Terminal != nil && *l.Forward.Terminal == t.Pin
		}
		if err := s.checkPolicyEvidence(ctx, t, in, udp, now); err != nil {
			return err
		}
		for _, target := range t.Spec.Targets {
			allDefault = allDefault && target.Spec.Type == "node_default"
		}
	}
	if allDefault {
		return nil
	}
	if len(inv.LocalAddresses) == 0 || inv.ProtectedEndpoints == nil || inv.ProtectedNetworks == nil || inv.ProtectedInterfaces == nil {
		return problem(domain.NotFound, "/egress/inventory")
	}
	for _, p := range inv.LocalAddresses {
		if !p.IsValid() || p.IsUnspecified() || p.IsMulticast() {
			return problem(domain.InvalidArgument, "/egress/inventory")
		}
	}
	for _, p := range inv.ProtectedEndpoints {
		if !p.IsValid() || p.Port() == 0 {
			return problem(domain.InvalidArgument, "/egress/inventory")
		}
	}
	for _, p := range inv.ProtectedNetworks {
		if !p.IsValid() {
			return problem(domain.InvalidArgument, "/egress/inventory")
		}
	}
	inv.ProtectedEndpoints = append([]netip.AddrPort{}, inv.ProtectedEndpoints...)
	for i, h := range path.Hops {
		ip, _ := netip.ParseAddr(h.DialIP)
		// Even the reviewed entry baseline is a candidate for later subscription use.
		if i == 0 || h.Endpoint.Protocol != domain.Hysteria2 {
			inv.ProtectedEndpoints = append(inv.ProtectedEndpoints, netip.AddrPortFrom(ip, h.Endpoint.Port))
		}
	}
	if in.sock.Spec.Transport != "" && in.sock.Spec.Transport != "udp" {
		for _, h := range path.Hops {
			if h.NodeID != b.NodeID {
				continue
			}
			bind := ports.Parse(in.sock.Spec.Address, int64(h.Endpoint.Port), "tcp")
			local := append(append([]netip.Addr{}, inv.LocalAddresses...), netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback())
			for _, ip := range local {
				if bind.Conflicts(ports.Binding{Address: ip.Unmap(), Port: h.Endpoint.Port, Transport: "tcp"}) {
					inv.ProtectedEndpoints = append(inv.ProtectedEndpoints, netip.AddrPortFrom(ip.Unmap(), h.Endpoint.Port))
				}
			}
			if !bind.Address.IsUnspecified() {
				inv.ProtectedEndpoints = append(inv.ProtectedEndpoints, netip.AddrPortFrom(bind.Address, h.Endpoint.Port))
			}
		}
	}

	if b.TaskID == "" {
		b.TaskID = b.NodeID
	}
	if b.CheckEgressLoops(inv) != nil {
		return problem(domain.DependencyInUse, "/egress/loop")
	}
	return nil
}

func overlaps(rows []occupiedPort, b control.BundleV2, s control.SocketSpec, port uint16, ignore string) bool {
	bindings := func(transport string) []string {
		if transport == "tcp_udp" {
			return []string{"tcp", "udp"}
		}
		return []string{transport}
	}
	for _, transport := range bindings(s.Transport) {
		want := ports.Parse(s.Address, int64(port), transport)
		for _, row := range rows {
			if row.Port != int64(port) || ignore != "" && row.Listener == ignore {
				continue
			}
			p := ports.Parse(row.Address, row.Port, row.Transport)
			if !p.Valid() || want.Conflicts(p) {
				return true
			}
		}
		for _, l := range b.Listeners {
			if l.Socket.Port != port || ignore != "" && string(l.Socket.ListenerID) == ignore {
				continue
			}
			for _, t := range bindings(l.Socket.Transport) {
				if want.Conflicts(ports.Parse(l.Socket.Address, int64(l.Socket.Port), t)) {
					return true
				}
			}
		}
	}
	return false
}

// Apply current authorization decisions to the candidate only. baseKey remains
// the exact original confirmed artifact; no filtered state is called confirmed.
func restrictBase(b *control.BundleV2, tombs []control.Tombstone) {
	for _, t := range tombs {
		found := false
		for _, old := range b.Tombstones {
			found = found || old.IdentityID == t.IdentityID
		}
		if !found {
			b.Tombstones = append(b.Tombstones, t)
		}
		b.Listeners = slices.DeleteFunc(b.Listeners, func(l control.ListenerV2) bool { return l.Entry != nil && l.Entry.IdentityID == t.IdentityID })
	}
	sortBundle(b)
}
