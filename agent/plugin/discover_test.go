package plugin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestDiscoverPluginHooks covers discoverPluginHooks's manifest-shape branches
// (gremlins flagged hooks.go:392-408 as not covered): hooks given as a STRING
// path to a hooks file vs an inline OBJECT, and a missing file. The existing
// hooks_test only drives parsePluginHooks directly, never the discover/file path.
func TestDiscoverPluginHooks(t *testing.T) {
	hooksJSON := `{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"echo hi","timeout":10}]}]}}`

	t.Run("string path to a hooks file", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "hooks.json"), []byte(hooksJSON), 0o644); err != nil {
			t.Fatal(err)
		}
		manifestHooks, _ := json.Marshal("hooks.json") // a JSON string value = path
		hooks, err := discoverPluginHooks(dir, manifestHooks, "p")
		if err != nil {
			t.Fatalf("discoverPluginHooks: %v", err)
		}
		got := hooks[HookPreToolUse]
		if len(got) != 1 {
			t.Fatalf("PreToolUse = %d, want 1", len(got))
		}
		if got[0].SourcePath == "" {
			t.Error("SourcePath empty, want the resolved hooks file path")
		}
	})

	t.Run("inline object", func(t *testing.T) {
		hooks, err := discoverPluginHooks(t.TempDir(), json.RawMessage(hooksJSON), "p")
		if err != nil {
			t.Fatalf("discoverPluginHooks: %v", err)
		}
		if len(hooks[HookPreToolUse]) != 1 {
			t.Fatalf("PreToolUse = %d, want 1", len(hooks[HookPreToolUse]))
		}
	})

	t.Run("missing hooks file errors", func(t *testing.T) {
		manifestHooks, _ := json.Marshal("nonexistent.json")
		if _, err := discoverPluginHooks(t.TempDir(), manifestHooks, "p"); err == nil {
			t.Fatal("err = nil, want a read error for a missing hooks file")
		}
	})
}

// TestDiscoverPluginAgents_Override covers discoverPluginAgents's override branch
// (gremlins flagged agents.go:168 as not covered): the agents override is parsed
// and folded into the scanned dirs. The existing TestDiscoverPluginAgents only
// passes a nil override (the false branch).
func TestDiscoverPluginAgents_Override(t *testing.T) {
	t.Run("override adds a custom dir", func(t *testing.T) {
		dir := t.TempDir()
		writeAgentFile(t, dir, "extra", "other.md", "---\nname: other\ndescription: d\n---\nx\n")
		override, _ := json.Marshal([]string{"extra"})
		agents, err := discoverPluginAgents(dir, override, "p")
		if err != nil {
			t.Fatalf("discoverPluginAgents: %v", err)
		}
		if _, ok := agents["p:other"]; !ok {
			t.Fatalf("agents = %v, want p:other from the override dir", agents)
		}
	})

	t.Run("malformed override errors", func(t *testing.T) {
		if _, err := discoverPluginAgents(t.TempDir(), json.RawMessage(`{bad`), "p"); err == nil {
			t.Fatal("err = nil, want an agents-override parse error")
		}
	})
}

func writeAgentFile(t *testing.T, pluginDir, sub, name, content string) {
	t.Helper()
	dir := filepath.Join(pluginDir, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
