package command

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/nicccce/meshstrata/internal/config"
)

func testApp(name string) Component[config.Agent] {
	return Component[config.Agent]{Name: name, Load: config.LoadAgent}
}

func TestCommands(t *testing.T) {
	for _, component := range []string{"controller", "agent"} {
		for _, tc := range []struct {
			name string
			args []string
			code int
		}{
			{"version", []string{"version"}, 0},
			{"config", []string{"check-config"}, 0},
			{"help", []string{"-h"}, 0},
			{"unknown", []string{"no-such-command"}, 2},
			{"extra", []string{"version", "extra"}, 2},
			{"bad_file", []string{"check-config", "-config", filepath.Join(t.TempDir(), "missing")}, 2},
		} {
			t.Run(component+"/"+tc.name, func(t *testing.T) {
				var out, stderr bytes.Buffer
				if code := Execute(testApp(component), tc.args, &out, &stderr); code != tc.code {
					t.Fatalf("exit %d, want %d: %s", code, tc.code, stderr.String())
				}
				if tc.name == "version" || tc.name == "config" {
					var result map[string]any
					if err := json.Unmarshal(out.Bytes(), &result); err != nil {
						t.Fatal(err)
					}
					if result["component"] != component {
						t.Fatalf("unexpected output: %s", out.String())
					}
				}
			})
		}
	}
}

func TestCheckConfigDoesNotCreateState(t *testing.T) {
	dir := t.TempDir()
	state := filepath.Join(dir, "absent")
	path := filepath.Join(dir, "config.json")
	body, _ := json.Marshal(map[string]string{"state_dir": state})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if code := Execute(testApp("agent"), []string{"check-config", "-config", path}, &output, &output); code != 0 {
		t.Fatal(output.String())
	}
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatal("check-config created state")
	}
}
