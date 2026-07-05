package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"primeradiant.com/serf/cmd/serf-hub/internal/appsource"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/envvars"
)

// ProviderConfig lists the models available from a single provider.
type ProviderConfig struct {
	Name   string   `toml:"name"`
	Models []string `toml:"models"`
}

// Config is the hub's runtime configuration loaded from ~/.serf/hub.toml.
type Config struct {
	Addr               string                          `toml:"addr"`
	HubStateRoot       string                          `toml:"hub_state_root"`
	StateGlob          string                          `toml:"state_glob"`
	RunDir             string                          `toml:"run_dir"`
	PastIndexDB        string                          `toml:"past_index_db"`
	StatusPollInterval time.Duration                   `toml:"status_poll_interval"`
	PastIndexRebuild   time.Duration                   `toml:"past_index_rebuild_interval"`
	SpawnTimeout       time.Duration                   `toml:"spawn_timeout"`
	PastResultsPerPage int                             `toml:"past_results_per_page"`
	Providers          []ProviderConfig                `toml:"providers"`
	CodexSources       []appsource.CodexSourceConfig   `toml:"codex_sources"`
	CodexLaunches      []codexlaunch.CodexLaunchConfig `toml:"codex_launches"`

	// PluginAutoUpgrade is the global on/off switch for the background plugin
	// auto-upgrade daemon (design doc §9.1). Defaults to on: the meaningful
	// consent gate is the per-plugin `autoUpgrade` opt-in (SetAutoUpgrade) —
	// enabling that on an already-installed, git-backed plugin is the
	// standing consent for it to be upgraded unattended. This switch exists
	// as an operator-level kill switch/tuning knob, not the primary gate; if
	// it defaulted off, flipping a plugin's auto-upgrade toggle in the web/TUI
	// would silently do nothing until hub.toml was also hand-edited.
	PluginAutoUpgrade bool `toml:"plugin_auto_upgrade"`
	// PluginAutoUpgradeInterval is how often the daemon refreshes marketplaces
	// and re-checks autoUpgrade-enabled plugins, plus once on hub start.
	PluginAutoUpgradeInterval time.Duration `toml:"plugin_auto_upgrade_interval"`
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Addr:                      "127.0.0.1:9180",
		HubStateRoot:              DefaultHubStateRoot(),
		StateGlob:                 "",
		RunDir:                    "",
		StatusPollInterval:        2 * time.Second,
		PastIndexRebuild:          60 * time.Second,
		SpawnTimeout:              30 * time.Second,
		PastResultsPerPage:        50,
		PluginAutoUpgrade:         true,
		PluginAutoUpgradeInterval: 12 * time.Hour,
	}
}

// DefaultHubStateRoot returns ~/.serf.
func DefaultHubStateRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".serf")
}

// DefaultConfigPath returns ~/.serf/hub.toml.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".serf", "hub.toml")
}

// DefaultStateGlob returns the project state roots indexed by the hub.
func DefaultStateGlob() string {
	base := envvars.XDGStateHome.Getenv()
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "serf", "projects", "*")
}

// DefaultPastIndexDBPath returns ~/.serf/index.db.
func DefaultPastIndexDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, ".serf", "index.db")
}

// LoadConfig reads path. A missing file returns DefaultConfig() and nil error.
func LoadConfig(path string) (Config, error) {
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config: %w", err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config: %w", err)
	}
	applyConfigDefaults(&cfg)
	return cfg, nil
}

func applyConfigDefaults(cfg *Config) {
	if cfg.Addr == "" {
		cfg.Addr = "127.0.0.1:9180"
	}
	if cfg.StatusPollInterval == 0 {
		cfg.StatusPollInterval = 2 * time.Second
	}
	if cfg.PastIndexRebuild == 0 {
		cfg.PastIndexRebuild = 60 * time.Second
	}
	if cfg.SpawnTimeout == 0 {
		cfg.SpawnTimeout = 30 * time.Second
	}
	if cfg.PastResultsPerPage == 0 {
		cfg.PastResultsPerPage = 50
	}
	if cfg.HubStateRoot == "" {
		cfg.HubStateRoot = DefaultHubStateRoot()
	}
	if cfg.PluginAutoUpgradeInterval == 0 {
		cfg.PluginAutoUpgradeInterval = 12 * time.Hour
	}
	// PluginAutoUpgrade is intentionally NOT defaulted here: it is a bool
	// whose zero value (false) is a legitimate explicit choice (an operator
	// opting out), indistinguishable at this point from "absent from the
	// file". DefaultConfig() already pre-populates true, and toml.Unmarshal
	// only overwrites keys actually present in the document, so an absent key
	// correctly leaves the true default and an explicit `false` correctly
	// sticks.
}
