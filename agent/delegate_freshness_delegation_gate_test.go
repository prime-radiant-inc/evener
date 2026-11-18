package agent

import (
	"context"
	"testing"
	"time"
)

// delegationDisabledProbeTripwire is far shorter than the request budget on
// purpose: the assertion is that no probe runs at all, so a session that runs
// one lands here instead of waiting out exclusionFreshnessRequestBudget.
const delegationDisabledProbeTripwire = 250 * time.Millisecond

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
	case <-time.After(10 * time.Second):
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
