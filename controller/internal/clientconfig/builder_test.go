package clientconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/controller/internal/rules/rulespec"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/mihomo"
)

func id(kind string, i int) domain.ID { return domain.ID(fmt.Sprintf("%s_%032x", kind, i)) }

func pin(t *testing.T, kind string, i int, data any) control.Pin {
	t.Helper()
	p, err := stage3.NewPin(id(kind, i), 1, data)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func profile(t *testing.T, in *stage3.GenerationInput, source string, sets ...stage3.DomainSetSnapshot) {
	t.Helper()
	parsed, issues := rulespec.ParseSource(source)
	if len(issues) > 0 {
		t.Fatalf("bad test source: %+v", issues)
	}
	for i := range parsed {
		for _, set := range sets {
			if parsed[i].Kind == "RULE-SET" && parsed[i].Value == string(set.Pin.ID) {
				p := set.Pin
				parsed[i].DomainSet = &p
			}
		}
	}
	spec := stage3.ProfileSpec{Rules: parsed, Sets: sets}
	in.Snapshot.Profile = stage3.ProfileSnapshot{Pin: pin(t, "profile", 10, spec), Spec: spec}
}

func repinRoute(t *testing.T, e *stage3.EntryView) {
	t.Helper()
	h, err := stage3.RouteDigest(e.Route)
	if err != nil {
		t.Fatal(err)
	}
	e.Route.Pin.Digest = h
}

func input(t *testing.T) (stage3.GenerationInput, stage3.DomainSetSnapshot, string) {
	t.Helper()
	in := stage3.GenerationInput{Snapshot: stage3.ClientSnapshot{SubscriptionID: id("subscription", 7), Revision: 6}}
	in.Snapshot.Preferences = stage3.ClientPreferences{Mode: "tun", AllRoutesGroup: true, GroupByExit: true,
		DNS: stage3.ClientDNS{Bootstrap: []string{"1.1.1.1"}, ProxyServers: []string{"9.9.9.9", "https://1.1.1.1/dns-query"},
			Business: []string{"https://1.0.0.1/dns-query"}}}
	egress := pin(t, "policy", 12, "frozen terminal policy")
	for i, protocol := range []domain.Protocol{domain.SS2022, domain.Hysteria2, domain.Trojan, domain.VLESS} {
		endpoint := control.ProxyEndpoint{Host: fmt.Sprintf("entry%d.example", i), Port: uint16(23000 + i), Protocol: protocol}
		secret := control.PrivateCredential{Audience: in.Snapshot.SubscriptionID, Ref: id("credential", i)}
		switch protocol {
		case domain.SS2022:
			endpoint.Cipher = "2022-blake3-aes-128-gcm"
			secret.Password = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 16))
		case domain.Hysteria2, domain.Trojan:
			endpoint.ServerName = fmt.Sprintf("cert%d.example", i)
			secret.Password = fmt.Sprintf("quoted-fixture-password-%d\": [x]", i)
		case domain.VLESS:
			endpoint.ServerName, endpoint.ShortID = "reality.example", "0102030405060708"
			endpoint.RealityPublicKey = base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
			secret.UUID = "4f5e7a02-9e5c-491a-b723-a67f0f8019de"
		}
		hop := control.HopSpec{NodeID: id("node", 100+i), NodeConfig: pin(t, "nodecfg", i, endpoint), DNS: pin(t, "dns", i, "node DNS"),
			Endpoint: endpoint, DialIP: fmt.Sprintf("192.0.2.%d", i+1)}
		view := stage3.EntryView{
			ID: id("entry", i), IdentityID: id("identity", i), Generation: 1, Endpoint: endpoint,
			Name: fmt.Sprintf("Friendly-%d", i), ExpiresAt: time.Unix(2_000_000_000, 0),
			Sync: "confirmed", Cleanup: "none",
			Route: stage3.PublishedRoute{Name: "Stored route", Verification: domain.NotChecked,
				Pin: control.Pin{ID: id("route", i), Revision: 1}, EgressIsDefault: true,
				Tail: control.RouteTail{Hops: []control.HopSpec{hop}, Egress: egress}},
		}
		// This subscription has a three-hop Hy2 entry ending at the same
		// exit as the one-hop SS entry. Downstream ports never leave B07.
		if i == 1 {
			for j, s := range []string{"middle.private.example", "terminal.private.example"} {
				downstream := hop
				downstream.NodeID = id("node", 800)
				if j == 1 {
					downstream.NodeID = id("node", 100)
				}
				downstream.Endpoint.Host, downstream.Endpoint.Port = s, uint16(32001+j)
				downstream.DialIP = fmt.Sprintf("192.0.2.%d", 200+j)
				view.Route.Tail.Hops = append(view.Route.Tail.Hops, downstream)
			}
		}
		view.Endpoint.Port += 1000
		repinRoute(t, &view)
		in.Snapshot.Entries = append(in.Snapshot.Entries, view)
		in.Secrets = append(in.Secrets, stage3.EntrySecret{IdentityID: view.IdentityID, Credential: secret})
	}
	s := stage3.DomainSetSpec{Domains: []string{"rule-set.example", "other.test"}}
	set := stage3.DomainSetSnapshot{Pin: pin(t, "set", 50, s), Spec: s}
	exit, err := stage3.ExitGroupName(id("node", 100), "")
	if err != nil {
		t.Fatal(err)
	}
	profile(t, &in, "DOMAIN-SUFFIX,example.net,"+exit+"\nRULE-SET,"+string(set.Pin.ID)+","+exit+"\nDOMAIN-KEYWORD,examplekeyword,"+exit+"\nIP-CIDR,203.0.113.0/24,REJECT,no-resolve\nMATCH,All-Proxies", set)
	return in, set, exit
}

func decoded(t *testing.T, b []byte) fields {
	t.Helper()
	var f fields
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFullAndProviderProjectOnlyTheirEntries(t *testing.T) {
	in, set, exit := input(t)
	in.Snapshot.Entries[0].Name = "Label \": [bracket] &yaml *anchor"
	in.Snapshot.Preferences.Selections = []stage3.GroupChoice{{Name: exit, SelectedEntryID: in.Snapshot.Entries[1].ID}}
	b, _ := NewBuilder(stage3.ClientOptions{})
	preview, err := b.Preview(in.Snapshot)
	if err != nil || len(preview.Groups) != 4 || preview.Groups[1].Name != exit ||
		preview.Groups[1].SelectedEntryID != in.Snapshot.Entries[1].ID {
		t.Fatalf("preview group choice: %+v, %v", preview, err)
	}
	full, err := b.BuildFull(in)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := b.BuildProvider(in)
	if err != nil {
		t.Fatal(err)
	}
	f, prov := decoded(t, full), decoded(t, provider)
	if len(prov) != 1 || !reflect.DeepEqual(f["proxies"], prov["proxies"]) {
		t.Fatal("provider contains fields other than projected proxy entries")
	}
	for _, s := range []string{"middle.private.example", "terminal.private.example", "32001", "32002", "192.0.2.200", "external-controller",
		string(in.Secrets[0].Credential.Ref), string(in.Snapshot.SubscriptionID), string(in.Secrets[0].IdentityID), "dialer-proxy", "skip-cert-verify\":true"} {
		if bytes.Contains(full, []byte(s)) || bytes.Contains(provider, []byte(s)) {
			t.Fatal("private or unbound transport data in artifact")
		}
	}
	proxies := f["proxies"].([]any)
	for i, typ := range []string{"ss", "hysteria2", "trojan", "vless"} {
		p := proxies[i].(map[string]any)
		e, s := in.Snapshot.Entries[i], in.Secrets[i].Credential
		if p["type"] != typ || p["server"] != e.Endpoint.Host || p["port"] != float64(e.Endpoint.Port) || p["name"] != proxyName(e) ||
			s.Password != "" && p["password"] != s.Password || s.UUID != "" && p["uuid"] != s.UUID {
			t.Fatalf("incorrect public projection for %s", typ)
		}
	}
	ss, hy, tj, vl := proxies[0].(fields), proxies[1].(fields), proxies[2].(fields), proxies[3].(fields)
	if ss["cipher"] != "2022-blake3-aes-128-gcm" || ss["udp"] != false || hy["udp"] != nil || hy["sni"] != "cert1.example" ||
		tj["sni"] != "cert2.example" || tj["skip-cert-verify"] != false || vl["flow"] != "xtls-rprx-vision" ||
		vl["client-fingerprint"] != "chrome" || vl["tls"] != true || vl["reality-opts"].(fields)["short-id"] != "0102030405060708" {
		t.Fatal("locked protocol mapping or transport semantics lost")
	}
	g := f["proxy-groups"].([]any)[1].(fields)
	want := []any{proxyName(in.Snapshot.Entries[0]), proxyName(in.Snapshot.Entries[1])}
	if g["type"] != "select" || !reflect.DeepEqual(g["proxies"], want) || g["default-selected"] != want[1] || g["empty-fallback"] != "REJECT" {
		t.Fatal("incorrect ordered manual exit group")
	}
	d := f["dns"].(fields)
	policy := d["nameserver-policy"].(fields)
	wantExitDNS := []any{"https://1.0.0.1/dns-query#" + exit}
	if f["ipv6"] != false || d["ipv6"] != false || d["enhanced-mode"] != "fake-ip" || f["tun"].(fields)["enable"] != true ||
		!reflect.DeepEqual(d["proxy-server-nameserver"], []any{"udp://9.9.9.9#DIRECT", "https://1.1.1.1/dns-query#DIRECT"}) ||
		!reflect.DeepEqual(d["default-nameserver"], []any{"1.1.1.1"}) ||
		!reflect.DeepEqual(d["nameserver"], []any{"https://1.0.0.1/dns-query#All-Proxies"}) ||
		!reflect.DeepEqual(policy["+.example.net"], wantExitDNS) || !reflect.DeepEqual(policy["rule-set:b07_set_"+string(set.Pin.ID)], wantExitDNS) ||
		len(d["fallback"].([]any)) != 0 {
		t.Fatal("incorrect DNS separation or policy projection")
	}
	actual := f["rules"].([]any)
	wantRules := []any{"NETWORK,udp,REJECT", "DOMAIN-SUFFIX,example.net," + exit, "RULE-SET,b07_set_" + string(set.Pin.ID) + "," + exit,
		"DOMAIN-KEYWORD,examplekeyword," + exit, "IP-CIDR,203.0.113.0/24,REJECT,no-resolve", "MATCH,All-Proxies"}
	if !reflect.DeepEqual(actual, wantRules) {
		t.Fatal("incorrect rule order or implicit fallback")
	}
	rp := f["rule-providers"].(fields)["b07_set_"+string(set.Pin.ID)].(fields)
	if rp["type"] != "inline" || rp["behavior"] != "domain" || !reflect.DeepEqual(rp["payload"], []any{"rule-set.example", "other.test"}) {
		t.Fatal("pinned DomainSet not represented as pure inline domains")
	}
	again, err := b.BuildFull(in)
	if err != nil || !bytes.Equal(again, full) {
		t.Fatal("builder is not deterministic")
	}
	encodedPreview, _ := json.Marshal(preview)
	for _, s := range in.Secrets {
		for _, secret := range []string{s.Credential.Password, s.Credential.UUID} {
			if secret != "" && bytes.Contains(encodedPreview, []byte(secret)) {
				t.Fatal("preview leaked a secret")
			}
		}
	}
}

func TestGenerationRejectsInvalidOrForeignBinding(t *testing.T) {
	b, _ := NewBuilder(stage3.ClientOptions{})
	for label, change := range map[string]func(*stage3.GenerationInput){
		"empty":                func(i *stage3.GenerationInput) { i.Snapshot.Entries, i.Secrets = nil, nil },
		"wrong audience":       func(i *stage3.GenerationInput) { i.Secrets[0].Credential.Audience = id("subscription", 13) },
		"wrong identity":       func(i *stage3.GenerationInput) { i.Secrets[0].IdentityID = id("identity", 10) },
		"wrong position":       func(i *stage3.GenerationInput) { i.Secrets[0], i.Secrets[1] = i.Secrets[1], i.Secrets[0] },
		"reused credential":    func(i *stage3.GenerationInput) { i.Secrets[1].Credential.Ref = i.Secrets[0].Credential.Ref },
		"reused password":      func(i *stage3.GenerationInput) { i.Secrets[1].Credential.Password = i.Secrets[2].Credential.Password },
		"pending":              func(i *stage3.GenerationInput) { i.Snapshot.Entries[0].Sync = "pending" },
		"revoked":              func(i *stage3.GenerationInput) { now := time.Unix(40, 0); i.Snapshot.Entries[0].RevokedAt = &now },
		"stale first endpoint": func(i *stage3.GenerationInput) { i.Snapshot.Entries[0].Endpoint.Host = "unbound.example" },
		"stale downstream":     func(i *stage3.GenerationInput) { i.Snapshot.Entries[1].Route.Tail.Hops[1].Endpoint.Port++ },
		"invalid profile pin":  func(i *stage3.GenerationInput) { i.Snapshot.Profile.Pin.Digest = strings.Repeat("e", 64) },
		"unmatched set pin": func(i *stage3.GenerationInput) {
			i.Snapshot.Profile.Spec.Rules[1].DomainSet.Revision++
			i.Snapshot.Profile.Pin = pin(t, "profile", 10, i.Snapshot.Profile.Spec)
		},
		"set payload tampered": func(i *stage3.GenerationInput) {
			i.Snapshot.Profile.Spec.Sets[0].Spec.Domains[0] = "tampered.example"
			i.Snapshot.Profile.Pin = pin(t, "profile", 10, i.Snapshot.Profile.Spec)
		},
		"same group new policy revision": func(i *stage3.GenerationInput) {
			i.Snapshot.Entries[1].Route.Tail.Egress.Revision++
			repinRoute(t, &i.Snapshot.Entries[1])
		},
		"missing group": func(i *stage3.GenerationInput) { i.Snapshot.Preferences.GroupByExit = false },
		"foreign selection": func(i *stage3.GenerationInput) {
			i.Snapshot.Preferences.Selections = []stage3.GroupChoice{{Name: stage3.AllProxies, SelectedEntryID: id("entry", 500)}}
		},
		"control entry name":     func(i *stage3.GenerationInput) { i.Snapshot.Entries[0].Name = "bad\x00name" },
		"malicious dns fragment": func(i *stage3.GenerationInput) { i.Snapshot.Preferences.DNS.Business[0] += "#DIRECT" },
		"nonliteral bootstrap": func(i *stage3.GenerationInput) {
			i.Snapshot.Preferences.DNS.ProxyServers[0] = "https://resolver.example/dns-query"
		},
	} {
		t.Run(label, func(t *testing.T) {
			in, _, _ := input(t)
			change(&in)
			if out, err := b.BuildFull(in); out != nil || err == nil {
				t.Fatal("bad snapshot produced a full config")
			}
			if out, err := b.BuildProvider(in); out != nil || err == nil {
				t.Fatal("bad snapshot produced a provider")
			}
		})
	}
}

func TestDNSLimitationsAndOutputBound(t *testing.T) {
	b, _ := NewBuilder(stage3.ClientOptions{})
	in, _, exit := input(t)
	in.Snapshot.Preferences.Mode = "system_proxy"
	in.Snapshot.Entries[0].Sync = "pending"
	p, err := b.Preview(in.Snapshot)
	if err != nil || len(p.Issues) != 3 || p.Issues[0].Code != limitedCoverage || p.Issues[1].Code != entryPending || p.Issues[2].Code != dnsNotInferred {
		t.Fatalf("coverage or pre-approval issues missing: %+v %v", p, err)
	}
	in.Snapshot.Entries[0].Sync = "confirmed"
	got, err := b.BuildFull(in)
	if err != nil || decoded(t, got)["mixed-port"] != float64(7890) || decoded(t, got)["tun"].(fields)["enable"] != false {
		t.Fatal("system proxy did not use restricted local ingress")
	}
	for _, source := range []string{
		"DOMAIN-SUFFIX,example.net," + exit + "\nDOMAIN,www.example.net,All-Proxies\nMATCH,All-Proxies",
		"DOMAIN,www.example.net," + exit + "\nDOMAIN,www.example.net,All-Proxies\nMATCH,All-Proxies",
		"DOMAIN-KEYWORD,net," + exit + "\nDOMAIN-SUFFIX,example.net,All-Proxies\nMATCH,All-Proxies",
		"IP-CIDR,203.0.113.0/24,REJECT\nMATCH,All-Proxies",
		"MATCH,DIRECT",
		"MATCH," + string(id("route", 42)),
	} {
		profile(t, &in, source)
		preview, err := b.Preview(in.Snapshot)
		if err == nil || len(preview.Issues) < 2 || preview.Issues[len(preview.Issues)-1].Line == 0 {
			t.Fatal("unrepresentable policy did not return a located issue")
		}
	}
	var src strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&src, "DOMAIN,n%d.example,All-Proxies\n", i)
	}
	fmt.Fprint(&src, "MATCH,All-Proxies")
	profile(t, &in, src.String())
	in.Snapshot.Preferences.DNS.Business = nil
	for i := range 8 {
		in.Snapshot.Preferences.DNS.Business = append(in.Snapshot.Preferences.DNS.Business, fmt.Sprintf("https://1.1.1.%d/%s", i+1, strings.Repeat("x", 120)))
	}
	if out, err := b.BuildFull(in); out != nil || err == nil {
		t.Fatal("DNS policy expansion escaped output limit")
	}
	if out, err := b.BuildProvider(in); err != nil || len(out) == 0 || len(out) > stage3.MaxConfigBytes {
		t.Fatal("provider should be bounded independently")
	}
}

func TestPinnedMihomoOfflineSyntax(t *testing.T) {
	binary := os.Getenv("B07_MIHOMO_BINARY")
	if binary == "" {
		t.Skip("requires the cached B03-locked binary")
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		t.Fatal("cannot read pinned kernel")
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != mihomo.LinuxAMD64SHA256 {
		t.Fatal("cached kernel hash does not match B03 lock")
	}
	in, _, _ := input(t)
	for i := range in.Snapshot.Entries {
		e := &in.Snapshot.Entries[i]
		e.Name = "香港 节点 / Name, [显示]"
		if e.Endpoint.Protocol == domain.Trojan || e.Endpoint.Protocol == domain.Hysteria2 {
			e.Endpoint.TLSLeafSHA256 = control.TLSLeafSHA256(strings.Repeat("a", 64))
			e.Route.Tail.Hops[0].Endpoint.TLSLeafSHA256 = e.Endpoint.TLSLeafSHA256
			repinRoute(t, e)
		}
	}
	b, _ := NewBuilder(stage3.ClientOptions{})
	preview, err := b.Preview(in.Snapshot)
	if err != nil || len(preview.ProxyLabels) != len(in.Snapshot.Entries) {
		t.Fatal("display mapping missing", err)
	}
	out, err := b.BuildFull(in)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err = os.WriteFile(path, out, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// -t parses, without bringing up listeners, TUN or outbound connections.
	if err = exec.CommandContext(ctx, binary, "-t", "-d", dir, "-f", path).Run(); err != nil {
		t.Fatalf("locked kernel did not accept generated config: %v", err)
	}
}

func TestExplicitBusinessUDPRequiresEveryRouteEvidence(t *testing.T) {
	b, _ := NewBuilder(stage3.ClientOptions{})
	in, _, _ := input(t)
	in.Snapshot.Entries = in.Snapshot.Entries[:2]
	in.Secrets = in.Secrets[:2]
	profile(t, &in, "DOMAIN,site.example,All-Proxies\nMATCH,All-Proxies")
	in.Snapshot.Preferences.BusinessUDP = true
	for i := range in.Snapshot.Entries {
		e := &in.Snapshot.Entries[i]
		e.Route.Tail.BusinessUDP = true
		repinRoute(t, e)
	}
	if out, err := b.BuildFull(in); out != nil || err == nil {
		t.Fatal("UDP enabled without route verification")
	}
	for i := range in.Snapshot.Entries {
		in.Snapshot.Entries[i].Route.Verification = domain.Verified
	}
	out, err := b.BuildFull(in)
	if err != nil {
		t.Fatal(err)
	}
	f := decoded(t, out)
	if f["rules"].([]any)[0] != "DOMAIN,site.example,All-Proxies" ||
		f["proxies"].([]any)[0].(fields)["udp"] != true || f["proxy-groups"].([]any)[0].(fields)["disable-udp"] != false {
		t.Fatal("verified UDP option was not carried into the config")
	}
}

func TestTwoPoliciesAtOneTerminalUseSeparateExitGroups(t *testing.T) {
	b, _ := NewBuilder(stage3.ClientOptions{})
	in, _, _ := input(t)
	in.Snapshot.Entries = in.Snapshot.Entries[:2]
	in.Secrets = in.Secrets[:2]
	custom := &in.Snapshot.Entries[1]
	custom.Route.EgressIsDefault = false
	custom.Route.Tail.Egress = pin(t, "policy", 51, "custom egress")
	repinRoute(t, custom)
	exit, err := stage3.ExitGroupName(id("node", 100), custom.Route.Tail.Egress.ID)
	if err != nil {
		t.Fatal(err)
	}
	profile(t, &in, "DOMAIN,custom.example,"+exit+"\nMATCH,REJECT")
	out, err := b.BuildFull(in)
	if err != nil {
		t.Fatal(err)
	}
	f := decoded(t, out)
	g := f["proxy-groups"].([]any)
	if len(g) != 3 || g[2].(fields)["name"] != exit || !reflect.DeepEqual(g[2].(fields)["proxies"], []any{proxyName(*custom)}) ||
		!reflect.DeepEqual(f["dns"].(fields)["nameserver"], []any{"https://1.0.0.1/dns-query#REJECT"}) {
		t.Fatal("custom and default exits mixed, or explicit reject fallback lost")
	}
}
