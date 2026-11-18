package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/plugin"
)

// TestPluginMCPEvidenceChangesWithThePluginFiles pins that a plugin's MCP
// configuration contributes freshness evidence.
//
// Without it, an installed plugin that adds a matching MCP server AFTER the
// parent starts leaves the matching CLI namespace classified as unreachable: the
// freshness loop has no file to check, so nothing notices the plugin changed.
//
// The child reloads the plugin directory at spawn. So the parent must record
// what each plugin's MCP configuration said at startup, and treat a difference —
// or an unreadable layer — as unknown reachability, which keeps the name
// eligible.
func TestPluginMCPEvidenceChangesWithThePluginFiles(t *testing.T) {
	t.Parallel()
	dir := realTempDirForTest(t)
	manifestDir := filepath.Join(dir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	manifestPath := filepath.Join(manifestDir, "plugin.json")
	manifest := `{"name":"team-plugin","version":"1.0.0"}`
	writeInfraFile(t, manifestPath, manifest, 0o644)
	mcpPath := filepath.Join(dir, ".mcp.json")
	// At startup the plugin declares a server in a namespace that does NOT match
	// the CLI entry, which is what makes the CLI name look CLI-only.
	writeInfraFile(t, mcpPath, `{"mcpServers":{"other":{"command":"/bin/other"}}}`, 0o644)

	instance, err := plugin.Load(dir)
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	evidence := instance.MCPEvidence
	// Two layers always: the manifest (a child re-parses it, and one that declares
	// no inline servers today can declare one later) and the .mcp.json.
	if len(evidence) != 2 {
		t.Fatalf("want the manifest and .mcp.json layers, got %d: %v", len(evidence), evidence)
	}
	if evidence[0].Path != instance.ManifestPath || evidence[0].Digest == "" {
		t.Errorf("manifest layer = %+v, want the parsed manifest %q with a digest", evidence[0], instance.ManifestPath)
	}
	if evidence[1].Path != mcpPath {
		t.Errorf("recorded layer path = %q, want %q", evidence[1].Path, mcpPath)
	}
	if evidence[1].Digest == "" {
		t.Errorf("a readable .mcp.json must be recorded with a digest, got %+v", evidence[1])
	}

	const (
		cliTool  = "svc__echo"
		ordinary = "read_file"
	)
	ceiling := []string{cliTool, ordinary}
	newClassified := func() *Session {
		// The plugin evidence is attached by recordCLIMCPServers, so the fixture
		// goes through it rather than assembling the value itself: a test that
		// sets Plugins by hand would still pass with the production wiring gone.
		s := &Session{
			env:               execenv.NewLocalExecutionEnvironment(dir),
			pluginMCPEvidence: append([]mcpconfig.LayerEvidence(nil), evidence...),
		}
		s.setManagerOnlyTools(nil)
		_, _, discovered, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(execenv.NewLocalExecutionEnvironment(dir), nil, nil)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		s.recordCLIMCPServers(nil, discovered)
		s.setCLIMCPToolNames(map[string]bool{cliTool: true})
		s.refreshCLIEvidenceFreshness()
		return s
	}
	ceilingOf := func(s *Session) []string {
		s.refreshCLIEvidenceFreshness()
		return s.removeChildUnreachableTools(ceiling, "")
	}

	t.Run("unchanged plugin config still excludes", func(t *testing.T) {
		got := ceilingOf(newClassified())
		if slices.Contains(got, cliTool) {
			t.Errorf("the plugin files still say what the classification read, so %q must stay excluded: %v", cliTool, got)
		}
		if !slices.Contains(got, ordinary) {
			t.Errorf("an ordinary tool was dropped: %v", got)
		}
	})

	// The finding itself: the plugin gains a server whose namespace matches the
	// CLI entry after the parent started. The child rereads this file and
	// discovers a server it can register, so the name is reachable now.
	t.Run("a plugin that gains a matching server leaves the name eligible", func(t *testing.T) {
		s := newClassified()
		writeInfraFile(t, mcpPath, `{"mcpServers":{"other":{"command":"/bin/other"},"svc":{"command":"/bin/svc"}}}`, 0o644)
		if !slices.Contains(ceilingOf(s), cliTool) {
			t.Errorf("the plugin gained a server matching %q, so reachability is unknown and the name must stay eligible", cliTool)
		}
	})

	// An unreadable layer is unknown, not unchanged. A directory where the file
	// should be is unreadable on every platform and does not depend on a unix
	// build tag: the point is that the read fails for a reason other than
	// absence.
	t.Run("an unreadable plugin layer leaves the name eligible", func(t *testing.T) {
		dir := realTempDirForTest(t)
		manifestDir := filepath.Join(dir, ".claude-plugin")
		if err := os.MkdirAll(manifestDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeInfraFile(t, filepath.Join(manifestDir, "plugin.json"), `{"name":"fifo-plugin"}`, 0o644)
		if err := os.MkdirAll(filepath.Join(dir, ".mcp.json"), 0o755); err != nil {
			t.Fatalf("mkdir .mcp.json: %v", err)
		}
		instance, err := plugin.Load(dir)
		if err != nil {
			t.Fatalf("load plugin: %v", err)
		}
		recorded := instance.MCPEvidence
		if len(recorded) != 2 {
			t.Fatalf("want the manifest and .mcp.json layers, got %d: %v", len(recorded), recorded)
		}
		if recorded[1].Path != "" {
			t.Errorf("an unreadable .mcp.json must record NO path (unknown), got %+v", recorded[1])
		}

		s := &Session{env: execenv.NewLocalExecutionEnvironment(dir)}
		s.setManagerOnlyTools(nil)
		_, _, discovered, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(execenv.NewLocalExecutionEnvironment(dir), nil, nil)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		s.pluginMCPEvidence = recorded
		s.recordCLIMCPServers(nil, discovered)
		s.setCLIMCPToolNames(map[string]bool{cliTool: true})
		if !slices.Contains(ceilingOf(s), cliTool) {
			t.Errorf("an unreadable plugin layer is unknown reachability, so %q must stay eligible", cliTool)
		}
	})

	// The other direction on the same fixture: a plugin that declares an inline
	// manifest layer records it, so an edit to the manifest invalidates the
	// evidence rather than passing unnoticed.
	t.Run("an inline manifest layer is recorded from the manifest file", func(t *testing.T) {
		dir := realTempDirForTest(t)
		manifestDir := filepath.Join(dir, ".claude-plugin")
		if err := os.MkdirAll(manifestDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		manifestPath := filepath.Join(manifestDir, "plugin.json")
		writeInfraFile(t, manifestPath, `{"name":"inline-plugin","mcpServers":{"svc":{"command":"/bin/svc"}}}`, 0o644)
		instance, err := plugin.Load(dir)
		if err != nil {
			t.Fatalf("load plugin: %v", err)
		}
		recorded := instance.MCPEvidence
		// The absent .mcp.json still records its absence, and the inline layer is
		// the manifest file.
		if len(recorded) != 2 {
			t.Fatalf("want the absent .mcp.json and the inline manifest layer, got %d: %v", len(recorded), recorded)
		}
		if recorded[0].Path != manifestPath || recorded[0].Digest == "" {
			t.Errorf("manifest layer = %+v, want the manifest path %q with a digest even though it declares no inline servers", recorded[0], manifestPath)
		}
		if recorded[1].Path != filepath.Join(dir, ".mcp.json") || recorded[1].Digest != "" {
			t.Errorf("an absent .mcp.json must record its path with an empty digest, got %+v", recorded[1])
		}
	})

	// A Codex-flavored load read the preferred Claude manifest's absence to
	// choose the Codex one, and that absence is evidence: the two manifests
	// declare different inline mcpServers, so a Claude manifest appearing
	// later changes which servers a child parses at spawn. Without the
	// recorded absence the freshness loop has no file to check, the
	// classification survives, and the ceiling strips a tool the child now
	// registers.
	t.Run("a codex load records the claude manifest it found absent", func(t *testing.T) {
		dir := realTempDirForTest(t)
		codexDir := filepath.Join(dir, ".codex-plugin")
		if err := os.MkdirAll(codexDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeInfraFile(t, filepath.Join(codexDir, "plugin.json"), `{"name":"codex-plugin"}`, 0o644)
		instance, err := plugin.Load(dir)
		if err != nil {
			t.Fatalf("load plugin: %v", err)
		}
		if instance.ManifestFlavor != "codex" {
			t.Fatalf("manifest flavor = %q, want codex", instance.ManifestFlavor)
		}
		preferred := filepath.Join(dir, ".claude-plugin", "plugin.json")
		var absent *mcpconfig.LayerEvidence
		for i := range instance.MCPEvidence {
			if instance.MCPEvidence[i].Path == preferred {
				absent = &instance.MCPEvidence[i]
			}
		}
		if absent == nil {
			t.Fatalf("a codex load must record the absent preferred manifest %q as evidence, got %v", preferred, instance.MCPEvidence)
		}
		if absent.Digest != "" {
			t.Errorf("a confirmed-absent manifest records no digest, got %+v", *absent)
		}

		s := &Session{env: execenv.NewLocalExecutionEnvironment(dir)}
		s.setManagerOnlyTools(nil)
		_, _, discovered, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(execenv.NewLocalExecutionEnvironment(dir), nil, nil)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		s.pluginMCPEvidence = instance.MCPEvidence
		s.recordCLIMCPServers(nil, discovered)
		s.setCLIMCPToolNames(map[string]bool{cliTool: true})
		if slices.Contains(ceilingOf(s), cliTool) {
			t.Errorf("the preferred manifest is still absent as recorded, so %q must stay excluded", cliTool)
		}
		// Now the upgrade lands: a Claude manifest with a matching inline
		// server appears and the Codex manifest is untouched. The child
		// selects the Claude manifest, so the recorded absence no longer
		// holds and the name must become eligible.
		if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeInfraFile(t, preferred, `{"name":"codex-plugin","mcpServers":{"svc":{"command":"/bin/svc"}}}`, 0o644)
		if !slices.Contains(ceilingOf(s), cliTool) {
			t.Errorf("the preferred Claude manifest appeared with a matching server, so %q must be eligible", cliTool)
		}
	})
}
