package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// freshnessJoinBound is the waiter's own caller deadline, deliberately SHORT:
// this test measures that a join ends when its caller does, so the bound must
// expire while the joined check is still stalled. A generous bound would let a
// join that ignored its context pass.
const freshnessJoinBound = 100 * time.Millisecond

// TestCeilingDerivationWaitsOnTheCallersContext pins that the JOIN path is bounded
// by its own caller, not just the worker.
//
// The refresh runs under a bound, but a second caller that finds a check already
// in flight waits on it. If that wait uses context.Background(), a delegate tool
// call can sit on a check stalled against a dead mount until the filesystem
// answers — past Stop cancelling the turn. The single-flight is exactly where this
// matters: the worker may be legitimately long, and the WAITER must still end when
// its own caller does.
func TestCeilingDerivationWaitsOnTheCallersContext(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	local := execenv.NewLocalExecutionEnvironment(root)
	_, _, layers, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(local, nil, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	env := stalledGitEnv{
		ExecutionEnvironment: local,
		started:              make(chan struct{}),
	}

	s := &Session{env: env}
	s.delegateExclusionsMu.Lock()
	s.cliMCPEvidence = cliMCPClassificationEvidence{layers: layers}
	s.cliMCPToolNames = map[string]bool{"svc__echo": true}
	s.cliMCPExclusionKnown = true
	s.cliMCPExclusionApplied = true
	s.delegateExclusionsMu.Unlock()

	// Occupy the single-flight with a check that will never finish, so the call
	// under test is the WAITER — the path the finding names.
	occupied := make(chan struct{})
	released := make(chan struct{})
	s.freshnessCheckMu.Lock()
	s.freshnessCheck = &exclusionFreshnessCheck{done: occupied}
	s.freshnessCheckMu.Unlock()
	go func() {
		<-released
		close(occupied)
	}()
	t.Cleanup(func() { close(released) })

	// The waiter's own bound is short. A join through Background() ignores it and
	// blocks here instead.
	ctx, cancel := context.WithTimeout(context.Background(), freshnessJoinBound)
	defer cancel()

	returned := make(chan struct{})
	go func() {
		s.refreshCLIEvidenceFreshnessContext(ctx)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second): // TRIPWIRE: the waiter ignored its caller's context
		t.Fatal("a freshness waiter outlived its own caller's context: a delegate call can be held past Stop by a check it merely joined")
	}

	// The join that could not finish established nothing, so the verdict stays
	// unknown and the names stay eligible.
	s.delegateExclusionsMu.RLock()
	known := s.cliMCPEvidenceFreshKnown
	s.delegateExclusionsMu.RUnlock()
	if known {
		t.Error("a joined check that could not finish recorded a verdict; unknown must not be cached as a negative")
	}
}
