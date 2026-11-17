package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nicccce/meshstrata/agent/internal/checkpoint"
	channel "github.com/nicccce/meshstrata/agent/internal/control"
	"github.com/nicccce/meshstrata/internal/command"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/domain"
	core "github.com/nicccce/meshstrata/internal/mihomo"
	"golang.org/x/sys/unix"
)

func TestCheckpointFailureProtectiveStop(t *testing.T) {
	binary := os.Getenv("R02_TEST_PINNED_MIHOMO")
	if binary == "" {
		t.Skip("set R02_TEST_PINNED_MIHOMO to the pinned binary; starts a local empty config")
	}
	for _, tc := range []struct {
		name, fault, ownership, expected string
	}{
		{"missing-key", "missing", "", "runtime checkpoint key unavailable"},
		{"wrong-key", "wrong", "", checkpoint.ErrCorrupt.Error()},
		{"bad-state", "bad", "", checkpoint.ErrCorrupt.Error()},
		{"unattributable", "wrong", "token", "AGENT_KERNEL_PROCESS_FAILED"},
		{"stale-pid", "wrong", "start", checkpoint.ErrCorrupt.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			root, err := os.MkdirTemp("", "r02-agent-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(root)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			write := func(path string, data []byte) { t.Helper(); must(os.WriteFile(path, data, 0600)) }
			read := func(path string) []byte {
				t.Helper()
				data, err := os.ReadFile(path)
				must(err)
				return data
			}

			node := domain.ID("node_00000000000000000000000000000001")
			cfg := config.Agent{Common: config.Common{StateDir: root}, Control: &config.AgentControl{NodeID: string(node)},
				Runtime: &config.AgentRuntime{MihomoBinary: binary}}
			dir := filepath.Join(root, "runtime")
			cfgDir := filepath.Join(dir, "config")
			cpDir := filepath.Join(dir, "checkpoint")
			pDir := filepath.Join(dir, "process")
			for _, sub := range []string{cfgDir, cpDir, pDir, filepath.Dir(cfg.RuntimeSocketPath())} {
				must(os.MkdirAll(sub, 0700))
			}
			key := bytes.Repeat([]byte{1}, 32)
			opts := checkpoint.Options{Dir: cpDir, NodeID: node, KeyID: "runtime-v1", Key: key}
			store, err := checkpoint.Open(opts)
			must(err)
			must(store.Close())
			keyPath := filepath.Join(dir, "checkpoint.key")
			cpPath := filepath.Join(cpDir, "checkpoint.aead")
			ciphertext := read(cpPath)
			write(keyPath, key)
			write(filepath.Join(root, runtimeModeFile), []byte(node))
			write(filepath.Join(dir, "prepared"), []byte(node))
			apiKey := bytes.Repeat([]byte{2}, 32)
			write(filepath.Join(dir, "api.key"), apiKey)

			// An empty, deny-all configuration supplies only the private Unix API.
			content, _, err := core.Generate(nil, core.DNS{Enable: true, EnhancedMode: "normal",
				Nameserver: []string{"127.0.0.1"}, DefaultNameserver: []string{"127.0.0.1"}}, cfg.RuntimeSocketPath(), []string{"MATCH,REJECT"})
			must(err)
			content.Secret = base64.RawURLEncoding.EncodeToString(apiKey)
			b, err := json.Marshal(content)
			must(err)
			p := filepath.Join(cfgDir, "effective-"+core.Digest(b)+".json")
			write(p, b)
			proc, err := runtimeProcess(cfg)
			must(err)
			pid, err := proc.Start(ctx, binary, []string{"-d", cfgDir, "-f", p})
			must(err)
			defer func() {
				stop, done := context.WithTimeout(context.Background(), 5*time.Second)
				defer done()
				if err := proc.Stop(stop); err != nil {
					t.Errorf("fixture cleanup: %v", err)
				}
			}()
			status, err := proc.Status(ctx)
			if err != nil || !status.Running || status.PID != pid {
				t.Fatalf("fixture process was never attributed: %+v, %v", status, err)
			}
			pidfd, err := unix.PidfdOpen(pid, 0)
			must(err)
			defer unix.Close(pidfd)
			// Require a live Unix listener, rather than a process observed
			// briefly before startup has a chance to fail on the fixture.
			ready := time.Now().Add(3 * time.Second)
			for {
				c, err := net.DialTimeout("unix", cfg.RuntimeSocketPath(), 50*time.Millisecond)
				if err == nil {
					c.Close()
					break
				}
				if time.Now().After(ready) {
					t.Fatal("fixture never opened its private Unix API", err)
				}
				time.Sleep(20 * time.Millisecond)
			}

			if tc.ownership != "" {
				path := filepath.Join(pDir, "kernel-process.json")
				original := read(path)
				defer write(path, original) // restore proof before the exact-PID cleanup
				var record map[string]json.RawMessage
				must(json.Unmarshal(original, &record))
				if tc.ownership == "token" {
					record["Token"] = json.RawMessage(`"00000000000000000000000000000000"`)
				} else {
					record["Start"] = json.RawMessage(`"1"`)
				}
				raw, err := json.Marshal(record)
				must(err)
				write(path, raw)
			}
			switch tc.fault {
			case "missing":
				must(os.Remove(keyPath))
			case "wrong":
				write(keyPath, bytes.Repeat([]byte{3}, 32))
			case "bad":
				write(cpPath, append([]byte(nil), ciphertext[:8]...))
			}
			before := read(cpPath)
			b, err = json.Marshal(cfg)
			must(err)
			conf := filepath.Join(root, "fixture.json")
			write(conf, b)
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestComposeRuntimeCLIChild$")
			cmd.Env = append(os.Environ(), "R02_TEST_COMPOSE_CONFIG="+conf)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err = cmd.Run()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), tc.expected) {
				t.Fatalf("unexpected CLI result: %v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			poll := []unix.PollFd{{Fd: int32(pidfd), Events: unix.POLLIN}}
			n, err := unix.Poll(poll, 0)
			must(err)
			if tc.ownership == "" && n != 1 || tc.ownership != "" && n != 0 {
				t.Fatalf("unexpected pidfd exit observation: %d", n)
			}
			if !bytes.Equal(before, read(cpPath)) {
				t.Fatal("failure modified checkpoint ciphertext")
			}
			if tc.fault == "bad" {
				write(cpPath, ciphertext) // only the fixture owner repairs its injected fault
			}
			opts.RequireExisting = true
			restored, err := checkpoint.Open(opts)
			must(err)
			must(restored.Close())
			t.Logf("exit_code=%d stdout_bytes=%d stderr_reason=%s owned_pid_exited=%v ciphertext_preserved=true", exit.ExitCode(), stdout.Len(), tc.expected, n == 1)
		})
	}
}

// Exercise composeRuntime's real startup failure with the same run/error/exit
// plumbing as main, without invoking enrollment or a Controller.
func TestComposeRuntimeCLIChild(t *testing.T) {
	path := os.Getenv("R02_TEST_COMPOSE_CONFIG")
	if path == "" {
		return
	}
	app := command.Component[config.Agent]{
		Name: "agent",
		Load: func(string) (config.Agent, error) {
			var cfg config.Agent
			b, err := os.ReadFile(path)
			if err == nil {
				err = json.Unmarshal(b, &cfg)
			}
			return cfg, err
		},
		Run: func(ctx context.Context, cfg config.Agent, logger *slog.Logger) error {
			_, closeRuntime, err := composeRuntime(ctx, cfg, new(atomic.Pointer[channel.Client]), logger)
			if err == nil {
				closeRuntime()
				return fmt.Errorf("unexpected successful compose")
			}
			return err
		},
	}
	os.Exit(command.Execute(app, []string{"run"}, os.Stdout, os.Stderr))
}
