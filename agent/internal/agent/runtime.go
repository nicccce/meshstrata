package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/authorization"
	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	channel "github.com/nicccce/meshstrata/agent/internal/control"
	"github.com/nicccce/meshstrata/agent/internal/mihomo"
	execution "github.com/nicccce/meshstrata/agent/internal/runtime"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/localstate"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"github.com/nicccce/meshstrata/internal/privatefile"
)

const runtimeModeFile = "runtime-mode"

func runtimeProcess(cfg config.Agent) (*execution.Supervisor, error) {
	if cfg.Runtime == nil || runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, errors.New("runtime requires the pinned Linux amd64 Mihomo")
	}
	dir := filepath.Join(cfg.StateDir, "runtime")
	// Mihomo permits API reload only from its own private home.
	return execution.NewProcess(execution.ProcessOptions{
		Binary: cfg.Runtime.MihomoBinary, WorkDir: filepath.Join(dir, "config"),
		StateDir: filepath.Join(dir, "process"), ConfigDir: filepath.Join(dir, "config"),
	})
}

// PrepareRuntime creates the keys and initial ledger once, after enrollment.
func PrepareRuntime(ctx context.Context, cfg config.Agent, _ io.Reader) error {
	if cfg.Runtime == nil {
		return errors.New("runtime configuration required")
	}
	unlock, err := localstate.Lock(cfg.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	identity, _, err := openEnrollment(cfg)
	if err != nil {
		return err
	}
	node := domain.ID(cfg.Control.NodeID)
	if identity.NodeID() != node {
		return errors.New("enrolled node identity mismatch")
	}
	if _, err := identity.Certificate(time.Now()); err != nil {
		return err
	}
	// Preserve a valid stage-one state until the irreversible mode transition.
	if _, err := channel.OpenControlOnly(cfg.StateDir, node); err != nil {
		return err
	}
	dir := filepath.Join(cfg.StateDir, "runtime")
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(dir) // This invocation created the unused directory under the component lock.
		}
	}()
	for _, sub := range []string{"checkpoint", "process", "api", "config", "config/materials", "probes"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0700); err != nil {
			return err
		}
	}
	if _, err := runtimeProcess(cfg); err != nil {
		return err
	}
	root, err := checkpoint.OpenPrivateDir(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	key, secret := make([]byte, 32), make([]byte, 32)
	defer clear(key)
	defer clear(secret)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	if _, err := rand.Read(secret); err != nil {
		return err
	}
	if _, err := root.Replace("checkpoint.key", key); err != nil {
		return err
	}
	if _, err := root.Replace("api.key", secret); err != nil {
		return err
	}
	store, err := checkpoint.Open(checkpoint.Options{Dir: filepath.Join(dir, "checkpoint"), NodeID: node, KeyID: "runtime-v1", Key: key})
	if err != nil {
		return err
	}
	if err := store.Close(); err != nil {
		return err
	}
	if _, err := root.Replace("prepared", []byte(node)); err != nil {
		return err
	}
	state, err := checkpoint.OpenPrivateDir(cfg.StateDir)
	if err != nil {
		return err
	}
	defer state.Close()
	// Once the marker rename has occurred, preserve all runtime files even
	// if its final sync returned an indeterminate error.
	committed, err = state.Replace(runtimeModeFile, []byte(node))
	return err
}

func stopOnStartupFailure(cfg config.Agent, cause error) error {
	if cfg.Runtime == nil {
		return cause
	}
	process, err := runtimeProcess(cfg)
	if err != nil {
		return errors.Join(cause, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return errors.Join(cause, process.Stop(ctx))
}

func StopRuntime(ctx context.Context, cfg config.Agent, _ io.Reader) error {
	unlock, err := localstate.Lock(cfg.StateDir)
	if err != nil {
		return err
	}
	defer unlock()
	process, err := runtimeProcess(cfg)
	if err != nil {
		return err
	}
	return process.Stop(ctx)
}

func composeRuntime(ctx context.Context, cfg config.Agent, client *atomic.Pointer[channel.Client], logger *slog.Logger) (_ *execution.Runtime, _ func(), resultErr error) {
	process, err := runtimeProcess(cfg)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if resultErr != nil {
			stop, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			resultErr = errors.Join(resultErr, process.Stop(stop))
		}
	}()
	dir := filepath.Join(cfg.StateDir, "runtime")
	root, err := checkpoint.OpenPrivateDir(dir)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	node := domain.ID(cfg.Control.NodeID)
	mode, err := privatefile.Read(filepath.Join(cfg.StateDir, runtimeModeFile), 128)
	if err != nil || string(mode) != string(node) {
		return nil, nil, errors.New("runtime mode marker missing or bound to a different node")
	}
	prepared, err := root.Read("prepared", 128)
	if err != nil || string(prepared) != string(node) {
		return nil, nil, errors.New("runtime preparation missing or bound to a different node")
	}
	key, err := root.Read("checkpoint.key", 32)
	if err != nil || len(key) != 32 {
		return nil, nil, errors.New("runtime checkpoint key unavailable")
	}
	defer clear(key)
	secret, err := root.Read("api.key", 32)
	if err != nil || len(secret) != 32 {
		return nil, nil, errors.New("runtime API key unavailable")
	}
	defer clear(secret)
	apiSecret := base64.RawURLEncoding.EncodeToString(secret)
	store, err := checkpoint.Open(checkpoint.Options{
		Dir: filepath.Join(dir, "checkpoint"), NodeID: node, KeyID: "runtime-v1", Key: key, RequireExisting: true,
	})
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if resultErr != nil {
			store.Close()
		}
	}()
	configDir := filepath.Join(dir, "config")
	files, err := checkpoint.OpenPrivateDir(filepath.Join(configDir, "materials"))
	if err != nil {
		return nil, nil, err
	}
	defer files.Close()
	materials := make([]authorization.Material, 0, len(cfg.Runtime.Materials))
	for _, ref := range cfg.Runtime.Materials {
		if !ref.ID.Valid() {
			return nil, nil, authorization.ErrMaterial
		}
		entry := authorization.Material{Ref: ref}
		name := string(ref.ID) + "." + ref.Purpose
		switch ref.Purpose {
		case "tls_certificate", "tls_private_key":
			entry.Path = filepath.Join("materials", name)
		case "reality_private_key":
			raw, err := files.Read(name, 128)
			if err != nil {
				return nil, nil, authorization.ErrMaterial
			}
			entry.Value = strings.TrimSpace(string(raw))
			clear(raw)
		default:
			return nil, nil, authorization.ErrMaterial
		}
		materials = append(materials, entry)
	}
	resolver, err := authorization.NewResolver(node, configDir, materials)
	if err != nil {
		return nil, nil, err
	}
	for _, material := range materials {
		if _, err := resolver.Resolve(ctx, node, material.Ref); err != nil {
			return nil, nil, err
		}
	}
	binary, socket := cfg.Runtime.MihomoBinary, cfg.RuntimeSocketPath()
	adapter, err := mihomo.New(mihomo.Options{
		Binary: binary, BinarySHA256: core.LinuxAMD64SHA256, WorkDir: filepath.Join(dir, "config"),
		StateDir: filepath.Join(dir, "process"), ConfigDir: configDir, SocketPath: socket, Secret: apiSecret,
	})
	if err != nil {
		return nil, nil, err
	}
	probes, err := execution.NewProbes(execution.ProbeOptions{Binary: binary, Dir: filepath.Join(dir, "probes"), Materials: resolver})
	if err != nil {
		return nil, nil, err
	}
	engine, err := execution.New(execution.Options{
		NodeID: node, State: store, Materials: resolver, Kernel: adapter, Process: process,
		ProbeFactory: probes.ForwardFactory, RevocationProbeFactory: probes.RevocationFactory,
		ConfigDir: configDir, SocketPath: socket, APISecret: apiSecret,
		Fetch: func(ctx context.Context, key control.ArtifactKey, maximum int64) ([]byte, error) {
			if c := client.Load(); c != nil {
				return c.DownloadArtifact(ctx, key, maximum)
			}
			return nil, channel.ErrUnavailable
		},
		OnApplyFailure: func(err error) {
			logger.Warn("Mihomo application failed", "error", err)
		},
		OnPending: func() {
			if c := client.Load(); c != nil {
				c.Reconnect()
			}
		},
	})
	if err != nil {
		return nil, nil, err
	}
	return engine, func() { engine.Close(); store.Close() }, nil
}
