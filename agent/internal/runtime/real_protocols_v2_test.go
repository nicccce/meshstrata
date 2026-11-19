package runtime

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/controltest"
	"github.com/nicccce/meshstrata/internal/domain"
)

// Actual independent kernels exercise each typed next-hop outbound. These local
// TCP canaries do not attest multi-host DNS, client imports or business UDP.
func TestRealV2NextHopProtocols(t *testing.T) {
	binary := os.Getenv("I03_MIHOMO_BINARY")
	if binary == "" {
		t.Skip("explicit locked-kernel verification required")
	}
	for _, protocol := range []domain.Protocol{domain.SS2022, domain.Trojan, domain.VLESS, domain.Hysteria2} {
		t.Run(string(protocol), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			chain := localChain(t)
			root := privateTestDir(t)
			cert := makeCertificate(t, root)
			leaf := sha256.Sum256(cert.Certificate[0])
			key, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			certificate := control.LocalMaterial{ID: controltest.ID("certificate"), Purpose: "tls_certificate"}
			privateKey := control.LocalMaterial{ID: controltest.ID("privatekey"), Purpose: "tls_private_key"}
			reality := control.LocalMaterial{ID: controltest.ID("reality"), Purpose: "reality_private_key"}
			socket := &chain[1].Listeners[0].Socket
			spec := control.SocketSpec{Address: "127.0.0.1", Transport: "tcp", ServerNames: []string{}, ShortIDs: []string{}}
			endpoint := control.ProxyEndpoint{Host: "127.0.0.1", Port: freeEndpoint(t, protocol), Protocol: protocol}
			socket.Port = endpoint.Port
			switch protocol {
			case domain.SS2022:
				endpoint.Cipher = "2022-blake3-aes-128-gcm"
			case domain.Trojan, domain.Hysteria2:
				endpoint.ServerName, endpoint.TLSLeafSHA256 = "test.local", control.TLSLeafSHA256(hex.EncodeToString(leaf[:]))
				spec.Certificate, spec.PrivateKey, spec.TLSLeafSHA256 = &certificate, &privateKey, endpoint.TLSLeafSHA256
				socket.Credential.Password = randomPassword(t)
				if protocol == domain.Hysteria2 {
					spec.Transport = "udp"
				}
			case domain.VLESS:
				endpoint.ServerName, endpoint.RealityPublicKey, endpoint.ShortID = "test.local", base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "0123456789abcdef"
				spec.RealityKey, spec.RealityDestination, spec.ServerNames, spec.ShortIDs = &reality, makeRealityTarget(t, cert), []string{"test.local"}, []string{endpoint.ShortID}
				socket.Credential.UUID, socket.Credential.Password = randomUUID(t), ""
			}
			socket.Transport, socket.Certificate, socket.PrivateKey, socket.TLSLeafSHA256, socket.RealityKey, socket.RealityDestination, socket.ServerNames, socket.ShortIDs = spec.Transport, spec.Certificate, spec.PrivateKey, spec.TLSLeafSHA256, spec.RealityKey, spec.RealityDestination, spec.ServerNames, spec.ShortIDs
			hop := chain[0].Listeners[0].Tail.Hops[1]
			hop.NodeConfig, hop.Endpoint = controltest.Pin("socketrelay", spec), endpoint
			for i := 0; i < 2; i++ {
				chain[i].Listeners[0].Tail.Hops[1-i] = hop
			}
			chain[1].Listeners[0].Internal.Digest, _ = chain[1].Listeners[0].Tail.Key()
			chain[0].Listeners[0].Forward.Next.Link = *chain[1].Listeners[0].Internal
			credential := socket.Credential
			credential.Audience = chain[0].NodeID
			chain[0].Listeners[0].Forward.Next.Credential = credential
			for _, b := range chain {
				if err := b.Validate(); err != nil {
					t.Fatal("invalid protocol fixture", err)
				}
			}
			configure := func(f *fixture) {
				if err := os.Mkdir(filepath.Join(f.opts.ConfigDir, "materials"), 0700); err != nil {
					t.Fatal(err)
				}
				for _, name := range []string{"cert.pem", "key.pem"} {
					data, err := os.ReadFile(filepath.Join(root, "materials", name))
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(f.opts.ConfigDir, "materials", name), data, 0600); err != nil {
						t.Fatal(err)
					}
				}
				materials, err := authorization.NewResolver(f.opts.NodeID, f.opts.ConfigDir, []authorization.Material{{Ref: certificate, Path: "materials/cert.pem"}, {Ref: privateKey, Path: "materials/key.pem"}, {Ref: reality, Value: base64.RawURLEncoding.EncodeToString(key.Bytes())}})
				if err != nil {
					t.Fatal(err)
				}
				f.opts.Materials = materials
			}
			for i := 2; i >= 0; i-- {
				var prepare func(*fixture)
				if i == 1 {
					prepare = configure
				}
				fixture, runtime, _, _, _ := realV2NodeConfigured(t, binary, chain[i], prepare)
				result, err := runtime.Execute(ctx, fixture.v2Command(chain[i]))
				if err != nil || result.Verification != domain.Verified {
					t.Fatal("next-hop TCP did not verify", i, protocol, result, err)
				}
			}
			if protocol == domain.Trojan || protocol == domain.Hysteria2 {
				// A separately admitted ingress expects the wrong remote leaf. The
				// real receiver stays unchanged; syntax/API/socket success cannot
				// promote this route without the authenticated end-to-end echo.
				data, err := json.Marshal(chain[0])
				if err != nil {
					t.Fatal(err)
				}
				var bad control.BundleV2
				if err := json.Unmarshal(data, &bad); err != nil {
					t.Fatal(err)
				}
				bad.NodeID, bad.TaskID = controltest.ID("badnode"), controltest.ID("badtask")
				l := &bad.Listeners[0]
				l.Socket.Port = freeEndpoint(t, domain.SS2022)
				l.Socket.Credential.Audience, l.Forward.Next.Credential.Audience = bad.NodeID, bad.NodeID
				l.Tail.Hops[0].NodeID, l.Tail.Hops[0].Endpoint.Port = bad.NodeID, l.Socket.Port
				wrongSpec := spec
				wrongSpec.TLSLeafSHA256 = control.TLSLeafSHA256(strings.Repeat("0", 64))
				l.Tail.Hops[1].NodeConfig = controltest.Pin("wrongleaf", wrongSpec)
				l.Tail.Hops[1].Endpoint.TLSLeafSHA256 = wrongSpec.TLSLeafSHA256
				suffix := l.Tail
				suffix.Hops = suffix.Hops[1:]
				l.Forward.Next.Link.Digest, _ = suffix.Key()
				if err := bad.Validate(); err != nil {
					t.Fatal("bad leaf fixture binding", err)
				}
				fixture, runtime, _, process, _ := realV2Node(t, binary, bad)
				result, err := runtime.Execute(ctx, fixture.v2Command(bad))
				if err != nil || result.Verification == domain.Verified || result.EffectiveDigest != "" {
					t.Fatal("wrong remote leaf accepted", protocol, result, err)
				}
				if status, err := process.Status(ctx); err != nil || status.Running {
					t.Fatal("unverified first publish left a managed kernel running", err)
				}
			}
		})
	}
}
