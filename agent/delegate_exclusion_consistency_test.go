package agent

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// TestSwapEnvClearsCLIClassificationWithoutTheSessionLock is H1: the clear that a
// worktree switch performs must not need s.mu, because swapEnvAndRefresh already
// holds it and s.mu is not reentrant.
//
// The old shape captured evidence by resolving the environment through
// currentEnv, which locks s.mu. A test can only show that by hanging, so the body
// runs in a goroutine behind a watchdog: holding s.mu here is the faithful
// reproduction of what the swap does, not a simulation of it.
func TestSwapEnvInvalidatesCLIClassificationWithoutTheSessionLock(t *testing.T) {
	t.Parallel()
	s := &Session{}
	s.setManagerOnlyTools(map[string]bool{"session_control": true})
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, mcpconfig.ConfigEvidence{
		Project: mcpconfig.LayerEvidence{Path: "/repo/.evener/mcp.json", Digest: "abc"},
	})

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.invalidateCLIMCPClassification()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: a lock-order hang, not a slow path; the work is four field writes
		t.Fatal("invalidateCLIMCPClassification did not return while s.mu was held: it takes the session lock, or reads the environment, which does")
	}

	// The invalidation must forget the VERDICT and keep the classification and
	// the Manager half. Keeping the classification is what lets the exclusion
	// recover when the session returns to the workspace its evidence describes;
	// dropping it meant an environment swap silenced the exclusion for the rest of
	// the session.
	s.delegateExclusionsMu.RLock()
	cli := s.cliMCPToolNames
	evidence := s.cliMCPEvidence
	freshKnown := s.cliMCPEvidenceFreshKnown
	s.delegateExclusionsMu.RUnlock()
	if len(cli) == 0 {
		t.Errorf("invalidateCLIMCPClassification erased the CLI tool names, which a swap does not change")
	}
	if evidence.layers.Project.Path == "" || evidence.layers.Project.Digest == "" {
		t.Errorf("invalidateCLIMCPClassification erased the evidence its classification was derived from: %+v", evidence)
	}
	if freshKnown {
		t.Errorf("invalidateCLIMCPClassification kept the verdict, which was an answer about the environment being left")
	}
	got := s.removeChildUnreachableTools([]string{"session_control", "svc__echo", "read_file"}, "")
	if slices.Contains(got, "session_control") {
		t.Errorf("the invalidation re-admitted the Manager-only tool: %v", got)
	}
	if !slices.Contains(got, "svc__echo") {
		t.Errorf("with no verdict the exclusion must stay inactive, so %q must be eligible: %v", "svc__echo", got)
	}
}

// TestProjectEvidenceComesFromTheParsedBytes makes the classification and its
// evidence one fact: the digest comes from the same read that supplied the parsed
// layer, so an edit between the two cannot leave a stale classification verifying
// against fresh bytes.
func TestProjectEvidenceComesFromTheParsedBytes(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	layer := filepath.Join(root, ".evener", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(layer), 0o755); err != nil {
		t.Fatal(err)
	}
	first := `{"mcpServers":{"team_tools":{"command":"one"}}}`
	if err := os.WriteFile(layer, []byte(first), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(root)

	configs, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs[0].Command != "one" {
		t.Fatalf("discovery did not return the project layer: %+v", configs)
	}
	if evidence.Project.Path != layer {
		t.Errorf("evidence path = %q, want %q", evidence.Project.Path, layer)
	}
	if want := mcpconfig.ConfigDigest([]byte(first)); evidence.Project.Digest != want {
		t.Errorf("evidence digest = %q, want the digest of the bytes discovery parsed (%q)", evidence.Project.Digest, want)
	}

	s := &Session{env: env}
	recorded := cliMCPClassificationEvidence{layers: evidence}
	if !s.cliMCPClassificationCurrent(recorded, s.currentEnv()) {
		t.Fatal("the evidence did not verify against the file it was read from")
	}
	if err := os.WriteFile(layer, []byte(`{"mcpServers":{"team_tools":{"command":"two"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if s.cliMCPClassificationCurrent(recorded, s.currentEnv()) {
		t.Error("an edited project layer still verified the old evidence")
	}
}

// TestCLIClassificationEvidenceRequiresTheCurrentProjectPath is the medium about
// a session that started outside Git and later had a repository initialized
// around it: the recorded "no project layer" must not verify forever.
func TestCLIClassificationEvidenceRequiresTheCurrentProjectPath(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	if err := os.MkdirAll(filepath.Join(root, ".evener"), 0o755); err != nil {
		t.Fatal(err)
	}
	layer := filepath.Join(root, ".evener", "mcp.json")
	if err := os.WriteFile(layer, []byte(`{"mcpServers":{"late_fallback":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := &Session{env: execenv.NewLocalExecutionEnvironment(root)}

	if s.cliMCPClassificationCurrent(cliMCPClassificationEvidence{}, s.currentEnv()) {
		t.Error("an empty recorded path verified while the workspace resolves to a real project layer")
	}
	other := filepath.Join(t.TempDir(), ".evener", "mcp.json")
	if s.cliMCPClassificationCurrent(cliMCPClassificationEvidence{layers: mcpconfig.ConfigEvidence{
		Project: mcpconfig.LayerEvidence{Path: other, Digest: "d"},
	}}, s.currentEnv()) {
		t.Error("evidence from a different project path verified against this workspace")
	}
}

// TestExclusionDecisionChangeInvalidatesCachedRoster is the medium that the
// cached prompt and tool definitions must not keep advertising the startup answer
// after the exclusion flips.
func TestExclusionDecisionChangeInvalidatesCachedRoster(t *testing.T) {
	t.Parallel()
	s := &Session{}
	applied := s.noteExclusionDecision(true)
	if applied == 0 {
		t.Fatal("the first decision did not bump the version")
	}
	if got := s.noteExclusionDecision(true); got != applied {
		t.Errorf("repeating the same decision bumped the version: %d -> %d", applied, got)
	}
	if s.cachedExclusionVersion == s.exclusionVersion() {
		t.Fatal("the cached version already matches before any flip; the test could not show staleness")
	}
	flipped := s.noteExclusionDecision(false)
	if flipped == applied {
		t.Error("a changed exclusion decision did not bump the version, so no cache would be rebuilt")
	}
	s.cachedExclusionVersion = applied
	if s.cachedExclusionVersion == s.exclusionVersion() {
		t.Error("a cache built from the old decision still matches after the flip")
	}
}

// TestVisionModelRebuildLeavesTheRosterStale is the medium about two caches and
// one stamp.
//
// The prompt roster and the tool definitions are separate caches with separate
// rebuild paths, and one of those paths — SetVisionModel — rebuilds only the
// definitions. A single shared version stamp therefore let that narrow rebuild
// mark the PROMPT current as well, so the request path then skipped its refresh
// and kept serving a delegate roster built from the old exclusion decision.
//
// The fixture settles both caches, flips the decision, then changes the vision
// model and asks the request path whether the prompt is still stale. It must be.
func TestVisionModelRebuildLeavesTheRosterStale(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	projectPath := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)

	const cliTool = "svc__echo"
	s := newSession(t, withEnv(env))
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})

	// Settle both caches on the startup answer.
	for range 4 {
		s.refreshCachesForExclusionChange()
	}
	s.mu.Lock()
	defsBuiltFrom := s.cachedExclusionVersion
	promptBuiltFrom := s.cachedPromptExclusionVersion
	s.mu.Unlock()
	if defsBuiltFrom != s.exclusionVersion() || promptBuiltFrom != s.exclusionVersion() {
		t.Fatalf("the fixture did not settle both caches (defs %d, prompt %d, current %d)", defsBuiltFrom, promptBuiltFrom, s.exclusionVersion())
	}

	// The project config gains a matching server: the decision flips, and both
	// caches are now stale.
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/a"},"other":{"command":"/bin/c"}}}`, 0o644)
	s.refreshCLIEvidenceFreshness()
	s.childExclusionDecision("")
	flipped := s.exclusionVersion()
	if flipped == defsBuiltFrom {
		t.Fatalf("the config edit did not move the decision version, so this test could not show staleness")
	}

	// The user changes the vision model before the next request. That rebuilds
	// the tool definitions only.
	if err := s.SetVisionModel("off"); err != nil {
		t.Fatalf("SetVisionModel: %v", err)
	}

	s.mu.Lock()
	promptAfterVision := s.cachedPromptExclusionVersion
	s.mu.Unlock()
	if promptAfterVision == flipped {
		t.Errorf("changing the vision model marked the PROMPT cache current at version %d although it was never re-rendered from that decision, so the next request would skip the refresh and keep advertising a roster built from %d", promptAfterVision, promptBuiltFrom)
	}

	// And the request path must take it: one refresh, then the prompt agrees.
	s.refreshCachesForExclusionChange()
	s.mu.Lock()
	promptAfterRefresh := s.cachedPromptExclusionVersion
	s.mu.Unlock()
	if promptAfterRefresh != flipped {
		t.Errorf("the request path did not re-render the prompt from the current decision: prompt stamp %d, want %d", promptAfterRefresh, flipped)
	}
}

// TestAbsentProjectLayerStillVerifiesItsEvidence is M1: absent is not
// unreadable. In a git checkout with no project MCP file, discovery records the
// RESOLVED path with an empty digest. A freshness check that instead recorded
// nothing would find a nonempty current path, reject the evidence, and leave a
// CLI-only server's tools in the roster and the frozen descriptor for every
// workspace that has no project layer — which is most of them.
func TestAbsentProjectLayerStillVerifiesItsEvidence(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	env := execenv.NewLocalExecutionEnvironment(root)

	configs, cliOnly, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 0 || len(cliOnly) != 0 {
		t.Fatalf("fixture expected no config layers: configs=%v cliOnly=%v", configs, cliOnly)
	}
	wantPath := filepath.Join(root, ".evener", "mcp.json")
	if evidence.Project.Path != wantPath {
		t.Fatalf("an absent project layer recorded path %q, want the resolved %q", evidence.Project.Path, wantPath)
	}
	if evidence.Project.Digest != "" {
		t.Errorf("an absent project layer recorded a digest %q, want empty", evidence.Project.Digest)
	}
	if !evidence.EvidenceCurrent(env) {
		t.Error("the evidence for an absent project layer did not verify while the layer is still absent")
	}

	// The other half: the file APPEARING is what invalidates it.
	writeInfraFile(t, wantPath, `{"mcpServers":{"late":{"command":"x"}}}`, 0o644)
	if evidence.EvidenceCurrent(env) {
		t.Error("a project layer that appeared still verified the evidence recorded for its absence")
	}
}

// TestGlobalLayerParticipatesInTheEvidence is M6: a matching server added to the
// user-global MCP config after startup is a new layer a child reads, so a
// classification that verified only the project layer would exclude a tool the
// child registers through it.
func TestGlobalLayerParticipatesInTheEvidence(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	globalDir := realTempDirForTest(t)
	t.Setenv("XDG_CONFIG_HOME", globalDir)
	globalPath := filepath.Join(globalDir, "evener", "mcp.json")

	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	env := execenv.NewLocalExecutionEnvironment(root)

	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.Global.Path != globalPath {
		t.Fatalf("absent global layer recorded path %q, want %q", evidence.Global.Path, globalPath)
	}
	if !evidence.EvidenceCurrent(env) {
		t.Fatal("the evidence did not verify with both layers unchanged")
	}

	writeInfraFile(t, globalPath, `{"mcpServers":{"team_tools":{"command":"x"}}}`, 0o644)
	if evidence.EvidenceCurrent(env) {
		t.Error("a server added to the global layer still verified the startup evidence, so the ceiling would keep a name that layer makes reachable")
	}
}

// TestFreshEvidenceSeesARepositoryInitializedAroundTheSession is M5: a session
// launched outside git caches an empty git root in its own environment. Once a
// repository is initialized around it with a project MCP fallback, a sandboxed
// child gets a fresh environment and discovers that layer — so the parent's
// startup classification no longer describes what the child reads.
//
// The property is that the check ANSWERS RELATIVELY TO NOW, not that the
// evidence still verifies. The startup evidence for this workspace recorded no
// project layer, because there was none; the layer appearing is exactly what must
// stop that evidence from being trusted, so the names stay eligible and the child
// is allowed to try. Verifying through the memoized root would keep answering
// "still no project layer" for the rest of the session and the ceiling would keep
// excluding a tool the child registers.
func TestFreshEvidenceSeesARepositoryInitializedAroundTheSession(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	env := execenv.NewLocalExecutionEnvironment(root)

	// Populate the memo while this is not yet a repository, exactly as a session
	// launched outside git does.
	if got := execenv.GitRootOrEmpty(env, root); got != "" {
		t.Fatalf("outside git the memoized root should be empty, got %q", got)
	}
	_, _, startupEvidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if startupEvidence.Project.Path != "" {
		t.Fatalf("a launch outside git recorded a project layer: %+v", startupEvidence.Project)
	}
	if !startupEvidence.EvidenceCurrent(env) {
		t.Fatal("the startup evidence did not verify while nothing had changed")
	}

	// The repository appears around the running session, with a fallback server.
	runInfraGit(t, root, "init", "-q")
	writeInfraFile(t, filepath.Join(root, ".evener", "mcp.json"),
		`{"mcpServers":{"late_fallback":{"command":"x"}}}`, 0o644)

	if got := mcpconfig.ProjectConfigPath(env); got == "" {
		t.Fatal("the project layer resolution still reported no config after a repository was initialized around the session")
	}
	// The memo is what a child sharing this environment would read, and it still
	// answers "no repository". That stale answer is the reason the project path
	// must be resolved WITHOUT it: with it, the child discovers no project layer
	// while the parent's freshness check makes the tool eligible.
	if execenv.GitRootOrEmpty(env, root) != "" {
		t.Fatal("the memoized root already sees the new repository; this test could not show a stale lookup")
	}
	if startupEvidence.EvidenceCurrent(env) {
		t.Error("the startup evidence still verified through a memoized root, so the ceiling would keep excluding a tool the new project layer makes reachable")
	}
	if mcpconfig.ProjectConfigPath(env) == "" {
		t.Error("the project path resolution lost the project layer")
	}
}

// TestConfigEditAloneInvalidatesTheCachedAnswer is M2: the invalidation must not
// wait for a ceiling build. The version the caches compare against is bumped
// where a decision is COMPUTED, and the only other place that computes one is a
// delegate spawn — so a refresh path that merely compared versions would leave a
// session whose project layer gained a fallback server hiding that server's tools
// until some unrelated action rebuilt the roster. The edit itself must be enough.
//
// This drives the real request path (refreshCachesForExclusionChange) on a real
// session, with no delegate created anywhere: the only thing that happens between
// the two observations is a file edit.
func TestConfigEditAloneInvalidatesTheCachedAnswer(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	projectPath := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)

	const cliTool = "svc__echo"
	s := newSession(t, withEnv(env))
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})

	// The startup answer, cached exactly as the request path caches it. Refresh
	// until the cache and the decision version agree, because that settled state is
	// the one this finding is about: while a rebuild is still pending the rebuild
	// itself recomputes the ceiling, and the defect hides behind that.
	for range 4 {
		s.refreshCachesForExclusionChange()
	}
	s.mu.Lock()
	builtFrom := s.cachedExclusionVersion
	s.mu.Unlock()
	if builtFrom != s.exclusionVersion() {
		t.Fatalf("the fixture did not settle the cache (cached %d, current %d), so a rebuild would recompute the ceiling and mask the defect", builtFrom, s.exclusionVersion())
	}
	if !s.cliMCPExclusionApplied {
		t.Fatal("the fixture did not start with the exclusion applied")
	}

	// The branch switches in place: the layer now declares a server the startup
	// snapshot never saw. No environment swap runs, and no delegate is spawned.
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/b"}}}`, 0o644)

	// The next model request.
	s.refreshCachesForExclusionChange()

	s.mu.Lock()
	rebuilt := s.cachedExclusionVersion
	s.mu.Unlock()
	if rebuilt == builtFrom {
		t.Fatalf("the request path kept the cached answer built from version %d after an in-place config edit, so the roster keeps hiding %q with no delegate spawn to correct it", builtFrom, cliTool)
	}
	if s.cliMCPExclusionApplied {
		t.Error("the decision still applies the startup exclusion after the layer changed")
	}
	got := s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "")
	if !slices.Contains(got, cliTool) {
		t.Errorf("the ceiling still excludes %q after the config edit: %v", cliTool, got)
	}
}

// A worktree spawn is a per-lane exception, not a change to the session's
// shared-workspace decision. Recording it in the shared version stamped both
// roster caches stale, so the next model request flipped the recorded answer
// back and rebuilt them although the shared-workspace roster was unchanged.
func TestWorktreeSpawnDoesNotInvalidateTheSharedRosterCaches(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	// A real git root, because the project layer only resolves inside one: without
	// it the evidence is never fresh and the exclusion never applies, which made
	// this fixture pass while testing nothing.
	runInfraGit(t, root, "init", "-q")
	writeInfraFile(t, filepath.Join(root, ".evener", "mcp.json"), `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)

	const cliTool = "svc__echo"
	s := newSession(t, withEnv(env))
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})

	// Settle both caches on the shared-workspace answer.
	for range 4 {
		s.refreshCachesForExclusionChange()
	}
	settled := s.exclusionVersion()

	// The lane's own ceiling keeps the CLI-only name: the startup snapshot
	// describes the session's workspace, not the fresh lane's, so reachability
	// there is unknown and unknown means the child gets to try. The exception is
	// real, and only its bookkeeping changed.
	if got := s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "worktree"); !slices.Contains(got, cliTool) {
		t.Fatalf("a fresh worktree lane dropped a CLI-only name whose reachability there is unknown: %v", got)
	}
	// The shared workspace still applies the exclusion.
	if got := s.removeChildUnreachableTools([]string{cliTool, "read_file"}, ""); slices.Contains(got, cliTool) {
		t.Fatalf("the shared workspace stopped applying the exclusion: %v", got)
	}

	if got := s.exclusionVersion(); got != settled {
		t.Fatalf("deriving a worktree lane's ceiling moved the shared decision version from %d to %d, so the next request rebuilds both roster caches for an unchanged shared roster", settled, got)
	}
}

// TestPluginMCPSourceKeepsCLINamesEligible pins the plugin half of the exclusion
// soundness rule.
//
// A spawned child clears the CLI layers and reloads every plugin directory from
// disk, so a plugin source can supply a name this session believes only a CLI
// server provides. The freshness evidence fingerprints the global and project
// layers, and that is all it can: plugin MCP configs arrive already merged, with
// no path or digest to compare. A plugin directory that gains a matching server
// after startup therefore makes a CLI-only name reachable with nothing able to
// notice, so the CLI half is not applied and its names stay eligible.
//
// The MANAGER half is unaffected: no spawn path composes the hub, whatever the
// plugins declare.
func TestPluginMCPSourceKeepsCLINamesEligible(t *testing.T) {
	t.Parallel()
	const cliTool = "svc__echo"

	// No plugin MCP sources: the evidence describes every layer the child reads,
	// so a fresh verdict applies the exclusion.
	plain := &Session{}
	plain.setCLIMCPToolNames(map[string]bool{cliTool: true})
	plain.setManagerOnlyTools(map[string]bool{"session_control": true})
	plain.publishFreshnessForRevision(true, plain.cliMCPEnvRevision)
	if got := plain.removeChildUnreachableTools([]string{cliTool}, ""); slices.Contains(got, cliTool) {
		t.Fatalf("with no plugin source the CLI-only name survived: %v", got)
	}

	// A plugin source the child reloads but the evidence cannot verify: the CLI
	// half is inapplicable, so the name stays eligible while the Manager half
	// still excludes.
	withPlugin := &Session{pluginMCPConfigs: []mcpconfig.ServerConfig{{Name: "plugin_svc"}}}
	withPlugin.setCLIMCPToolNames(map[string]bool{cliTool: true})
	withPlugin.setManagerOnlyTools(map[string]bool{"session_control": true})
	withPlugin.publishFreshnessForRevision(true, withPlugin.cliMCPEnvRevision)
	got := withPlugin.removeChildUnreachableTools([]string{cliTool, "session_control"}, "")
	if !slices.Contains(got, cliTool) {
		t.Errorf("an unverifiable plugin source did not keep the CLI-only name eligible: %v", got)
	}
	if slices.Contains(got, "session_control") {
		t.Errorf("the Manager-only name survived a plugin source: %v", got)
	}

	// A configured plugin directory that supplied NO MCP entries at startup is
	// the case a check on the merged configs alone misses. The child reloads the
	// directory from disk, so it can gain a first .mcp.json during the session
	// and register a name this session still believes is CLI-only. Empty
	// pluginMCPConfigs says nothing about that, so the directory itself makes
	// the source unverified — and the Manager half stays excluded regardless.
	emptyPlugin := &Session{cfg: SessionConfig{PluginDirs: []string{"/plugins/quiet"}}}
	emptyPlugin.setCLIMCPToolNames(map[string]bool{cliTool: true})
	emptyPlugin.setManagerOnlyTools(map[string]bool{"session_control": true})
	emptyPlugin.publishFreshnessForRevision(true, emptyPlugin.cliMCPEnvRevision)
	got = emptyPlugin.removeChildUnreachableTools([]string{cliTool, "session_control"}, "")
	if !slices.Contains(got, cliTool) {
		t.Errorf("a plugin directory with no startup MCP entries did not keep the CLI-only name eligible: %v", got)
	}
	if slices.Contains(got, "session_control") {
		t.Errorf("the Manager-only name survived a plugin directory that is merely unverifiable: %v", got)
	}
}
