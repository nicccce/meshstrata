package mihomo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/mihomo/routeconfig"
)

type noV2Material struct{}

func (noV2Material) Resolve(context.Context, domain.ID, control.LocalMaterial) (string, error) {
	return "", core.ErrInvalid
}

func TestV2PolicyBoundaries(t *testing.T) {
	a, _ := fixture(t)
	b := controltest.Chain()[0]
	cfg, expected, err := routeconfig.Generate(context.Background(), b, noV2Material{}, a.o.SocketPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Secret = a.o.Secret
	data, _ := json.Marshal(cfg)
	if err := a.policy(data, expected, 2); err == nil {
		t.Fatal("V2 enabled by config fields alone")
	}
	a.o.EnableV2 = true
	if err := a.policy(data, expected, 0); err == nil {
		t.Fatal("V2 fields accepted as legacy candidate")
	}
	if err := a.policy(data, expected, 2); err != nil {
		t.Fatal("typed route rejected", err)
	}
	tests := map[string]func(*core.Config){
		"direct fallback":           func(c *core.Config) { c.Rules = []string{"MATCH,DIRECT"} },
		"skip TLS verification":     func(c *core.Config) { c.Proxies[0].SkipCertVerify = true },
		"domain next-hop":           func(c *core.Config) { c.Proxies[0].Server = "unapproved.example" },
		"extra HTTP outbound field": func(c *core.Config) { c.Proxies[0].Interface = "tun0" },
		"unknown outbound":          func(c *core.Config) { c.Proxies[0].Type = "wireguard" },
		"listener policy omitted":   func(c *core.Config) { c.Listeners[0].Rule = "" },
		"listener policy swapped":   func(c *core.Config) { c.Listeners[0].Rule = "other" },
		"rule injection": func(c *core.Config) {
			c.SubRules[c.Listeners[0].Rule] = []string{"SCRIPT,unapproved,DIRECT", "MATCH,REJECT"}
		},
		"unknown proxy target": func(c *core.Config) { c.SubRules[c.Listeners[0].Rule] = []string{"MATCH,unapproved"} },
		"TUN":                  func(c *core.Config) { c.Tun.Enable = true },
		"generic inbound":      func(c *core.Config) { c.SocksPort = 1080 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			var changed core.Config
			json.Unmarshal(data, &changed)
			mutate(&changed)
			bytes, _ := json.Marshal(changed)
			if a.policy(bytes, expected, 2) == nil {
				t.Fatal("escape accepted")
			}
		})
	}
}

func TestV2RealityHandshakeCannotBecomeBusinessFallback(t *testing.T) {
	cfg := core.Config{Rules: []string{"MATCH,REJECT"}, Listeners: []core.Listener{{Name: "relay", Type: "vless", Rule: "listener_relay", Reality: &core.Reality{Destination: "127.0.0.1:443", Proxy: "reality_relay"}}}, SubRules: map[string][]string{"listener_relay": {"MATCH,REJECT"}}, Proxies: []core.OutboundV2{{Name: "reality_relay", Type: "direct", UDP: true}}}
	if err := policyV2Routes(cfg); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(cfg)
	for name, mutate := range map[string]func(*core.Config){
		"business bypass":  func(c *core.Config) { c.SubRules["listener_relay"] = []string{"MATCH,reality_relay"} },
		"builtin bypass":   func(c *core.Config) { c.Listeners[0].Reality.Proxy = "DIRECT" },
		"domain handshake": func(c *core.Config) { c.Listeners[0].Reality.Destination = "unresolved.example:443" },
		"interface escape": func(c *core.Config) { c.Proxies[0].Interface = "eth0" },
	} {
		t.Run(name, func(t *testing.T) {
			var candidate core.Config
			if err := json.Unmarshal(data, &candidate); err != nil {
				t.Fatal(err)
			}
			mutate(&candidate)
			if policyV2Routes(candidate) == nil {
				t.Fatal("handshake routing escape accepted")
			}
		})
	}
}
