package agent

import (
	"context"
	"slices"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// delegationDisabledProbeTripwire is far shorter than the request budget on
// purpose: the assertion is that no probe runs at all, so a session that runs
// one lands here instead of waiting out exclusionFreshnessRequestBudget.
const delegationDisabledProbeTripwire = 250 * time.Millisecond

// delegatingProbeTripwire bounds the opposite direction: how long a session that
// CAN delegate may take to resolve the roster decision. The probe it waits on is
// a filesystem read of two config layers plus one git fork in a fixture with
// neither, so a passing run finishes in milliseconds; this only fires on a
// genuine hang.
const delegatingProbeTripwire = 10 * time.Second

// TestDelegationDisabledSessionSkipsTheFreshnessProbe pins that a session which
// can never offer a delegate does not pay for the delegate-reachability decision.
//
// With no delegation there is no ceiling, no roster and no delegate schema: the
// decision cannot change a byte the session sends. The probe still reads and
// hashes up to two config layers and forks git for the root, and a project or
// global config that stalls would charge that session the full twelve-second
// budget on every round for an answer nothing reads.
func TestDelegationDisabledSessionSkipsTheFreshnessProbe(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	local := execenv.NewLocalExecutionEnvironment(realTempDirForTest(t))
	stalled := stalledGitEnv{ExecutionEnvironment: local, started: make(chan struct{})}

	s := &Session{env: stalled}
	// Nothing to delegate to: no allowance, so hasConfiguredDelegateCapability
	// is false and no surface this session exposes can carry the decision.
	s.delegationAllowance = 0
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})

	returned := make(chan struct{})
	go func() {
		s.refreshCachesForExclusionChangeContext(context.Background())
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(delegationDisabledProbeTripwire): // TRIPWIRE: a pointless probe ran
		t.Fatal("a session with delegation disabled probed delegate reachability, whose answer cannot affect any surface it exposes")
	}
	select {
	case <-stalled.started:
		t.Error("a session with delegation disabled reached the git probe")
	default:
	}
}

// TestDelegatingSessionStillProbes is the other direction: the skip is a gate on
// the delegation capability, not a refusal to ever probe. A session that CAN
// delegate must still resolve the decision, so the exclusion still applies.
func TestDelegatingSessionStillProbes(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	env := execenv.NewLocalExecutionEnvironment(root)
	s := newSession(t, withEnv(env), withConfig(SessionConfig{MaxSubagentDepth: 2}))
	if !s.hasConfiguredDelegateCapability() {
		t.Fatal("fixture is wrong: the session cannot delegate, so the delegation gate would return before the probe")
	}
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	// Evidence captured from a DIFFERENT workspace, so a probe that runs answers
	// "not current" — the observable half of having probed at all.
	evidence := evidenceForOtherWorkspace(t)
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidence)
	// The decision starts applied, so the probe's "not current" answer must CLEAR
	// it: a session that skipped the probe would leave the stale denial standing.
	s.setRosterExclusion(true)

	done := make(chan struct{})
	go func() {
		s.refreshCachesForExclusionChangeContext(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(delegatingProbeTripwire): // TRIPWIRE: a delegating session never probed
		t.Fatal("a delegating session never resolved its reachability decision, so the exclusion would never lift")
	}
	if s.rosterExclusion() {
		t.Error("a delegating session kept a decision its own probe contradicts, so it did not probe the installed workspace")
	}
}

// TestPluginMCPSourceSkipsTheFreshnessProbe pins the third gate: a session whose
// plugin sources the evidence cannot verify does not probe at all.
//
// A child reloads every plugin directory from disk, so a plugin-supplied server
// can make a name this session believes is CLI-only reachable, and no fingerprint
// of the global and project layers can rule that out. With such a source present
// the CLI half of the exclusion is never applied, so the probe's answer cannot
// change a ceiling this session builds — and the check still reads and hashes two
// config layers and forks git for the root on every model round. The Manager-only
// half is not part of that answer and must keep applying.
func TestPluginMCPSourceSkipsTheFreshnessProbe(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	const cliTool = "svc__echo"
	s := newSession(t, withConfig(SessionConfig{
		MaxSubagentDepth: 2,
		// An empty but CONFIGURED directory: the child scans it again, so a
		// server it gains during the session is one this session never saw.
		PluginDirs: []string{t.TempDir()},
	}))
	if !s.hasConfiguredDelegateCapability() {
		t.Fatal("fixture is wrong: the session cannot delegate, so the delegation gate would return before the probe")
	}
	s.setCLIMCPToolNames(map[string]bool{cliTool: true})
	s.setManagerOnlyTools(map[string]bool{"session_control": true})
	// Evidence that would answer "not current" if it were ever consulted, so a
	// probe's effect would be visible as a cleared decision.
	s.setCLIMCPServers(map[string]bool{"svc": true}, nil, evidenceForOtherWorkspace(t))
	s.setRosterExclusion(true)

	// The ceiling is asserted BEFORE the refresh, because the refresh rebuilds the
	// tool-defs cache and that recomposes the hub registry, which re-derives
	// managerOnlyTools from the session's own configuration. Reading the ceiling
	// afterwards would test the recomposition, not this gate.
	//
	// What the gate must not have cost: the CLI-only name stays eligible for a
	// child, because the plugin source makes its reachability unknown.
	got := s.removeChildUnreachableTools([]string{cliTool, "session_control"}, "", true)
	if !slices.Contains(got, cliTool) {
		t.Errorf("the unverifiable plugin source did not keep the CLI-only name eligible: %v", got)
	}
	// And the Manager-only half still applies: no spawn path composes the hub,
	// whatever the plugins declare, and that answer never came from the probe.
	if slices.Contains(got, "session_control") {
		t.Errorf("the Manager-only name survived a plugin source: %v", got)
	}

	returned := make(chan struct{})
	go func() {
		s.refreshCachesForExclusionChangeContext(context.Background())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(delegatingProbeTripwire): // TRIPWIRE: an unverifiable plugin source still probed
		t.Fatal("a session with an unverifiable plugin source waited on a probe whose answer its ceiling never reads")
	}
}

// evidenceForOtherWorkspace returns real evidence captured from a workspace this
// test's session does not hold, so any probe against the session's own
// environment answers "not current".
func evidenceForOtherWorkspace(t *testing.T) mcpconfig.ConfigEvidence {
	t.Helper()
	other := execenv.NewLocalExecutionEnvironment(realTempDirForTest(t))
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(other, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	return evidence
}
