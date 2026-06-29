package plugin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// validPluginManifest is the minimal manifest that lets Load succeed so seeds
// reach past ParseManifest into the component-dir walk.
var validPluginManifest = []byte(`{"name":"p","version":"1.0.0"}`)

// FuzzPluginLoad drives plugin.Load(dir) over a bounded, fixed-shape directory
// tree materialized under t.TempDir(). The layout bits gate which files exist;
// fuzz bytes (manifest, mcpJSON) flow ONLY into file CONTENTS — every path
// component is a compile-time constant, so nothing is ever written outside the
// per-run temp dir. The oracle is floor "no panic" (a malformed manifest / mcp /
// skill / hooks file on disk must yield an error, never a crash) plus a
// flavor-fallback invariant (bit0 set → "claude"; only bit1 → "codex";
// ManifestPath inside the resolved dir) and no-partial on error (Load returns
// Instance{} on every error path).
func FuzzPluginLoad(f *testing.F) {
	f.Add(uint16(0b000001), validPluginManifest, []byte(nil))                 // claude only
	f.Add(uint16(0b000010), validPluginManifest, []byte(nil))                 // codex fallback
	f.Add(uint16(0b000011), validPluginManifest, []byte(nil))                 // both -> claude wins
	f.Add(uint16(0b010001), validPluginManifest, []byte(`{"mcpServers":{}}`)) // claude + .mcp.json

	f.Fuzz(func(t *testing.T, layout uint16, manifest, mcpJSON []byte) {
		dir := t.TempDir()
		writeFile := func(rel string, content []byte) {
			full := filepath.Join(dir, rel)
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				t.Fatalf("mkdir for %s: %v", rel, err)
			}
			if err := os.WriteFile(full, content, 0o600); err != nil {
				t.Fatalf("write %s: %v", rel, err)
			}
		}

		hasClaude := layout&0b000001 != 0
		hasCodex := layout&0b000010 != 0
		if hasClaude {
			writeFile(filepath.Join(".claude-plugin", "plugin.json"), manifest)
		}
		if hasCodex {
			writeFile(filepath.Join(".codex-plugin", "plugin.json"), manifest)
		}
		if layout&0b000100 != 0 {
			writeFile(filepath.Join("skills", "s", "SKILL.md"), manifest)
		}
		if layout&0b001000 != 0 {
			writeFile(filepath.Join("agents", "a.md"), manifest)
		}
		if layout&0b010000 != 0 {
			writeFile(".mcp.json", mcpJSON)
		}
		if layout&0b100000 != 0 {
			writeFile(filepath.Join("hooks", "hooks.json"), mcpJSON)
		}

		inst, err := Load(dir)
		if err != nil {
			// No-partial: every Load error path returns Instance{}.
			if inst.Dir != "" || inst.ManifestFlavor != "" || inst.ManifestPath != "" || inst.Manifest.Name != "" {
				t.Fatalf("Load error returned non-zero Instance: %#v\n layout=%b manifest=%q", inst, layout, manifest)
			}
			return
		}

		// Flavor-fallback invariant: Claude is preferred when present.
		wantFlavor := "codex"
		if hasClaude {
			wantFlavor = "claude"
		}
		if inst.ManifestFlavor != wantFlavor {
			t.Fatalf("ManifestFlavor = %q, want %q\n layout=%b", inst.ManifestFlavor, wantFlavor, layout)
		}

		// ManifestPath must be inside the resolved temp dir.
		resolved, rerr := filepath.EvalSymlinks(dir)
		if rerr != nil {
			t.Fatalf("EvalSymlinks(%q): %v", dir, rerr)
		}
		if !strings.HasPrefix(inst.ManifestPath, resolved) {
			t.Fatalf("ManifestPath %q escaped dir %q\n layout=%b", inst.ManifestPath, resolved, layout)
		}
	})
}
