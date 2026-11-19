package agent

import (
	"context"
	"slices"
	"testing"
	"time"
)

// delegationDisabledProbeTripwire is far shorter than the request budget on
// purpose: the assertion is that no probe runs at all, so a session that runs
// one lands here instead of waiting out exclusionFreshnessRequestBudget.
const delegationDisabledProbeTripwire = 250 * time.Millisecond

// delegatingProbeTripwire bounds the opposite direction: how long a session that
// CAN delegate may take to resolve the freshness verdict. The probe it waits on
// is a filesystem read of two config layers plus one git fork in a fixture with
// neither, so a passing run finishes in milliseconds; this only fires on a
// genuine hang.
const delegatingProbeTripwire = 10 * time.Second

// TestDelegationDisabledSessionSkipsTheFreshnessProbe pins that a session which
// can never offer a delegate does not pay for the delegate-reachability verdict.
//
// With no delegation there is no ceiling, no roster and no delegate schema: the
// verdict cannot change a byte the session sends. The check still reads and
// hashes up to two config layers and forks git for the root, and a project or
// global config that stalls would charge that session the full twelve-second
// budget on every round for an answer nothing reads.
func TestDelegationDisabledSessionSkipsTheFreshnessProbe(t *testing.T) {
	t.Parallel()
	s := &Session{}
	// Nothing to delegate to: no allowance, so hasConfiguredDelegateCapability
	// is false and no surface this session exposes can carry the verdict.
	s.delegationAllowance = 0
	s.cliMCPToolNames = map[string]bool{"svc__echo": true}

	// Occupy the single-flight with a check that never answers, and make it the
	// caller's own revision so a probe would JOIN it rather than start its own:
	// that is the wait the finding names, reached without touching a filesystem.
	occupied := make(chan struct{})
	s.freshnessCheckMu.Lock()
	s.freshnessCheck = &exclusionFreshnessCheck{done: occupied, revision: s.cliMCPEnvRevision}
	s.freshnessCheckMu.Unlock()
	t.Cleanup(func() { close(occupied) })

	returned := make(chan struct{})
	go func() {
		s.refreshCachesForExclusionChangeContext(context.Background())
		close(returned)
	}()

	select {
	case <-returned:
	case <-time.After(delegationDisabledProbeTripwire): // TRIPWIRE: a pointless probe ran
		t.Fatal("a session with delegation disabled probed delegate reachability, whose verdict cannot affect any surface it exposes")
	}
}

// TestDelegatingSessionStillProbes is the other direction: the skip is a gate on
// the delegation capability, not a refusal to ever probe. A session that CAN
// delegate must still resolve the verdict, so the exclusion still applies.
func TestDelegatingSessionStillProbes(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	s := newSession(t, withConfig(SessionConfig{MaxSubagentDepth: 2}))
	s.cliMCPToolNames = map[string]bool{"svc__echo": true}

	// A check for another revision occupies the slot, so a session that wrongly
	// skipped the probe would leave that stale check in place.
	staleRevision := s.cliMCPEnvRevision + 7
	occupied := make(chan struct{})
	s.freshnessCheckMu.Lock()
	s.freshnessCheck = &exclusionFreshnessCheck{done: occupied, revision: staleRevision}
	s.freshnessCheckMu.Unlock()
	t.Cleanup(func() { close(occupied) })

	done := make(chan struct{})
	go func() {
		s.refreshCachesForExclusionChangeContext(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(delegatingProbeTripwire): // TRIPWIRE: a delegating session never probed
		t.Fatal("a delegating session never resolved its freshness verdict, so the exclusion would never apply")
	}
	// The probe replaced the stale slot rather than joining it.
	s.freshnessCheckMu.Lock()
	current := s.freshnessCheck
	s.freshnessCheckMu.Unlock()
	if current != nil && current.revision == staleRevision {
		t.Error("a delegating session joined a check for another revision instead of probing the installed one")
	}
}

// TestPluginMCPSourceSkipsTheFreshnessProbe pins the third gate: a session whose
// plugin sources the evidence cannot verify does not start OR join the probe.
//
// A child reloads every plugin directory from disk, so a plugin-supplied server
// can make a name this session believes is CLI-only reachable, and no
// fingerprint of the global and project layers can rule that out. With such a
// source present the CLI half of the exclusion is never applied
// (childExclusionDecision), so the verdict cannot change a ceiling this session
// builds — and the check still reads and hashes two config layers and forks git
// for the root on every model round. The Manager-only half is not part of that
// verdict and must keep applying.
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

	// Warm caches, as a session that has served a round already has. The call
	// under test is the probe gate, not the cache rebuild: with both stamps
	// current the refresh has nothing to rebuild, so the Manager-only set this
	// fixture recorded is exactly what the ceiling below reads.
	s.mu.Lock()
	s.cachedExclusionVersion = s.exclusionVersion()
	s.cachedPromptExclusionVersion = s.cachedExclusionVersion
	s.mu.Unlock()

	// A check for this session's OWN revision occupies the slot, so a session
	// that sampled would JOIN it and block: that is the wait the finding names,
	// reached without touching a filesystem.
	occupied := make(chan struct{})
	s.freshnessCheckMu.Lock()
	s.freshnessCheck = &exclusionFreshnessCheck{done: occupied, revision: s.cliMCPEnvRevision}
	s.freshnessCheckMu.Unlock()
	t.Cleanup(func() { close(occupied) })

	returned := make(chan struct{})
	go func() {
		s.refreshCachesForExclusionChangeContext(context.Background())
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(delegatingProbeTripwire): // TRIPWIRE: an unverifiable plugin source still probed
		t.Fatal("a session with an unverifiable plugin source waited on a freshness probe whose verdict its ceiling never reads")
	}

	// The call neither joined the check nor replaced it with one of its own.
	s.freshnessCheckMu.Lock()
	current := s.freshnessCheck
	s.freshnessCheckMu.Unlock()
	if current == nil || current.done != occupied {
		t.Error("the call touched the freshness single-flight although its verdict cannot be applied")
	}
	s.delegateExclusionsMu.RLock()
	known := s.cliMCPEvidenceFreshKnown
	s.delegateExclusionsMu.RUnlock()
	if known {
		t.Error("a verdict was published from a probe whose answer the ceiling discards")
	}

	// What the probe must not have cost: the CLI-only name stays eligible for a
	// child, because the plugin source makes its reachability unknown.
	got := s.removeChildUnreachableTools([]string{cliTool, "session_control"}, "")
	if !slices.Contains(got, cliTool) {
		t.Errorf("the unverifiable plugin source did not keep the CLI-only name eligible: %v", got)
	}
	// And the Manager-only half still applies: no spawn path composes the hub,
	// whatever the plugins declare, and that answer never came from the probe.
	if slices.Contains(got, "session_control") {
		t.Errorf("the Manager-only name survived a plugin source: %v", got)
	}
}
