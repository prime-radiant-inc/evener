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
	if err := os.Chmod(path, 0o644); err != nil {
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

func TestStore_OpenAICompatibleUsesAPIKeyEnv(t *testing.T) {
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "compat-key")
	t.Setenv("OPENAI_COMPATIBLE_BASE_URL", "https://compat.example.test/v1")
	s, _ := LoadStore(filepath.Join(t.TempDir(), "credentials.toml"))
	v, src := s.Get("openai-compatible")
	if v != "compat-key" || src != SourceEnv {
		t.Errorf("openai-compatible env fallback: v=%q src=%q", v, src)
	}
	if got := EnvVars("openai-compatible"); len(got) != 1 || got[0] != "OPENAI_COMPATIBLE_API_KEY" {
		t.Errorf("EnvVars(openai-compatible) = %v", got)
	}
}

func TestResolveKeyNameThenTypeEnv(t *testing.T) {
	// Create a credentials.toml with [providers.work] api_key="file-work"
	path := filepath.Join(t.TempDir(), "credentials.toml")
	content := "schema = 1\n[providers.work]\napi_key = \"file-work\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	// 1) file entry under the instance name wins
	if v, src := s.ResolveKey("work", "openai"); v != "file-work" || src != SourceFile {
		t.Fatalf("name lookup = %q/%v, want file-work/file", v, src)
	}
	// 2) a custom instance with no file entry → env by TYPE
	t.Setenv("OPENAI_API_KEY", "env-openai")
	if v, src := s.ResolveKey("work2", "openai"); v != "env-openai" || src != SourceEnv {
		t.Fatalf("type-env fallback = %q/%v, want env-openai/env", v, src)
	}
	// 3) nothing anywhere → absent
	if v, src := s.ResolveKey("nope", "kimi"); v != "" || src != SourceAbsent {
		t.Fatalf("absent = %q/%v", v, src)
	}
}

func TestResolveKeyOpenAICompatibleUsesCompatEnv(t *testing.T) {
	// A seeded openai-compatible instance has name="openai-compatible", type="openai".
	// Its key env var is OPENAI_COMPATIBLE_API_KEY, NOT the type's OPENAI_API_KEY.
	path := filepath.Join(t.TempDir(), "credentials.toml")
	if err := os.WriteFile(path, []byte("schema = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := LoadStore(path)
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}

	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_COMPATIBLE_API_KEY", "compat-key")
	if v, src := s.ResolveKey("openai-compatible", "openai"); v != "compat-key" || src != SourceEnv {
		t.Fatalf("openai-compatible env = %q/%v, want compat-key/env", v, src)
	}
	// the name's env var wins over the type's when both are set
	t.Setenv("OPENAI_API_KEY", "type-key")
	if v, _ := s.ResolveKey("openai-compatible", "openai"); v != "compat-key" {
		t.Fatalf("name env must win over type env: got %q, want compat-key", v)
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
