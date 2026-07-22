package main

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"primeradiant.com/serf/agent/mcpconfig"
	"primeradiant.com/serf/agent/mcpprobe"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubedge"
)

// hubSettingsOverview answers serf/settings/overview: the field bag behind
// Settings → General/Hub/Storage/Agents/Codex launch/MCP servers (probed
// half). See appwire.SettingsOverviewResponse's doc comment for the exact
// web_settings.go field citations this mirrors — it is the JSON-RPC
// replacement for that file's settingsData construction, for exactly those
// six sections.
func hubSettingsOverview(ctx context.Context, cfg hubcore.WebConfig) (appwire.SettingsOverviewResponse, error) {
	return appwire.SettingsOverviewResponse{
		Hub:           settingsHubOverview(cfg),
		Storage:       &appwire.SettingsStorageOverview{StateDir: cfg.StateDir},
		Agents:        settingsAgentRoster(),
		CodexLaunches: settingsCodexLaunchEntries(cfg.CodexLaunches),
		McpDiscovered: settingsMCPOverview(ctx, cfg),
	}, nil
}

// settingsHubOverview builds the hub/runtime section. Mirrors web_settings.go
// renderSettingsPartial's settingsData.{HubVersion,HubCommit,HubAddr,RunDir,
// SpawnTimeout,BearerTokenAge} plus the past-index fields.
func settingsHubOverview(cfg hubcore.WebConfig) *appwire.SettingsHubOverview {
	bearerTokenAge := ""
	if cfg.HubStateRoot != "" {
		bearerTokenAge = fileAgeHuman(filepath.Join(cfg.HubStateRoot, hubedge.TokenFileName))
	}

	var pastIndex *appwire.SettingsPastIndexOverview
	if cfg.Past != nil {
		pastIndex = &appwire.SettingsPastIndexOverview{
			Path:    tildeHome(cfg.PastIndexPath),
			Size:    fileSizeHuman(cfg.PastIndexPath),
			PerPage: cfg.PastPerPage,
			Count:   len(cfg.Past.AllMetas()),
		}
	}

	return &appwire.SettingsHubOverview{
		Version:        Version,
		Commit:         buildinfo.GitSHA,
		ListenAddr:     cfg.HubAddr,
		RunDir:         cfg.RunDir,
		SpawnTimeout:   settingsSpawnTimeoutDisplay,
		BearerTokenAge: bearerTokenAge,
		PastIndex:      pastIndex,
	}
}

// settingsAgentRoster returns the built-in agent roster for Settings →
// Agents. Mirrors renderSettingsPartial's agents construction over
// builtinAgentNames — EditPath is always empty (see that var's doc comment).
func settingsAgentRoster() []appwire.SettingsAgentEntry {
	agents := make([]appwire.SettingsAgentEntry, 0, len(builtinAgentNames))
	for _, name := range builtinAgentNames {
		agents = append(agents, appwire.SettingsAgentEntry{Name: name})
	}
	return agents
}

// settingsCodexLaunchEntries maps configured codex launches to their wire
// display form. See appwire.SettingsCodexLaunchEntry's doc comment for why
// Args/BearerToken/BearerTokenFile are excluded.
func settingsCodexLaunchEntries(configs []codexlaunch.CodexLaunchConfig) []appwire.SettingsCodexLaunchEntry {
	if len(configs) == 0 {
		return nil
	}
	out := make([]appwire.SettingsCodexLaunchEntry, 0, len(configs))
	for _, c := range configs {
		var envKeys []string
		if len(c.Env) > 0 {
			envKeys = make([]string, 0, len(c.Env))
			for k := range c.Env {
				envKeys = append(envKeys, k)
			}
			sort.Strings(envKeys)
		}
		out = append(out, appwire.SettingsCodexLaunchEntry{
			ID:            c.ID,
			Binary:        c.Binary,
			WorkingDir:    c.WorkingDir,
			Listen:        c.Listen,
			TimeoutMillis: c.Timeout.Milliseconds(),
			EnvKeys:       envKeys,
		})
	}
	return out
}

// settingsMCPOverview probes the configured MCP servers, mirroring
// web_settings.go's discoverMCPsForSettings: a missing config file is the
// empty state, not an error; a parse failure surfaces as Error; each probe
// (agent/mcpprobe.Probe) runs under its own bounded timeout in parallel with
// the others, so this never blocks the response beyond that per-probe bound
// regardless of how many servers are configured.
func settingsMCPOverview(ctx context.Context, cfg hubcore.WebConfig) *appwire.SettingsMCPOverview {
	path := cfg.MCPConfigPath
	if path == "" {
		path = defaultMCPConfigPath()
	}
	if path == "" {
		return &appwire.SettingsMCPOverview{}
	}
	if _, err := os.Stat(path); err != nil {
		return &appwire.SettingsMCPOverview{} //nolint:nilerr // a missing MCP config file is the empty state, not an error
	}
	configs, err := mcpconfig.LoadFile(path)
	if err != nil {
		return &appwire.SettingsMCPOverview{Error: err.Error()}
	}
	results := mcpprobe.Probe(ctx, configs)
	servers := make([]appwire.SettingsMCPServerEntry, 0, len(configs))
	for i, c := range configs {
		servers = append(servers, appwire.SettingsMCPServerEntry{
			Name:      c.Name,
			Transport: results[i].Transport,
			Status:    results[i].Status,
			Error:     results[i].Error,
		})
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	return &appwire.SettingsMCPOverview{Servers: servers}
}
