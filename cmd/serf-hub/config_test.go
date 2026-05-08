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
	if len(cfg.Providers) != 0 {
		t.Errorf("expected no providers by default, got %d", len(cfg.Providers))
	}
}

func TestLoadConfig_ParsesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	body := `
addr = "127.0.0.1:9180"
spawn_timeout = "10s"
past_results_per_page = 25

[[providers]]
name = "openai"
models = ["gpt-5", "gpt-5-mini"]

[[providers]]
name = "anthropic"
models = ["claude-opus-4-7", "claude-sonnet-4-6"]
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
	if len(cfg.Providers) != 2 {
		t.Fatalf("providers: got %d, want 2", len(cfg.Providers))
	}
	if cfg.Providers[0].Name != "openai" || len(cfg.Providers[0].Models) != 2 {
		t.Errorf("providers[0] mismatch: %+v", cfg.Providers[0])
	}
	if cfg.Providers[1].Name != "anthropic" || cfg.Providers[1].Models[0] != "claude-opus-4-7" {
		t.Errorf("providers[1] mismatch: %+v", cfg.Providers[1])
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
