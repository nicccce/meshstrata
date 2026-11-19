package mihomo

import (
	"net/netip"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
)

func routeName(s string) bool {
	if len(s) == 0 || len(s) > 192 {
		return false
	}
	for _, c := range s {
		if c != '_' && c != '-' && !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func proxyText(s string) bool {
	return len(s) <= 512 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// Validate a finite typed subset independently of the source compiler. This is
// also applied on reload and inspection; enabling V2 cannot admit arbitrary YAML.
func validV2Outbound(p core.OutboundV2) bool {
	if !routeName(p.Name) || p.Name == "REJECT" || p.SkipCertVerify {
		return false
	}
	expected := core.OutboundV2{Name: p.Name, Type: p.Type, UDP: p.UDP}
	switch p.Type {
	case "direct":
		if p.Interface != "" {
			if len(p.Interface) > 15 {
				return false
			}
			for _, c := range p.Interface {
				if c != '_' && c != '-' && c != '.' && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') && !(c >= '0' && c <= '9') {
					return false
				}
			}
		}
		expected.Interface = p.Interface
		if !p.UDP {
			return false
		}
	case "http", "socks5":
		ip, err := netip.ParseAddr(p.Server)
		if err != nil || ip.Zone() != "" || ip.Is4In6() || ip.IsUnspecified() || ip.IsMulticast() || p.Port == 0 || !proxyText(p.Username) || !proxyText(p.Password) || (p.Username == "" && p.Password != "") || (p.Type == "http" && p.UDP) {
			return false
		}
		expected.Server, expected.Port, expected.Username, expected.Password = p.Server, p.Port, p.Username, p.Password
	default:
		proto := domain.Protocol(p.Type)
		if p.Type == "ss" {
			proto = domain.SS2022
		}
		endpoint := control.ProxyEndpoint{Protocol: proto, Host: p.Server, Port: p.Port, Cipher: p.Cipher, ServerName: p.SNI, TLSLeafSHA256: control.TLSLeafSHA256(p.Fingerprint)}
		expected.Server, expected.Port, expected.Password, expected.UUID = p.Server, p.Port, p.Password, p.UUID
		switch proto {
		case domain.SS2022:
			expected.Cipher = p.Cipher
		case domain.Trojan, domain.Hysteria2:
			expected.SNI, expected.Fingerprint = p.SNI, p.Fingerprint
		case domain.VLESS:
			if p.Reality == nil {
				return false
			}
			endpoint.ServerName, endpoint.RealityPublicKey, endpoint.ShortID = p.ServerName, p.Reality.PublicKey, p.Reality.ShortID
			if !p.TLS || p.Network != "tcp" || p.Flow != "xtls-rprx-vision" || p.ClientFingerprint != "chrome" {
				return false
			}
			expected.TLS, expected.Network, expected.Flow, expected.ClientFingerprint, expected.ServerName, expected.Reality = p.TLS, p.Network, p.Flow, p.ClientFingerprint, p.ServerName, p.Reality
		default:
			return false
		}
		ip, err := netip.ParseAddr(p.Server)
		if err != nil || ip.IsUnspecified() || ip.IsMulticast() || ip.Zone() != "" || ip.Is4In6() {
			return false
		}
		audience := domain.ID("node_00000000000000000000000000000000")
		if !control.ValidProxyCredential(endpoint, control.PrivateCredential{Ref: audience, Audience: audience, Password: p.Password, UUID: p.UUID}, audience) {
			return false
		}
	}
	return reflect.DeepEqual(p, expected)
}

func policyV2Routes(c core.Config) error {
	if len(c.Rules) != 1 || c.Rules[0] != "MATCH,REJECT" || len(c.SubRules) != len(c.Listeners) || len(c.Proxies) > control.MaxListeners+control.MaxEgressTargets*control.MaxEgressTargets {
		return core.ErrInvalid
	}
	targets := map[string]bool{"REJECT": true}
	outbounds := map[string]core.OutboundV2{}
	used := map[string]bool{}
	for _, p := range c.Proxies {
		if targets[p.Name] || !validV2Outbound(p) {
			return core.ErrInvalid
		}
		targets[p.Name] = true
		outbounds[p.Name] = p
	}
	for _, l := range c.Listeners {
		if reality := l.Reality; reality != nil {
			name := "reality_" + l.Name
			p, ok := outbounds[name]
			dest, err := netip.ParseAddrPort(reality.Destination)
			if reality.Proxy != name || !ok || p.Type != "direct" || p.Interface != "" || err != nil || dest.Port() == 0 || dest.Addr().IsUnspecified() || dest.Addr().IsMulticast() || dest.Addr().Is4In6() || dest.Addr().Zone() != "" {
				return core.ErrInvalid
			}
			used[name] = true
		}
		if l.Rule != "listener_"+l.Name {
			return core.ErrInvalid
		}
		rules, ok := c.SubRules[l.Rule]
		if !ok || len(rules) == 0 || len(rules) > control.MaxEgressRules+1 {
			return core.ErrInvalid
		}
		for i, rule := range rules {
			parts := strings.Split(rule, ",")
			if len(parts) < 2 {
				return core.ErrInvalid
			}
			var target string
			switch parts[0] {
			case "NETWORK":
				if i != 0 || rule != "NETWORK,udp,REJECT" {
					return core.ErrInvalid
				}
				target = "REJECT"
			case "MATCH":
				if len(parts) != 2 || i != len(rules)-1 {
					return core.ErrInvalid
				}
				target = parts[1]
			case "IP-CIDR", "IP-CIDR6":
				if len(parts) != 3 && (len(parts) != 4 || parts[3] != "no-resolve") {
					return core.ErrInvalid
				}
				prefix, err := netip.ParsePrefix(parts[1])
				if err != nil || prefix.Masked().String() != parts[1] || prefix.Addr().Is4In6() || prefix.Addr().Is4() != (parts[0] == "IP-CIDR") {
					return core.ErrInvalid
				}
				target = parts[2]
			default:
				return core.ErrInvalid
			}
			if !targets[target] || strings.HasPrefix(target, "reality_") {
				return core.ErrInvalid
			}
			used[target] = true
		}
		if !strings.HasPrefix(rules[len(rules)-1], "MATCH,") {
			return core.ErrInvalid
		}
	}
	for _, p := range c.Proxies {
		if !used[p.Name] {
			return core.ErrInvalid
		}
	}
	return nil
}
