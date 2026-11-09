package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestControllerConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		valid      bool
	}{
		{"defaults", `{}`, true},
		{"ipv6", `{"listen_address":"[::1]:8443"}`, true},
		{"public", `{"listen_address":"0.0.0.0:8443"}`, false},
		{"public_ipv6", `{"listen_address":"[::]:8443"}`, false},
		{"hostname", `{"listen_address":"localhost:8443"}`, false},
		{"port_zero", `{"listen_address":"127.0.0.1:0"}`, false},
		{"port_range", `{"listen_address":"127.0.0.1:65536"}`, false},
		{"unknown", `{"listen_addr":"127.0.0.1:8443"}`, false},
		{"empty_state", `{"state_dir":" "}`, false},
		{"level", `{"log_level":"verbose"}`, false},
		{"null", `null`, false},
		{"array", `[]`, false},
		{"trailing_object", `{} {}`, false},
		{"trailing_garbage", `{} broken`, false},
		{"malformed", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadController(path)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, error=%v", tc.valid, err)
			}
			if tc.valid && !filepath.IsAbs(cfg.StateDir) {
				t.Fatal("state path was not resolved")
			}
		})
	}
	if _, err := LoadController(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing configuration accepted")
	}
}

func TestAgentConfiguration(t *testing.T) {
	cfg, err := LoadAgent("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "warn" || !filepath.IsAbs(cfg.SocketPath()) {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"listen_address":"127.0.0.1:9443"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgent(path); err == nil {
		t.Fatal("Controller field accepted by Agent")
	}
}
