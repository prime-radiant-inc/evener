package hub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/buildinfo"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubedge"
)

// requestSettingsOverview dials hub, initializes the connection, and issues
// evener/settings/overview, returning the typed response.
func requestSettingsOverview(t *testing.T, hubCfg hubcore.WebConfig) appwire.SettingsOverviewResponse {
	t.Helper()
	hub := newHubRPCTestServer(t, hubCfg)
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	var resp appwire.SettingsOverviewResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsOverview, appwire.EmptyParams{}, &resp); err != nil {
		t.Fatalf("evener/settings/overview: %v", err)
	}
	return resp
}

// writeMCPConfigLayer writes one MCP config file, creating its directory.
func writeMCPConfigLayer(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("creating MCP config directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing MCP config %s: %v", path, err)
	}
}

// TestHubRPCSettingsOverview_HubAndStorage covers the General/Hub/Storage
// sections' field bag against real hub state: a real listen address (the
// httptest server's own), a real run dir, a real bearer-token file (age
// derived from its real mtime), and a real seeded past index.
func TestHubRPCSettingsOverview_HubAndStorage(t *testing.T) {
	runDir := t.TempDir()
	stateDir := t.TempDir()
	hubStateRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hubStateRoot, hubedge.TokenFileName), []byte("tok"), 0o600); err != nil {
		t.Fatalf("writing auth-token fixture: %v", err)
	}

	pastIndexDir := t.TempDir()
	pastIndexPath := filepath.Join(pastIndexDir, "index.db")
	if err := os.WriteFile(pastIndexPath, []byte("0123456789"), 0o600); err != nil {
		t.Fatalf("writing past-index fixture: %v", err)
	}

	past := hubcore.NewPastIndex("")
	past.SeedForTest([]schema.SessionMeta{
		{ID: "02wMz5Txv1C3Hut0M8GCeB"},
		{ID: "02wMz5Txv2enqVTitaig6F"},
		{ID: "02wMz5Txv5aIxgf9yVdd0N"},
	})

	cfg := hubcore.WebConfig{
		RunDir:        runDir,
		StateDir:      stateDir,
		HubStateRoot:  hubStateRoot,
		PastIndexPath: pastIndexPath,
		PastPerPage:   25,
		Past:          past,
		SpawnTimeout:  37 * time.Second,
		// Hermetic: an empty MCPConfigPath would fall back to the ambient
		// machine's ~/.config/evener/mcp.json, which this test has no business
		// touching (this test isn't about MCP at all).
		MCPConfigPath:   filepath.Join(t.TempDir(), "no-mcp.json"),
		MCPDiscoveryDir: t.TempDir(),
	}

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPCInitialized(t, hub)
	var resp appwire.SettingsOverviewResponse
	if err := client.Request(context.Background(), appwire.MethodEvenerSettingsOverview, appwire.EmptyParams{}, &resp); err != nil {
		t.Fatalf("evener/settings/overview: %v", err)
	}

	if resp.Hub == nil {
		t.Fatal("Hub = nil, want populated")
	}
	if resp.Hub.Version != Version {
		t.Errorf("Hub.Version = %q, want %q", resp.Hub.Version, Version)
	}
	if resp.Hub.Commit != buildinfo.GitSHA {
		t.Errorf("Hub.Commit = %q, want %q", resp.Hub.Commit, buildinfo.GitSHA)
	}
	if resp.Hub.ListenAddr != hub.Listener.Addr().String() {
		t.Errorf("Hub.ListenAddr = %q, want %q", resp.Hub.ListenAddr, hub.Listener.Addr().String())
	}
	if resp.Hub.RunDir != runDir {
		t.Errorf("Hub.RunDir = %q, want %q", resp.Hub.RunDir, runDir)
	}
	if resp.Hub.SpawnTimeout != (37 * time.Second).String() {
		t.Errorf("Hub.SpawnTimeout = %q, want the configured %v", resp.Hub.SpawnTimeout, 37*time.Second)
	}
	if resp.Hub.BearerTokenAge == "" {
		t.Error("Hub.BearerTokenAge = \"\", want a populated age string (auth-token file exists)")
	}
	if resp.Hub.PastIndex == nil {
		t.Fatal("Hub.PastIndex = nil, want populated (cfg.Past is set)")
	}
	if resp.Hub.PastIndex.Path != pastIndexPath {
		t.Errorf("Hub.PastIndex.Path = %q, want %q", resp.Hub.PastIndex.Path, pastIndexPath)
	}
	if resp.Hub.PastIndex.Size == "" {
		t.Error("Hub.PastIndex.Size = \"\", want a populated human size (file exists)")
	}
	if resp.Hub.PastIndex.PerPage != 25 {
		t.Errorf("Hub.PastIndex.PerPage = %d, want 25", resp.Hub.PastIndex.PerPage)
	}
	if resp.Hub.PastIndex.Count != 3 {
		t.Errorf("Hub.PastIndex.Count = %d, want 3", resp.Hub.PastIndex.Count)
	}

	if resp.Storage == nil {
		t.Fatal("Storage = nil, want populated")
	}
	if resp.Storage.StateDir != stateDir {
		t.Errorf("Storage.StateDir = %q, want %q", resp.Storage.StateDir, stateDir)
	}
}

// TestHubRPCSettingsOverview_NilPastIndexWhenNotConfigured proves a hub with
// no past index configured (cfg.Past == nil, a legitimate minimal config)
// omits PastIndex rather than reporting a fabricated zero-valued one.
func TestHubRPCSettingsOverview_NilPastIndexWhenNotConfigured(t *testing.T) {
	// Hermetic: pin MCPConfigPath so this test never falls back to the
	// ambient machine's ~/.config/evener/mcp.json.
	resp := requestSettingsOverview(t, hubcore.WebConfig{
		MCPConfigPath:   filepath.Join(t.TempDir(), "no-mcp.json"),
		MCPDiscoveryDir: t.TempDir(),
	})
	if resp.Hub == nil {
		t.Fatal("Hub = nil, want populated")
	}
	if resp.Hub.PastIndex != nil {
		t.Errorf("Hub.PastIndex = %+v, want nil (cfg.Past is nil)", resp.Hub.PastIndex)
	}
}

// TestHubRPCSettingsOverview_Agents pins the built-in agent roster — the
// same three names, in the same order, that renderSettingsPartial has always
// reported (web_settings.go's builtinAgentNames), each with no on-disk file
// to open (EditPath empty).
func TestHubRPCSettingsOverview_Agents(t *testing.T) {
	// Hermetic: pin MCPConfigPath so this test never falls back to the
	// ambient machine's ~/.config/evener/mcp.json.
	resp := requestSettingsOverview(t, hubcore.WebConfig{
		Past:            hubcore.NewPastIndex(""),
		MCPConfigPath:   filepath.Join(t.TempDir(), "no-mcp.json"),
		MCPDiscoveryDir: t.TempDir(),
	})

	want := []appwire.SettingsAgentEntry{
		{Name: "default"},
		{Name: "explorer"},
		{Name: "subagent"},
	}
	if len(resp.Agents) != len(want) {
		t.Fatalf("Agents = %+v, want %+v", resp.Agents, want)
	}
	for i := range want {
		if resp.Agents[i] != want[i] {
			t.Errorf("Agents[%d] = %+v, want %+v", i, resp.Agents[i], want[i])
		}
	}
}

// TestHubRPCSettingsOverview_MCPDiscovered exercises the real mcpprobe
// integration (real command-present check, real refused-connection probe) —
// no mocking of what the method reports on, matching writeMCPFixture /
// refusedURL's existing use in web_settings_test.go.
func TestHubRPCSettingsOverview_MCPDiscovered(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skipf("`true` not found on PATH: %v", err)
	}
	dir := t.TempDir()
	path := writeMCPFixture(t, dir, "true", refusedURL(t))

	resp := requestSettingsOverview(t, hubcore.WebConfig{
		Past:            hubcore.NewPastIndex(""),
		MCPConfigPath:   path,
		MCPDiscoveryDir: t.TempDir(),
	})

	if resp.McpDiscovered == nil {
		t.Fatal("McpDiscovered = nil, want populated")
	}
	if resp.McpDiscovered.Error != "" {
		t.Errorf("McpDiscovered.Error = %q, want empty", resp.McpDiscovered.Error)
	}
	if len(resp.McpDiscovered.Servers) != 2 {
		t.Fatalf("McpDiscovered.Servers = %+v, want 2 rows", resp.McpDiscovered.Servers)
	}
	// Sorted by Name: "local-tool" < "remote-api".
	stdioRow, httpRow := resp.McpDiscovered.Servers[0], resp.McpDiscovered.Servers[1]
	if stdioRow.Name != "local-tool" || stdioRow.Transport != "stdio" || stdioRow.Status != "available" {
		t.Errorf("stdio row = %+v, want {local-tool stdio available}", stdioRow)
	}
	if stdioRow.Error != "" {
		t.Errorf("stdio row Error = %q, want empty on success", stdioRow.Error)
	}
	if httpRow.Name != "remote-api" || httpRow.Transport != "http" || httpRow.Status != "unreachable" {
		t.Errorf("http row = %+v, want {remote-api http unreachable}", httpRow)
	}
	if httpRow.Error == "" {
		t.Error("http row Error = \"\", want a populated connection failure")
	}
}

// TestHubRPCSettingsOverview_MCPDiscoveredEmptyWhenConfigMissing proves a
// missing MCP config file is the empty state, not an error — matching
// discoverMCPsForSettings's own contract.
func TestHubRPCSettingsOverview_MCPDiscoveredEmptyWhenConfigMissing(t *testing.T) {
	resp := requestSettingsOverview(t, hubcore.WebConfig{
		Past:            hubcore.NewPastIndex(""),
		MCPConfigPath:   filepath.Join(t.TempDir(), "does-not-exist.json"),
		MCPDiscoveryDir: t.TempDir(),
	})

	if resp.McpDiscovered == nil {
		t.Fatal("McpDiscovered = nil, want populated (empty state, not omitted)")
	}
	if resp.McpDiscovered.Error != "" {
		t.Errorf("McpDiscovered.Error = %q, want empty (a missing file is the empty state)", resp.McpDiscovered.Error)
	}
	if len(resp.McpDiscovered.Servers) != 0 {
		t.Errorf("McpDiscovered.Servers = %+v, want empty", resp.McpDiscovered.Servers)
	}
}

// TestHubRPCSettingsOverview_MCPDiscoveredMergedSet proves the overview probes
// the same layered set a session's initMCP builds: user-global, project, and
// launch-config MCP files and inline servers, merged with later layers
// shadowing earlier ones by server name. Each layer also contributes a name no
// other layer defines. The launch layer is driven through a real launch.toml,
// not by injecting MCPConfigPath, so the production source is covered.
func TestHubRPCSettingsOverview_MCPDiscoveredMergedSet(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skipf("`true` not found on PATH: %v", err)
	}
	refused := refusedURL(t)

	// The user-global layer lives at $XDG_CONFIG_HOME/evener/mcp.json.
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeMCPConfigLayer(t, filepath.Join(xdg, "evener", "mcp.json"), `{"mcpServers": {
		"shared": {"command": "true"},
		"project-shadow": {"type": "http", "url": "`+refused+`"},
		"user-only": {"command": "true"}
	}}`)

	// The project layer is .evener/mcp.json at the git root. It shadows the
	// user layer's project-shadow and defines a name of its own. Its version of
	// shared is unreachable, so a merged result that reads available proves the
	// session-local layer shadowed it.
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("creating project .git directory: %v", err)
	}
	writeMCPConfigLayer(t, filepath.Join(repo, ".evener", "mcp.json"), `{"mcpServers": {
		"shared": {"type": "http", "url": "`+refused+`"},
		"inline-shadow": {"type": "http", "url": "`+refused+`"},
		"project-only": {"command": "true"},
		"project-shadow": {"command": "true"}
	}}`)

	// The launch configuration supplies the files and inline servers a spawned
	// session receives on argv. Its config file shadows the project's shared,
	// and its inline entry shadows the project's inline-shadow.
	launchConfigFile := filepath.Join(t.TempDir(), "launch-mcp.json")
	writeMCPConfigLayer(t, launchConfigFile, `{"mcpServers": {
		"shared": {"command": "true"},
		"session-only": {"command": "true"}
	}}`)
	launchRoot := t.TempDir()
	writeMCPConfigLayer(t, filepath.Join(launchRoot, "launch.toml"), `mcp_configs = ["`+launchConfigFile+`"]

[[mcps]]
name = "inline-shadow"
command = "true"

[[mcps]]
name = "launch-inline-only"
command = "true"
`)

	resp := requestSettingsOverview(t, hubcore.WebConfig{
		Past:             hubcore.NewPastIndex(""),
		LaunchConfigRoot: launchRoot,
		MCPDiscoveryDir:  repo,
	})

	if resp.McpDiscovered == nil {
		t.Fatal("McpDiscovered = nil, want populated")
	}
	if resp.McpDiscovered.Error != "" {
		t.Errorf("McpDiscovered.Error = %q, want empty", resp.McpDiscovered.Error)
	}
	if len(resp.McpDiscovered.Servers) != 7 {
		t.Fatalf("McpDiscovered.Servers = %+v, want 7 merged rows", resp.McpDiscovered.Servers)
	}
	got := make(map[string]appwire.SettingsMCPServerEntry, len(resp.McpDiscovered.Servers))
	for _, server := range resp.McpDiscovered.Servers {
		got[server.Name] = server
	}
	want := []struct {
		name      string
		transport string
		status    string
	}{
		{name: "inline-shadow", transport: "stdio", status: "available"},
		{name: "launch-inline-only", transport: "stdio", status: "available"},
		{name: "project-only", transport: "stdio", status: "available"},
		{name: "project-shadow", transport: "stdio", status: "available"},
		{name: "session-only", transport: "stdio", status: "available"},
		{name: "shared", transport: "stdio", status: "available"},
		{name: "user-only", transport: "stdio", status: "available"},
	}
	for _, w := range want {
		server, ok := got[w.name]
		if !ok {
			t.Errorf("merged set is missing %q; got %+v", w.name, resp.McpDiscovered.Servers)
			continue
		}
		if server.Transport != w.transport || server.Status != w.status {
			t.Errorf("%s row = %+v, want {%s %s}", w.name, server, w.transport, w.status)
		}
	}
}

// TestHubRPCSettingsOverview_MCPDiscoveredMalformedGlobalKeepsServers proves a
// malformed user-global layer is reported as a diagnostic while the layers that
// did load still populate Servers. Without the diagnostic the list would shrink
// with no visible cause.
func TestHubRPCSettingsOverview_MCPDiscoveredMalformedGlobalKeepsServers(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skipf("`true` not found on PATH: %v", err)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	writeMCPConfigLayer(t, filepath.Join(xdg, "evener", "mcp.json"), `{"mcpServers":`)

	launchConfigFile := filepath.Join(t.TempDir(), "launch-mcp.json")
	writeMCPConfigLayer(t, launchConfigFile, `{"mcpServers": {
		"launch-only": {"command": "true"}
	}}`)
	launchRoot := t.TempDir()
	writeMCPConfigLayer(t, filepath.Join(launchRoot, "launch.toml"), `mcp_configs = ["`+launchConfigFile+`"]`)

	resp := requestSettingsOverview(t, hubcore.WebConfig{
		Past:             hubcore.NewPastIndex(""),
		LaunchConfigRoot: launchRoot,
		MCPDiscoveryDir:  t.TempDir(),
	})

	if resp.McpDiscovered == nil {
		t.Fatal("McpDiscovered = nil, want populated")
	}
	if !strings.Contains(resp.McpDiscovered.Error, "mcp.json") {
		t.Errorf("McpDiscovered.Error = %q, want the malformed global config named", resp.McpDiscovered.Error)
	}
	if len(resp.McpDiscovered.Servers) != 1 {
		t.Fatalf("McpDiscovered.Servers = %+v, want the successfully discovered launch-only row", resp.McpDiscovered.Servers)
	}
	server := resp.McpDiscovered.Servers[0]
	if server.Name != "launch-only" || server.Transport != "stdio" || server.Status != "available" {
		t.Errorf("server = %+v, want {launch-only stdio available}", server)
	}
}

// TestHubRPCSettingsOverview_MCPDiscoveredLaunchEnvExpansion proves a config
// file referenced by launch.toml can use a variable defined in that same
// launch configuration's env. Expansion must see the launch env as an overlay
// without mutating the hub process environment.
func TestHubRPCSettingsOverview_MCPDiscoveredLaunchEnvExpansion(t *testing.T) {
	if _, err := exec.LookPath("true"); err != nil {
		t.Skipf("`true` not found on PATH: %v", err)
	}
	const envName = "EVENER_TEST_MCP_BIN_4428"

	mcpConfigFile := filepath.Join(t.TempDir(), "env-mcp.json")
	writeMCPConfigLayer(t, mcpConfigFile, `{"mcpServers": {
		"env-tool": {"command": "${`+envName+`}"}
	}}`)
	launchRoot := t.TempDir()
	writeMCPConfigLayer(t, filepath.Join(launchRoot, "launch.toml"), `env = { `+envName+` = "true" }
mcp_configs = ["`+mcpConfigFile+`"]
`)

	resp := requestSettingsOverview(t, hubcore.WebConfig{
		Past:             hubcore.NewPastIndex(""),
		LaunchConfigRoot: launchRoot,
		MCPDiscoveryDir:  t.TempDir(),
	})

	if resp.McpDiscovered == nil {
		t.Fatal("McpDiscovered = nil, want populated")
	}
	if resp.McpDiscovered.Error != "" {
		t.Errorf("McpDiscovered.Error = %q, want empty", resp.McpDiscovered.Error)
	}
	if len(resp.McpDiscovered.Servers) != 1 {
		t.Fatalf("McpDiscovered.Servers = %+v, want the env-expanded server", resp.McpDiscovered.Servers)
	}
	server := resp.McpDiscovered.Servers[0]
	if server.Name != "env-tool" || server.Transport != "stdio" || server.Status != "available" {
		t.Errorf("server = %+v, want {env-tool stdio available}", server)
	}
	if _, ok := os.LookupEnv(envName); ok {
		t.Errorf("discovery leaked launch env %s into the hub process environment", envName)
	}
}

// TestHubRPCSettingsOverview_MCPDiscoveredErrorOnMalformedSessionConfig pins
// the error contract: an explicitly configured session-local file that fails to
// parse surfaces as Error, the same failure a session's initMCP returns.
func TestHubRPCSettingsOverview_MCPDiscoveredErrorOnMalformedSessionConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken-mcp.json")
	writeMCPConfigLayer(t, path, `{"mcpServers":`)

	resp := requestSettingsOverview(t, hubcore.WebConfig{
		Past:            hubcore.NewPastIndex(""),
		MCPConfigPath:   path,
		MCPDiscoveryDir: t.TempDir(),
	})

	if resp.McpDiscovered == nil {
		t.Fatal("McpDiscovered = nil, want populated")
	}
	if resp.McpDiscovered.Error == "" {
		t.Error("McpDiscovered.Error = \"\", want a parse failure for the explicit session config")
	}
	if len(resp.McpDiscovered.Servers) != 0 {
		t.Errorf("McpDiscovered.Servers = %+v, want empty on fatal discovery error", resp.McpDiscovered.Servers)
	}
}

func TestSettingsHubOverviewReportsBuildChannel(t *testing.T) {
	previous := buildinfo.Channel
	buildinfo.Channel = "snapshot"
	t.Cleanup(func() { buildinfo.Channel = previous })

	got := settingsHubOverview(hubcore.WebConfig{})
	if got.BuildChannel != "snapshot" {
		t.Fatalf("BuildChannel = %q, want snapshot", got.BuildChannel)
	}
}
