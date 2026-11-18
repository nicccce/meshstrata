package clientconfig

import (
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/nicccce/meshstrata/controller/internal/stage3"
	"github.com/nicccce/meshstrata/internal/domain"
)

const dnsPath = "/preferences/dns"

type dnsConfig struct {
	Enable         bool                `json:"enable"`
	IPv6           bool                `json:"ipv6"`
	EnhancedMode   string              `json:"enhanced-mode"`
	RespectRules   bool                `json:"respect-rules"`
	UseSystemHosts bool                `json:"use-system-hosts"`
	UseHosts       bool                `json:"use-hosts"`
	PreferH3       bool                `json:"prefer-h3"`
	FakeIPFilter   []string            `json:"fake-ip-filter"`
	Bootstrap      []string            `json:"default-nameserver"`
	ProxyServers   []string            `json:"proxy-server-nameserver"`
	Business       []string            `json:"nameserver"`
	Policy         map[string][]string `json:"nameserver-policy,omitempty"`
	Fallback       []string            `json:"fallback"`
}

type inlineProvider struct {
	Type     string   `json:"type"`
	Behavior string   `json:"behavior"`
	Payload  []string `json:"payload"`
}

var fakeIP = netip.MustParsePrefix("198.18.0.0/15")

func dnsIP(ip netip.Addr) bool {
	return ip.Zone() == "" && !ip.Is4In6() && ip.IsGlobalUnicast() && !fakeIP.Contains(ip)
}

func numeric(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4() && a.String() == s && dnsIP(a)
}

func safeToken(s string) bool {
	return s != "" && !strings.ContainsFunc(s, func(c rune) bool {
		return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_' || c == '.')
	})
}

func doh(s string) bool {
	if len(s) > 192 || !strings.HasPrefix(s, "https://") || strings.ContainsAny(s, "?#%\\ \t\r\n") {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || !numeric(u.Hostname()) || u.Path == "" || u.Path[0] != '/' {
		return false
	}
	for _, component := range strings.Split(u.Path[1:], "/") {
		if !safeToken(component) || component == "." || component == ".." {
			return false
		}
	}
	if u.Port() == "" {
		return u.Host == u.Hostname()
	}
	n, err := strconv.ParseUint(u.Port(), 10, 16)
	return err == nil && n > 0 && u.Host == u.Hostname()+":"+strconv.FormatUint(n, 10)
}

func validateServers(servers []string, valid func(string) bool) bool {
	if len(servers) == 0 || len(servers) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, s := range servers {
		if seen[s] || !valid(s) {
			return false
		}
		seen[s] = true
	}
	return true
}

type selector struct {
	target string
	rule   stage3.RuleLine
}

func (p *plan) compileRules(s stage3.ClientSnapshot) error {
	noRule := stage3.RuleLine{}
	prefs := s.Preferences
	in := prefs.DNS
	if !validateServers(in.Bootstrap, numeric) {
		return p.block(dnsPath+"/bootstrap", noRule, domain.InvalidArgument)
	}
	if !validateServers(in.ProxyServers, func(s string) bool { return numeric(s) || doh(s) }) {
		return p.block(dnsPath+"/proxy_servers", noRule, domain.InvalidArgument)
	}
	if !validateServers(in.Business, doh) {
		return p.block(dnsPath+"/business", noRule, domain.InvalidArgument)
	}
	p.dns = dnsConfig{Enable: true, EnhancedMode: "fake-ip",
		Bootstrap: append([]string(nil), in.Bootstrap...), FakeIPFilter: []string{}, Fallback: []string{},
		Policy: map[string][]string{}}
	for _, server := range in.ProxyServers {
		if numeric(server) {
			server = "udp://" + server
		}
		p.dns.ProxyServers = append(p.dns.ProxyServers, server+"#DIRECT")
	}
	p.extras = map[string]inlineProvider{}
	setDomains := map[domain.ID][]string{}
	for _, set := range s.Profile.Spec.Sets {
		setDomains[set.Pin.ID] = set.Spec.Domains
	}
	if !prefs.BusinessUDP {
		p.rules = append(p.rules, "NETWORK,udp,REJECT")
	}
	var exact, suffix, keyword, setTargets = map[string]selector{}, map[string]selector{}, map[string]selector{}, map[string]selector{}
	mapped := map[string][]string{}
	domainTargets := map[string]bool{}
	var keywordTarget string
	upstreams := func(target string) []string {
		if a, ok := mapped[target]; ok {
			return a
		}
		a := make([]string, len(in.Business))
		for i, server := range in.Business {
			a[i] = server + "#" + target
		}
		mapped[target] = a
		return a
	}
	add := func(m map[string]selector, key string, sel selector) error {
		if old, ok := m[key]; ok && old.target != sel.target {
			return p.block("/profile/rules", sel.rule, dnsConflict)
		}
		m[key] = sel
		return nil
	}
	for _, r := range s.Profile.Spec.Rules {
		t, ok := p.target(r.Target)
		if !ok {
			return p.block("/profile/rules", r, domain.NotFound)
		}
		if r.Kind == "IP-CIDR" || r.Kind == "IP-CIDR6" {
			// An IP-only predicate cannot pick a DNS egress. Eager resolution
			// here would additionally change the point of resolution for TCP.
			p.issue("/profile/rules", r, dnsNotInferred)
			if !r.NoResolve || prefs.BusinessUDP {
				return p.block("/profile/rules", r, domain.NotImplemented)
			}
			p.rules = append(p.rules, r.Kind+","+r.Value+","+t+",no-resolve")
			continue
		}
		// Domain/MATCH business DNS never gets a direct resolver. REJECT
		// can deliberately block a DoH dial. An explicit route must be a
		// representable DNS dialer name, never an interface/options string.
		if t == "DIRECT" || !safeToken(t) {
			return p.block("/profile/rules", r, domain.NotImplemented)
		}
		if r.Kind == "MATCH" {
			p.dns.Business = upstreams(t)
			p.rules = append(p.rules, "MATCH,"+t)
			continue
		}
		domainTargets[t] = true
		sel := selector{t, r}
		switch r.Kind {
		case "DOMAIN":
			if err := add(exact, r.Value, sel); err != nil {
				return err
			}
			p.dns.Policy[r.Value] = upstreams(t)
		case "DOMAIN-SUFFIX":
			if err := add(suffix, r.Value, sel); err != nil {
				return err
			}
			p.dns.Policy["+."+r.Value] = upstreams(t)
		case "DOMAIN-KEYWORD":
			if keywordTarget != "" && keywordTarget != t {
				return p.block("/profile/rules", r, dnsConflict)
			}
			keywordTarget = t
			if _, seen := keyword[r.Value]; !seen {
				name := "b07_keyword_" + strconv.Itoa(r.Line)
				keyword[r.Value] = sel
				p.extras[name] = inlineProvider{"inline", "classical", []string{"DOMAIN-KEYWORD," + r.Value}}
				p.dns.Policy["rule-set:"+name] = upstreams(t)
			}
		case "RULE-SET":
			name := "b07_set_" + r.Value
			if old, seen := setTargets[name]; seen {
				if old.target != t {
					return p.block("/profile/rules", r, dnsConflict)
				}
			} else {
				setTargets[name] = sel
				domains := setDomains[r.DomainSet.ID]
				for _, d := range domains {
					if err := add(exact, d, sel); err != nil {
						return err
					}
				}
				p.extras[name] = inlineProvider{"inline", "domain", append([]string(nil), domains...)}
				p.dns.Policy["rule-set:"+name] = upstreams(t)
			}
			p.rules = append(p.rules, "RULE-SET,"+name+","+t)
			continue
		}
		p.rules = append(p.rules, r.Kind+","+r.Value+","+t)
	}
	// The kernel combines trie entries and matcher providers with different
	// precedence. Only emit overlapping predicates when they agree on target.
	// Keyword overlap across heterogeneous domains is conservatively rejected.
	if keywordTarget != "" && len(domainTargets) > 1 {
		for _, r := range s.Profile.Spec.Rules {
			if r.Kind == "DOMAIN-KEYWORD" {
				return p.block("/profile/rules", r, dnsConflict)
			}
		}
	}
	var conflictAt stage3.RuleLine
	for _, selectors := range []map[string]selector{exact, suffix} {
		for name, selected := range selectors {
			for d := name; d != ""; {
				if other, ok := suffix[d]; ok && other.target != selected.target {
					r := selected.rule
					if other.rule.Line > r.Line {
						r = other.rule
					}
					if conflictAt.Line == 0 || r.Line < conflictAt.Line {
						conflictAt = r
					}
				}
				_, d, _ = strings.Cut(d, ".")
			}
		}
	}
	if conflictAt.Line > 0 {
		return p.block("/profile/rules", conflictAt, dnsConflict)
	}
	return nil
}
