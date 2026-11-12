package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/mcpprobe"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
	"primeradiant.com/evener/cmd/evener-hub/internal/launchconfig"
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
// launch configuration's MCP files and inline servers (the argv sources a
// spawned session receives), with later layers shadowing earlier ones by
// server name. An explicitly supplied cfg.MCPConfigPath, when it exists, is
// appended as the highest-precedence file.
//
// A missing file is the empty state, not an error. A malformed explicit
// session-local file surfaces as Error, as it does for a session (Discover
// treats CLI-supplied files as fatal). A malformed user-global or project
// layer is skipped by Discover with a warning, exactly as a session skips it;
// those warnings stay in Error alongside the successfully discovered Servers
// so a shrinking server list has a visible cause. Each probe
// (agent/mcpprobe.Probe) runs under its own bounded timeout in parallel with
// the others, so this never blocks the response beyond that per-probe bound
// regardless of how many servers are configured.
func settingsMCPOverview(ctx context.Context, cfg hubcore.WebConfig) *appwire.SettingsMCPOverview {
	dir := mcpOverviewDiscoveryDir(cfg)
	extraFiles, inlineSpecs, launchEnv, diagnostics := mcpOverviewLaunchSources(cfg, dir)
	if cfg.MCPConfigPath != "" {
		if _, err := os.Stat(cfg.MCPConfigPath); err == nil {
			extraFiles = append(extraFiles, cfg.MCPConfigPath)
		}
	}
	configs, warnings, err := mcpconfig.DiscoverWithEnvLookup(mcpOverviewDiscoveryEnv(dir), extraFiles, inlineSpecs, mcpOverviewEnvLookup(launchEnv))
	if err != nil {
		return &appwire.SettingsMCPOverview{Error: err.Error()}
	}
	diagnostics = append(diagnostics, warnings...)
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
	return &appwire.SettingsMCPOverview{Servers: servers, Error: strings.Join(diagnostics, "\n")}
}

// mcpOverviewDiscoveryDir is the working directory whose project and launch
// layers the overview resolves. A zero WebConfig uses the hub process working
// directory, the only directory a hub-global settings read has.
func mcpOverviewDiscoveryDir(cfg hubcore.WebConfig) string {
	dir := cfg.MCPDiscoveryDir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	return dir
}

// mcpOverviewDiscoveryEnv is the environment whose working directory supplies
// Discover's project layer. A nil environment suppresses that layer, which is
// Discover's own contract.
func mcpOverviewDiscoveryEnv(dir string) execenv.ExecutionEnvironment {
	if dir == "" {
		return nil
	}
	return execenv.NewLocalExecutionEnvironment(dir)
}

// mcpOverviewLaunchSources resolves the launch configuration for dir and
// returns the MCP file list, inline servers, and effective env a session
// spawned there receives (launchconfig.ToArgs and ToEnv render the same
// values). A resolution failure is a diagnostic, not a failed overview: the
// user-global and project layers are still discovered and reported.
func mcpOverviewLaunchSources(cfg hubcore.WebConfig, dir string) (extraFiles, inlineSpecs []string, launchEnv map[string]string, diagnostics []string) {
	root := hubLaunchConfigRoot(cfg)
	if root == "" {
		return nil, nil, nil, nil
	}

	resolved, err := mcpOverviewResolveLaunch(root, dir)
	if err != nil {
		return nil, nil, nil, []string{fmt.Sprintf("launch config: %v", err)}
	}

	extraFiles = append(extraFiles, resolved.Effective.MCPConfigs...)
	for _, server := range resolved.Effective.MCPs {
		inlineSpecs = append(inlineSpecs, launchconfig.MCPSpecArg(server))
	}
	return extraFiles, inlineSpecs, resolved.Effective.Env, nil
}

// mcpOverviewResolveLaunch resolves every launch layer for dir. Without a
// usable directory only the user layers exist, which ResolveUserOnly answers.
func mcpOverviewResolveLaunch(root, dir string) (launchconfig.Resolved, error) {
	if dir != "" {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return launchconfig.Resolve(root, dir, launchconfig.Layer{})
		}
	}
	return launchconfig.ResolveUserOnly(root, launchconfig.Layer{})
}

// mcpOverviewEnvLookup resolves MCP config variables through the launch
// layer's effective env first, then the hub process environment. The overlay
// is a closure, so discovery never mutates the hub's environment.
func mcpOverviewEnvLookup(overlay map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if value, ok := overlay[name]; ok {
			return value, true
		}
		return os.LookupEnv(name)
	}
}
