// Package routeconfig compiles the private V2 routing graph to the locked kernel.
package routeconfig

import (
	"context"
	"strconv"

	"github.com/nicccce/meshstrata/internal/control"
	core "github.com/nicccce/meshstrata/internal/mihomo"
)

func componentName(kind string, p control.Pin) string {
	return kind + "_" + string(p.ID) + "_" + strconv.FormatInt(int64(p.Revision), 10) + "_" + p.Digest
}
func nextProxy(name string, h control.HopSpec, c control.PrivateCredential, udp bool) core.OutboundV2 {
	p := h.Endpoint
	out := core.OutboundV2{Name: name, Server: h.DialIP, Port: p.Port, Password: c.Password, UUID: c.UUID, UDP: udp, Fingerprint: string(p.TLSLeafSHA256)}
	switch core.Protocol(p.Protocol) {
	case core.SS2022:
		out.Type = "ss"
		out.Cipher = p.Cipher
	case core.Trojan:
		out.Type = "trojan"
		out.SNI = p.ServerName
	case core.Hysteria2:
		out.Type = "hysteria2"
		out.SNI = p.ServerName
	case core.VLESS:
		out.Type = "vless"
		out.TLS = true
		out.Network = "tcp"
		out.Flow = "xtls-rprx-vision"
		out.ServerName = p.ServerName
		out.ClientFingerprint = "chrome"
		out.Reality = &core.OutboundRealityV2{PublicKey: p.RealityPublicKey, ShortID: p.ShortID}
	}
	return out
}

// GenerateV2 compiles an already authorized, filtered, full-node bundle. Callers
// must persist restrictions and check actual LoopInventory before activation.
func Generate(ctx context.Context, b control.BundleV2, materials control.MaterialResolver, socket string) (core.Config, []core.Expected, error) {
	if b.Validate() != nil || materials == nil {
		return core.Config{}, nil, core.ErrInvalid
	}
	entries := make([]core.Entry, 0, len(b.Listeners))
	for _, l := range b.Listeners {
		s := l.Socket
		h := l.Tail.Hops[0]
		identity := string(s.ListenerID)
		if l.Entry != nil {
			identity = string(l.Entry.IdentityID)
		}
		e := core.Entry{NodeID: string(b.NodeID), ListenerID: string(s.ListenerID), IdentityID: identity, Protocol: core.Protocol(h.Endpoint.Protocol), Address: s.Address, Port: s.Port, BusinessUDP: l.Tail.BusinessUDP, Cipher: h.Endpoint.Cipher, Password: s.Credential.Password, UUID: s.Credential.UUID, RealityDestination: s.RealityDestination, ServerNames: s.ServerNames, ShortIDs: s.ShortIDs}
		for _, m := range []struct {
			ref  *control.LocalMaterial
			dest *string
		}{{s.Certificate, &e.Certificate}, {s.PrivateKey, &e.PrivateKey}, {s.RealityKey, &e.RealityPrivateKey}} {
			if m.ref != nil {
				v, err := materials.Resolve(ctx, b.NodeID, *m.ref)
				if err != nil {
					return core.Config{}, nil, core.ErrInvalid
				}
				*m.dest = v
			}
		}
		entries = append(entries, e)
	}
	cfg, expected, err := core.Generate(entries, core.DNS{Enable: true, IPv6: b.DNS.IPv6, EnhancedMode: "normal", DefaultNameserver: b.DNS.Bootstrap, Nameserver: b.DNS.Resolvers}, socket, []string{"MATCH,REJECT"})
	if err != nil {
		return core.Config{}, nil, err
	}
	cfg.SubRules = map[string][]string{}
	policies := map[control.Pin][]string{}
	outbounds := map[string]core.OutboundV2{}
	selectedPolicies := map[control.Pin]bool{}
	for _, l := range b.Listeners {
		if l.Forward.Terminal != nil {
			selectedPolicies[*l.Forward.Terminal] = true
		}
	}
	for _, p := range b.Terminal {
		if !selectedPolicies[p.Pin] {
			continue
		}
		for _, t := range p.Spec.Targets {
			name := componentName("egress", t.Pin)
			o := core.OutboundV2{Name: name, Type: t.Spec.Type, Server: t.Spec.DialIP, Port: t.Spec.Port, Interface: t.Spec.Interface, UDP: t.Spec.Type != "http"}
			if t.Spec.Type == "node_default" || t.Spec.Type == "interface" {
				o.Type = "direct"
				o.Server = ""
				o.Port = 0
			}
			for _, a := range p.Auth {
				if a.Target == t.Pin {
					o.Username = a.Username
					o.Password = a.Password
				}
			}
			if previous, exists := outbounds[name]; exists {
				if previous != o {
					return core.Config{}, nil, core.ErrInvalid
				}
			} else {
				outbounds[name] = o
				cfg.Proxies = append(cfg.Proxies, o)
			}
		}
		rules := []string{}
		for _, r := range p.Spec.Rules {
			target := "REJECT"
			if r.Target != nil {
				target = componentName("egress", *r.Target)
			}
			rule := "MATCH," + target
			if r.Destination != "MATCH" {
				kind := "IP-CIDR"
				for _, c := range r.Destination {
					if c == ':' {
						kind = "IP-CIDR6"
						break
					}
				}
				rule = kind + "," + r.Destination + "," + target
				if r.NoResolve {
					rule += ",no-resolve"
				}
			}
			rules = append(rules, rule)
		}
		policies[p.Pin] = rules
	}
	for i, l := range b.Listeners {
		if reality := cfg.Listeners[i].Reality; reality != nil {
			// The kernel routes REALITY's fixed handshake destination separately
			// from listener traffic. Global MATCH remains REJECT.
			reality.Proxy = "reality_" + string(l.Socket.ListenerID)
			cfg.Proxies = append(cfg.Proxies, core.OutboundV2{Name: reality.Proxy, Type: "direct", UDP: true})
		}
		name := "listener_" + string(l.Socket.ListenerID)
		rules := []string{}
		if !l.Tail.BusinessUDP {
			rules = append(rules, "NETWORK,udp,REJECT")
		}
		if n := l.Forward.Next; n != nil {
			proxy := "hop_" + string(l.Socket.ListenerID)
			cfg.Proxies = append(cfg.Proxies, nextProxy(proxy, l.Tail.Hops[1], n.Credential, l.Tail.BusinessUDP))
			rules = append(rules, "MATCH,"+proxy)
		} else if l.Forward.Terminal != nil {
			// Select the terminal policy by listener, preserving first-match
			// ordering. DNS remains the single real resolver for this instance.
			rules = append(rules, policies[*l.Forward.Terminal]...)
		} else {
			return core.Config{}, nil, core.ErrInvalid
		}
		cfg.SubRules[name] = rules
		cfg.Listeners[i].Rule = name
	}
	return cfg, expected, nil
}
