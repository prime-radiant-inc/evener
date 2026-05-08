package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_DefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	cfg, err := LoadConfig(filepath.Join(dir, "nope.toml"))
	if err != nil {
		t.Fatalf("LoadConfig missing: %v", err)
	}
	if cfg.Addr != "127.0.0.1:9180" {
		t.Errorf("Addr default: got %q", cfg.Addr)
	}
	if cfg.SpawnTimeout != 30*time.Second {
		t.Errorf("SpawnTimeout default: got %v", cfg.SpawnTimeout)
	}
	if cfg.PastResultsPerPage != 50 {
		t.Errorf("PastResultsPerPage default: got %d", cfg.PastResultsPerPage)
	}
	if len(cfg.SpawnTemplates) != 0 {
		t.Errorf("expected no spawn templates by default, got %d", len(cfg.SpawnTemplates))
	}
}

func TestLoadConfig_ParsesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	body := `
addr = "127.0.0.1:9180"
spawn_timeout = "10s"
past_results_per_page = 25

[[spawn_template]]
name = "code, gpt"
provider = "openai"
model = "gpt-5.2"
agent = "default"

[[spawn_template]]
name = "review, claude"
provider = "anthropic"
model = "claude-opus-4-7"
agent = "reviewer"
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.SpawnTimeout != 10*time.Second {
		t.Errorf("SpawnTimeout: got %v", cfg.SpawnTimeout)
	}
	if cfg.PastResultsPerPage != 25 {
		t.Errorf("PastResultsPerPage: got %d", cfg.PastResultsPerPage)
	}
	if len(cfg.SpawnTemplates) != 2 {
		t.Fatalf("templates: got %d, want 2", len(cfg.SpawnTemplates))
	}
	if cfg.SpawnTemplates[0].Name != "code, gpt" || cfg.SpawnTemplates[0].Model != "gpt-5.2" {
		t.Errorf("template[0] mismatch: %+v", cfg.SpawnTemplates[0])
	}
}

func TestDefaultConfigPath_RespectsHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	got := DefaultConfigPath()
	want := "/tmp/fakehome/.serf/hub.toml"
	if got != want {
		t.Fatalf("DefaultConfigPath: got %q, want %q", got, want)
	}
}
