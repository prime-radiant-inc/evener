package hub

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/mcpprobe"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
)

// hubSettingsOverview answers evener/settings/overview: the field bag behind
// Settings → General/Hub/Storage/Agents/MCP servers (probed half). See
// appwire.SettingsOverviewResponse's doc comment for the exact web_settings.go
// field citations this mirrors — it is the JSON-RPC replacement for that file's
// settingsData construction, for exactly those five sections.
func hubSettingsOverview(ctx context.Context, cfg hubcore.WebConfig) (appwire.SettingsOverviewResponse, error) {
	return appwire.SettingsOverviewResponse{
		Hub:           settingsHubOverview(cfg),
		Storage:       &appwire.SettingsStorageOverview{StateDir: cfg.StateDir},
		Agents:        settingsAgentRoster(),
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
		BuildChannel:   buildinfo.BuildChannel(),
		ListenAddr:     cfg.HubAddr,
		RunDir:         cfg.RunDir,
		SpawnTimeout:   cfg.SpawnTimeout.String(),
		BearerTokenAge: bearerTokenAge,
		PastIndex:      pastIndex,
		// Milliseconds, zero included: "disabled" and "not yet reported" must
		// never decode as the same value.
		DaemonIdleTimeoutMillis: appwire.DurationMillis(cfg.DaemonIdleTimeout),
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

// settingsMCPOverview probes the merged MCP configuration a session would use,
// through the same mcpconfig.Discover path as Session.initMCP: the user-global
// config, the project config at the discovery directory's git root, and the
// explicit session-local file (cfg.MCPConfigPath), with later layers shadowing
// earlier ones by server name.
//
// A missing file is the empty state, not an error. A malformed explicit
// session-local file surfaces as Error, as it does for a session (Discover
// treats CLI-supplied files as fatal). A malformed user-global or project
// layer is skipped by Discover with a warning, exactly as a session skips it,
// so the probed set stays the set a session connects to. Each probe
// (agent/mcpprobe.Probe) runs under its own bounded timeout in parallel with
// the others, so this never blocks the response beyond that per-probe bound
// regardless of how many servers are configured.
func settingsMCPOverview(ctx context.Context, cfg hubcore.WebConfig) *appwire.SettingsMCPOverview {
	configs, _, err := mcpconfig.Discover(mcpOverviewDiscoveryEnv(cfg), mcpOverviewExtraFiles(cfg), nil)
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

// mcpOverviewDiscoveryEnv is the environment whose working directory supplies
// Discover's project layer. A nil environment suppresses that layer, which is
// Discover's own contract.
func mcpOverviewDiscoveryEnv(cfg hubcore.WebConfig) execenv.ExecutionEnvironment {
	dir := cfg.MCPDiscoveryDir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	if dir == "" {
		return nil
	}
	return execenv.NewLocalExecutionEnvironment(dir)
}

// mcpOverviewExtraFiles is Discover's CLI-file layer: the session-local MCP
// config the hub was given. A missing explicit file is the empty state, not an
// error, matching the single-file probe this replaced; the user-global layer is
// not listed here because Discover loads it itself.
func mcpOverviewExtraFiles(cfg hubcore.WebConfig) []string {
	if cfg.MCPConfigPath == "" {
		return nil
	}
	if _, err := os.Stat(cfg.MCPConfigPath); err != nil {
		return nil
	}
	return []string{cfg.MCPConfigPath}
}
