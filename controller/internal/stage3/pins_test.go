package stage3

import (
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

func fixtureID(prefix string) domain.ID { return domain.ID(prefix + "_" + strings.Repeat("1", 32)) }

func TestGenerationBindingRejectsStaleRouteAndForeignCredential(t *testing.T) {
	node, sub, route := fixtureID("node"), fixtureID("sub"), fixtureID("route")
	stub := func(prefix string) control.Pin {
		return control.Pin{ID: fixtureID(prefix), Revision: 1, Digest: strings.Repeat("a", 64)}
	}
	endpoint := control.ProxyEndpoint{Host: "192.0.2.10", Port: 22000, Protocol: domain.SS2022, Cipher: "2022-blake3-aes-128-gcm"}
	public := PublishedRoute{Name: "fixture", Tail: control.RouteTail{
		Hops:   []control.HopSpec{{NodeID: node, NodeConfig: stub("socket"), DNS: stub("dns"), Endpoint: endpoint, DialIP: "192.0.2.10"}},
		Egress: stub("egress")}, EgressIsDefault: true}
	hash, err := RouteDigest(public)
	if err != nil {
		t.Fatal(err)
	}
	public.Pin = control.Pin{ID: route, Revision: 1, Digest: hash}
	endpoint.Port++
	e := EntryView{ID: fixtureID("entry"), IdentityID: fixtureID("identity"), Generation: 1, Route: public,
		Endpoint: endpoint, ExpiresAt: time.Unix(2000000000, 0).UTC(), Sync: "confirmed", Cleanup: "none"}
	private := EntrySecret{e.IdentityID, control.PrivateCredential{Ref: fixtureID("credential"), Audience: sub, Password: "AAAAAAAAAAAAAAAAAAAAAA=="}}
	in := GenerationInput{ClientSnapshot{SubscriptionID: sub, Revision: 1, Profile: ProfileSnapshot{Pin: stub("profile")}, Entries: []EntryView{e}}, []EntrySecret{private}}
	if err := ValidateGenerationBinding(in); err != nil {
		t.Fatal(err)
	}
	in.Secrets[0].Credential.Audience = fixtureID("another_sub")
	if ValidateGenerationBinding(in) == nil {
		t.Fatal("foreign credential")
	}
	in.Secrets[0] = private
	in.Snapshot.Entries[0].Route.EgressIsDefault = false
	if ValidateGenerationBinding(in) == nil {
		t.Fatal("stale route digest")
	}
	in.Snapshot.Entries[0] = e
	in.Snapshot.Entries[0].Sync = "pending"
	if ValidateGenerationBinding(in) == nil {
		t.Fatal("pending route")
	}
}
