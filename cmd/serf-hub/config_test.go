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
past_index_db = "/tmp/serf-index.db"
spawn_timeout = "10s"
past_results_per_page = 25

[[providers]]
name = "openai"
models = ["gpt-5", "gpt-5-mini"]

[[providers]]
name = "anthropic"
models = ["claude-opus-4-7", "claude-sonnet-4-6"]

[[codex_sources]]
id = "codex-local"
endpoint = "ws://127.0.0.1:9900"
bearer_token_file = "/tmp/codex-token"

[[codex_launches]]
id = "codex-managed"
binary = "/usr/local/bin/codex"
working_dir = "/tmp/work"
listen = "ws://127.0.0.1:9901"
timeout = "5s"
args = ["app-server"]

[codex_launches.env]
CODEX_HOME = "/tmp/codex-home"

[serf_launch]
sse_ring_size = 4096

[serf_launch.env]
OPENROUTER_API_KEY = "configured-openrouter-key"
SERF_STATE_DIR = "/tmp/serf-state"
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
	if cfg.PastIndexDB != "/tmp/serf-index.db" {
		t.Errorf("PastIndexDB: got %q", cfg.PastIndexDB)
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
	if len(cfg.CodexSources) != 1 {
		t.Fatalf("codex sources: got %d, want 1", len(cfg.CodexSources))
	}
	if cfg.CodexSources[0].ID != "codex-local" || cfg.CodexSources[0].Endpoint != "ws://127.0.0.1:9900" || cfg.CodexSources[0].BearerTokenFile != "/tmp/codex-token" {
		t.Errorf("codex source mismatch: %+v", cfg.CodexSources[0])
	}
	if len(cfg.CodexLaunches) != 1 {
		t.Fatalf("codex launches: got %d, want 1", len(cfg.CodexLaunches))
	}
	launch := cfg.CodexLaunches[0]
	if launch.ID != "codex-managed" || launch.Binary != "/usr/local/bin/codex" || launch.WorkingDir != "/tmp/work" || launch.Listen != "ws://127.0.0.1:9901" || launch.Timeout != 5*time.Second {
		t.Errorf("codex launch mismatch: %+v", launch)
	}
	if len(launch.Args) != 1 || launch.Args[0] != "app-server" {
		t.Errorf("codex launch args mismatch: %+v", launch.Args)
	}
	if launch.Env["CODEX_HOME"] != "/tmp/codex-home" {
		t.Errorf("codex launch env mismatch: %+v", launch.Env)
	}
	if cfg.SerfLaunch.Env["OPENROUTER_API_KEY"] != "configured-openrouter-key" || cfg.SerfLaunch.Env["SERF_STATE_DIR"] != "/tmp/serf-state" {
		t.Errorf("serf launch env mismatch: %+v", cfg.SerfLaunch.Env)
	}
	if cfg.SerfLaunch.SSERingSize != 4096 {
		t.Errorf("serf launch sse_ring_size: got %d, want 4096", cfg.SerfLaunch.SSERingSize)
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

func TestDefaultStateGlob_RespectsXDGStateHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	t.Setenv("XDG_STATE_HOME", "/srv/serf-state")
	got := DefaultStateGlob()
	want := "/srv/serf-state/serf/projects/*"
	if got != want {
		t.Fatalf("DefaultStateGlob: got %q, want %q", got, want)
	}
}

func TestDefaultPastIndexDBPath_RespectsHome(t *testing.T) {
	t.Setenv("HOME", "/tmp/fakehome")
	got := DefaultPastIndexDBPath()
	want := "/tmp/fakehome/.serf/index.db"
	if got != want {
		t.Fatalf("DefaultPastIndexDBPath: got %q, want %q", got, want)
	}
}
