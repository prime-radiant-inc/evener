package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfig_DefaultsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
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
	wantHubStateRoot := filepath.Join(dir, "home", ".serf")
	if cfg.HubStateRoot != wantHubStateRoot {
		t.Errorf("HubStateRoot default: got %q, want %q", cfg.HubStateRoot, wantHubStateRoot)
	}
	if len(cfg.Providers) != 0 {
		t.Errorf("expected no providers by default, got %d", len(cfg.Providers))
	}
	if !cfg.PluginAutoUpgrade {
		t.Error("PluginAutoUpgrade default: got false, want true (per-plugin autoUpgrade opt-in is the real gate)")
	}
	if cfg.PluginAutoUpgradeInterval != 12*time.Hour {
		t.Errorf("PluginAutoUpgradeInterval default: got %v, want 12h", cfg.PluginAutoUpgradeInterval)
	}
}

func TestLoadConfig_PluginAutoUpgradeExplicitFalseSticks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(path, []byte(`plugin_auto_upgrade = false`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PluginAutoUpgrade {
		t.Error("explicit plugin_auto_upgrade = false was overridden back to true")
	}
	// interval default still applies even though auto-upgrade is off.
	if cfg.PluginAutoUpgradeInterval != 12*time.Hour {
		t.Errorf("PluginAutoUpgradeInterval: got %v, want 12h", cfg.PluginAutoUpgradeInterval)
	}
}

func TestLoadConfig_PluginAutoUpgradeIntervalOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(path, []byte(`plugin_auto_upgrade_interval = "1h"`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.PluginAutoUpgradeInterval != time.Hour {
		t.Errorf("PluginAutoUpgradeInterval: got %v, want 1h", cfg.PluginAutoUpgradeInterval)
	}
	if !cfg.PluginAutoUpgrade {
		t.Error("PluginAutoUpgrade should still default true when only the interval is overridden")
	}
}

// TestLoadConfig_PluginAutoUpgradeIntervalGuardsAgainstBadValues covers
// values time.NewTicker would panic or busy-loop on. BurntSushi/toml v1.6.0
// parses a negative duration string ("-1h") and a bare integer ("12", read
// as a nanosecond count) without error, so neither trips a naive `== 0`
// guard: time.NewTicker(-1h) panics (d <= 0), and time.NewTicker(12ns) spins
// the daemon in a busy loop. Both must be caught by applyConfigDefaults.
func TestLoadConfig_PluginAutoUpgradeIntervalGuardsAgainstBadValues(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want time.Duration
	}{
		{"zero falls back to default", `plugin_auto_upgrade_interval = "0s"`, 12 * time.Hour},
		{"negative falls back to default", `plugin_auto_upgrade_interval = "-1h"`, 12 * time.Hour},
		{"bare integer (units confusion) clamped to floor", `plugin_auto_upgrade_interval = 12`, time.Minute},
		{"positive but sub-minute clamped to floor", `plugin_auto_upgrade_interval = "30s"`, time.Minute},
		{"exactly the floor is left alone", `plugin_auto_upgrade_interval = "1m"`, time.Minute},
		{"a normal value is left alone", `plugin_auto_upgrade_interval = "2h"`, 2 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "hub.toml")
			if err := os.WriteFile(path, []byte(tt.toml), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if cfg.PluginAutoUpgradeInterval != tt.want {
				t.Errorf("PluginAutoUpgradeInterval = %v, want %v", cfg.PluginAutoUpgradeInterval, tt.want)
			}
		})
	}
}

func TestLoadConfig_DefaultsHubStateRootWhenOmitted(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", filepath.Join(dir, "home"))
	path := filepath.Join(dir, "hub.toml")
	if err := os.WriteFile(path, []byte(`addr = "127.0.0.1:9191"`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	want := filepath.Join(dir, "home", ".serf")
	if cfg.HubStateRoot != want {
		t.Fatalf("HubStateRoot = %q, want %q", cfg.HubStateRoot, want)
	}
}

func TestLoadConfig_PreservesExplicitHubStateRoot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.toml")
	explicit := filepath.Join(dir, "explicit-state")
	if err := os.WriteFile(path, []byte(`hub_state_root = "`+explicit+`"`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HubStateRoot != explicit {
		t.Fatalf("HubStateRoot = %q, want %q", cfg.HubStateRoot, explicit)
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
