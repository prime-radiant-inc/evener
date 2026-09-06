package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/identifier"
)

// reserveWorktreeIsolatedDelegate takes a create reservation for a delegate
// spawned with isolation:"worktree", which is what prepareIsolation needs
// before it can cut the lane.
func reserveWorktreeIsolatedDelegate(t *testing.T, root *Session, task string) (delegateRuntime, *delegateStartReservation, identifier.Project) {
	t.Helper()
	runtime := delegateRuntime{owner: root}
	ctx := context.Background()
	args := delegateArgs{Task: task, Isolation: "worktree", DelegationAllowance: new(0)}
	selection, err := root.selectSubagentModel(ctx, args.Model, args.AgentType)
	if err != nil {
		t.Fatalf("selectSubagentModel: %v", err)
	}
	toolNameCeiling := root.stableDelegateEffectiveToolNameCeiling(selection, args, args.Isolation)
	descriptor, project, err := runtime.describe(ctx, args, args.Task, args.Isolation, nil, selection, toolNameCeiling)
	if err != nil {
		t.Fatalf("describe delegate: %v", err)
	}
	reservation, err := root.delegateController.ReserveCreate(rootDelegateActor(root.ID()), descriptor)
	if err != nil {
		t.Fatalf("ReserveCreate: %v", err)
	}
	return runtime, reservation, project
}

// assertNoDelegateLane checks that nothing of an isolation lane for delegateID
// remains: no worktree, no branch, no sidecar.
func assertNoDelegateLane(t *testing.T, r *wtRepo, delegateID, lanePath string) {
	t.Helper()
	if laneWorktreePresent(lanePath) {
		t.Errorf("the isolation lane %s was left behind", lanePath)
	}
	if r.branchExists(t, delegateID) {
		t.Errorf("the branch %q cut for the isolation lane was left behind", delegateID)
	}
	sidecar := filepath.Join(metaDirForLane(lanePath), worktree.EncodeSidecarName(delegateID)+".json")
	if _, err := os.Stat(sidecar); err == nil {
		t.Errorf("the sidecar %s was left behind", sidecar)
	}
}

// Cutting a delegate's isolation lane forks git on the PARENT's environment,
// the one Close reaps the process table of. It is not a manage_worktree
// operation, so the dispatch's close fence never sees it: without an admission
// of its own, a close that begins while the lane's git is in flight walks
// straight past it and cleans the environment underneath, leaving a
// half-created lane nobody owns.
func TestDelegateIsolation_CloseWaitsForTheLaneCreateItRaces(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	runtime, reservation, project := reserveWorktreeIsolatedDelegate(t, root, "close under the lane create")
	lanePath := reservation.worktreePath

	closeBegun := make(chan struct{})
	closeDone := make(chan struct{})
	envCleaned := make(chan struct{})
	var cleanedOnce sync.Once
	var held, cleanupDuringCreate atomic.Bool

	root.cfg.testOnly.closeAfterDisposeSweepJoin = func() { close(closeBegun) }
	root.cfg.testOnly.envCleanupObserved = func(execenv.ExecutionEnvironment) {
		cleanedOnce.Do(func() { close(envCleaned) })
	}
	root.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		inner := gitRunner(ctx, env)
		return func(args ...string) (string, error) {
			if len(args) >= 2 && args[0] == "worktree" && args[1] == "add" && held.CompareAndSwap(false, true) {
				go func() {
					defer close(closeDone)
					root.Close()
				}()
				<-closeBegun
				select {
				case <-envCleaned:
					cleanupDuringCreate.Store(true)
				case <-time.After(closeFenceProbe):
				}
			}
			return inner(args...)
		}
	}

	isolation, err := runtime.prepareIsolation(context.Background(), reservation, project, nil)
	if !held.Load() {
		t.Fatal("the create never reached git worktree add; the test observed nothing")
	}
	<-closeDone

	if cleanupDuringCreate.Load() {
		t.Error("the close cleaned the environment while the delegate lane's create still held its git")
	}
	if err != nil {
		assertNoDelegateLane(t, r, reservation.delegateID, lanePath)
		return
	}
	t.Cleanup(func() { isolation.cleanup(root, reservation.delegateID) })
	if !laneWorktreePresent(isolation.worktreePath) {
		t.Errorf("the create reported success but left no lane at %s", isolation.worktreePath)
	}
}

// A spawn that reaches the isolation step once the close has begun cannot be
// fenced — the close may already be past its join — so cutting the lane would
// add a locked worktree, a branch and a sidecar against an environment being
// reaped and stores being closed. The refusal has to reach the caller with no
// git run at all.
func TestDelegateIsolation_LaneCreateAfterCloseBeganIsRefused(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	runtime, reservation, project := reserveWorktreeIsolatedDelegate(t, root, "spawn after close began")
	lanePath := reservation.worktreePath

	root.Close()

	var calls atomic.Int64
	root.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		inner := gitRunner(ctx, env)
		return func(args ...string) (string, error) {
			calls.Add(1)
			return inner(args...)
		}
	}

	isolation, err := runtime.prepareIsolation(context.Background(), reservation, project, nil)
	if !errors.Is(err, errWorktreeOpWhileClosing) {
		t.Fatalf("isolation prepared while closing = %v, want the closing refusal", err)
	}
	if isolation.worktreePath != "" || isolation.env != nil {
		t.Errorf("refused isolation = %+v, want nothing built", isolation)
	}
	if got := calls.Load(); got != 0 {
		t.Errorf("the refused create ran %d git commands, want none", got)
	}
	assertNoDelegateLane(t, r, reservation.delegateID, lanePath)
}

// A failure arm of the isolation step owes the lane's admission a release the
// moment its rollback returns: nothing later in the spawn will ever release it.
// A leak stays invisible until a close arrives, and then the fence join waits
// out the whole cascade budget on work that finished long ago and cleans the
// environment under a warning naming a lane already taken back.
func TestDelegateIsolation_FailedIsolationReleasesTheLaneAdmission(t *testing.T) {
	r := newWorktreeRepo(t)
	root := r.s
	runtime, reservation, project := reserveWorktreeIsolatedDelegate(t, root, "isolation environment failure")
	lanePath := reservation.worktreePath
	// Fail the delegate's environment step, which is the arm that rolls the
	// just-cut lane back from inside the admission the cut was made under.
	root.cfg.testOnly.subagentPrepareFault = func(point string) error {
		if point == "working_dir_env" {
			return errors.New("this environment does not support a working_dir override")
		}
		return nil
	}
	warnings := collectWarningsUntilClosed(root)

	if _, err := runtime.prepareIsolation(context.Background(), reservation, project, nil); err == nil {
		t.Fatal("prepareIsolation succeeded with the delegate's environment step faulted, want a failure")
	}
	assertNoDelegateLane(t, r, reservation.delegateID, lanePath)

	// Bound the close only now: the rollback above needs the production budget
	// for its own git, while the close needs a deadline short enough that
	// waiting one out is a test failure rather than a slow test.
	shortenCloseCascadeBudget(t, 200*time.Millisecond)
	root.Close()

	if got := fenceWarnings(<-warnings); len(got) != 0 {
		t.Errorf("the close waited out its budget on an admission the failed isolation step never released: %q", got)
	}
}
