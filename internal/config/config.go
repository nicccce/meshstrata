package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
}
type ControllerSecure struct {
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
		if c.Mode != "control_only" || c.ControllerURL == "" || c.CAFile == "" || c.NodeID == "" {
			return cfg, errors.New("stage 1 control requires mode control_only, controller_url, ca_file and node_id")
		}
	}
	// Leave enough room for the Unix socket path on Linux (108 bytes including NUL).
	if len(cfg.SocketPath()) > 107 {
		return cfg, errors.New("state_dir is too long for the Agent health socket")
	}
	return cfg, nil
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
	if len(raw) == 0 || raw[0] != '{' {
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
