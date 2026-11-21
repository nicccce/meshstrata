package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"github.com/nicccce/meshstrata/internal/privatefile"
)

type Common struct {
	StateDir string `json:"state_dir"`
	LogLevel string `json:"log_level"`
}

type Controller struct {
	Common
	ListenAddress string            `json:"listen_address"`
	Secure        *ControllerSecure `json:"secure,omitempty"`
}

type Agent struct {
	Common
	Control *AgentControl `json:"control,omitempty"`
	Runtime *AgentRuntime `json:"runtime,omitempty"`
}
type ControllerSecure struct {
	EnableV2             bool              `json:"enable_v2,omitempty"`
	PublicOrigin         string            `json:"public_origin,omitempty"`
	EgressProbes         []netip.AddrPort  `json:"egress_probes,omitempty"`
	TLSCertFile          string            `json:"tls_cert_file"`
	TLSKeyFile           string            `json:"tls_key_file"`
	WebCAFile            string            `json:"web_ca_file"`
	AllowedOrigins       []string          `json:"allowed_origins"`
	CurrentKeyID         string            `json:"current_key_id"`
	KeyFiles             map[string]string `json:"key_files"`
	BootstrapFile        string            `json:"bootstrap_file,omitempty"`
	ControlListenAddress string            `json:"control_listen_address"`
	ControllerURL        string            `json:"controller_url"`
	ServerNames          []string          `json:"server_names"`
}

// Provisioning is trusted local policy, separate from administrative requests.
type Provisioning struct {
	DNS          control.DNSInput             `json:"dns"`
	Sockets      []control.PinnedSocketSpecV2 `json:"sockets"`
	Topology     ProtectedTopology            `json:"topology"`
	EgressProbes []netip.AddrPort             `json:"egress_probes"`
	Targets      []ProvisionedTarget          `json:"targets,omitempty"`
}
type ProtectedTopology struct {
	LocalAddresses      []netip.Addr     `json:"local_addresses"`
	ProtectedEndpoints  []netip.AddrPort `json:"protected_endpoints"`
	ProtectedNetworks   []netip.Prefix   `json:"protected_networks"`
	ProtectedInterfaces []string         `json:"protected_interfaces"`
}

func (p ProtectedTopology) Inventory() control.LoopInventory {
	return control.LoopInventory{LocalAddresses: p.LocalAddresses, ProtectedEndpoints: p.ProtectedEndpoints, ProtectedNetworks: p.ProtectedNetworks, ProtectedInterfaces: p.ProtectedInterfaces}
}

type ProvisionedTarget struct {
	Pin  control.Pin          `json:"pin"`
	Spec control.EgressTarget `json:"spec"`
	Auth *control.EgressAuth  `json:"auth,omitempty"`
}
type AgentRuntime struct {
	EnableV2     bool                    `json:"enable_v2,omitempty"`
	Provisioning *Provisioning           `json:"provisioning,omitempty"`
	MihomoBinary string                  `json:"mihomo_binary"`
	Materials    []control.LocalMaterial `json:"materials,omitempty"`
}

type AgentControl struct {
	Mode          string `json:"mode"`
	ControllerURL string `json:"controller_url"`
	CAFile        string `json:"ca_file"`
	CAFingerprint string `json:"ca_fingerprint,omitempty"`
	NodeID        string `json:"node_id"`
	TokenFile     string `json:"token_file,omitempty"`
}

func (cfg Common) LoggingLevel() string { return cfg.LogLevel }

func LoadController(path string) (Controller, error) {
	cfg := Controller{Common: Common{StateDir: ".local/controller", LogLevel: "warn"}, ListenAddress: "127.0.0.1:8443"}
	if err := decode(path, &cfg); err != nil {
		return cfg, err
	}
	if err := cfg.Common.validate(); err != nil {
		return cfg, err
	}
	host, port, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		return cfg, fmt.Errorf("listen_address must be an IP address and port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || (cfg.Secure == nil && !ip.IsLoopback()) {
		return cfg, errors.New("listen_address must use a loopback IP; this development server does not support public HTTP")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return cfg, errors.New("listen_address port must be between 1 and 65535")
	}
	if cfg.Secure != nil {
		s := cfg.Secure
		if s.TLSCertFile == "" || s.TLSKeyFile == "" || s.CurrentKeyID == "" || len(s.KeyFiles) == 0 || len(s.AllowedOrigins) == 0 || s.ControlListenAddress == "" || s.ControllerURL == "" || len(s.ServerNames) == 0 {
			return cfg, errors.New("secure mode requires complete TLS, keyring, origin and control configuration")
		}
		if s.EnableV2 && s.PublicOrigin == "" {
			return cfg, errors.New("V2 requires trusted public_origin")
		}
		if len(s.EgressProbes) > 8 || s.EnableV2 && len(s.EgressProbes) == 0 {
			return cfg, errors.New("V2 requires 1-8 numeric egress probes")
		}
		for _, p := range s.EgressProbes {
			if !p.IsValid() || p.Port() == 0 || p.Addr().IsUnspecified() || p.Addr().IsMulticast() {
				return cfg, errors.New("invalid numeric canary")
			}
		}
		if _, _, err := net.SplitHostPort(s.ControlListenAddress); err != nil {
			return cfg, errors.New("invalid control_listen_address")
		}
	}
	return cfg, nil
}

func LoadAgent(path string) (Agent, error) {
	cfg := Agent{Common: Common{StateDir: ".local/agent", LogLevel: "warn"}}
	if err := decode(path, &cfg); err != nil {
		return cfg, err
	}
	if err := cfg.Common.validate(); err != nil {
		return cfg, err
	}
	if cfg.Control != nil {
		c := cfg.Control
		if c.Mode == "" {
			c.Mode = "control_only"
		}
		if (c.Mode != "control_only" && c.Mode != "runtime") ||
			c.ControllerURL == "" || c.CAFile == "" || !domain.ID(c.NodeID).Valid() {
			return cfg, errors.New("control requires mode control_only or runtime, controller_url, ca_file and valid node_id")
		}
	}
	runtimeEnabled := cfg.Control != nil && cfg.Control.Mode == "runtime"
	if runtimeEnabled != (cfg.Runtime != nil) {
		return cfg, errors.New("runtime configuration requires explicit runtime mode")
	}
	if runtimeEnabled && (!filepath.IsAbs(cfg.Runtime.MihomoBinary) ||
		filepath.Clean(cfg.Runtime.MihomoBinary) != cfg.Runtime.MihomoBinary || len(cfg.RuntimeSocketPath()) > 100) {
		return cfg, errors.New("runtime requires an absolute Mihomo path and a short state_dir")
	}
	if cfg.Runtime != nil && cfg.Runtime.EnableV2 {
		p := cfg.Runtime.Provisioning
		if p == nil || len(p.Sockets) < 1 || len(p.Sockets) > 32 || !p.DNS.Pin.Valid() || len(p.Topology.ProtectedEndpoints) == 0 || p.Topology.ProtectedNetworks == nil || p.Topology.ProtectedInterfaces == nil || len(p.EgressProbes) < 1 || len(p.EgressProbes) > 8 || len(p.Targets) > control.MaxEgressTargets {
			return cfg, errors.New("V2 requires bounded local provisioning and explicit protection topology")
		}
		for _, probe := range p.EgressProbes {
			if !probe.IsValid() || probe.Port() == 0 || probe.Addr().IsUnspecified() || probe.Addr().IsMulticast() {
				return cfg, errors.New("invalid numeric canary")
			}
		}
	}
	if cfg.Runtime != nil && cfg.Runtime.Provisioning != nil {
		for _, target := range cfg.Runtime.Provisioning.Targets {
			if target.Auth != nil {
				absolute, err := filepath.Abs(path)
				if err != nil {
					return cfg, errors.New("private provisioning file unavailable")
				}
				raw, err := privatefile.Read(absolute, 1<<20)
				clear(raw)
				if err != nil {
					return cfg, errors.New("proxy authentication requires a private provisioning file")
				}
				break
			}
		}
	}
	// Leave enough room for the Unix socket path on Linux (108 bytes including NUL).
	if len(cfg.SocketPath()) > 107 {
		return cfg, errors.New("state_dir is too long for the Agent health socket")
	}
	return cfg, nil
}

func (cfg Agent) RuntimeSocketPath() string {
	return filepath.Join(cfg.StateDir, "runtime", "api", "m.sock")
}

func (cfg Agent) SocketPath() string {
	return filepath.Join(cfg.StateDir, "health.sock")
}

func (cfg *Common) validate() error {
	if strings.TrimSpace(cfg.StateDir) == "" {
		return errors.New("state_dir must not be empty")
	}
	abs, err := filepath.Abs(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("resolve state_dir: %w", err)
	}
	cfg.StateDir = abs
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return fmt.Errorf("invalid log_level: %w", err)
	}
	return nil
}

func decode(path string, dest any) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open configuration: %w", err)
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	// Decode into a raw object first so null cannot silently retain all defaults.
	var raw json.RawMessage
	if err := decoder.Decode(&raw); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	if len(raw) == 0 || raw[0] != '{' || !control.UniqueJSON(raw) {
		return errors.New("configuration must be a JSON object")
	}
	strict := json.NewDecoder(strings.NewReader(string(raw)))
	strict.DisallowUnknownFields()
	if err := strict.Decode(dest); err != nil {
		return fmt.Errorf("decode configuration: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("configuration must contain exactly one JSON object")
	}
	return nil
}
