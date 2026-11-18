package control

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/internal/domain"
)

// Format 2 is an offline codec. Do not add it to BundleVersion or advertise
// runtime support before both peers implement versioned persistence and execution.
const (
	BundleVersion2   = 2
	MaxRouteHops     = 8
	MaxEgressRules   = 128
	MaxEgressTargets = 32
)

func (p Pin) Valid() bool {
	return p.ID.Valid() && validTarget(p.Revision, p.Digest)
}

// ProxyEndpoint contains only public transport parameters. A literal IP or an
// ASCII DNS name is allowed. ServerName is the authenticated TLS/REALITY name.
// TLSLeafSHA256 is a lowercase SHA-256 of a business TLS leaf certificate.
// Empty uses the normal trusted chain and server name.
type TLSLeafSHA256 string

func (p TLSLeafSHA256) Valid() bool {
	b, err := hex.DecodeString(string(p))
	return len(p) == 64 && err == nil && len(b) == 32 && strings.ToLower(string(p)) == string(p)
}

type ProxyEndpoint struct {
	TLSLeafSHA256    TLSLeafSHA256   `json:"tls_leaf_sha256,omitempty"`
	Host             string          `json:"host"`
	Port             uint16          `json:"port"`
	Protocol         domain.Protocol `json:"protocol"`
	Cipher           string          `json:"cipher"`
	ServerName       string          `json:"server_name"`
	RealityPublicKey string          `json:"reality_public_key"`
	ShortID          string          `json:"short_id"`
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var ifaceName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,14}$`)

// DomainName accepts canonical ASCII names. IDNA conversion, if supported,
// happens before pinning. No URL, wildcard, IP literal, trailing dot or comma.
func DomainName(s string) bool {
	if len(s) < 1 || len(s) > 253 {
		return false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if !dnsLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func dialIP(s string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(s)
	return ip, err == nil && ip.Zone() == "" && !ip.Is4In6() && !ip.IsUnspecified() &&
		!ip.IsMulticast() && ip.String() == s
}

func (p ProxyEndpoint) Valid() bool {
	_, literal := dialIP(p.Host)
	if (!literal && !DomainName(p.Host)) || p.Port == 0 || p.TLSLeafSHA256 != "" && (!p.TLSLeafSHA256.Valid() || p.Protocol != domain.Trojan && p.Protocol != domain.Hysteria2) {
		return false
	}
	switch p.Protocol {
	case domain.SS2022:
		return ssKeySize(p.Cipher) != 0 && p.ServerName == "" && p.RealityPublicKey == "" && p.ShortID == ""
	case domain.Hysteria2, domain.Trojan:
		_, ipName := dialIP(p.ServerName)
		return (ipName || DomainName(p.ServerName)) && p.Cipher == "" && p.RealityPublicKey == "" && p.ShortID == ""
	case domain.VLESS:
		if len(p.RealityPublicKey) != 43 || len(p.ShortID) > 16 {
			return false
		}
		key, err := base64.RawURLEncoding.Strict().DecodeString(p.RealityPublicKey)
		short, e := hex.DecodeString(p.ShortID)
		return DomainName(p.ServerName) && p.Cipher == "" && len(p.RealityPublicKey) == 43 && err == nil &&
			len(key) == 32 && !bytes.Equal(key, make([]byte, 32)) && e == nil && len(short) >= 1 && len(short) <= 8 && strings.ToLower(p.ShortID) == p.ShortID
	default:
		return false
	}
}

func ssKeySize(cipher string) int {
	switch cipher {
	case "2022-blake3-aes-128-gcm":
		return 16
	case "2022-blake3-aes-256-gcm", "2022-blake3-chacha20-poly1305":
		return 32
	default:
		return 0
	}
}

// ValidProxyCredential checks the protocol shape for a non-server consumer.
// Resolve the ref only after checking recipient, purpose, and the pinned source.
func ValidProxyCredential(p ProxyEndpoint, c PrivateCredential, audience domain.ID) bool {
	if !p.Valid() || !audience.Valid() || c.Audience != audience || !c.Ref.Valid() {
		return false
	}
	switch p.Protocol {
	case domain.SS2022:
		size := ssKeySize(p.Cipher)
		if c.UUID != "" || len(c.Password) != base64.StdEncoding.EncodedLen(size) {
			return false
		}
		key, err := base64.StdEncoding.Strict().DecodeString(c.Password)
		return err == nil && len(key) == size
	case domain.Trojan, domain.Hysteria2:
		return c.UUID == "" && len(c.Password) >= 16 && safeText(c.Password, 512)
	case domain.VLESS:
		if c.Password != "" || len(c.UUID) != 36 || c.UUID[8] != '-' || c.UUID[13] != '-' || c.UUID[18] != '-' || c.UUID[23] != '-' {
			return false
		}
		key, err := hex.DecodeString(strings.ReplaceAll(c.UUID, "-", ""))
		return err == nil && len(key) == 16
	}
	return false
}

func safeText(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// EgressSpec is pinned independently of per-node plaintext proxy auth.
type EgressSpec struct {
	NodeID  domain.ID            `json:"node_id"`
	DNS     Pin                  `json:"dns"`
	Targets []PinnedEgressTarget `json:"targets"` // sorted by target ID
	Rules   []EgressRule         `json:"rules"`   // first match wins, exactly one final MATCH
}
type EgressRule struct {
	Destination string `json:"destination"` // canonical CIDR or MATCH
	Target      *Pin   `json:"target"`      // nil is REJECT, including the default MATCH
	NoResolve   bool   `json:"no_resolve"`
}
type PinnedEgressTarget struct {
	Pin  Pin          `json:"pin"`
	Spec EgressTarget `json:"spec"`
}
type EgressTarget struct {
	NodeID        domain.ID `json:"node_id"`
	Type          string    `json:"type"` // node_default, interface, http, socks5
	Interface     string    `json:"interface"`
	Host          string    `json:"host"`    // original canonical name or literal
	DialIP        string    `json:"dial_ip"` // fixed numeric address from approved bootstrap evidence
	Port          uint16    `json:"port"`
	CredentialRef domain.ID `json:"credential_ref"`
}
type EgressAuth struct {
	Target   Pin       `json:"target"`
	Ref      domain.ID `json:"ref"`
	Audience domain.ID `json:"audience"`
	Username string    `json:"username"`
	Password string    `json:"password"`
}

func (t EgressTarget) Valid() bool {
	if !t.NodeID.Valid() {
		return false
	}
	noProxy := t.Host == "" && t.DialIP == "" && t.Port == 0 && t.CredentialRef == ""
	switch t.Type {
	case "node_default":
		return noProxy && t.Interface == ""
	case "interface":
		return noProxy && ifaceName.MatchString(t.Interface)
	case "http", "socks5":
		_, hostIP := dialIP(t.Host)
		_, fixed := dialIP(t.DialIP)
		return t.Interface == "" && t.Port != 0 && fixed && (!hostIP && DomainName(t.Host) || hostIP && t.Host == t.DialIP) &&
			(t.CredentialRef == "" || t.CredentialRef.Valid())
	}
	return false
}

func (e EgressSpec) Validate(pin Pin, ipv6 bool) error {
	if !e.NodeID.Valid() || !e.DNS.Valid() || e.Targets == nil || len(e.Targets) > MaxEgressTargets ||
		len(e.Rules) < 1 || len(e.Rules) > MaxEgressRules {
		return ErrBundle
	}
	targets := map[Pin]bool{}
	for i, t := range e.Targets {
		ip, _ := dialIP(t.Spec.DialIP)
		if (i > 0 && e.Targets[i-1].Pin.ID >= t.Pin.ID) || t.Spec.NodeID != e.NodeID || !t.Spec.Valid() ||
			(ip.Is6() && !ipv6) || !pinValid(t.Pin, t.Spec) {
			return ErrBundle
		}
		targets[t.Pin] = false
	}
	for i, r := range e.Rules {
		if i == len(e.Rules)-1 {
			if r.Destination != "MATCH" || r.NoResolve {
				return ErrBundle
			}
		} else {
			if len(r.Destination) > 50 {
				return ErrBundle
			}
			p, err := netip.ParsePrefix(r.Destination)
			if err != nil || p.Addr().Is4In6() || !p.Addr().Is4() && !ipv6 || p.Masked().String() != r.Destination {
				return ErrBundle
			}
		}
		if r.Target != nil {
			if _, ok := targets[*r.Target]; !ok {
				return ErrBundle
			}
			targets[*r.Target] = true
		}
	}
	for _, used := range targets {
		if !used {
			return ErrBundle
		}
	}
	if !pinValid(pin, e) {
		return ErrBundle
	}
	return nil
}

// HopSpec pins every remaining node, listener setup, DNS and dial endpoint.
// Acceptance/cleanup evidence is intentionally excluded from the key.
type HopSpec struct {
	NodeID     domain.ID     `json:"node_id"`
	NodeConfig Pin           `json:"node_config"` // address/port intent + listener transport/material settings
	DNS        Pin           `json:"dns"`
	Endpoint   ProxyEndpoint `json:"endpoint"`
	DialIP     string        `json:"dial_ip"` // numeric node-to-node address; no runtime re-resolution
}
type RouteTail struct {
	Hops        []HopSpec `json:"hops"` // current receiver first
	Egress      Pin       `json:"egress"`
	BusinessUDP bool      `json:"business_udp"`
}

func (t RouteTail) Key() (string, error) {
	if !t.Egress.Valid() || len(t.Hops) < 1 || len(t.Hops) > MaxRouteHops {
		return "", ErrBundle
	}
	nodes := map[domain.ID]bool{}
	for _, h := range t.Hops {
		_, dial := dialIP(h.DialIP)
		_, literal := dialIP(h.Endpoint.Host)
		if !h.NodeID.Valid() || nodes[h.NodeID] || !h.DNS.Valid() || !h.NodeConfig.Valid() || !h.Endpoint.Valid() || !dial ||
			(literal && h.Endpoint.Host != h.DialIP) ||
			(t.BusinessUDP && h.Endpoint.Protocol != domain.Hysteria2 && h.Endpoint.Protocol != domain.SS2022) {
			return "", ErrBundle
		}
		nodes[h.NodeID] = true
	}
	return ComponentDigest(t)
}

// SocketV2 is the common listener shape with no entry authorization.
type SocketV2 struct {
	// ConfigPort preserves the reviewed preferred port independently of this entry lease.
	ConfigPort         uint16            `json:"config_port,omitempty"`
	TLSLeafSHA256      TLSLeafSHA256     `json:"tls_leaf_sha256,omitempty"`
	ListenerID         domain.ID         `json:"listener_id"`
	Address            string            `json:"address"`
	Port               uint16            `json:"port"`
	Transport          string            `json:"transport"`
	Credential         PrivateCredential `json:"credential"`
	Certificate        *LocalMaterial    `json:"certificate"`
	PrivateKey         *LocalMaterial    `json:"private_key"`
	RealityKey         *LocalMaterial    `json:"reality_key"`
	RealityDestination string            `json:"reality_destination"`
	ServerNames        []string          `json:"server_names"`
	ShortIDs           []string          `json:"short_ids"`
}
type NodeRuleV2 struct {
	// A terminal policy is selected by ingress listener, then matched by CIDR.
	// An intermediate listener bypasses terminal policy/DNS and dials only Next.
	Next     *NextHop `json:"next"`
	Terminal *Pin     `json:"terminal"`
}
type NextHop struct {
	Link       Pin               `json:"link"`       // real internal identity; digest is key of tail starting at receiver
	Credential PrivateCredential `json:"credential"` // audience = sending node, internal_proxy only
}
type ListenerV2 struct {
	Socket   SocketV2             `json:"socket"`
	Tail     RouteTail            `json:"tail"`
	Entry    *BundleAuthorization `json:"entry"`
	Internal *Pin                 `json:"internal"` // immutable resource identity, no subscription or expiry
	Forward  NodeRuleV2           `json:"forward"`
}
type InternalRetirement struct {
	Link  Pin             `json:"link"`
	Epoch domain.Revision `json:"epoch"`
}
type TerminalV2 struct {
	Pin  Pin          `json:"pin"`
	Spec EgressSpec   `json:"spec"`
	Auth []EgressAuth `json:"auth"` // sorted by target ID, exactly credentialed targets
}

type BundleV2 struct {
	Format          int                  `json:"format"`
	Kind            domain.TaskKind      `json:"kind"`
	TaskID          domain.ID            `json:"task_id"`
	NodeID          domain.ID            `json:"node_id"`
	TargetRevision  domain.Revision      `json:"target_revision"`
	RevocationEpoch domain.Revision      `json:"revocation_epoch"`
	DNS             DNSInput             `json:"dns"`
	Listeners       []ListenerV2         `json:"listeners"`     // sorted by socket listener ID
	Terminal        []TerminalV2         `json:"terminal"`      // sorted by policy ID, then revision
	Tombstones      []Tombstone          `json:"tombstones"`    // V1 entry tombstones, sorted by identity ID
	RetiredLinks    []InternalRetirement `json:"retired_links"` // distinct permanent retirement history
}

// SocketSpec excludes per-entry allocation and private material. The port intent
// (0 means pool selection) belongs to the fixed node config, not the entry.
type SocketSpec struct {
	TLSLeafSHA256      TLSLeafSHA256  `json:"tls_leaf_sha256,omitempty"`
	Address            string         `json:"address"`
	RequestedPort      uint16         `json:"requested_port"`
	Transport          string         `json:"transport"`
	Certificate        *LocalMaterial `json:"certificate"`
	PrivateKey         *LocalMaterial `json:"private_key"`
	RealityKey         *LocalMaterial `json:"reality_key"`
	RealityDestination string         `json:"reality_destination"`
	ServerNames        []string       `json:"server_names"`
	ShortIDs           []string       `json:"short_ids"`
}

// SameV2Grant compares the entire private entry binding. Same internal identity
// likewise implies byte-identical socket, credentials, suffix and next hop.
func SameV2Grant(previous, next ListenerV2) error {
	if previous.Entry == nil || next.Entry == nil {
		return ErrAuthorization
	}
	return sameV2Binding(previous, next)
}

func SameInternalLink(previous, next ListenerV2) error {
	if previous.Internal == nil || next.Internal == nil {
		return ErrAuthorization
	}
	return sameV2Binding(previous, next)
}

func sameV2Binding(previous, next ListenerV2) error {
	a, err := json.Marshal(previous)
	b, e := json.Marshal(next)
	if err != nil || e != nil || !bytes.Equal(a, b) {
		return ErrAuthorization
	}
	return nil
}

func (b BundleV2) Validate() error {
	switch b.Kind {
	case domain.Publish, domain.Revoke, domain.Cleanup, domain.Rollback:
	default:
		return ErrBundle
	}
	if b.Format != BundleVersion2 || !b.TaskID.Valid() || !b.NodeID.Valid() || !b.TargetRevision.Valid() ||
		!b.RevocationEpoch.Valid() || b.Listeners == nil || len(b.Listeners) > MaxListeners ||
		b.Terminal == nil || len(b.Terminal) > MaxEgressTargets || b.Tombstones == nil || len(b.Tombstones) > MaxTombstones ||
		b.RetiredLinks == nil || len(b.RetiredLinks) > MaxTombstones {
		return ErrBundle
	}
	if !pinValid(b.DNS.Pin, DNSPolicy{b.DNS.Bootstrap, b.DNS.Resolvers, b.DNS.IPv6}) ||
		len(b.DNS.Bootstrap) < 1 || len(b.DNS.Bootstrap) > 8 || len(b.DNS.Resolvers) < 1 || len(b.DNS.Resolvers) > 8 {
		return ErrBundle
	}
	dnsSeen := map[string]bool{}
	for _, server := range b.DNS.Bootstrap {
		ip, ok := dialIP(server)
		if !ok || ip.Is6() && !b.DNS.IPv6 || dnsSeen["b"+server] {
			return ErrBundle
		}
		dnsSeen["b"+server] = true
	}
	for _, server := range b.DNS.Resolvers {
		if len(server) > 256 || !resolverValid(server) || dnsSeen["r"+server] {
			return ErrBundle
		}
		dnsSeen["r"+server] = true
	}
	policies := map[Pin]bool{}
	for i, p := range b.Terminal {
		if i > 0 && !pinBefore(b.Terminal[i-1].Pin, p.Pin) || p.Spec.DNS != b.DNS.Pin || p.Spec.NodeID != b.NodeID ||
			p.Spec.Validate(p.Pin, b.DNS.IPv6) != nil || p.Auth == nil || len(p.Auth) > MaxEgressTargets {
			return ErrBundle
		}
		j := 0
		for _, target := range p.Spec.Targets {
			if target.Spec.CredentialRef == "" {
				continue
			}
			if j == len(p.Auth) {
				return ErrBundle
			}
			a := p.Auth[j]
			if a.Target != target.Pin || a.Ref != target.Spec.CredentialRef || a.Audience != b.NodeID ||
				a.Username == "" || !safeText(a.Username, 255) || !safeText(a.Password, 255) {
				return ErrBundle
			}
			j++
		}
		if j != len(p.Auth) {
			return ErrBundle
		}
		policies[p.Pin] = false
	}
	ids := map[domain.ID]bool{}
	retired := map[domain.ID]bool{}
	for i, t := range b.Tombstones {
		if !t.IdentityID.Valid() || !t.Generation.Valid() || !t.Epoch.Valid() || t.Epoch > b.RevocationEpoch || !utc(t.At) ||
			(t.Reason != "expired" && t.Reason != "revoked") || i > 0 && b.Tombstones[i-1].IdentityID >= t.IdentityID {
			return ErrBundle
		}
		ids[t.IdentityID] = true
	}
	for i, t := range b.RetiredLinks {
		if !t.Link.Valid() || !t.Epoch.Valid() || t.Epoch > b.RevocationEpoch || ids[t.Link.ID] ||
			i > 0 && b.RetiredLinks[i-1].Link.ID >= t.Link.ID {
			return ErrBundle
		}
		retired[t.Link.ID] = true
	}
	entries := map[domain.ID]bool{}
	outgoing := map[domain.ID]NextHop{}
	for i, l := range b.Listeners {
		s := l.Socket
		if (l.Entry == nil) == (l.Internal == nil) || !s.ListenerID.Valid() || (i > 0 && b.Listeners[i-1].Socket.ListenerID >= s.ListenerID) {
			return ErrBundle
		}
		hash, err := l.Tail.Key()
		if err != nil {
			return ErrBundle
		}
		h := l.Tail.Hops[0]
		if h.NodeID != b.NodeID || h.DNS != b.DNS.Pin {
			return ErrBundle
		}
		var id domain.ID
		if a := l.Entry; a != nil {
			id = a.IdentityID
			if !a.EntryID.Valid() || entries[a.EntryID] || !a.Generation.Valid() || !utc(a.ExpiresAt) || a.RevokedAt != nil {
				return ErrBundle
			}
			entries[a.EntryID] = true
		} else {
			id = l.Internal.ID
			if !l.Internal.Valid() || l.Internal.Digest != hash || s.Port != h.Endpoint.Port {
				return ErrBundle
			}
		}
		if !id.Valid() || ids[id] || retired[id] {
			return ErrBundle
		}
		ids[id] = true
		ip, e := netip.ParseAddr(s.Address)
		if e != nil || ip.Zone() != "" || ip.Is4In6() || ip.IsMulticast() || ip.String() != s.Address ||
			s.Port == 0 || !ValidProxyCredential(h.Endpoint, s.Credential, b.NodeID) ||
			s.ServerNames == nil || s.ShortIDs == nil || len(s.ServerNames) > 8 || len(s.ShortIDs) > 8 || !safeText(s.RealityDestination, 320) {
			return ErrBundle
		}
		socketSpec := SocketSpec{Address: s.Address, RequestedPort: s.ConfigPort, Transport: s.Transport, Certificate: s.Certificate, PrivateKey: s.PrivateKey, RealityKey: s.RealityKey, RealityDestination: s.RealityDestination, ServerNames: s.ServerNames, ShortIDs: s.ShortIDs, TLSLeafSHA256: s.TLSLeafSHA256}
		if s.TLSLeafSHA256 != h.Endpoint.TLSLeafSHA256 || s.ConfigPort != 0 && l.Internal != nil && s.ConfigPort != s.Port {
			return ErrBundle
		}
		if !pinValid(h.NodeConfig, socketSpec) {
			// Compatibility for offline templates prepared before ConfigPort.
			if s.ConfigPort != 0 {
				return ErrBundle
			}
			socketSpec.RequestedPort = s.Port
			if !pinValid(h.NodeConfig, socketSpec) {
				return ErrBundle
			}
		}
		if h.Endpoint.Protocol == domain.VLESS {
			if !contains(s.ServerNames, h.Endpoint.ServerName) || !contains(s.ShortIDs, h.Endpoint.ShortID) {
				return ErrBundle
			}
		}
		if validateListenerProtocol(BundleListener{ListenerID: s.ListenerID, NodeID: b.NodeID, Protocol: h.Endpoint.Protocol,
			Address: s.Address, Port: s.Port, Transport: s.Transport, BusinessUDP: l.Tail.BusinessUDP, Credential: s.Credential,
			Cipher: h.Endpoint.Cipher, Certificate: s.Certificate, PrivateKey: s.PrivateKey, RealityKey: s.RealityKey,
			RealityDestination: s.RealityDestination, ServerNames: s.ServerNames, ShortIDs: s.ShortIDs}, id) != nil {
			return ErrBundle
		}
		for _, previous := range b.Listeners[:i] {
			p := previous.Socket
			if p.Port == s.Port && transportsOverlap(p.Transport, s.Transport) && addressesOverlap(p.Address, s.Address) {
				return ErrBundle
			}
		}
		next, terminal := l.Forward.Next, l.Forward.Terminal
		if len(l.Tail.Hops) == 1 {
			if next != nil || terminal == nil || *terminal != l.Tail.Egress {
				return ErrBundle
			}
			if _, ok := policies[*terminal]; !ok {
				return ErrBundle
			}
			policies[*terminal] = true
		} else {
			if next == nil || terminal != nil || !next.Link.Valid() || retired[next.Link.ID] {
				return ErrBundle
			}
			tail := l.Tail
			tail.Hops = tail.Hops[1:]
			digest, _ := tail.Key()
			if next.Link.Digest != digest || !ValidProxyCredential(tail.Hops[0].Endpoint, next.Credential, b.NodeID) {
				return ErrBundle
			}
			if previous, ok := outgoing[next.Link.ID]; ok && previous != *next {
				return ErrBundle
			}
			outgoing[next.Link.ID] = *next
		}
	}
	for id := range outgoing {
		if ids[id] {
			return ErrBundle
		}
	}
	return nil
}

func pinBefore(a, b Pin) bool {
	return a.ID < b.ID || a.ID == b.ID && a.Revision < b.Revision
}

// CheckLinkPair binds a sender's private next hop to the exact receiver
// socket, public transport parameters, internal identity and credentials.
// B04 must check against the candidate or confirmed receiver before arming.
func CheckLinkPair(sender, receiver ListenerV2) error {
	if sender.Forward.Next == nil || receiver.Internal == nil ||
		len(sender.Tail.Hops) < 2 || len(receiver.Tail.Hops) < 1 || receiver.Entry != nil {
		return ErrBinding
	}
	suffix := sender.Tail
	suffix.Hops = suffix.Hops[1:]
	expected, err := suffix.Key()
	actual, e := receiver.Tail.Key()
	next := sender.Forward.Next
	from := next.Credential
	to := receiver.Socket.Credential
	if err != nil || e != nil || expected != actual || next.Link != *receiver.Internal ||
		next.Link.Digest != actual || receiver.Socket.Port != suffix.Hops[0].Endpoint.Port ||
		from.Audience != sender.Tail.Hops[0].NodeID || to.Audience != receiver.Tail.Hops[0].NodeID ||
		from.Ref != to.Ref || from.Password != to.Password || from.UUID != to.UUID {
		return ErrBinding
	}
	return nil
}

func contains(values []string, key string) bool {
	for _, s := range values {
		if s == key {
			return true
		}
	}
	return false
}

// LoopInventory must be supplied from the actual node and Controller topology.
// ProtectedEndpoints includes mesh listener/tunnel/control/API tuples and aliases;
// ProtectedNetworks includes mesh TUN and fake-IP ranges. At apply time, the
// inventory includes current and candidate processes, sockets and interfaces.
type LoopInventory struct {
	LocalAddresses      []netip.Addr
	ProtectedEndpoints  []netip.AddrPort
	ProtectedNetworks   []netip.Prefix
	ProtectedInterfaces []string
}

// CheckEgressTargetLoops validates a pinned target against trusted topology.
// It is also used before admitting restricted probe evidence, without creating
// a bundle or placeholder authentication material.
func CheckEgressTargetLoops(s EgressTarget, inv LoopInventory, sockets []SocketV2) error {
	if !s.Valid() || len(inv.LocalAddresses) == 0 || inv.ProtectedEndpoints == nil || inv.ProtectedNetworks == nil || inv.ProtectedInterfaces == nil {
		return ErrBundle
	}
	if s.Type == "interface" && contains(inv.ProtectedInterfaces, s.Interface) {
		return ErrBundle
	}
	ip, proxy := dialIP(s.DialIP)
	if !proxy {
		return nil
	}
	for _, p := range inv.ProtectedNetworks {
		if p.Contains(ip) {
			return ErrBundle
		}
	}
	for _, ep := range inv.ProtectedEndpoints {
		if ep.Port() == s.Port && ep.Addr().Unmap() == ip {
			return ErrBundle
		}
	}
	local := ip.IsLoopback()
	for _, addr := range inv.LocalAddresses {
		local = local || addr.Unmap() == ip
	}
	for _, l := range sockets {
		if l.Port == s.Port && l.Transport != "udp" && (l.Address == ip.String() || local && addressesOverlap(l.Address, ip.String())) {
			return ErrBundle
		}
	}
	return nil
}
func (b BundleV2) CheckEgressLoops(inv LoopInventory) error {
	if b.Validate() != nil || len(inv.LocalAddresses) == 0 || inv.ProtectedEndpoints == nil || inv.ProtectedNetworks == nil || inv.ProtectedInterfaces == nil {
		return ErrBundle
	}
	sockets := make([]SocketV2, 0, len(b.Listeners))
	for _, l := range b.Listeners {
		sockets = append(sockets, l.Socket)
	}
	for _, p := range b.Terminal {
		for _, t := range p.Spec.Targets {
			if CheckEgressTargetLoops(t.Spec, inv, sockets) != nil {
				return ErrBundle
			}
		}
	}
	return nil
}

func EncodeBundleV2(b BundleV2) ([]byte, ArtifactKey, error) {
	if b.Validate() != nil {
		return nil, ArtifactKey{}, ErrBundle
	}
	data, err := json.Marshal(b)
	if err != nil || len(data) > MaxBundleBytes {
		return nil, ArtifactKey{}, ErrBundle
	}
	return data, ArtifactKey{b.TaskID, b.NodeID, b.TargetRevision, DigestBytes(data)}, nil
}

// No format auto-detection or fallback. The production DecodeBundle still
// rejects format 2 (and all future versions), even with a valid digest.
func DecodeBundleV2(data []byte, key ArtifactKey, authenticatedNode domain.ID) (BundleV2, error) {
	var b BundleV2
	if len(data) > MaxBundleBytes || !key.TaskID.Valid() || !key.NodeID.Valid() ||
		key.NodeID != authenticatedNode || !validTarget(key.TargetRevision, key.TargetDigest) ||
		DigestBytes(data) != key.TargetDigest {
		return b, ErrBinding
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF {
		return BundleV2{}, ErrBundle
	}
	encoded, actual, err := EncodeBundleV2(b)
	if err != nil || !bytes.Equal(encoded, data) {
		return BundleV2{}, ErrBundle
	}
	if actual != key {
		return BundleV2{}, ErrBinding
	}
	return b, nil
}

func (b BundleV2) BindCommand(c Command) error {
	_, key, err := EncodeBundleV2(b)
	if err != nil || b.Kind != c.Kind || key.TaskID != c.TaskID || key.NodeID != c.NodeID ||
		key.TargetRevision != c.TargetRevision || key.TargetDigest != c.TargetDigest ||
		b.RevocationEpoch != c.RevocationEpoch {
		return ErrBinding
	}
	return nil
}
