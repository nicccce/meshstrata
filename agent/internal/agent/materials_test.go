package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/nicccce/meshstrata/internal/config"
	"github.com/nicccce/meshstrata/internal/control"
	"github.com/nicccce/meshstrata/internal/domain"
	"os"
	"path/filepath"
	"testing"
)

func TestLocalMaterialsApprovedImmutableAndPrivate(t *testing.T) {
	ref := control.LocalMaterial{ID: domain.ID("material_11111111111111111111111111111111"), Purpose: "reality_private_key"}
	cfg := config.Agent{Common: config.Common{StateDir: t.TempDir()}, Runtime: &config.AgentRuntime{Materials: []control.LocalMaterial{ref}}}
	if e := os.MkdirAll(filepath.Join(cfg.StateDir, "runtime", "config", "materials"), 0700); e != nil {
		t.Fatal(e)
	}
	input := func(r control.LocalMaterial, b byte) *bytes.Reader {
		raw, _ := json.Marshal(map[string]any{"materials": []any{map[string]any{"ref": r, "data": []byte(base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32)))}}})
		return bytes.NewReader(raw)
	}
	ctx := context.Background()
	if e := ImportMaterials(ctx, cfg, input(ref, 1)); e != nil {
		t.Fatal(e)
	}
	if e := ImportMaterials(ctx, cfg, input(ref, 1)); e != nil {
		t.Fatal("identical import replay", e)
	}
	if e := ImportMaterials(ctx, cfg, input(ref, 2)); e == nil {
		t.Fatal("changed immutable material accepted")
	}
	other := ref
	other.ID = "material_22222222222222222222222222222222"
	if e := ImportMaterials(ctx, cfg, input(other, 1)); e == nil {
		t.Fatal("unapproved reference accepted")
	}
	path := filepath.Join(cfg.StateDir, "runtime", "config", "materials", string(ref.ID)+"."+ref.Purpose)
	info, e := os.Stat(path)
	if e != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private material permissions", e)
	}
	raw, e := os.ReadFile(path)
	if e != nil || string(raw) != base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)) {
		t.Fatal("failed import changed old bytes", e)
	}
}
