package control

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/domain"
)

func bid(kind string) domain.ID { return domain.ID(kind + "_" + strings.Repeat("1", 32)) }
func bundleFixture() Bundle {
	now := time.Date(2026, 11, 15, 0, 0, 0, 0, time.UTC)
	b := Bundle{Format: 1, Kind: domain.Publish, TaskID: bid("task"), NodeID: bid("node"), TargetRevision: 2, RevocationEpoch: 3, Rules: RuleInput{Default: "DIRECT"}, DNS: DNSInput{Bootstrap: []string{"1.1.1.1"}, Resolvers: []string{"https://1.1.1.1/dns-query"}}, Egress: EgressInput{Mode: "direct"}, Listeners: []BundleListener{}, Tombstones: []Tombstone{}, Resources: []Resource{}}
	b.Rules.Pin = Pin{bid("rules"), 1, ""}
	b.Rules.Pin.Digest, _ = ComponentDigest(b.Rules.Default)
	b.DNS.Pin = Pin{bid("dns"), 1, ""}
	b.DNS.Pin.Digest, _ = ComponentDigest(DNSPolicy{b.DNS.Bootstrap, b.DNS.Resolvers, b.DNS.IPv6})
	b.Egress.Pin = Pin{bid("egress"), 1, ""}
	b.Egress.Pin.Digest, _ = ComponentDigest(b.Egress.Mode)
	b.Listeners = append(b.Listeners, BundleListener{ListenerID: bid("listener"), NodeID: b.NodeID, Protocol: domain.SS2022, Address: "0.0.0.0", Port: 12345, Transport: "tcp", Authorization: BundleAuthorization{bid("entry"), bid("identity"), 1, now.Add(time.Hour), nil}, Credential: PrivateCredential{bid("credential"), b.NodeID, "AAAAAAAAAAAAAAAAAAAAAA==", ""}, Cipher: "2022-blake3-aes-128-gcm", ServerNames: []string{}, ShortIDs: []string{}})
	return b
}
func TestBundleCanonicalBinding(t *testing.T) {
	b := bundleFixture()
	data, key, e := EncodeBundle(b)
	if e != nil {
		t.Fatal(e)
	}
	again, key2, e := EncodeBundle(b)
	if e != nil || !bytes.Equal(data, again) || key != key2 {
		t.Fatal("not deterministic")
	}
	if _, e = DecodeBundle(data, key, b.NodeID); e != nil {
		t.Fatal(e)
	}
	command := Command{TaskID: key.TaskID, NodeID: key.NodeID, Kind: domain.Publish, TargetRevision: key.TargetRevision, TargetDigest: key.TargetDigest, RevocationEpoch: b.RevocationEpoch}
	if b.BindCommand(command) != nil {
		t.Fatal("bind failed")
	}
	command.RevocationEpoch++
	if b.BindCommand(command) == nil {
		t.Fatal("wrong epoch")
	}
	for _, change := range []func(*ArtifactKey){func(k *ArtifactKey) { k.TaskID = bid("other") }, func(k *ArtifactKey) { k.NodeID = bid("other") }, func(k *ArtifactKey) { k.TargetRevision++ }, func(k *ArtifactKey) { k.TargetDigest = strings.Repeat("0", 64) }} {
		bad := key
		change(&bad)
		if _, e = DecodeBundle(data, bad, b.NodeID); !errors.Is(e, ErrBinding) {
			t.Fatal("unbound artifact", e)
		}
	}
	b.DNS.Pin.Revision++
	changed, _, _ := EncodeBundle(b)
	if DigestBytes(data) == DigestBytes(changed) {
		t.Fatal("revision vector not covered")
	}
	for _, bad := range [][]byte{append(append([]byte{}, data...), '\n'), append(data, data...), bytes.Replace(data, []byte(`"format":1`), []byte(`"format":1,"format":1`), 1), bytes.Replace(data, []byte(`"format":1`), []byte(`"format":1,"extra":1`), 1), bytes.Replace(data, []byte(`"format":1`), []byte(`"format":2`), 1), []byte(strings.Repeat("x", MaxBundleBytes+1))} {
		k := key
		k.TargetDigest = DigestBytes(bad)
		if _, e = DecodeBundle(bad, k, b.NodeID); e == nil {
			t.Fatal("noncanonical/unknown/oversized accepted")
		}
	}
}
func TestBundleRejectsUnsafeAndInconsistentInputs(t *testing.T) {
	cases := map[string]func(*Bundle){
		"invalid utf8": func(b *Bundle) { b.Listeners[0].Credential.Password = string([]byte{255}) },
		"wrong kind":   func(b *Bundle) { b.Kind = domain.QueryStatus },
		"wrong node":   func(b *Bundle) { b.Listeners[0].NodeID = bid("other") },
		"audience":     func(b *Bundle) { b.Listeners[0].Credential.Audience = bid("other") },
		"generation":   func(b *Bundle) { b.Listeners[0].Authorization.Generation = 0 },
		"path":         func(b *Bundle) { b.Listeners[0].PrivateKey = &LocalMaterial{bid("key"), "/etc/shadow"} },
		"shell":        func(b *Bundle) { b.Rules.Default = "MATCH,EXEC" },
		"digest":       func(b *Bundle) { b.Rules.Pin.Digest = strings.Repeat("0", 64) },
		"transport":    func(b *Bundle) { b.Listeners[0].Transport = "udp" },
		"duplicate identity": func(b *Bundle) {
			l := b.Listeners[0]
			l.ListenerID = bid("zlistener")
			l.Port++
			b.Listeners = append(b.Listeners, l)
		},
		"dual stack conflict": func(b *Bundle) {
			l := b.Listeners[0]
			l.ListenerID = bid("zlistener")
			l.Authorization.IdentityID = bid("zidentity")
			l.Address = "::"
			b.Listeners = append(b.Listeners, l)
		},
		"tombstone conflict": func(b *Bundle) {
			b.Tombstones = append(b.Tombstones, Tombstone{bid("identity"), 1, 1, b.Listeners[0].Authorization.ExpiresAt, "revoked"})
		},
		"count": func(b *Bundle) { b.Listeners = make([]BundleListener, MaxListeners+1) },
		"nil":   func(b *Bundle) { b.Resources = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			b := bundleFixture()
			change(&b)
			if _, _, e := EncodeBundle(b); e == nil {
				t.Fatal("accepted")
			}
		})
	}
	for _, p := range []domain.Protocol{domain.Trojan, domain.Hysteria2, domain.VLESS} {
		b := bundleFixture()
		l := &b.Listeners[0]
		l.Protocol = p
		l.Cipher = ""
		l.Credential.Password = "private-fixture-password"
		l.Certificate = &LocalMaterial{bid("cert"), "tls_certificate"}
		l.PrivateKey = &LocalMaterial{bid("key"), "tls_private_key"}
		if p == domain.Hysteria2 {
			l.Transport = "udp"
		}
		if p == domain.VLESS {
			l.Certificate = nil
			l.PrivateKey = nil
			l.Credential.Password = ""
			l.Credential.UUID = "00000000-0000-4000-8000-000000000001"
			l.RealityKey = &LocalMaterial{bid("key"), "reality_private_key"}
			l.RealityDestination = "127.0.0.1:443"
			l.ServerNames = []string{"example.invalid"}
			l.ShortIDs = []string{"abcd"}
		}
		if _, _, e := EncodeBundle(b); e != nil {
			t.Fatal(p, e)
		}
	}
}
func TestExpiryTombstonesSurviveRollbackAndRenewal(t *testing.T) {
	b := bundleFixture()
	at := b.Listeners[0].Authorization.ExpiresAt
	active, tombs, epoch, e := FilterAuthorizations(b, 5, nil, at)
	if e != nil || len(active) != 0 || len(tombs) != 1 || epoch != 5 {
		t.Fatal("expiry boundary", e)
	}
	// Old snapshot, clock and epoch cannot erase durable identity tombstones.
	active, tombs, epoch, e = FilterAuthorizations(b, epoch, tombs, at.Add(-time.Hour))
	if e != nil || len(active) != 0 || epoch != 5 {
		t.Fatal("resurrected", e)
	}
	b.Listeners[0].Authorization.IdentityID = bid("newidentity")
	b.Listeners[0].Authorization.Generation = 2
	b.Listeners[0].Authorization.ExpiresAt = at.Add(time.Hour)
	active, tombs, _, e = FilterAuthorizations(b, epoch, tombs, at)
	if e != nil || len(active) != 1 || len(tombs) != 1 {
		t.Fatal("new generation failed", e)
	}
	b.Listeners[0].Authorization.IdentityID = bid("identity")
	if _, _, _, e = FilterAuthorizations(b, epoch, tombs, at); e == nil {
		t.Fatal("same identity changed generation")
	}
}
func TestLocalEvidenceUsesRealTaskAndExactReceipt(t *testing.T) {
	b := bundleFixture()
	_, key, _ := EncodeBundle(b)
	now := b.Listeners[0].Authorization.ExpiresAt
	r := Result{TaskID: key.TaskID, NodeResult: domain.NodeResult{NodeID: key.NodeID, TargetRevision: key.TargetRevision, TargetDigest: key.TargetDigest, Verification: domain.NotChecked, Rollback: "none", Cleanup: "pending"}, Sequence: 2, Restrictions: []Restriction{{bid("identity"), bid("listener"), 1, "expired", now, 3, strings.Repeat("b", 64)}}}
	m := Message{Version: Version, Type: "hello", MessageID: bid("msg"), NodeID: key.NodeID, SentAt: now, Hello: &Hello{NodeID: key.NodeID, RevocationEpoch: 3, PendingResults: []Result{r}}}
	data, _ := json.Marshal(m)
	if _, e := Decode(data, key.NodeID); e != nil {
		t.Fatal(e)
	}
	r.Applied = true
	if r.validate(key.NodeID) == nil {
		t.Fatal("local expiry claimed original grant applied")
	}
	r.Applied = false
	r.Restrictions[0].IdentityID = ""
	if r.validate(key.NodeID) == nil {
		t.Fatal("bad evidence")
	}
}

func TestGrantCannotExtendOrReplaceIdentity(t *testing.T) {
	b := bundleFixture()
	_, key, _ := EncodeBundle(b)
	l := b.Listeners[0]
	g := Grant{key, l}
	if CheckGrant(g, l) != nil {
		t.Fatal("unchanged grant rejected")
	}
	l.Authorization.ExpiresAt = l.Authorization.ExpiresAt.Add(time.Hour)
	if CheckGrant(g, l) == nil {
		t.Fatal("same identity expiry extended")
	}
	l = g.Listener
	l.Credential.Password = "BBBBBBBBBBBBBBBBBBBBBB=="
	if CheckGrant(g, l) == nil {
		t.Fatal("same identity credential changed")
	}
}
