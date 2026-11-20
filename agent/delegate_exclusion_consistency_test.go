package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
	"primeradiant.com/evener/agent/plugin"
)

// TestWorkspaceChangeResetKeepsTheClassificationWithoutTheSessionLock is H1: the
// reset a worktree switch performs must not need s.mu, because swapEnvAndRefresh
// already holds it and s.mu is not reentrant.
//
// The old shape captured evidence by resolving the environment through
// currentEnv, which locks s.mu. A test can only show that by hanging, so the body
// runs in a goroutine behind a watchdog: holding s.mu here is the faithful
// reproduction of what the swap does, not a simulation of it.
func TestWorkspaceChangeResetKeepsTheClassificationWithoutTheSessionLock(t *testing.T) {
	t.Parallel()
	s := &Session{}
	s.setManagerOnlyTools(map[string]bool{"session_control": true})
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, mcpconfig.ConfigEvidence{
		Project: mcpconfig.LayerEvidence{Path: "/repo/.evener/mcp.json", Digest: "abc"},
	})
	s.setRosterExclusion(true)

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.mu.Lock()
		defer s.mu.Unlock()
		s.resetRosterExclusionForWorkspaceChange()
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second): // TRIPWIRE: a lock-order hang, not a slow path; the work is one field write
		t.Fatal("resetRosterExclusionForWorkspaceChange did not return while s.mu was held: it takes the session lock, or reads the environment, which does")
	}

	// The reset must drop the DECISION and keep the classification and the
	// Manager half. Keeping the classification is what lets the exclusion recover
	// when the session returns to the workspace its evidence describes; dropping
	// it meant an environment swap silenced the exclusion for the rest of the
	// session.
	s.delegateExclusionsMu.RLock()
	cli := s.cliMCPToolNames
	evidence := s.cliMCPEvidence
	s.delegateExclusionsMu.RUnlock()
	if len(cli) == 0 {
		t.Error("the reset erased the CLI tool names, which a workspace change does not alter")
	}
	if evidence.layers.Project.Path == "" || evidence.layers.Project.Digest == "" {
		t.Errorf("the reset erased the evidence its classification was derived from: %+v", evidence)
	}
	if s.rosterExclusion() {
		t.Error("the reset kept the decision, which was an answer about the workspace being left")
	}
	got := s.removeChildUnreachableTools(
		[]string{"session_control", "svc__echo", "read_file"}, "", s.rosterExclusion())
	if slices.Contains(got, "session_control") {
		t.Errorf("the reset re-admitted the Manager-only tool: %v", got)
	}
	if !slices.Contains(got, "svc__echo") {
		t.Errorf("after the reset the exclusion must stay inactive, so %q must be eligible: %v", "svc__echo", got)
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

	configs, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
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

	if !evidence.EvidenceCurrent(env) {
		t.Fatal("the evidence did not verify against the file it was read from")
	}
	if err := os.WriteFile(layer, []byte(`{"mcpServers":{"team_tools":{"command":"two"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if evidence.EvidenceCurrent(env) {
		t.Error("an edited project layer still verified the old evidence")
	}
}

// TestCLIClassificationEvidenceRequiresTheCurrentProjectPath is the medium about
// a session that started outside Git and later had a repository initialized
// around it: the recorded "no project layer" must not verify forever.
//
// Every case starts from the evidence DISCOVERY captured and asserts that the
// unmodified copy verifies, so a negative below can only come from the project
// field it changed. Evidence with an empty global half is rejected by the global
// check before the project path is ever compared, which would let both negatives
// pass without testing this at all.
func TestCLIClassificationEvidenceRequiresTheCurrentProjectPath(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	layer := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, layer, `{"mcpServers":{"late_fallback":{"command":"x"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)

	configs, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 {
		t.Fatalf("discovery did not return the project layer: %+v", configs)
	}
	if evidence.Project.Path != layer {
		t.Fatalf("discovery recorded project path %q, want %q", evidence.Project.Path, layer)
	}

	// The baseline: the evidence exactly as discovery captured it verifies. Both
	// negatives below change ONLY the project field of this same value, so the
	// global layer is known to have passed in each of them.
	if !evidence.EvidenceCurrent(env) {
		t.Fatal("evidence captured by discovery did not verify against the workspace it was read from, so the negative cases below could pass for an unrelated reason")
	}

	// A recorded "no project layer" — what a session launched outside Git holds —
	// must not verify once the workspace resolves to a real one.
	absentProject := evidence
	absentProject.Project = mcpconfig.LayerEvidence{}
	if absentProject.EvidenceCurrent(env) {
		t.Error("an empty recorded path verified while the workspace resolves to a real project layer")
	}

	// Evidence recorded for a different project path describes a different
	// workspace, so it must not verify against this one.
	movedProject := evidence
	movedProject.Project.Path = filepath.Join(t.TempDir(), ".evener", "mcp.json")
	if movedProject.EvidenceCurrent(env) {
		t.Error("evidence from a different project path verified against this workspace")
	}
}

// TestExclusionDecisionFlipRebuildsBothRosterSurfaces is the medium that a
// changed roster decision must reach BOTH cached surfaces, and it replaces the
// old assertion on a decision version counter.
//
// The counter existed to tell each cache whether it was current. It brought two
// caches and one invalidation rule into a testable coupling, and every later
// round found another way for the two to drift apart — a narrow rebuild stamping
// the shared version, a worktree spawn stamping it, a refresh that compared
// instead of recomputing. The design under test has one decision and one rebuild
// site, so the property is stated directly on the two surfaces a model reads:
// after the decision flips, the prompt roster and the delegate schema carry the
// same answer.
func TestExclusionDecisionFlipRebuildsBothRosterSurfaces(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	projectPath := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)

	const cliTool = "svc__echo"
	s := newSession(t, withEnv(env))
	s.pluginAgents = map[string]plugin.Agent{
		"worker": {Name: "worker", Description: "does the work"},
	}
	if err := s.reg.Register(syntheticTool(cliTool)); err != nil {
		t.Fatalf("register %s: %v", cliTool, err)
	}
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})

	// Settle both caches on the startup answer.
	s.refreshCachesForExclusionChangeContext(context.Background())
	if !s.rosterExclusion() {
		t.Fatal("fixture is wrong: the untouched evidence did not apply the exclusion, so the flip below proves nothing")
	}
	before := prepareRequestSnapshot(t, s)
	if strings.Contains(before.systemPrompt, cliTool) {
		t.Fatalf("the settled prompt roster already advertises %s", cliTool)
	}
	if strings.Contains(before.delegateRoster, cliTool) {
		t.Fatalf("the settled delegate schema already advertises %s", cliTool)
	}

	// The project layer changes so the classification no longer describes it: the
	// probe answers "not current", the decision must flip, and BOTH surfaces must
	// follow in one step.
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/b"}}}`, 0o644)
	s.refreshCachesForExclusionChangeContext(context.Background())
	if s.rosterExclusion() {
		t.Fatal("the request path kept the exclusion after the layer it was derived from changed")
	}
	after := prepareRequestSnapshot(t, s)
	if !strings.Contains(after.systemPrompt, cliTool) {
		t.Error("the prompt roster still omits the newly reachable tool: the decision flipped and the prompt cache was not rebuilt from it")
	}
	if !strings.Contains(after.delegateRoster, cliTool) {
		t.Error("the delegate schema still omits the newly reachable tool: the decision flipped and the tool definitions were not rebuilt from it")
	}
}

// TestNarrowRebuildCannotLeaveTheRostersDisagreeing is the medium about two
// caches and one stamp, restated on the design that removed the stamp.
//
// The prompt roster and the tool definitions are separate caches, and one rebuild
// path — SetVisionModel — touches only the definitions. With a shared decision
// VERSION, that narrow rebuild could mark the prompt current too, and the next
// request then skipped its refresh and kept serving a roster built from the old
// answer.
//
// There is no stamp now. The decision itself is the only shared fact, and the one
// place that changes it rebuilds both caches. So the fixture flips the decision
// and runs the narrow rebuild BEFORE the next request: both surfaces must still
// agree afterwards, whichever order they were rebuilt in.
func TestNarrowRebuildCannotLeaveTheRostersDisagreeing(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	projectPath := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)

	const cliTool = "svc__echo"
	s := newSession(t, withEnv(env))
	s.pluginAgents = map[string]plugin.Agent{
		"worker": {Name: "worker", Description: "does the work"},
	}
	if err := s.reg.Register(syntheticTool(cliTool)); err != nil {
		t.Fatalf("register %s: %v", cliTool, err)
	}
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})
	s.refreshCachesForExclusionChangeContext(context.Background())
	if !s.rosterExclusion() {
		t.Fatal("fixture is wrong: the exclusion was not applied, so the flip below proves nothing")
	}

	// The layer changes, then the user changes the vision model — a rebuild that
	// touches the tool definitions only — all BEFORE the next request.
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/b"}}}`, 0o644)
	if err := s.SetVisionModel("off"); err != nil {
		t.Fatalf("SetVisionModel: %v", err)
	}

	// The next request must carry the new answer on both surfaces. A prompt cache
	// that considered itself current would still omit the tool here.
	next := prepareRequestSnapshot(t, s)
	if !strings.Contains(next.systemPrompt, cliTool) {
		t.Error("the prompt roster still omits the tool the layer now makes reachable: the narrow definitions rebuild left the prompt cache serving the old answer")
	}
	if !strings.Contains(next.delegateRoster, cliTool) {
		t.Error("the delegate schema still omits the tool the layer now makes reachable")
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

	configs, cliOnly, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
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

	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
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
	_, _, startupEvidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
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
// wait for a ceiling build. Nothing but the request path recomputes the roster
// decision, so a session whose project layer gained a fallback server must see the
// edit on its very next request with no delegate spawned anywhere.
//
// This drives the real request path (refreshCachesForExclusionChangeContext) on a
// real session: the only thing that happens between the two observations is a
// file edit.
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
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})

	// The startup answer, reached through the request path itself.
	s.refreshCachesForExclusionChangeContext(context.Background())
	if !s.rosterExclusion() {
		t.Fatal("the fixture did not start with the exclusion applied")
	}
	if got := s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "", s.rosterExclusion()); slices.Contains(got, cliTool) {
		t.Fatalf("the settled ceiling kept %q, so the edit below could not show it being re-admitted: %v", cliTool, got)
	}

	// The branch switches in place: the layer now declares a server the startup
	// snapshot never saw. No environment swap runs, and no delegate is spawned.
	writeInfraFile(t, projectPath, `{"mcpServers":{"svc":{"command":"/bin/b"}}}`, 0o644)

	// The next model request.
	s.refreshCachesForExclusionChangeContext(context.Background())

	if s.rosterExclusion() {
		t.Error("the decision still applies the startup exclusion after the layer changed")
	}
	got := s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "", s.rosterExclusion())
	if !slices.Contains(got, cliTool) {
		t.Errorf("the ceiling still excludes %q after the config edit: %v", cliTool, got)
	}
}

// TestWorktreeSpawnIsAPerSpawnExceptionNotARosterChange pins that a fresh lane's
// ceiling and the session's own roster decision are separate answers.
//
// A worktree spawn is a per-lane exception, not a change to the session's
// shared-workspace decision. Under the old design the exception was recorded in a
// shared version, which stamped both roster caches stale, so the next model
// request rebuilt them although the shared-workspace roster was unchanged.
//
// Now nothing a spawn computes is written anywhere: the boolean travels in the
// call and reaches the descriptor, and the roster decision is exactly what the
// last request left behind.
func TestWorktreeSpawnIsAPerSpawnExceptionNotARosterChange(t *testing.T) {
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
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})

	// Settle the roster decision on the shared-workspace answer.
	s.refreshCachesForExclusionChangeContext(context.Background())
	if !s.rosterExclusion() {
		t.Fatal("fixture is wrong: the exclusion was not applied, so the worktree half proves nothing")
	}

	// The lane's own ceiling keeps the CLI-only name: the startup snapshot
	// describes the session's workspace, not the fresh lane's, so reachability
	// there is unknown and unknown means the child gets to try.
	spawnExclude := s.probeExclusionForSpawn(context.Background(), "worktree")
	if got := s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "worktree", spawnExclude); !slices.Contains(got, cliTool) {
		t.Fatalf("a fresh worktree lane dropped a CLI-only name whose reachability there is unknown: %v", got)
	}
	// The shared workspace still applies the exclusion.
	if got := s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "", s.rosterExclusion()); slices.Contains(got, cliTool) {
		t.Fatalf("the shared workspace stopped applying the exclusion: %v", got)
	}

	// And the spawn changed nothing the next request renders from.
	if !s.rosterExclusion() {
		t.Error("deriving a worktree lane's ceiling changed the session's roster decision, so the next request would rebuild both caches for an unchanged shared roster")
	}
}

// TestPluginMCPSourceKeepsCLINamesEligible pins the plugin half of the exclusion
// soundness rule.
//
// A spawned child clears the CLI layers and reloads every plugin directory from
// disk, so a plugin source can supply a name this session believes only a CLI
// server provides. The freshness evidence fingerprints the global and project
// layers, and that is all it can: plugin MCP configs arrive here already merged,
// with no path or digest to compare. A plugin directory that gains a matching
// server after startup therefore makes a CLI-only name reachable with nothing able
// to notice, so the CLI half is not applied and its names stay eligible.
//
// The MANAGER half is unaffected: no spawn path composes the hub, whatever the
// plugins declare.
func TestPluginMCPSourceKeepsCLINamesEligible(t *testing.T) {
	t.Parallel()
	const cliTool = "svc__echo"

	// No plugin MCP sources: the evidence describes every layer the child reads,
	// so a decision to exclude is honored.
	plain := &Session{}
	plain.setCLIMCPToolNames(map[string]bool{cliTool: true})
	plain.setManagerOnlyTools(map[string]bool{"session_control": true})
	if got := plain.removeChildUnreachableTools([]string{cliTool}, "", true); slices.Contains(got, cliTool) {
		t.Fatalf("with no plugin source the CLI-only name survived: %v", got)
	}

	// A plugin source the child reloads but the evidence cannot verify: the CLI
	// half is inapplicable, so the name stays eligible while the Manager half still
	// excludes. The probe reports false for such a session by construction.
	withPlugin := &Session{pluginMCPConfigs: []mcpconfig.ServerConfig{{Name: "plugin_svc"}}}
	withPlugin.setCLIMCPToolNames(map[string]bool{cliTool: true})
	withPlugin.setManagerOnlyTools(map[string]bool{"session_control": true})
	if withPlugin.probeCLIEvidenceFresh(context.Background(), nil) {
		t.Fatal("a session with an unverifiable plugin source probed")
	}
	got := withPlugin.removeChildUnreachableTools([]string{cliTool, "session_control"}, "", false)
	if !slices.Contains(got, cliTool) {
		t.Errorf("an unverifiable plugin source did not keep the CLI-only name eligible: %v", got)
	}
	if slices.Contains(got, "session_control") {
		t.Errorf("the Manager-only name survived a plugin source: %v", got)
	}

	// A configured plugin directory that supplied NO MCP entries at startup is the
	// case a check on the merged configs alone misses. The child reloads the
	// directory from disk, so it can gain a first .mcp.json during the session and
	// register a name this session still believes is CLI-only. Empty
	// pluginMCPConfigs says nothing about that, so the directory itself makes the
	// source unverified — and the Manager half stays excluded regardless.
	emptyPlugin := &Session{cfg: SessionConfig{PluginDirs: []string{"/plugins/quiet"}}}
	emptyPlugin.setCLIMCPToolNames(map[string]bool{cliTool: true})
	emptyPlugin.setManagerOnlyTools(map[string]bool{"session_control": true})
	if emptyPlugin.probeCLIEvidenceFresh(context.Background(), nil) {
		t.Fatal("a session with a configured plugin directory probed")
	}
	got = emptyPlugin.removeChildUnreachableTools([]string{cliTool, "session_control"}, "", false)
	if !slices.Contains(got, cliTool) {
		t.Errorf("a plugin directory with no startup MCP entries did not keep the CLI-only name eligible: %v", got)
	}
	if slices.Contains(got, "session_control") {
		t.Errorf("the Manager-only name survived a plugin directory that is merely unverifiable: %v", got)
	}
}
