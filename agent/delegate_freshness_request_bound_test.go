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
// The refresh probes the CLI-MCP layers before every model request. That probe
// reads files and forks git, and nothing in it observes cancellation, so an
// unbounded refresh hangs request preparation with no way for Stop to interrupt
// it. The refresh therefore runs under the turn's context, and treats the
// expired wait as UNKNOWN.
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
	writeInfraFile(t, root+"/.evener/mcp.json", `{"mcpServers":{"svc":{"command":"/bin/a"}}}`, 0o644)
	local := execenv.NewLocalExecutionEnvironment(root)
	// REAL evidence, captured from this environment: a hand-built value leaves the
	// global layer's recorded path empty while hermeticInfraEnv resolves a nonempty
	// one, so the check returns at the GLOBAL comparison and never reaches the git
	// call this test exists to bound. That is the false green the assertion below
	// on env.started rules out.
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	env := stalledGitEnv{
		// The wrapper, not the local environment: a *LocalExecutionEnvironment makes
		// the root resolution answer structurally and never fork git.
		ExecutionEnvironment: local,
		started:              make(chan struct{}),
	}

	// A REAL session, because the refresh rebuilds both caches after a flip and
	// that needs the profile and registry a session is built with. It is
	// constructed against the healthy environment and the stalled one is installed
	// afterwards, so session init does not pay a stall of its own.
	s := newSession(t, withEnv(local))
	s.mu.Lock()
	s.env = env
	s.mu.Unlock()
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, evidence)
	// The startup answer was the exclusion, which is the ordinary case this bound
	// protects: a session that has something to lose.
	s.setRosterExclusion(true)

	// A short bound, so the test measures the BOUND rather than the suite's
	// patience. Production uses exclusionFreshnessRequestBudget; what is under
	// test is that the bound exists and comes from the caller's context.
	ctx, cancel := context.WithTimeout(context.Background(), freshnessRequestBound)
	defer cancel()

	returned := make(chan struct{})
	go func() {
		s.refreshCachesForExclusionChangeContext(ctx)
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

	// A probe that never finished established nothing, so the answer is UNKNOWN
	// and the names stay eligible. Recording "not fresh" as a decision here would
	// let a stalled workspace permanently exclude a tool the child can register.
	if s.rosterExclusion() {
		t.Error("a probe that could not finish cleared the roster decision; unknown reachability must leave the names eligible")
	}
}

// TestSpawnProbeIsBoundedByTheCallersContext pins the same bound on the SPAWN
// path, which derives it from the delegate call's own context.
//
// A delegate tool call runs on the turn's context. A spawn probe that ignored it
// would sit on a check stalled against a dead mount until the filesystem
// answered — past Stop cancelling the turn. The ceiling still gets built, with
// reachability unknown, and the spawn proceeds.
func TestSpawnProbeIsBoundedByTheCallersContext(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	runInfraGit(t, root, "init", "-q")
	local := execenv.NewLocalExecutionEnvironment(root)
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidenceForProjectEnv(local, nil, nil, nil, false, false)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	env := stalledGitEnv{ExecutionEnvironment: local, started: make(chan struct{})}

	s := &Session{env: env}
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})
	s.setCLIMCPServers(map[string]bool{"svc": true}, evidence)

	// The waiter's own bound is short. A probe through Background() ignores it and
	// blocks here instead.
	ctx, cancel := context.WithTimeout(context.Background(), freshnessRequestBound)
	defer cancel()

	returned := make(chan struct{})
	go func() {
		s.probeExclusionForSpawn(ctx, "")
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(10 * time.Second): // TRIPWIRE: the waiter ignored its caller's context
		t.Fatal("a spawn probe outlived its caller's context: a delegate call can be held past Stop by a probe it started")
	}
}

// TestWorktreeSpawnDoesNotProbeAtAll pins that a fresh lane does not pay for an
// answer its ceiling ignores.
//
// A worktree ceiling never applies the CLI-MCP half (removeChildUnreachableTools),
// so probing for it made every spawn wait on a check whose result it would
// discard. Against a config that had been readable and then stalled, that wait
// ends only when the mount answers or the user presses Stop.
func TestWorktreeSpawnDoesNotProbeAtAll(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	local := execenv.NewLocalExecutionEnvironment(realTempDirForTest(t))
	stalled := stalledGitEnv{ExecutionEnvironment: local, started: make(chan struct{})}

	s := &Session{env: stalled}
	s.setCLIMCPToolNames(map[string]bool{"svc__echo": true})

	returned := make(chan bool, 1)
	go func() {
		returned <- s.probeExclusionForSpawn(context.Background(), "worktree")
	}()
	select {
	case got := <-returned:
		if got {
			t.Error("a worktree spawn reported an exclusion; its lane may declare any server")
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: a worktree spawn probed
		t.Fatal("a worktree spawn probed reachability, whose answer its ceiling ignores")
	}
	select {
	case <-stalled.started:
		t.Error("a worktree spawn reached the git call: it probed for an answer it discards")
	default:
	}
}
