package credentials

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStore_LoadMissingFile(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	s, err := LoadStore(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("LoadStore missing: %v", err)
	}
	if v, _ := s.Get("anthropic"); v != "" {
		t.Errorf("Get on empty store returned %q", v)
	}
}

func TestStore_SetGetClear(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	path := filepath.Join(t.TempDir(), "credentials.toml")
	s, _ := LoadStore(path)
	if err := s.Set("anthropic", "sk-ant-1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	v, src := s.Get("anthropic")
	if v != "sk-ant-1" {
		t.Errorf("Get value = %q, want sk-ant-1", v)
	}
	if src != SourceFile {
		t.Errorf("Get source = %q, want file", src)
	}
	// Reload from disk; persistence works.
	s2, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore reload: %v", err)
	}
	if v, _ := s2.Get("anthropic"); v != "sk-ant-1" {
		t.Errorf("reloaded value = %q", v)
	}
	if err := s2.Clear("anthropic"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if v, _ := s2.Get("anthropic"); v != "" {
		t.Errorf("after Clear, value = %q", v)
	}
}

func TestStore_PermissionsEnforced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	if err := os.WriteFile(path, []byte("schema = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadStore(path); err == nil {
		t.Errorf("LoadStore should reject 0644-mode file")
	}
}

func TestStore_GetFallsBackToEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	s, _ := LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	v, src := s.Get("anthropic")
	if v != "env-key" || src != SourceEnv {
		t.Errorf("env fallback: v=%q src=%q", v, src)
	}
}

func TestStore_List(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	t.Setenv("GEMINI_API_KEY", "")
	path := filepath.Join(t.TempDir(), "credentials.toml")
	s, _ := LoadStore(path)
	_ = s.Set("openrouter", "or-key")
	list := s.List()
	bySource := map[string]Source{}
	for _, p := range list {
		bySource[p.Name] = p.Source
	}
	if bySource["anthropic"] != SourceEnv {
		t.Errorf("anthropic source = %q", bySource["anthropic"])
	}
	if bySource["openrouter"] != SourceFile {
		t.Errorf("openrouter source = %q", bySource["openrouter"])
	}
	if _, ok := bySource["ollama"]; !ok {
		t.Errorf("ollama (no creds needed) should be in List")
	}
}
