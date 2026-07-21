package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/buildinfo"
	"primeradiant.com/serf/cmd/serf-hub/internal/codexlaunch"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubedge"
)

// requestSettingsOverview dials hub, initializes the connection, and issues
// serf/settings/overview, returning both the typed response and the raw
// result bytes (the latter so security-sensitive assertions — e.g. a secret
// never appearing on the wire — can inspect the actual JSON rather than only
// what the typed struct happens to decode into).
func requestSettingsOverview(t *testing.T, hubCfg hubcore.WebConfig) (appwire.SettingsOverviewResponse, json.RawMessage) {
	t.Helper()
	hub := newHubRPCTestServer(t, hubCfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()

	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var raw json.RawMessage
	if err := client.Request(context.Background(), appwire.MethodSerfSettingsOverview, appwire.EmptyParams{}, &raw); err != nil {
		t.Fatalf("serf/settings/overview: %v", err)
	}
	var resp appwire.SettingsOverviewResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decoding serf/settings/overview response: %v", err)
	}
	return resp, raw
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
	}

	hub := newHubRPCTestServer(t, cfg)
	defer hub.Close()
	client := dialHubRPC(t, hub)
	defer client.Close()
	if _, err := client.Initialize(context.Background(), appwire.InitializeParams{}); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var resp appwire.SettingsOverviewResponse
	if err := client.Request(context.Background(), appwire.MethodSerfSettingsOverview, appwire.EmptyParams{}, &resp); err != nil {
		t.Fatalf("serf/settings/overview: %v", err)
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
	if resp.Hub.SpawnTimeout != "30s" {
		t.Errorf("Hub.SpawnTimeout = %q, want 30s", resp.Hub.SpawnTimeout)
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
	resp, _ := requestSettingsOverview(t, hubcore.WebConfig{})
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
	resp, _ := requestSettingsOverview(t, hubcore.WebConfig{Past: hubcore.NewPastIndex("")})

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

// TestHubRPCSettingsOverview_CodexLaunches covers a configured
// [[codex_launches]] entry, and — the security-critical half — proves the
// bearer token/file never reach the wire even as raw bytes, matching the
// credential never-echo invariant the legacy template itself upholds (it
// never renders BearerToken/BearerTokenFile either).
func TestHubRPCSettingsOverview_CodexLaunches(t *testing.T) {
	const secretToken = "sekrit-codex-bearer-token-xyz"
	cfg := hubcore.WebConfig{
		Past: hubcore.NewPastIndex(""),
		CodexLaunches: []codexlaunch.CodexLaunchConfig{{
			ID:              "codex-managed",
			Binary:          "/usr/local/bin/codex",
			WorkingDir:      "/repo",
			Listen:          "ws://127.0.0.1:9190",
			Timeout:         45 * time.Second,
			Args:            []string{"--flag"},
			Env:             map[string]string{"ZETA": "1", "ALPHA": "2"},
			BearerToken:     secretToken,
			BearerTokenFile: "/secrets/codex-token",
		}},
	}
	resp, raw := requestSettingsOverview(t, cfg)

	if len(resp.CodexLaunches) != 1 {
		t.Fatalf("CodexLaunches = %+v, want 1 entry", resp.CodexLaunches)
	}
	got := resp.CodexLaunches[0]
	want := appwire.SettingsCodexLaunchEntry{
		ID:            "codex-managed",
		Binary:        "/usr/local/bin/codex",
		WorkingDir:    "/repo",
		Listen:        "ws://127.0.0.1:9190",
		TimeoutMillis: 45000,
		// EnvKeys must come back sorted regardless of the source map's
		// iteration order.
		EnvKeys: []string{"ALPHA", "ZETA"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CodexLaunches[0] = %+v, want %+v", got, want)
	}

	if strings.Contains(string(raw), secretToken) {
		t.Fatal("serf/settings/overview response leaks the codex bearer token onto the wire")
	}
	if strings.Contains(string(raw), "/secrets/codex-token") {
		t.Fatal("serf/settings/overview response leaks the codex bearer-token-file path onto the wire")
	}
	if strings.Contains(string(raw), "--flag") {
		t.Error(`serf/settings/overview response includes codex launch Args, which the legacy template never rendered`)
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

	resp, _ := requestSettingsOverview(t, hubcore.WebConfig{
		Past:          hubcore.NewPastIndex(""),
		MCPConfigPath: path,
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
	resp, _ := requestSettingsOverview(t, hubcore.WebConfig{
		Past:          hubcore.NewPastIndex(""),
		MCPConfigPath: filepath.Join(t.TempDir(), "does-not-exist.json"),
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
