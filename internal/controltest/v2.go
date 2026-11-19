// Package controltest supplies private, synthetic protocol fixtures for local
// component tests. Its addresses and credentials must never be used in deployment.
package controltest

import (
	"strings"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func ID(kind string) domain.ID { return domain.ID(kind + "_" + strings.Repeat("1", 32)) }
func Pin(kind string, value any) control.Pin {
	digest, _ := control.ComponentDigest(value)
	return control.Pin{ID: ID(kind), Revision: 1, Digest: digest}
}

// Chain builds three independently bound node artifacts with a shared suffix.
func Chain() [3]control.BundleV2 {
	dns := control.DNSInput{Bootstrap: []string{"127.0.0.1"}, Resolvers: []string{"127.0.0.1"}}
	dns.Pin = Pin("dns", control.DNSPolicy{Bootstrap: dns.Bootstrap, Resolvers: dns.Resolvers})
	nodes := []domain.ID{ID("ingress"), ID("relay"), ID("terminal")}
	target := control.EgressTarget{NodeID: nodes[2], Type: "node_default"}
	targetPin := Pin("target", target)
	spec := control.EgressSpec{NodeID: nodes[2], DNS: dns.Pin, Targets: []control.PinnedEgressTarget{{Pin: targetPin, Spec: target}}, Rules: []control.EgressRule{{Destination: "127.0.0.0/8", Target: &targetPin, NoResolve: true}, {Destination: "MATCH"}}}
	policy := Pin("policy", spec)
	socket := control.SocketSpec{Address: "127.0.0.1", Transport: "tcp", ServerNames: []string{}, ShortIDs: []string{}}
	config := Pin("socket", socket)
	tail := control.RouteTail{Egress: policy}
	for i, node := range nodes {
		tail.Hops = append(tail.Hops, control.HopSpec{NodeID: node, NodeConfig: config, DNS: dns.Pin, Endpoint: control.ProxyEndpoint{Host: "127.0.0.1", Port: uint16(32100 + i), Protocol: domain.SS2022, Cipher: "2022-blake3-aes-128-gcm"}, DialIP: "127.0.0.1"})
	}
	var out [3]control.BundleV2
	for i, node := range nodes {
		l := control.ListenerV2{Socket: control.SocketV2{ListenerID: ID("listener" + string(rune('a'+i))), Address: socket.Address, Port: tail.Hops[i].Endpoint.Port, Transport: socket.Transport, Credential: control.PrivateCredential{Ref: ID("credential" + string(rune('a'+i))), Audience: node, Password: "AAAAAAAAAAAAAAAAAAAAAA=="}, ServerNames: []string{}, ShortIDs: []string{}}, Tail: control.RouteTail{Hops: tail.Hops[i:], Egress: policy}}
		if i == 0 {
			l.Entry = &control.BundleAuthorization{EntryID: ID("entry"), IdentityID: ID("identity"), Generation: 1, ExpiresAt: time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)}
		} else {
			digest, _ := l.Tail.Key()
			l.Internal = &control.Pin{ID: ID("link" + string(rune('a'+i))), Revision: 1, Digest: digest}
		}
		out[i] = control.BundleV2{Format: 2, Kind: domain.Publish, TaskID: ID("task" + string(rune('a'+i))), NodeID: node, TargetRevision: 2, RevocationEpoch: 3, DNS: dns, Listeners: []control.ListenerV2{l}, Terminal: []control.TerminalV2{}, Tombstones: []control.Tombstone{}, RetiredLinks: []control.InternalRetirement{}}
	}
	for i := 0; i < 2; i++ {
		credential := out[i+1].Listeners[0].Socket.Credential
		credential.Audience = nodes[i]
		out[i].Listeners[0].Forward.Next = &control.NextHop{Link: *out[i+1].Listeners[0].Internal, Credential: credential}
	}
	out[2].Listeners[0].Forward.Terminal = &policy
	out[2].Terminal = []control.TerminalV2{{Pin: policy, Spec: spec, Auth: []control.EgressAuth{}}}
	return out
}
