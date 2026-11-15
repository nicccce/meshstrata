package control

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
)

const (
	BundleVersion  = 1
	MaxBundleBytes = 1 << 20
	MaxListeners   = 128
	MaxTombstones  = 512
	MaxResources   = 256
)

var (
	ErrBundle        = errors.New("INVALID_BUNDLE")
	ErrBinding       = errors.New("ARTIFACT_BINDING_MISMATCH")
	ErrAuthorization = errors.New("AUTHORIZATION_CONFLICT")
)

// Bundle is PRIVATE node-scoped input, never a management DTO or a log value.
// There is deliberately no self digest: ArtifactKey.TargetDigest hashes EncodeBundle's
// exact bytes, including TaskID, NodeID, TargetRevision and every revision pin.
// V1 supports the single-node direct/reject subset; B06-B08 extend via a new format.
type Bundle struct {
	Format          int              `json:"format"`
	Kind            domain.TaskKind  `json:"kind"`
	TaskID          domain.ID        `json:"task_id"`
	NodeID          domain.ID        `json:"node_id"`
	TargetRevision  domain.Revision  `json:"target_revision"`
	RevocationEpoch domain.Revision  `json:"revocation_epoch"`
	Rules           RuleInput        `json:"rules"`
	DNS             DNSInput         `json:"dns"`
	Egress          EgressInput      `json:"egress"`
	Listeners       []BundleListener `json:"listeners"`  // sorted by ListenerID; one identity each
	Tombstones      []Tombstone      `json:"tombstones"` // sorted by IdentityID, never removed locally
	Resources       []Resource       `json:"resources"`  // sorted by ID
}
type Pin struct {
	ID       domain.ID       `json:"id"`
	Revision domain.Revision `json:"revision"`
	Digest   string          `json:"digest"`
}
type RuleInput struct {
	Pin     Pin    `json:"pin"`
	Default string `json:"default"` // DIRECT or REJECT, sole MATCH in this format
}
type DNSInput struct {
	Pin       Pin      `json:"pin"`
	Bootstrap []string `json:"bootstrap"` // numeric IPs only
	Resolvers []string `json:"resolvers"` // numeric IP or https://numeric-IP[:port]/dns-query
	IPv6      bool     `json:"ipv6"`
}
type EgressInput struct {
	Pin  Pin    `json:"pin"`
	Mode string `json:"mode"` // direct or reject; no arbitrary scripts/paths/proxies
}

// Resource is a pinned bookkeeping dependency, not a URL or host file path.
type Resource struct {
	ID       domain.ID       `json:"id"`
	Kind     string          `json:"kind"` // listener, identity, certificate
	Revision domain.Revision `json:"revision"`
}

// LocalMaterial names Agent-provisioned material. Resolver allowlists ID/purpose,
// owner, private permissions and symlink-free containment before returning a value.
// Node management private keys and local API secrets have no representable purpose.
type LocalMaterial struct {
	ID      domain.ID `json:"id"`
	Purpose string    `json:"purpose"` // tls_certificate, tls_private_key, reality_private_key
}
type MaterialResolver interface {
	Resolve(context.Context, domain.ID, LocalMaterial) (string, error)
}

// PrivateCredential may contain ONLY this listener's Controller-allocated ingress
// password/UUID. Audience is the only node to which it may be disclosed. TLS/REALITY
// private keys remain node-local references. Ciphertext at rest belongs to B04/B05.
type PrivateCredential struct {
	Ref      domain.ID `json:"ref"`
	Audience domain.ID `json:"audience"`
	Password string    `json:"password"`
	UUID     string    `json:"uuid"`
}
type BundleAuthorization struct {
	EntryID    domain.ID       `json:"entry_id"`
	IdentityID domain.ID       `json:"identity_id"` // immutable generation ID; renewal gets a NEW ID
	Generation domain.Revision `json:"generation"`
	ExpiresAt  time.Time       `json:"expires_at"`
	RevokedAt  *time.Time      `json:"revoked_at"`
}
type BundleListener struct {
	ListenerID         domain.ID           `json:"listener_id"`
	NodeID             domain.ID           `json:"node_id"`
	Protocol           domain.Protocol     `json:"protocol"`
	Address            string              `json:"address"`
	Port               uint16              `json:"port"`
	Transport          string              `json:"transport"` // tcp / udp / tcp_udp (outer)
	BusinessUDP        bool                `json:"business_udp"`
	Authorization      BundleAuthorization `json:"authorization"`
	Credential         PrivateCredential   `json:"credential"`
	Cipher             string              `json:"cipher"`
	Certificate        *LocalMaterial      `json:"certificate"`
	PrivateKey         *LocalMaterial      `json:"private_key"`
	RealityKey         *LocalMaterial      `json:"reality_key"`
	RealityDestination string              `json:"reality_destination"`
	ServerNames        []string            `json:"server_names"`
	ShortIDs           []string            `json:"short_ids"`
}
type Tombstone struct {
	IdentityID domain.ID       `json:"identity_id"`
	Generation domain.Revision `json:"generation"`
	Epoch      domain.Revision `json:"epoch"` // Controller epoch; local expiry does not invent an epoch
	At         time.Time       `json:"at"`
	Reason     string          `json:"reason"` // expired / revoked
}

func DigestBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// ComponentDigest pins the exact typed component payload (excluding its Pin).
// Rules: string Default; DNS: DNSPolicy; Egress: string Mode. No secret input.
func ComponentDigest(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", ErrBundle
	}
	return DigestBytes(b), nil
}

type DNSPolicy struct {
	Bootstrap []string `json:"bootstrap"`
	Resolvers []string `json:"resolvers"`
	IPv6      bool     `json:"ipv6"`
}

func pinValid(p Pin, v any) bool {
	digest, err := ComponentDigest(v)
	return err == nil && p.ID.Valid() && p.Revision.Valid() && p.Digest == digest
}
func utc(t time.Time) bool {
	_, offset := t.Zone()
	return !t.IsZero() && offset == 0 && t.Year() >= 1 && t.Year() <= 9999
}
func material(m *LocalMaterial, purpose string) bool {
	return m != nil && m.ID.Valid() && m.Purpose == purpose
}
func resolverValid(s string) bool {
	if ip, e := netip.ParseAddr(s); e == nil {
		return ip.Zone() == "" && !ip.IsUnspecified() && !ip.IsMulticast()
	}
	u, e := url.Parse(s)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Path != "/dns-query" || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	ip, e := netip.ParseAddr(u.Hostname())
	if e != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	if u.Port() != "" {
		if address, err := netip.ParseAddrPort(net.JoinHostPort(u.Hostname(), u.Port())); err != nil || address.Port() == 0 {
			return false
		}
	}
	return true
}
func (b Bundle) Validate() error {
	switch b.Kind {
	case domain.Publish, domain.Rollback, domain.Revoke, domain.Cleanup:
	default:
		return ErrBundle
	}

	if b.Format != BundleVersion || !b.TaskID.Valid() || !b.NodeID.Valid() || !b.TargetRevision.Valid() || !b.RevocationEpoch.Valid() || b.Listeners == nil || len(b.Listeners) > MaxListeners || b.Tombstones == nil || len(b.Tombstones) > MaxTombstones || b.Resources == nil || len(b.Resources) > MaxResources {
		return ErrBundle
	}
	if (b.Rules.Default != "DIRECT" && b.Rules.Default != "REJECT") || !pinValid(b.Rules.Pin, b.Rules.Default) || (b.Egress.Mode != "direct" && b.Egress.Mode != "reject") || !pinValid(b.Egress.Pin, b.Egress.Mode) || (b.Rules.Default == "DIRECT" && b.Egress.Mode != "direct") {
		return ErrBundle
	}
	if !pinValid(b.DNS.Pin, DNSPolicy{b.DNS.Bootstrap, b.DNS.Resolvers, b.DNS.IPv6}) || len(b.DNS.Bootstrap) < 1 || len(b.DNS.Bootstrap) > 8 || len(b.DNS.Resolvers) < 1 || len(b.DNS.Resolvers) > 8 {
		return ErrBundle
	}
	seenDNS := map[string]bool{}
	for _, s := range b.DNS.Bootstrap {
		ip, e := netip.ParseAddr(s)
		if e != nil || ip.Zone() != "" || ip.IsUnspecified() || ip.IsMulticast() || seenDNS["b"+s] {
			return ErrBundle
		}
		seenDNS["b"+s] = true
	}
	for _, s := range b.DNS.Resolvers {
		if len(s) > 256 || !resolverValid(s) || seenDNS["r"+s] {
			return ErrBundle
		}
		seenDNS["r"+s] = true
	}
	tombs := map[domain.ID]bool{}
	for i, t := range b.Tombstones {
		if !t.IdentityID.Valid() || !t.Generation.Valid() || !t.Epoch.Valid() || t.Epoch > b.RevocationEpoch || !utc(t.At) || (t.Reason != "expired" && t.Reason != "revoked") || (i > 0 && b.Tombstones[i-1].IdentityID >= t.IdentityID) {
			return ErrBundle
		}
		tombs[t.IdentityID] = true
	}
	ids := map[domain.ID]bool{}
	for i, l := range b.Listeners {
		a := l.Authorization
		ip, e := netip.ParseAddr(l.Address)
		if !l.ListenerID.Valid() || (i > 0 && b.Listeners[i-1].ListenerID >= l.ListenerID) || l.NodeID != b.NodeID || e != nil || ip.Zone() != "" || ip.Is4In6() || ip.IsMulticast() || ip.String() != l.Address || l.Port == 0 || !a.EntryID.Valid() || !a.IdentityID.Valid() || !a.Generation.Valid() || !utc(a.ExpiresAt) || (a.RevokedAt != nil && !utc(*a.RevokedAt)) || ids[a.IdentityID] || tombs[a.IdentityID] || !l.Credential.Ref.Valid() || l.Credential.Audience != b.NodeID || !utf8.ValidString(l.Credential.Password) || len(l.Credential.Password) > 512 || len(l.Credential.UUID) > 36 || l.ServerNames == nil || l.ShortIDs == nil || len(l.ServerNames) > 8 || len(l.ShortIDs) > 8 || !utf8.ValidString(l.RealityDestination) || len(l.RealityDestination) > 320 {
			return ErrBundle
		}
		ids[a.IdentityID] = true
		for _, s := range l.ServerNames {
			if !utf8.ValidString(s) || len(s) > 253 || s == "" || strings.ContainsAny(s, " /\\\r\n,#") {
				return ErrBundle
			}
		}
		tcp, udp := l.Protocol != domain.Hysteria2, l.Protocol == domain.Hysteria2 || l.Protocol == domain.SS2022 && l.BusinessUDP
		transport := "tcp"
		if !tcp {
			transport = "udp"
		} else if udp {
			transport = "tcp_udp"
		}
		if l.Transport != transport {
			return ErrBundle
		}
		entry := core.Entry{NodeID: string(l.NodeID), ListenerID: string(l.ListenerID), IdentityID: string(a.IdentityID), Protocol: core.Protocol(l.Protocol), Address: l.Address, Port: l.Port, BusinessUDP: l.BusinessUDP, Cipher: l.Cipher, Password: l.Credential.Password, UUID: l.Credential.UUID, RealityDestination: l.RealityDestination, ServerNames: l.ServerNames, ShortIDs: l.ShortIDs}
		// Validate protocol credentials with the frozen B03 builder; local key values
		// are resolved/validated only by B05. These placeholders never leave validation.
		switch l.Protocol {
		case domain.SS2022:
			if l.Credential.UUID != "" || l.Certificate != nil || l.PrivateKey != nil || l.RealityKey != nil {
				return ErrBundle
			}
		case domain.Trojan, domain.Hysteria2:
			if l.Credential.UUID != "" || l.Cipher != "" || !material(l.Certificate, "tls_certificate") || !material(l.PrivateKey, "tls_private_key") || l.RealityKey != nil {
				return ErrBundle
			}
			entry.Certificate = "local"
			entry.PrivateKey = "local"
		case domain.VLESS:
			if l.Credential.Password != "" || l.Cipher != "" || l.Certificate != nil || l.PrivateKey != nil || !material(l.RealityKey, "reality_private_key") {
				return ErrBundle
			}
			entry.RealityPrivateKey = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		default:
			return ErrBundle
		}
		if l.Protocol != domain.VLESS && (l.RealityDestination != "" || len(l.ServerNames) != 0 || len(l.ShortIDs) != 0) {
			return ErrBundle
		}
		if _, e = core.BuildListener(entry); e != nil {
			return ErrBundle
		}
		for _, prev := range b.Listeners[:i] {
			if prev.Port == l.Port && transportsOverlap(prev.Transport, l.Transport) && addressesOverlap(prev.Address, l.Address) {
				return ErrBundle
			}
		}
	}
	for i, r := range b.Resources {
		if !r.ID.Valid() || !r.Revision.Valid() || (i > 0 && b.Resources[i-1].ID >= r.ID) || (r.Kind != "listener" && r.Kind != "identity" && r.Kind != "certificate") {
			return ErrBundle
		}
	}
	return nil
}
func transportsOverlap(a, b string) bool { return a == b || a == "tcp_udp" || b == "tcp_udp" }

// Conservatively reserve :: across both families (no Controller-supplied v6only).
func addressesOverlap(a, b string) bool {
	x, _ := netip.ParseAddr(a)
	y, _ := netip.ParseAddr(b)
	return x == y || x == netip.IPv6Unspecified() || y == netip.IPv6Unspecified() || x.Is4() == y.Is4() && (x.IsUnspecified() || y.IsUnspecified())
}
func EncodeBundle(b Bundle) ([]byte, ArtifactKey, error) {
	if b.Validate() != nil {
		return nil, ArtifactKey{}, ErrBundle
	}
	data, err := json.Marshal(b)
	if err != nil || len(data) > MaxBundleBytes {
		return nil, ArtifactKey{}, ErrBundle
	}
	return data, ArtifactKey{b.TaskID, b.NodeID, b.TargetRevision, DigestBytes(data)}, nil
}

// DecodeBundle requires canonical bytes, thereby rejecting duplicates, missing
// fields, unknown fields, alternate ordering/whitespace and trailing content.
// A valid digest is integrity only; ArtifactSource still authorizes the ledger.
func DecodeBundle(data []byte, key ArtifactKey, authenticatedNode domain.ID) (Bundle, error) {
	var b Bundle
	if len(data) > MaxBundleBytes || !key.TaskID.Valid() || !key.NodeID.Valid() || key.NodeID != authenticatedNode || !validTarget(key.TargetRevision, key.TargetDigest) || DigestBytes(data) != key.TargetDigest {
		return b, ErrBinding
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&b) != nil || d.Decode(new(any)) != io.EOF {
		return Bundle{}, ErrBundle
	}
	encoded, actual, err := EncodeBundle(b)
	if err != nil || !bytes.Equal(encoded, data) {
		return Bundle{}, ErrBundle
	}
	if actual != key {
		return Bundle{}, ErrBinding
	}
	return b, nil
}

// BindCommand additionally pins the epoch and rejects non-configuration tasks.
func (b Bundle) BindCommand(c Command) error {
	switch c.Kind {
	case domain.Publish, domain.Rollback, domain.Revoke, domain.Cleanup:
	default:
		return ErrBinding
	}
	_, key, err := EncodeBundle(b)
	if err != nil || b.Kind != c.Kind || key.TaskID != c.TaskID || key.NodeID != c.NodeID || key.TargetRevision != c.TargetRevision || key.TargetDigest != c.TargetDigest || b.RevocationEpoch != c.RevocationEpoch {
		return ErrBinding
	}
	return nil
}
