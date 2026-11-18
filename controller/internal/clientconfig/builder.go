// Package clientconfig projects authorized entry snapshots into Mihomo config.
// Generation has no I/O or clock.
package clientconfig

import (
	"encoding/json"
	"net/netip"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/controller/internal/rules/rulespec"
	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
)

type Builder struct{}

var _ stage3.ClientBuilder = (*Builder)(nil)

func NewBuilder(stage3.ClientOptions) (*Builder, error) { return &Builder{}, nil }

const (
	limitedCoverage = domain.ErrorCode("LIMITED_COVERAGE")
	dnsNotInferred  = domain.ErrorCode("DNS_EGRESS_NOT_INFERRED")
	dnsConflict     = domain.ErrorCode("DNS_POLICY_CONFLICT")
	egressNotProven = domain.ErrorCode("DNS_TERMINAL_EGRESS_NOT_CHECKED")
	entryPending    = domain.ErrorCode("ENTRY_NOT_CONFIRMED")
)

type fields = map[string]any

type group struct {
	Name            string   `json:"name"`
	Type            string   `json:"type"`
	Proxies         []string `json:"proxies"`
	DefaultSelected string   `json:"default-selected"`
	EmptyFallback   string   `json:"empty-fallback"`
	DisableUDP      bool     `json:"disable-udp"`
	ids             []domain.ID
}

type plan struct {
	preview stage3.Preview
	groups  []group
	routes  map[domain.ID]string
	names   map[string]int
	rules   []string
	extras  map[string]inlineProvider
	dns     dnsConfig
}

func failure(path string, code domain.ErrorCode) *domain.Problem {
	return &domain.Problem{Code: code, Fields: []domain.FieldError{{Path: path, Code: code}}}
}

func (p *plan) issue(path string, r stage3.RuleLine, code domain.ErrorCode) {
	p.preview.Issues = append(p.preview.Issues, stage3.Issue{Path: path, Line: r.Line, Column: r.Column, Code: code})
}

func (p *plan) block(path string, r stage3.RuleLine, code domain.ErrorCode) error {
	p.issue(path, r, code)
	return failure(path, code)
}

func (b *Builder) Preview(s stage3.ClientSnapshot) (stage3.Preview, error) {
	p, err := compile(s, true)
	return p.preview, err
}

func validateEntry(e stage3.EntryView, draft bool) bool {
	digest, err := stage3.RouteDigest(e.Route)
	if !e.ID.Valid() || !e.Route.Pin.Valid() || err != nil || digest != e.Route.Pin.Digest ||
		e.RevokedAt != nil || e.Cleanup != "none" && !(draft && e.Cleanup == "") {
		return false
	}
	if e.Sync != "confirmed" {
		if !draft || e.Sync != "pending" {
			return false
		}
		if e.Endpoint == (control.ProxyEndpoint{}) {
			return true
		}
	} else if !e.IdentityID.Valid() || !e.Generation.Valid() || e.ExpiresAt.IsZero() {
		return false
	}
	pinned := e.Route.Tail.Hops[0].Endpoint
	pinned.Port = e.Endpoint.Port
	return pinned == e.Endpoint && e.Endpoint.Valid()
}

func proxyName(e stage3.EntryView) string { return "Entry-" + string(e.ID) }

func validName(n string) bool {
	if n == "" || len(n) > 128 || strings.TrimSpace(n) != n || !utf8.ValidString(n) ||
		strings.ContainsFunc(n, func(r rune) bool {
			return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
		}) {
		return false
	}

	return true
}

func compile(s stage3.ClientSnapshot, draft bool) (*plan, error) {
	p := &plan{groups: []group{}, routes: map[domain.ID]string{}, names: map[string]int{}}
	noRule := stage3.RuleLine{}
	block := func(path string, code domain.ErrorCode) (*plan, error) {
		return p, p.block(path, noRule, code)
	}
	if !s.SubscriptionID.Valid() || !s.Revision.Valid() {
		return block("/subscription", domain.InvalidArgument)
	}
	if !rulespec.ValidateProfile(s.Profile) {
		return block("/profile", domain.InvalidArgument)
	}
	if len(s.Entries) == 0 || len(s.Entries) > stage3.MaxSubscriptionEntries {
		return block("/entries", domain.InvalidArgument)
	}
	opts := s.Preferences
	if opts.Mode != "" && opts.Mode != "tun" && opts.Mode != "system_proxy" {
		return block("/preferences/mode", domain.InvalidArgument)
	}
	if opts.Mode == "system_proxy" {
		p.issue("/preferences/mode", noRule, limitedCoverage)
		if opts.BusinessUDP {
			return block("/preferences/business_udp", domain.NotImplemented)
		}
	}
	if opts.IPv6 { // ClientSnapshot lacks IPv6 target and client capability evidence.
		return block("/preferences/ipv6", domain.NotImplemented)
	}
	if opts.AllRoutesGroup {
		p.names[stage3.AllProxies] = 0
		p.groups = append(p.groups, newGroup(stage3.AllProxies, opts.BusinessUDP))
	}
	seenID, identities, labels, endpoints := map[domain.ID]bool{}, map[domain.ID]bool{}, map[string]bool{}, map[string]bool{}
	type exit struct {
		policy    control.Pin
		node      domain.ID
		isDefault bool
	}
	exits := map[string]exit{}
	customEgress := false
	for i, e := range s.Entries {
		path := "/entries/" + strconv.Itoa(i)
		if !validateEntry(e, draft) || seenID[e.ID] || e.IdentityID != "" && identities[e.IdentityID] ||
			p.routes[e.Route.Pin.ID] != "" || !validName(e.Name) || labels[proxyName(e)] {
			return block(path, domain.InvalidArgument)
		}
		seenID[e.ID], identities[e.IdentityID], labels[proxyName(e)] = true, true, true
		p.routes[e.Route.Pin.ID] = proxyName(e)
		p.preview.ProxyLabels = append(p.preview.ProxyLabels, stage3.ProxyLabel{Name: proxyName(e), DisplayName: e.Name, EntryID: e.ID})
		endpoint := e.Endpoint
		if e.Sync != "confirmed" {
			p.issue(path, noRule, entryPending)
			if endpoint == (control.ProxyEndpoint{}) {
				endpoint = e.Route.Tail.Hops[0].Endpoint
			}
		} else {
			addr := endpoint.Host + ":" + strconv.Itoa(int(endpoint.Port))
			if endpoints[addr] {
				return block(path+"/endpoint", domain.InvalidArgument)
			}
			endpoints[addr] = true
		}
		if ip, err := netip.ParseAddr(endpoint.Host); err == nil {
			if !ip.Is4() || !dnsIP(ip) {
				return block(path+"/endpoint", domain.InvalidArgument)
			}
		} else if d, ok := rulespec.CanonicalDomain(endpoint.Host); !ok || d != endpoint.Host {
			return block(path+"/endpoint", domain.InvalidArgument)
		}
		tail := e.Route.Tail
		if opts.BusinessUDP && (!tail.BusinessUDP || e.Route.Verification != domain.Verified) {
			return block(path+"/business_udp", domain.InvalidArgument)
		}
		if opts.AllRoutesGroup {
			p.groups[0].add(e)
		}
		terminal := tail.Hops[len(tail.Hops)-1].NodeID
		policy := tail.Egress.ID
		if e.Route.EgressIsDefault {
			policy = ""
		} else {
			customEgress = true
		}
		name, err := stage3.ExitGroupName(terminal, policy)
		if err != nil {
			return block(path, domain.InvalidArgument)
		}
		current := exit{tail.Egress, terminal, e.Route.EgressIsDefault}
		if previous, ok := exits[name]; ok && previous != current {
			return block(path+"/route/egress", domain.RevisionConflict)
		}
		exits[name] = current
		if opts.GroupByExit {
			n, ok := p.names[name]
			if !ok {
				n = len(p.groups)
				p.names[name] = n
				p.groups = append(p.groups, newGroup(name, opts.BusinessUDP))
			}
			p.groups[n].add(e)
		}
	}
	if customEgress {
		p.issue("/entries", noRule, egressNotProven)
	}
	for name := range labels {
		if _, ok := p.names[name]; ok {
			return block("/entries/name", domain.InvalidArgument)
		}
	}
	if len(opts.Selections) > len(p.groups) {
		return block("/preferences/selections", domain.InvalidArgument)
	}
	selected := map[string]domain.ID{}
	for i, c := range opts.Selections {
		if _, ok := p.names[c.Name]; !ok || selected[c.Name] != "" || !c.SelectedEntryID.Valid() {
			return block("/preferences/selections/"+strconv.Itoa(i), domain.InvalidArgument)
		}
		selected[c.Name] = c.SelectedEntryID
	}
	for i := range p.groups {
		g := &p.groups[i]
		chosen := selected[g.Name]
		if len(g.ids) == 0 {
			return block("/groups", domain.InvalidArgument)
		}
		if chosen == "" {
			chosen = g.ids[0]
		}
		for j, id := range g.ids {
			if id == chosen {
				g.DefaultSelected = g.Proxies[j]
			}
		}
		if g.DefaultSelected == "" {
			return block("/preferences/selections", domain.InvalidArgument)
		}
		p.preview.Groups = append(p.preview.Groups, stage3.GroupChoice{Name: g.Name, SelectedEntryID: chosen})
	}
	if err := p.compileRules(s); err != nil {
		return p, err
	}
	return p, nil
}

func newGroup(name string, udp bool) group {
	return group{Name: name, Type: "select", EmptyFallback: "REJECT", DisableUDP: !udp}
}

func (g *group) add(e stage3.EntryView) {
	g.Proxies, g.ids = append(g.Proxies, proxyName(e)), append(g.ids, e.ID)
}

func (p *plan) target(t stage3.RuleTarget) (string, bool) {
	if t.Builtin != "" {
		return t.Builtin, true
	}
	if t.RouteID != "" {
		name, ok := p.routes[t.RouteID]
		return name, ok
	}
	_, ok := p.names[t.Group]
	return t.Group, ok
}

func project(input stage3.GenerationInput) ([]fields, error) {
	if err := stage3.ValidateGenerationBinding(input); err != nil {
		return nil, err
	}
	out := make([]fields, 0, len(input.Secrets))
	seen := map[string]bool{}
	for i, s := range input.Secrets {
		e, c := input.Snapshot.Entries[i], s.Credential
		secret := c.Password
		if c.UUID != "" {
			secret = strings.ToLower(c.UUID)
		}
		if seen[secret] {
			return nil, failure("/entries/"+strconv.Itoa(i)+"/credential", domain.InvalidArgument)
		}
		seen[secret] = true
		q := e.Endpoint
		udp := input.Snapshot.Preferences.BusinessUDP
		proxy := fields{"name": proxyName(e), "server": q.Host, "port": q.Port, "ip-version": "ipv4"}
		switch q.Protocol {
		case domain.SS2022:
			proxy["type"], proxy["cipher"], proxy["password"], proxy["udp"] = "ss", q.Cipher, c.Password, udp
		case domain.Trojan:
			proxy["type"], proxy["sni"], proxy["password"], proxy["udp"] = "trojan", q.ServerName, c.Password, udp
			proxy["skip-cert-verify"] = false
		case domain.VLESS:
			proxy["type"], proxy["servername"], proxy["uuid"], proxy["udp"] = "vless", q.ServerName, c.UUID, udp
			proxy["network"], proxy["tls"], proxy["flow"] = "tcp", true, "xtls-rprx-vision"
			proxy["client-fingerprint"], proxy["skip-cert-verify"] = "chrome", false
			proxy["reality-opts"] = fields{"public-key": q.RealityPublicKey, "short-id": q.ShortID}
		case domain.Hysteria2:
			proxy["type"], proxy["sni"], proxy["password"], proxy["skip-cert-verify"] = "hysteria2", q.ServerName, c.Password, false
			// Hy2 always uses outer QUIC; the full config's rule gates inner UDP.
		}
		if q.TLSLeafSHA256 != "" {
			proxy["fingerprint"] = string(q.TLSLeafSHA256)
		}
		out = append(out, proxy)
	}
	return out, nil
}

func (b *Builder) BuildFull(input stage3.GenerationInput) (stage3.FullConfig, error) {
	proxies, err := project(input)
	if err != nil {
		return nil, err
	}
	p, err := compile(input.Snapshot, false)
	if err != nil {
		return nil, err
	}
	c := fields{"mode": "rule", "ipv6": false, "allow-lan": false, "dns": p.dns, "proxies": proxies,
		"rules": p.rules, "proxy-groups": p.groups, "profile": fields{"store-selected": false, "store-fake-ip": false}}
	if len(p.extras) != 0 {
		c["rule-providers"] = p.extras
	}
	if input.Snapshot.Preferences.Mode == "system_proxy" {
		c["mixed-port"], c["tun"] = 7890, fields{"enable": false}
	} else {
		c["tun"] = fields{"enable": true, "stack": "mixed", "auto-route": true, "auto-detect-interface": true,
			"dns-hijack": []string{"any:53", "tcp://any:53"}}
	}
	buf, err := render(c)
	return stage3.FullConfig(buf), err
}

func (b *Builder) BuildProvider(input stage3.GenerationInput) (stage3.ProxyProvider, error) {
	proxies, err := project(input)
	if err != nil {
		return nil, err
	}
	if _, err = compile(input.Snapshot, false); err != nil {
		return nil, err
	}
	buf, err := render(fields{"proxies": proxies})
	return stage3.ProxyProvider(buf), err
}

// JSON is the restricted YAML representation accepted by B03's pinned parser.
func render(config fields) ([]byte, error) {
	out, err := json.Marshal(config)
	if err != nil {
		return nil, failure("/config", "INTERNAL_ERROR")
	}
	if len(out) > stage3.MaxConfigBytes {
		return nil, failure("/config", domain.InvalidArgument)
	}
	return out, nil
}
