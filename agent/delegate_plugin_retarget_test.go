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

// TestRetargetedPluginDirectoryInvalidatesTheEvidence is the medium about a
// plugin directory that is a symlink.
//
// Freshness hashes the RESOLVED directory, which answers "did those files
// change". It does not answer "would a child read these files", and a child
// resolves the configured directory again at spawn. An upgrade retargets the
// symlink and leaves the old version on disk: every recorded digest under the
// old target still matches, freshness reports the evidence current, a child
// reads the NEW version and registers a server the upgrade added, and the
// ceiling goes on excluding that server's tools.
//
// Both directions matter. Retargeted to a directory exposing a matching server,
// the name must become eligible. Pointed back at the original, it must be
// excluded again — otherwise the fix would simply disable the exclusion.
func TestRetargetedPluginDirectoryInvalidatesTheEvidence(t *testing.T) {
	t.Parallel()
	root := realTempDirForTest(t)

	oldDir := filepath.Join(root, "plugin-v1")
	newDir := filepath.Join(root, "plugin-v2")
	for _, dir := range []string{oldDir, newDir} {
		if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeInfraFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"team-plugin"}`, 0o644)
	}
	// v1 exposes nothing matching the CLI namespace; that is what made the CLI
	// entry look CLI-only at startup.
	writeInfraFile(t, filepath.Join(oldDir, ".mcp.json"), `{"mcpServers":{"other":{"command":"/bin/other"}}}`, 0o644)
	// v2 adds the matching server.
	writeInfraFile(t, filepath.Join(newDir, ".mcp.json"), `{"mcpServers":{"other":{"command":"/bin/other"},"svc":{"command":"/bin/svc"}}}`, 0o644)

	// The CONFIGURED directory is a symlink the operator points at v1.
	configured := filepath.Join(root, "plugin")
	if err := os.Symlink(oldDir, configured); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	instance, err := plugin.Load(configured)
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	evidence := instance.MCPEvidence
	var carried bool
	for _, layer := range evidence {
		if layer.ConfiguredPath == configured {
			carried = true
		}
	}
	if !carried {
		t.Fatalf("no recorded layer carries the configured directory %q, so a retargeted symlink could never be noticed: %v", configured, evidence)
	}

	const (
		cliTool = "svc__echo"
		dir     = ""
	)
	classified := func() *Session {
		s := &Session{env: execenv.NewLocalExecutionEnvironment(root)}
		s.setManagerOnlyTools(nil)
		s.pluginMCPEvidence = evidence
		_, _, discovered, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(execenv.NewLocalExecutionEnvironment(root), nil, nil)
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
		return s.removeChildUnreachableTools([]string{cliTool, "read_file"}, dir)
	}

	// While the symlink still points at v1, the classification holds.
	if got := ceilingOf(classified()); slices.Contains(got, cliTool) {
		t.Fatalf("the unchanged plugin directory no longer excludes %q, so the exclusion is not applied at all here: %v", cliTool, got)
	}

	// The upgrade lands: the symlink is retargeted to v2, which exposes a matching
	// server. The old target is untouched, so every recorded digest still matches
	// under it.
	if err := os.Remove(configured); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	if err := os.Symlink(newDir, configured); err != nil {
		t.Fatalf("retarget symlink: %v", err)
	}
	if got := ceilingOf(classified()); !slices.Contains(got, cliTool) {
		t.Errorf("the configured plugin directory now resolves to a version exposing a server matching %q, so the child can register it and the name must be eligible; a digest under the old target still matched: %v", cliTool, got)
	}

	// The other direction: pointed back at the original, the exclusion applies
	// again. A fix that merely stopped excluding would pass the assertion above.
	if err := os.Remove(configured); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	if err := os.Symlink(oldDir, configured); err != nil {
		t.Fatalf("restore symlink: %v", err)
	}
	if got := ceilingOf(classified()); slices.Contains(got, cliTool) {
		t.Errorf("the configured directory resolves to the original version again, so the classification describes it and %q must be excluded: %v", cliTool, got)
	}
}

// TestPluginRetargetedToAnAncestorInvalidatesTheEvidence is the same finding's
// harder case, and the one a CONTAINMENT check cannot see.
//
// Point the configured symlink at a version subdirectory, then retarget it at
// that subdirectory's PARENT. Every recorded file still sits under the new
// target, so its digest still matches and a check that asks "is the recorded file
// inside what the configured path resolves to" answers yes. But a child reads the
// parent's manifest and .mcp.json, not the version's, and the parent can declare
// a server the version does not.
//
// Equality against the directory recorded at load is what notices the move; only
// an exact comparison can, because containment is true in both states.
func TestPluginRetargetedToAnAncestorInvalidatesTheEvidence(t *testing.T) {
	t.Parallel()
	root := realTempDirForTest(t)
	parent := filepath.Join(root, "plugins")
	versioned := filepath.Join(parent, "team-v1")
	if err := os.MkdirAll(filepath.Join(versioned, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeInfraFile(t, filepath.Join(versioned, ".claude-plugin", "plugin.json"), `{"name":"team-plugin"}`, 0o644)
	// The versioned directory exposes nothing matching the CLI namespace.
	writeInfraFile(t, filepath.Join(versioned, ".mcp.json"), `{"mcpServers":{"other":{"command":"/bin/other"}}}`, 0o644)
	// Its PARENT declares the matching server. The child reads this after the
	// retarget, and the version's files are still underneath it.
	if err := os.MkdirAll(filepath.Join(parent, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeInfraFile(t, filepath.Join(parent, ".claude-plugin", "plugin.json"), `{"name":"team-plugin"}`, 0o644)
	writeInfraFile(t, filepath.Join(parent, ".mcp.json"), `{"mcpServers":{"other":{"command":"/bin/other"},"svc":{"command":"/bin/svc"}}}`, 0o644)

	configured := filepath.Join(root, "plugin")
	if err := os.Symlink(versioned, configured); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	instance, err := plugin.Load(configured)
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	evidence := instance.MCPEvidence
	for _, layer := range evidence {
		if layer.ResolvedDir != versioned {
			t.Fatalf("layer %+v did not record the directory it resolved to (%q), so no later move could be detected", layer, versioned)
		}
	}

	const cliTool = "svc__echo"
	ceilingOf := func() []string {
		s := &Session{env: execenv.NewLocalExecutionEnvironment(root)}
		s.setManagerOnlyTools(nil)
		s.pluginMCPEvidence = evidence
		_, _, discovered, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(execenv.NewLocalExecutionEnvironment(root), nil, nil)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		s.recordCLIMCPServers(nil, discovered)
		s.setCLIMCPToolNames(map[string]bool{cliTool: true})
		s.refreshCLIEvidenceFreshness()
		return s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "")
	}

	if got := ceilingOf(); slices.Contains(got, cliTool) {
		t.Fatalf("fixture is wrong: the unchanged directory did not apply the exclusion: %v", got)
	}

	// Retarget to the ANCESTOR. Every recorded file is still under the new target,
	// so a containment check would pass here.
	if err := os.Remove(configured); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	if err := os.Symlink(parent, configured); err != nil {
		t.Fatalf("retarget symlink: %v", err)
	}
	if got := ceilingOf(); !slices.Contains(got, cliTool) {
		t.Errorf("the configured directory now resolves to %q, which declares a server matching %q, so the child can register it; containment against the old target held because the version's files still sit underneath: %v", parent, cliTool, got)
	}
}

// TestBrokenPluginSkipIsRecordedAsUnverifiableEvidence covers the plugin
// directories the loader SKIPS.
//
// A plugin whose manifest will not parse contributes no evidence of its own:
// LoadAllFailSoft reports it in the skipped list and returns nothing to record.
// That is not the same fact as "this plugin has no MCP configuration". The
// manifest is the file that fails, and it says nothing about the .mcp.json
// beside it, which a child rereads at spawn. Repair the manifest before a
// delegate is created and the child loads the plugin, registers the server it
// declares, while the parent's evidence still names only the plugins that
// loaded — so the exclusion stays current and the ceiling keeps dropping a tool
// the child can register.
//
// The skip therefore has to enter the evidence as UNVERIFIABLE, not as absence:
// a layer with no path answers "not current", which every caller reads as
// unknown reachability and which keeps the name eligible.
func TestBrokenPluginSkipIsRecordedAsUnverifiableEvidence(t *testing.T) {
	t.Parallel()
	root := realTempDirForTest(t)

	broken := filepath.Join(root, "broken-plugin")
	if err := os.MkdirAll(filepath.Join(broken, ".claude-plugin"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A manifest that will not parse: the plugin never loads, so its own
	// evidence is never produced.
	writeInfraFile(t, filepath.Join(broken, ".claude-plugin", "plugin.json"), `{"name":`, 0o644)
	// The .mcp.json the repaired plugin will declare. A child reads it; the
	// skipped paste never does.
	writeInfraFile(t, filepath.Join(broken, ".mcp.json"), `{"mcpServers":{"svc":{"command":"/bin/svc"}}}`, 0o644)

	load := func() *Session {
		s := &Session{env: execenv.NewLocalExecutionEnvironment(root)}
		s.setManagerOnlyTools(nil)
		if loaded := s.loadPluginsFailSoft([]string{broken}); len(loaded) != 0 {
			t.Fatalf("fixture is wrong: the broken plugin loaded: %d instances", len(loaded))
		}
		return s
	}

	s := load()
	// The skip must be recorded. Without a recorded layer the evidence names
	// only the plugins that loaded, which is indistinguishable from "nothing
	// else is configured".
	var unverifiable int
	for _, layer := range s.pluginMCPEvidence {
		if layer.Path == "" {
			unverifiable++
		}
	}
	if unverifiable == 0 {
		t.Fatalf("the skipped plugin contributed no unverifiable layer, so a later repair leaves the exclusion current: %v", s.pluginMCPEvidence)
	}

	const (
		cliTool  = "svc__echo"
		ordinary = "read_file"
	)
	// classify installs the CLI-only classification over the evidence the loader
	// produced, then answers the ceiling question the spawn path asks.
	classify := func(s *Session) []string {
		_, _, discovered, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(execenv.NewLocalExecutionEnvironment(root), nil, nil)
		if err != nil {
			t.Fatalf("discover: %v", err)
		}
		s.recordCLIMCPServers(nil, discovered)
		s.setCLIMCPToolNames(map[string]bool{cliTool: true})
		s.refreshCLIEvidenceFreshness()
		return s.removeChildUnreachableTools([]string{cliTool, ordinary}, "")
	}

	got := classify(load())
	if !slices.Contains(got, cliTool) {
		t.Errorf("a plugin the loader skipped may declare a server matching %q once its manifest is repaired, so reachability is unknown and the name must stay eligible: %v", cliTool, got)
	}
	if !slices.Contains(got, ordinary) {
		t.Errorf("an ordinary tool was dropped: %v", got)
	}
}

// TestDuplicatePluginRetargetInvalidatesTheEvidence covers the plugin directory
// the loader skipped as a DUPLICATE.
//
// First-manifest-wins means the second directory contributes no plugin, and
// the obvious reading is that it therefore needs no evidence: the directory
// that won supplies the manifest name, and its own evidence covers the
// configuration a child reads. That holds while the skipped directory still
// names what it named at startup.
//
// It stops holding on an upgrade that retargets the skipped directory at a
// different plugin. The winner is untouched, so its evidence still matches, and
// the next child loads the retargeted plugin — including any MCP server it
// declares — while the parent's evidence accounts for neither. Recording the
// resolution that made the directory a duplicate is what makes the change
// visible. Selection stays first-manifest-wins either way.
func TestDuplicatePluginRetargetInvalidatesTheEvidence(t *testing.T) {
	t.Parallel()
	root := realTempDirForTest(t)

	writePlugin := func(dir, name, mcp string) {
		if err := os.MkdirAll(filepath.Join(dir, ".claude-plugin"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		writeInfraFile(t, filepath.Join(dir, ".claude-plugin", "plugin.json"), `{"name":"`+name+`"}`, 0o644)
		writeInfraFile(t, filepath.Join(dir, ".mcp.json"), mcp, 0o644)
	}

	winner := filepath.Join(root, "winner")
	writePlugin(winner, "team-plugin", `{"mcpServers":{"other":{"command":"/bin/other"}}}`)

	// v1 duplicates the winner's name and exposes nothing matching the CLI
	// namespace.
	dupV1 := filepath.Join(root, "dup-v1")
	writePlugin(dupV1, "team-plugin", `{"mcpServers":{"other":{"command":"/bin/other"}}}`)
	// v2 carries a DIFFERENT manifest name and declares the matching server. The
	// distinct name is the point: if v2 kept "team-plugin" it would still be
	// skipped as a duplicate after the retarget, so a fresh child would load
	// nothing from it and the test would never exercise the case it claims to —
	// a child loading a server that only became reachable through the retarget.
	dupV2 := filepath.Join(root, "dup-v2")
	writePlugin(dupV2, "team-plugin-v2", `{"mcpServers":{"other":{"command":"/bin/other"},"svc":{"command":"/bin/svc"}}}`)

	skippedDir := filepath.Join(root, "skipped")
	if err := os.Symlink(dupV1, skippedDir); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	const cliTool = "svc__echo"
	// One session, loaded ONCE. Evidence is what startup recorded; reloading
	// after the retarget would re-record it and the test would pass with the
	// duplicate unaccounted for.
	s := &Session{env: execenv.NewLocalExecutionEnvironment(root)}
	s.setManagerOnlyTools(nil)
	if loaded := s.loadPluginsFailSoft([]string{winner, skippedDir}); len(loaded) != 1 {
		t.Fatalf("fixture is wrong: want the winner loaded and the duplicate skipped, got %d loaded", len(loaded))
	}
	_, _, discovered, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(execenv.NewLocalExecutionEnvironment(root), nil, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	s.recordCLIMCPServers(nil, discovered)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})
	ceilingOf := func() []string {
		s.refreshCLIEvidenceFreshness()
		return s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "")
	}

	// Unchanged duplicate: first-manifest-wins still applies, and the evidence
	// describes what a child would read.
	if got := ceilingOf(); slices.Contains(got, cliTool) {
		t.Fatalf("fixture is wrong: the unchanged duplicate did not apply the exclusion: %v", got)
	}

	// The upgrade retargets the SKIPPED directory at a different plugin with a
	// matching server. The winner is untouched.
	if err := os.Remove(skippedDir); err != nil {
		t.Fatalf("remove symlink: %v", err)
	}
	if err := os.Symlink(dupV2, skippedDir); err != nil {
		t.Fatalf("retarget symlink: %v", err)
	}
	if got := ceilingOf(); !slices.Contains(got, cliTool) {
		t.Errorf("the skipped directory now resolves to a plugin declaring a server matching %q, which a child loads; the winner is unchanged, so only the duplicate's own recorded resolution can notice: %v", cliTool, got)
	}

	// The claim above is that a CHILD loads this plugin. Prove it rather than
	// asserting it: a fresh load over the same directories must select v2 and
	// expose its server, which is what makes the exclusion wrong in the
	// permissive direction had it been kept.
	fresh := &Session{env: execenv.NewLocalExecutionEnvironment(root)}
	fresh.setManagerOnlyTools(nil)
	freshLoad := fresh.loadPluginsFailSoft([]string{winner, skippedDir})
	var loadedNames []string
	for _, instance := range freshLoad {
		loadedNames = append(loadedNames, instance.Manifest.Name)
	}
	if !slices.Contains(loadedNames, "team-plugin-v2") {
		t.Fatalf("a fresh child did not load the retargeted plugin, so this fixture does not exercise a newly reachable server: loaded %v", loadedNames)
	}
}
