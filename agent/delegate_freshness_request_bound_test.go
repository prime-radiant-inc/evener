package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// freshnessRequestBound is deliberately SHORT: this test measures that the
// refresh ends when its caller's context does, against an environment that
// never answers, so the bound must expire while the check is still stalled.
const freshnessRequestBound = 100 * time.Millisecond

// stalledGitEnv is an environment whose git invocation never answers until its
// context ends, which is what a stalled workspace mount looks like to the
// freshness check: the call is made and the filesystem or the process never
// returns. It embeds a real local environment so every other call behaves.
type stalledGitEnv struct {
	execenv.ExecutionEnvironment
	// started is closed once git has been asked, so the test can assert the
	// check reached the I/O rather than returning at an earlier comparison.
	started chan struct{}
}

// ExecCommand is the method this environment actually uses: RunGit takes its
// ArgvExecutor fast path only when the environment implements it, and this one
// does not, so git arrives here. Blocking ExecArgv instead would leave the check
// free to answer in milliseconds and the test would pass with the refresh
// unbounded — the vacuous-fixture failure, not the property.
func (e stalledGitEnv) ExecCommand(ctx context.Context, _ string, _ int, _ string, _ map[string]string) (execenv.ExecResult, error) {
	select {
	case <-e.started:
	default:
		close(e.started)
	}
	// Block exactly as a stalled mount does: this never returns until the caller
	// gives up. Nothing in the underlying syscall observes cancellation, so the
	// bound must come from the caller's context.
	<-ctx.Done()
	return execenv.ExecResult{}, ctx.Err()
}

// TestRequestFreshnessRefreshIsBoundedByTheTurnContext pins that request
// preparation cannot be wedged by a workspace that stops answering.
//
// The freshness check resolves the Git root before every model request. That
// resolution blocks in the environment, and nothing in it observes cancellation,
// so an unbounded refresh hangs request preparation with no way for Stop to
// interrupt it. The refresh therefore runs under the turn's context, and reports
// UNKNOWN when that context ends.
//
// The assertion is that the call RETURNS once its bound passes, and that it
// leaves the names eligible rather than recording a negative it never observed.
// A fixture that never reached the git call would pass with the refresh
// unbounded, so the test also asserts the call was actually made.
func TestRequestFreshnessRefreshIsBoundedByTheTurnContext(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	local := execenv.NewLocalExecutionEnvironment(root)
	// REAL evidence, captured from this environment: a hand-built value leaves the
	// global layer's recorded path empty while hermeticInfraEnv resolves a nonempty
	// one, so the check returns at the GLOBAL comparison and never reaches the git
	// call this test exists to bound. That is the false green the assertion below
	// on env.started rules out.
	_, _, layers, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	env := stalledGitEnv{
		// The wrapper, not the local environment: a *LocalExecutionEnvironment makes
		// the root resolution answer structurally and never fork git.
		ExecutionEnvironment: local,
		started:              make(chan struct{}),
	}

	s := &Session{env: env}
	s.delegateExclusionsMu.Lock()
	s.cliMCPEvidence = cliMCPClassificationEvidence{layers: layers}
	// A classified name makes the refresh run at all, and an applied exclusion
	// makes its answer matter: this is the ordinary path a session with CLI-only
	// tools takes on every request.
	s.cliMCPToolNames = map[string]bool{"svc__echo": true}
	s.cliMCPExclusionKnown = true
	s.cliMCPExclusionApplied = true
	s.delegateExclusionsMu.Unlock()

	// A short bound, so the test measures the BOUND rather than the suite's
	// patience. Production uses exclusionFreshnessRequestBudget; what is under
	// test is that the bound exists and comes from the caller's context.
	ctx, cancel := context.WithTimeout(context.Background(), freshnessRequestBound)
	defer cancel()

	returned := make(chan struct{})
	go func() {
		s.refreshCLIEvidenceFreshnessContext(ctx)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second): // TRIPWIRE: an unbounded refresh, not a slow environment
		t.Fatal("request freshness refresh outlived its context against a stalled environment: preparation can be wedged with no interruption")
	}
	select {
	case <-env.started:
	default:
		t.Fatal("the refresh never reached the git call, so this fixture did not exercise the stall it claims to")
	}

	// A check that never finished established nothing, so the verdict is UNKNOWN
	// and the names stay eligible. Recording "not fresh" here would let a stalled
	// workspace permanently exclude a tool the child can register.
	s.delegateExclusionsMu.RLock()
	known := s.cliMCPEvidenceFreshKnown
	fresh := s.cliMCPEvidenceFresh
	applied := s.cliMCPExclusionApplied
	s.delegateExclusionsMu.RUnlock()
	if known {
		t.Errorf("a check that could not finish recorded a verdict (fresh=%v); unknown reachability must not be cached as a negative", fresh)
	}
	if !applied {
		t.Error("the applied exclusion was cleared by a check that never observed anything")
	}
}
