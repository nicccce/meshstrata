package runtime

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
)

type rendered struct {
	mihomo.Candidate
	bundle control.Bundle
}

func entry(l control.BundleListener) core.Entry {
	return core.Entry{NodeID: string(l.NodeID), ListenerID: string(l.ListenerID), IdentityID: string(l.Authorization.IdentityID),
		Protocol: core.Protocol(l.Protocol), Address: l.Address, Port: l.Port, BusinessUDP: l.BusinessUDP,
		Cipher: l.Cipher, Password: l.Credential.Password, UUID: l.Credential.UUID, RealityDestination: l.RealityDestination,
		ServerNames: l.ServerNames, ShortIDs: l.ShortIDs}
}

func denyBundle(node domain.ID) control.Bundle {
	return control.Bundle{NodeID: node, Rules: control.RuleInput{Default: "REJECT"},
		DNS: control.DNSInput{Bootstrap: []string{"127.0.0.1"}, Resolvers: []string{"127.0.0.1"}}}
}

func configName(digest string) string { return "effective-" + digest + ".json" }

func validConfigName(name string) bool {
	if !strings.HasPrefix(name, "effective-") || !strings.HasSuffix(name, ".json") {
		return false
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, "effective-"), ".json")
	b, err := hex.DecodeString(digest)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == digest
}

func (r *Runtime) render(ctx context.Context, b control.Bundle, listeners []control.BundleListener) (rendered, error) {
	entries := make([]core.Entry, 0, len(listeners))
	for _, l := range listeners {
		e := entry(l)
		for _, role := range []struct {
			ref   *control.LocalMaterial
			value *string
		}{
			{l.Certificate, &e.Certificate}, {l.PrivateKey, &e.PrivateKey}, {l.RealityKey, &e.RealityPrivateKey},
		} {
			if role.ref == nil {
				continue
			}
			v, err := r.o.Materials.Resolve(ctx, r.o.NodeID, *role.ref)
			if err != nil {
				return rendered{}, authorization.ErrMaterial
			}
			*role.value = v
		}
		if e.Certificate != "" && authorization.CheckTLS(e.Certificate, e.PrivateKey) != nil {
			return rendered{}, authorization.ErrMaterial
		}
		entries = append(entries, e)
	}
	if err := ctx.Err(); err != nil {
		return rendered{}, err
	}
	config, expected, err := core.Generate(entries, core.DNS{Enable: true, IPv6: b.DNS.IPv6, EnhancedMode: "normal",
		DefaultNameserver: b.DNS.Bootstrap, Nameserver: b.DNS.Resolvers}, r.o.SocketPath, []string{"MATCH," + b.Rules.Default})
	if err != nil {
		return rendered{}, err
	}
	config.Secret = r.o.APISecret
	data, err := json.Marshal(config)
	if err != nil || len(data) > control.MaxBundleBytes {
		return rendered{}, control.ErrBundle
	}
	digest := core.Digest(data)
	name := configName(digest)
	dir, err := checkpoint.OpenPrivateDir(r.o.ConfigDir)
	if err != nil {
		return rendered{}, err
	}
	defer dir.Close()
	old, err := dir.Read(name, control.MaxBundleBytes)
	if err == nil && core.Digest(old) != digest {
		return rendered{}, checkpoint.ErrCorrupt
	}
	if errors.Is(err, os.ErrNotExist) {
		_, err = dir.Replace(name, data)
	}
	if err != nil {
		return rendered{}, err
	}
	b.Listeners = listeners
	return rendered{Candidate: mihomo.Candidate{Path: filepath.Join(r.o.ConfigDir, name), Digest: digest, Expected: expected}, bundle: b}, nil
}

func (r *Runtime) artifact(ctx context.Context, s control.Snapshot) (control.Bundle, error) {
	if err := s.Validate(); err != nil {
		return control.Bundle{}, err
	}
	key := control.ArtifactKey{TaskID: s.TaskID, NodeID: r.o.NodeID, TargetRevision: s.Revision, TargetDigest: s.ArtifactDigest}
	data, err := r.o.State.ReadArtifact(ctx, key)
	if err != nil {
		return control.Bundle{}, err
	}
	return control.DecodeBundle(data, key, r.o.NodeID)
}

// checkedRender reconstructs the exact prior listener view from the pinned
// effective file, even when the current ledger already tombstones a listener.
func (r *Runtime) checkedRender(ctx context.Context, s *control.Snapshot) (rendered, error) {
	if s == nil {
		return rendered{}, nil
	}
	b, err := r.artifact(ctx, *s)
	if err != nil {
		return rendered{}, err
	}
	dir, err := checkpoint.OpenPrivateDir(r.o.ConfigDir)
	if err != nil {
		return rendered{}, err
	}
	defer dir.Close()
	name := configName(s.EffectiveDigest)
	data, err := dir.Read(name, control.MaxBundleBytes)
	if err != nil || core.Digest(data) != s.EffectiveDigest {
		return rendered{}, checkpoint.ErrCorrupt
	}
	var config core.Config
	if json.Unmarshal(data, &config) != nil {
		return rendered{}, checkpoint.ErrCorrupt
	}
	byName := make(map[string]control.BundleListener, len(b.Listeners))
	for _, l := range b.Listeners {
		byName[string(l.ListenerID)] = l
	}
	b.Listeners = make([]control.BundleListener, 0, len(config.Listeners))
	expected := make([]core.Expected, 0, len(config.Listeners))
	for _, l := range config.Listeners {
		src, ok := byName[l.Name]
		if !ok {
			return rendered{}, checkpoint.ErrCorrupt
		}
		e := entry(src)
		expected = append(expected, core.Expected{NodeID: e.NodeID, ListenerID: e.ListenerID, Protocol: e.Protocol,
			Address: e.Address, Port: e.Port, TCP: e.Protocol != core.Hysteria2,
			UDP: e.Protocol == core.Hysteria2 || (e.Protocol == core.SS2022 && e.BusinessUDP), BusinessUDP: e.BusinessUDP})
		b.Listeners = append(b.Listeners, src)
	}
	return rendered{Candidate: mihomo.Candidate{Path: filepath.Join(r.o.ConfigDir, name), Digest: s.EffectiveDigest, Expected: expected}, bundle: b}, nil
}
