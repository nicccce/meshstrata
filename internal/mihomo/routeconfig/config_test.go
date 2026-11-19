package routeconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
)

type noMaterial struct{}

func (noMaterial) Resolve(context.Context, domain.ID, control.LocalMaterial) (string, error) {
	return "", core.ErrInvalid
}

func TestV2RoutesAndLockedKernelParser(t *testing.T) {
	chain := controltest.Chain()
	binary := os.Getenv("I03_MIHOMO_BINARY")
	if binary != "" {
		data, err := os.ReadFile(binary)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != core.LinuxAMD64SHA256 {
			t.Fatal("wrong locked kernel")
		}
	}
	for i, b := range chain {
		cfg, expected, err := Generate(context.Background(), b, noMaterial{}, filepath.Join(t.TempDir(), "api.sock"))
		if err != nil || len(expected) != 1 {
			t.Fatal(i, err)
		}
		rules := cfg.SubRules[cfg.Listeners[0].Rule]
		if len(rules) < 2 || rules[0] != "NETWORK,udp,REJECT" {
			t.Fatal("listener rule lost", rules)
		}
		if i < 2 {
			if len(cfg.Proxies) != 1 || cfg.Proxies[0].Server != b.Listeners[0].Tail.Hops[1].DialIP || cfg.Proxies[0].Password != b.Listeners[0].Forward.Next.Credential.Password {
				t.Fatal("next-hop binding lost")
			}
		} else {
			if len(rules) != 3 || rules[2] != "MATCH,REJECT" {
				t.Fatal("ordered terminal policy lost", rules)
			}
		}
		data, err := json.Marshal(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if binary != "" {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			out, err := exec.Command(binary, "-t", "-d", dir, "-f", path).CombinedOutput()
			if err != nil {
				t.Fatalf("node %d locked kernel rejected: %v\n%s", i, err, out)
			}
		}
	}
}
