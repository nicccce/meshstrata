package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/mihomo/routeconfig"
)

// transportV2 is only an adapter for authenticated network probes and exact
// connection cleanup. It has no entry authorization and is never a Grant.
func transportV2(node domain.ID, l control.ListenerV2) control.BundleListener {
	s := l.Socket
	return control.BundleListener{NodeID: node, ListenerID: s.ListenerID, Protocol: l.Tail.Hops[0].Endpoint.Protocol,
		Address: s.Address, Port: s.Port, Transport: s.Transport, BusinessUDP: l.Tail.BusinessUDP,
		Cipher: l.Tail.Hops[0].Endpoint.Cipher, Credential: s.Credential, Certificate: s.Certificate,
		PrivateKey: s.PrivateKey, RealityKey: s.RealityKey, RealityDestination: s.RealityDestination,
		ServerNames: s.ServerNames, ShortIDs: s.ShortIDs}
}

func (v rendered) listeners() []control.BundleListener {
	if v.v2 == nil {
		return v.bundle.Listeners
	}
	out := make([]control.BundleListener, 0, len(v.v2.Listeners))
	for _, l := range v.v2.Listeners {
		out = append(out, transportV2(v.v2.NodeID, l))
	}
	return out
}
func physicalID(l control.BundleListener) domain.ID {
	if l.Authorization.IdentityID != "" {
		return l.Authorization.IdentityID
	}
	return l.Credential.Ref
}

// Policy pins include credential refs but exclude plaintext auth. Reusing a Pin
// must still preserve its first private authentication binding. The original
// source bytes are retained permanently, including for retired listeners.
func (r *Runtime) checkV2PolicyOrigins(ctx context.Context, b control.BundleV2, state control.DurableState) error {
	wanted := map[control.Pin]control.TerminalV2{}
	for _, p := range b.Terminal {
		wanted[p.Pin] = p
	}
	seen := map[control.ArtifactKey]bool{}
	for _, g := range state.BindingsV2 {
		if seen[g.Origin] {
			continue
		}
		seen[g.Origin] = true
		data, err := r.o.State.ReadArtifact(ctx, g.Origin)
		if err != nil {
			return err
		}
		old, err := control.DecodeBundleV2(data, g.Origin, r.o.NodeID)
		clear(data)
		if err != nil {
			return err
		}
		for _, p := range old.Terminal {
			if next, ok := wanted[p.Pin]; ok && !reflect.DeepEqual(next, p) {
				return control.ErrAuthorization
			}
		}
	}
	return nil
}

func (r *Runtime) artifactV2(ctx context.Context, s control.Snapshot) (control.BundleV2, error) {
	if s.Format != 2 || s.Validate() != nil || !r.o.EnableV2 {
		return control.BundleV2{}, control.ErrBinding
	}
	key := control.ArtifactKey{TaskID: s.TaskID, NodeID: r.o.NodeID, TargetRevision: s.Revision, TargetDigest: s.ArtifactDigest}
	data, err := r.o.State.ReadArtifact(ctx, key)
	if err != nil {
		return control.BundleV2{}, err
	}
	defer clear(data)
	return control.DecodeBundleV2(data, key, r.o.NodeID)
}

func (r *Runtime) renderV2(ctx context.Context, b control.BundleV2, listeners []control.ListenerV2) (rendered, error) {
	if !r.o.EnableV2 || r.o.Inventory == nil {
		return rendered{}, ErrUnavailable
	}
	// Validate the original full graph before filtering. Filtering only removes
	// authorization; it cannot alter a Pin or manufacture a new effective route.
	if b.Validate() != nil {
		return rendered{}, control.ErrBundle
	}
	inventory, err := r.loopInventory(ctx)
	if err != nil || b.CheckEgressLoops(inventory) != nil {
		return rendered{}, control.ErrBundle
	}
	b.Listeners = listeners
	for _, l := range listeners {
		if l.Socket.Certificate == nil {
			continue
		}
		cert, err := r.o.Materials.Resolve(ctx, r.o.NodeID, *l.Socket.Certificate)
		if err != nil || l.Socket.PrivateKey == nil {
			return rendered{}, authorization.ErrMaterial
		}
		key, err := r.o.Materials.Resolve(ctx, r.o.NodeID, *l.Socket.PrivateKey)
		if err != nil || authorization.CheckTLSLeaf(cert, key, l.Socket.TLSLeafSHA256) != nil {
			return rendered{}, authorization.ErrMaterial
		}
	}
	cfg, expected, err := routeconfig.Generate(ctx, b, r.o.Materials, r.o.SocketPath)
	if err != nil {
		return rendered{}, err
	}
	cfg.Secret = r.o.APISecret
	data, err := json.Marshal(cfg)
	if err != nil || len(data) > control.MaxBundleBytes {
		return rendered{}, control.ErrBundle
	}
	digest := core.Digest(data)
	dir, err := checkpoint.OpenPrivateDir(r.o.ConfigDir)
	if err != nil {
		return rendered{}, err
	}
	defer dir.Close()
	name := configName(digest)
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
	return rendered{Candidate: mihomo.Candidate{Format: 2, Path: filepath.Join(r.o.ConfigDir, name), Digest: digest, Expected: expected}, v2: &b}, nil
}

func (r *Runtime) filteredSnapshot(ctx context.Context, snap control.Snapshot, s control.DurableState) (rendered, error) {
	now := authorization.Clock(s, r.o.Now())
	if snap.Format == 2 {
		b, err := r.artifactV2(ctx, snap)
		if err != nil {
			return rendered{}, err
		}
		return r.renderV2(ctx, b, authorization.ActiveV2(s, b, now))
	}
	b, err := r.artifact(ctx, snap)
	if err != nil {
		return rendered{}, err
	}
	return r.render(ctx, b, authorization.Active(s, b, now))
}

func (r *Runtime) checkedRenderV2(ctx context.Context, snap *control.Snapshot) (rendered, error) {
	b, err := r.artifactV2(ctx, *snap)
	if err != nil {
		return rendered{}, err
	}
	dir, err := checkpoint.OpenPrivateDir(r.o.ConfigDir)
	if err != nil {
		return rendered{}, err
	}
	data, err := dir.Read(configName(snap.EffectiveDigest), control.MaxBundleBytes)
	dir.Close()
	if err != nil || core.Digest(data) != snap.EffectiveDigest {
		return rendered{}, checkpoint.ErrCorrupt
	}
	var cfg core.Config
	if json.Unmarshal(data, &cfg) != nil {
		return rendered{}, checkpoint.ErrCorrupt
	}
	byName := map[string]control.ListenerV2{}
	for _, l := range b.Listeners {
		byName[string(l.Socket.ListenerID)] = l
	}
	selected := make([]control.ListenerV2, 0, len(cfg.Listeners))
	for _, l := range cfg.Listeners {
		source, ok := byName[l.Name]
		if !ok {
			return rendered{}, checkpoint.ErrCorrupt
		}
		selected = append(selected, source)
		delete(byName, l.Name)
	}
	rebuilt, err := r.renderV2(ctx, b, selected)
	if err != nil || rebuilt.Digest != snap.EffectiveDigest {
		return rendered{}, checkpoint.ErrCorrupt
	}
	return rebuilt, nil
}
