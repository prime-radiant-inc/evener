package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"primeradiant.com/serf/internal/appsource"
)

// ProviderConfig lists the models available from a single provider.
type ProviderConfig struct {
	Name   string   `toml:"name"`
	Models []string `toml:"models"`
}

// Config is the hub's runtime configuration loaded from ~/.serf/hub.toml.
type Config struct {
	Addr               string                        `toml:"addr"`
	HubStateRoot       string                        `toml:"hub_state_root"`
	StateGlob          string                        `toml:"state_glob"`
	RunDir             string                        `toml:"run_dir"`
	PastIndexDB        string                        `toml:"past_index_db"`
	StatusPollInterval time.Duration                 `toml:"status_poll_interval"`
	PastIndexRebuild   time.Duration                 `toml:"past_index_rebuild_interval"`
	SpawnTimeout       time.Duration                 `toml:"spawn_timeout"`
	PastResultsPerPage int                           `toml:"past_results_per_page"`
	Providers          []ProviderConfig              `toml:"providers"`
	CodexSources       []appsource.CodexSourceConfig `toml:"codex_sources"`
	CodexLaunches      []CodexLaunchConfig           `toml:"codex_launches"`
}

// DefaultConfig returns a Config populated with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Addr:               "127.0.0.1:9180",
		StateGlob:          "",
		RunDir:             "",
		StatusPollInterval: 2 * time.Second,
		PastIndexRebuild:   60 * time.Second,
		SpawnTimeout:       30 * time.Second,
		PastResultsPerPage: 50,
	}
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
	base := os.Getenv("XDG_STATE_HOME")
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
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		cfg.HubStateRoot = filepath.Join(home, ".serf")
	}
	return cfg, nil
}
