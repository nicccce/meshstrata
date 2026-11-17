package control

import (
	"bytes"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/nicccce/meshstrata/internal/domain"
)

func testPin(id domain.ID, payload any) Pin {
	hash, _ := ComponentDigest(payload)
	return Pin{id, 1, hash}
}

func v2Fixture() (BundleV2, BundleV2, BundleV2) {
	legacy := bundleFixture()
	dns := legacy.DNS
	terminalNode := bid("terminal")
	target := EgressTarget{NodeID: terminalNode, Type: "http", Host: "proxy.example.invalid", DialIP: "192.0.2.100", Port: 3128, CredentialRef: bid("egress_auth")}
	targetPin := testPin(bid("target"), target)
	spec := EgressSpec{NodeID: terminalNode, DNS: dns.Pin, Targets: []PinnedEgressTarget{{targetPin, target}},
		Rules: []EgressRule{{"203.0.113.0/24", &targetPin, false}, {"MATCH", nil, false}}}
	pin := testPin(bid("policy"), spec)
	socket := SocketSpec{Address: "0.0.0.0", Transport: "tcp", ServerNames: []string{}, ShortIDs: []string{}}
	socketPin := testPin(bid("socket_config"), socket)
	ids := []domain.ID{legacy.NodeID, bid("relay"), terminalNode}
	hosts := []string{"192.0.2.1", "192.0.2.2", "192.0.2.3"}
	tail := RouteTail{Egress: pin}
	for i, node := range ids {
		tail.Hops = append(tail.Hops, HopSpec{node, socketPin, dns.Pin, ProxyEndpoint{
			Host: hosts[i], Port: uint16(32000 + i), Protocol: domain.SS2022, Cipher: "2022-blake3-aes-128-gcm"}, hosts[i]})
	}
	var bundles [3]BundleV2
	for i, h := range tail.Hops {
		cred := legacy.Listeners[0].Credential
		cred.Ref = bid("cred" + string(rune('a'+i)))
		cred.Audience = h.NodeID
		l := ListenerV2{Socket: SocketV2{ListenerID: bid("listener" + string(rune('a'+i))), Address: socket.Address,
			Port: h.Endpoint.Port, Transport: socket.Transport, Credential: cred, ServerNames: []string{}, ShortIDs: []string{}},
			Tail: RouteTail{Hops: tail.Hops[i:], Egress: pin}}
		if i == 0 {
			a := legacy.Listeners[0].Authorization
			l.Entry = &a
			l.Socket.Port++ // independently allocated subscription port
		} else {
			digest, _ := l.Tail.Key()
			l.Internal = &Pin{ID: bid("link" + string(rune('a'+i))), Revision: 1, Digest: digest}
		}
		bundles[i] = BundleV2{Format: 2, Kind: domain.Publish, TaskID: legacy.TaskID, NodeID: h.NodeID,
			TargetRevision: 2, RevocationEpoch: 3, DNS: dns, Listeners: []ListenerV2{l}, Terminal: []TerminalV2{},
			Tombstones: []Tombstone{}, RetiredLinks: []InternalRetirement{}}
	}
	for i := 0; i < 2; i++ {
		receiver := bundles[i+1].Listeners[0]
		cred := receiver.Socket.Credential
		cred.Audience = bundles[i].NodeID
		bundles[i].Listeners[0].Forward.Next = &NextHop{*receiver.Internal, cred}
	}
	bundles[2].Listeners[0].Forward.Terminal = &pin
	bundles[2].Terminal = append(bundles[2].Terminal, TerminalV2{pin, spec, []EgressAuth{{targetPin, target.CredentialRef, terminalNode, "fixture-user", "fixture-password"}}})
	return bundles[0], bundles[1], bundles[2]
}

func TestOfflineBundleV2RoundTripAndVersionGates(t *testing.T) {
	sender, relay, terminal := v2Fixture()
	for _, b := range []BundleV2{sender, relay, terminal} {
		data, key, err := EncodeBundleV2(b)
		if err != nil {
			t.Fatalf("valid node %s: %v", b.NodeID, err)
		}
		roundTrip, err := DecodeBundleV2(data, key, b.NodeID)
		if err != nil {
			t.Fatal(err)
		}
		cmd := Command{TaskID: key.TaskID, NodeID: key.NodeID, TargetRevision: key.TargetRevision, TargetDigest: key.TargetDigest, Kind: b.Kind, RevocationEpoch: b.RevocationEpoch}
		if roundTrip.BindCommand(cmd) != nil {
			t.Fatal("command binding")
		}
		cmd.RevocationEpoch++
		if roundTrip.BindCommand(cmd) == nil {
			t.Fatal("wrong epoch")
		}
		if _, err := DecodeBundle(data, key, b.NodeID); !errors.Is(err, ErrBundle) {
			t.Fatal("v1 runtime accepted v2", err)
		}
		if _, err := DecodeBundleV2(data, key, bid("wrong")); !errors.Is(err, ErrBinding) {
			t.Fatal("wrong audience accepted")
		}
		for _, bad := range [][]byte{
			bytes.Replace(data, []byte(`"format":2`), []byte(`"format":3`), 1),
			bytes.Replace(data, []byte(`"format":2`), []byte(`"format":2,"format":2`), 1),
			bytes.Replace(data, []byte(`"format":2`), []byte(`"format":2,"yaml":"anything"`), 1),
			append(append([]byte{}, data...), '\n'),
		} {
			badKey := key
			badKey.TargetDigest = DigestBytes(bad)
			if _, err := DecodeBundleV2(bad, badKey, b.NodeID); !errors.Is(err, ErrBundle) {
				t.Fatal("noncanonical/unknown accepted", err)
			}
		}
	}
	v1 := bundleFixture()
	data, key, err := EncodeBundle(v1)
	if err != nil {
		t.Fatal(err)
	}
	// Measured on the unmodified R02 baseline. Protect existing encrypted
	// artifacts, digests and checkpoints from accidental re-encoding changes.
	const legacyDigest = "bd40828e1c1fff7753667712fb887ba3c38a7d2575a8ece79bdd8d153ca8b7ea"
	if key.TargetDigest != legacyDigest {
		t.Fatal("format 1 canonical bytes changed")
	}
	if _, err := DecodeBundleV2(data, key, v1.NodeID); !errors.Is(err, ErrBundle) {
		t.Fatal("v2 codec should never reinterpret v1")
	}
	if _, err := DecodeBundle(data, key, v1.NodeID); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]ListenerV2{{sender.Listeners[0], relay.Listeners[0]}, {relay.Listeners[0], terminal.Listeners[0]}} {
		if err := CheckLinkPair(pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestV2RejectsMixedRevisionAndBinding(t *testing.T) {
	for name, change := range map[string]func(*BundleV2){
		"mixed target revision": func(b *BundleV2) {
			p := *b.Terminal[0].Spec.Rules[0].Target
			p.Revision++
			b.Terminal[0].Spec.Rules[0].Target = &p
		},
		"early match":    func(b *BundleV2) { b.Terminal[0].Spec.Rules[0].Destination = "MATCH" },
		"proxy audience": func(b *BundleV2) { b.Terminal[0].Auth[0].Audience = bid("wrong") },
		"fake dual role": func(b *BundleV2) { a := bundleFixture().Listeners[0].Authorization; b.Listeners[0].Entry = &a },
		"retired link": func(b *BundleV2) {
			b.RetiredLinks = append(b.RetiredLinks, InternalRetirement{*b.Listeners[0].Internal, 2})
		},
		"host bits in CIDR": func(b *BundleV2) { b.Terminal[0].Spec.Rules[0].Destination = "203.0.113.8/24" },
	} {
		t.Run(name, func(t *testing.T) {
			_, _, b := v2Fixture()
			change(&b)
			// Even a newly pinned policy must fail its structural checks.
			_, _, err := EncodeBundleV2(b)
			if !errors.Is(err, ErrBundle) {
				t.Fatal("invalid bundle accepted", err)
			}
			if name == "mixed target revision" || name == "early match" || name == "host bits in CIDR" {
				p := b.Terminal[0].Spec
				if p.Validate(testPin(b.Terminal[0].Pin.ID, p), false) == nil {
					t.Fatal("re-pinned invalid policy accepted")
				}
			}
		})
	}
	a, b, _ := v2Fixture()
	old := a.Listeners[0]
	changed := old
	auth := *changed.Entry
	auth.ExpiresAt = auth.ExpiresAt.AddDate(0, 0, 1)
	changed.Entry = &auth
	if SameV2Grant(old, old) != nil || SameV2Grant(old, changed) == nil {
		t.Fatal("renewal reused immutable binding")
	}
	receiver := b.Listeners[0]
	if SameInternalLink(receiver, receiver) != nil || SameInternalLink(receiver, old) == nil {
		t.Fatal("internal and entry identities collapsed")
	}
	receiver.Socket.Credential.Ref = bid("incorrect")
	if CheckLinkPair(old, receiver) == nil {
		t.Fatal("wrong receiving credential accepted")
	}
	tail := old.Tail
	tail.Hops = append([]HopSpec{}, tail.Hops...)
	tail.Hops[2].DNS.Revision++
	newKey, _ := tail.Key()
	oldKey, _ := old.Tail.Key()
	if newKey == "" || newKey == oldKey {
		t.Fatal("suffix not pinned")
	}
	a.Listeners[0].Tail = tail
	if a.Validate() == nil {
		t.Fatal("accepted link from old suffix")
	}
}

func TestV2OfflineLoopInventory(t *testing.T) {
	_, _, b := v2Fixture()
	inv := LoopInventory{LocalAddresses: []netip.Addr{netip.MustParseAddr("192.0.2.3")},
		ProtectedEndpoints: []netip.AddrPort{}, ProtectedNetworks: []netip.Prefix{}, ProtectedInterfaces: []string{}}
	if err := b.CheckEgressLoops(inv); err != nil {
		t.Fatal(err)
	}
	inv.ProtectedEndpoints = append(inv.ProtectedEndpoints, netip.MustParseAddrPort("192.0.2.100:3128"))
	if b.CheckEgressLoops(inv) == nil {
		t.Fatal("egress to known mesh socket accepted")
	}
	inv.ProtectedEndpoints = nil
	if b.CheckEgressLoops(inv) == nil {
		t.Fatal("missing loop inventory accepted")
	}
}

func TestDomainsArePureNames(t *testing.T) {
	if !DomainName("xn--fsqu00a.example") {
		t.Fatal("canonical name rejected")
	}
	for _, name := range []string{"a.", "*.example", "https://a.example/", "127.0.0.1", "2001:db8::1", "foo,bar", " A", "a\nb", strings.Repeat("a", 64)} {
		if DomainName(name) {
			t.Fatalf("not a domain: %q", name)
		}
	}
}
