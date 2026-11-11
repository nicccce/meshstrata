package agent

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/internal/config"
)

func TestIndependentLifecycle(t *testing.T) {
	dir, err := os.MkdirTemp("", "meshstrata-agent-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	cfg := config.Agent{Common: config.Common{StateDir: dir, LogLevel: "warn"}}
	// Simulate a socket left by SIGKILL. Recovery must not require Controller.
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: cfg.SocketPath(), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	stale.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	go func() { done <- Run(ctx, cfg, logger) }()
	// Always join the goroutine, even when an assertion fails.
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("Agent did not shut down")
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if health, err := CheckHealth(context.Background(), cfg); err == nil {
			if health.Component != "agent" || health.Status != "ok" {
				t.Fatalf("unexpected health: %+v", health)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Agent never became healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	for path, mode := range map[string]os.FileMode{dir: 0o700, cfg.SocketPath(): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Fatalf("unexpected permissions on %s: %v", path, info.Mode())
		}
	}
	if err := Run(context.Background(), cfg, logger); err == nil {
		t.Fatal("duplicate Agent started")
	}
	cancel()
	// Wait for socket cleanup without consuming the completion channel twice.
	deadline = time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Lstat(cfg.SocketPath()); os.IsNotExist(err) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("health socket was not removed")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHealthPathDoesNotOverwriteFile(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Agent{Common: config.Common{StateDir: dir, LogLevel: "warn"}}
	path := filepath.Join(dir, "health.sock")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	if err := Run(context.Background(), cfg, logger); err == nil {
		t.Fatal("existing file was accepted as socket")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep" {
		t.Fatalf("existing file changed: %q, %v", data, err)
	}
}
