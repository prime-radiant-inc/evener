package agent

import (
	"context"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// staleProbeHoldTripwire bounds the wait for the held probe to reach its check.
// The hold itself is deliberate; this only fires when the fixture never ran it.
const staleProbeHoldTripwire = 10 * time.Second

// TestStaleProbeCannotEraseANewerWorkspaceDecision is the regression test for
// the whole spiraling finding.
//
// Three rounds of fixes grew out of ONE shape — a probe PUBLISHING its answer
// into session state — and each round patched a different way that publication
// could go wrong: a timed-out caller erased a newer verdict, a completed obsolete
// caller erased it too, and a caller holding an old revision could replace the
// new revision's probe slot.
//
// The design under test removes the publication. A probe returns its answer to
// its caller, and only request preparation — under s.mu, after confirming the
// workspace it probed is still installed — writes the one roster decision. So
// this test runs the obsolete caller to completion against the workspace the
// session has left, and then drives a real model request for the installed
// workspace. The decision the installed workspace established must survive both.
func TestStaleProbeCannotEraseANewerWorkspaceDecision(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	rootA := realTempDirForTest(t)
	runInfraGit(t, rootA, "init", "-q")
	writeInfraFile(t, filepath.Join(rootA, ".evener", "mcp.json"),
		`{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	envA := execenv.NewLocalExecutionEnvironment(rootA)

	rootB := realTempDirForTest(t)
	runInfraGit(t, rootB, "init", "-q")
	writeInfraFile(t, filepath.Join(rootB, ".evener", "mcp.json"),
		`{"mcpServers":{"svc":{"command":"/bin/b"}}}`, 0o644)
	envB := execenv.NewLocalExecutionEnvironment(rootB)

	// Evidence for each workspace's own layers.
	_, _, evidenceA, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(envA, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover A: %v", err)
	}
	_, _, evidenceB, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(envB, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover B: %v", err)
	}
	// A's evidence must NOT verify against B, or the obsolete probe's answer
	// would be indistinguishable from a current one and the test would prove
	// nothing.
	if evidenceA.EvidenceCurrent(envB) {
		t.Fatal("fixture is wrong: A's evidence verifies against B, so the two workspaces cannot be told apart")
	}

	const cliTool = "svc__echo"
	s := newSession(t, withEnv(envB))
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, evidenceB)
	s.setRosterExclusion(true)
	// Hold B's first probe after its check has answered, before its caller
	// collects the answer. That is the window the merge-group flake lives in: the
	// check is over, so nothing is outstanding, but the request path below can
	// still run. It must ask the workspace again rather than read the probe's
	// bookkeeping as "unknown" and clear the standing decision.
	checked := make(chan struct{})
	release := make(chan struct{})
	var held atomic.Bool
	s.cfg.testOnly.evidenceProbeChecked = func() {
		if held.CompareAndSwap(false, true) {
			close(checked)
			<-release
		}
	}
	var releaseOnce sync.Once
	releaseProbe := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(releaseProbe)

	// B's own answer really is "exclude", so the request path below has a
	// standing decision to preserve rather than one it would clear anyway.
	firstAnswer := make(chan bool, 1)
	go func() { firstAnswer <- s.probeCLIEvidenceFresh(context.Background(), envB) }()
	select {
	case <-checked:
	case <-time.After(staleProbeHoldTripwire): // TRIPWIRE: the probe never reached its check
		t.Fatal("the first probe never finished its check, so this fixture did not exercise the held window")
	}

	// The obsolete caller finishes its own question about A.
	if !s.boundedEvidenceCurrent(context.Background(), cliMCPClassificationEvidence{layers: evidenceA}, envA) {
		t.Fatal("fixture is wrong: A's evidence did not verify against A, so the obsolete caller had no answer at all")
	}
	// It wrote nothing. There is no shared verdict for it to erase.
	if !s.rosterExclusion() {
		t.Fatal("an obsolete probe erased the installed workspace's roster decision, so the next request would advertise tools that workspace's children cannot register")
	}

	// And the real request path, which probes the INSTALLED workspace, keeps it:
	// B's evidence verifies, so the standing decision is confirmed, not cleared.
	s.refreshCachesForExclusionChangeContext(context.Background())
	if !s.rosterExclusion() {
		t.Error("the request path cleared the installed workspace's decision after an obsolete probe ran")
	}
	if got := s.removeChildUnreachableTools([]string{cliTool, "read_file"}, "", s.rosterExclusion()); slices.Contains(got, cliTool) {
		t.Errorf("the roster re-admitted %q after an obsolete probe ran: %v", cliTool, got)
	}
	releaseProbe()
	if !<-firstAnswer {
		t.Fatal("fixture is wrong: B's evidence does not verify against B, so there is no current answer to preserve")
	}
}

// TestTimedOutProbeLeavesTheRosterDecisionStanding is the timing half: a probe
// whose caller gives up must not clear a decision that is already in force.
//
// The old design forgot the verdict on a timeout, which left the roster
// permissive — a safe direction in isolation, but the same write raced a newer
// publication and erased it. Both are impossible here because the only writer of
// the decision is the request path, and it writes only from a probe that ANSWERED
// for the installed workspace.
func TestTimedOutProbeLeavesTheRosterDecisionStanding(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	writeInfraFile(t, filepath.Join(root, ".evener", "mcp.json"),
		`{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	local := execenv.NewLocalExecutionEnvironment(root)
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	env := stalledGitEnv{ExecutionEnvironment: local, started: make(chan struct{})}

	s := &Session{env: env}
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, evidence)
	s.setRosterExclusion(true)

	// A bound far below the stall, so the probe returns while the read is stuck.
	ctx, cancel := context.WithTimeout(context.Background(), freshnessRequestBound)
	defer cancel()
	if s.probeCLIEvidenceFresh(ctx, s.currentEnv()) {
		t.Fatal("a probe that could not finish answered true; an unobserved layer is unknown, never current")
	}
	select {
	case <-env.started:
	default:
		t.Fatal("the probe never reached the git call, so this fixture did not exercise the stall it claims to")
	}

	if !s.rosterExclusion() {
		t.Error("a timed-out probe cleared the standing roster decision")
	}
}

// TestProbeReturnsTheAnswerToItsCaller pins that the probe is a QUESTION with an
// ANSWER, not a state transition.
//
// This is the property that makes the whole design work, and it is worth its own
// test because every previous round lost it: the probe returned nothing and
// published instead, so its only observable effect was on shared state — which is
// exactly what made the ordering cases possible.
func TestProbeReturnsTheAnswerToItsCaller(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	layer := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, layer, `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	s := &Session{env: env}
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, evidence)

	if !s.probeCLIEvidenceFresh(context.Background(), env) {
		t.Fatal("the probe did not report current evidence for the workspace the evidence describes")
	}
	// The answer did not become state: the roster decision is still the safe
	// default, so a later caller is free to reach its own answer.
	if s.rosterExclusion() {
		t.Error("the probe wrote the roster decision; only the request path may, under s.mu, after confirming the workspace is still installed")
	}

	// The other direction: evidence for a layer that has since changed answers
	// false, and again writes nothing.
	writeInfraFile(t, layer, `{"mcpServers":{"svc":{"command":"/bin/changed"}}}`, 0o644)
	if s.probeCLIEvidenceFresh(context.Background(), env) {
		t.Error("the probe reported current evidence for a layer that changed")
	}
	if s.rosterExclusion() {
		t.Error("the probe wrote the roster decision")
	}
}

// TestProbeSkipsWorkItCouldNotUse pins the two short-circuits, which are what
// keep the request path from paying for an answer nothing reads.
//
// Neither is a behavioural nicety: with no classified CLI names the exclusion
// could not remove anything, and with an unverifiable plugin source the CLI half
// is never applied. In both cases a probe on every model round would be pure
// cost, and on a stalled mount it would be the full budget of it.
func TestProbeSkipsWorkItCouldNotUse(t *testing.T) {
	t.Parallel()

	// Nothing classified: no probe, and the answer is the safe one.
	noNames := &Session{}
	if noNames.probeCLIEvidenceFresh(context.Background(), nil) {
		t.Error("a session with no classified CLI names probed anyway")
	}

	// A configured plugin directory: the child reloads it from disk, so the CLI
	// half cannot be applied and the probe's answer would be discarded.
	withPlugin := &Session{cfg: SessionConfig{PluginDirs: []string{t.TempDir()}}}
	withPlugin.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	if withPlugin.probeCLIEvidenceFresh(context.Background(), nil) {
		t.Error("a session with an unverifiable plugin source probed anyway")
	}

	// A plugin config, even with no configured directory, is the same case.
	withPluginConfig := &Session{pluginMCPConfigs: []mcpconfig.ServerConfig{{Name: "plugin_svc"}}}
	withPluginConfig.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	if withPluginConfig.probeCLIEvidenceFresh(context.Background(), nil) {
		t.Error("a session whose plugin MCP configs cannot be fingerprinted probed anyway")
	}
}

// TestProbeAgainstAChangedWorkspaceAnswersFalse is the workspace-scoping half,
// stated directly on the helper the two callers share.
//
// A session that switches branches in place, or has a repository initialized
// around it, keeps its environment, so nothing else would notice that a layer now
// declares a server the startup snapshot never saw. The probe must answer for the
// WORKSPACE it is given, not for the evidence's own workspace.
func TestProbeAgainstAChangedWorkspaceAnswersFalse(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	layer := filepath.Join(root, ".evener", "mcp.json")
	writeInfraFile(t, layer, `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	env := execenv.NewLocalExecutionEnvironment(root)
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(env, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}

	s := &Session{env: env}
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, evidence)
	if !s.probeCLIEvidenceFresh(context.Background(), env) {
		t.Fatal("fixture is wrong: the probe did not verify unchanged evidence, so the negative below proves nothing")
	}

	// The layer gains a server the child could discover.
	writeInfraFile(t, layer, `{"mcpServers":{"svc":{"command":"/bin/a"},"other":{"command":"/bin/c"}}}`, 0o644)
	if s.probeCLIEvidenceFresh(context.Background(), env) {
		t.Error("the probe reported current evidence after the project layer changed, so the roster would keep excluding a name that layer makes reachable")
	}

	// A different workspace entirely: the evidence describes the first one.
	other := execenv.NewLocalExecutionEnvironment(realTempDirForTest(t))
	if s.probeCLIEvidenceFresh(context.Background(), other) {
		t.Error("the probe reported current evidence for a workspace the evidence does not describe")
	}
}
