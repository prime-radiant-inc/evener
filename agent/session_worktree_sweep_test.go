package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/agenttest"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// These cover P3 automatic residue collection (auto-delegate-lane-disposal spec
// §P3): the no-model-in-the-loop sweep that reclaims other sessions'
// cleanly-closed, unlocked, D0-auto-collectible delegate lanes.
//
// This file is MIXED across the two lane harnesses; see docs/testing.md for the
// rule. Most tests' subject is serf's own sweep DECISION — which skip rung fires
// (locked, in-grace, not-a-delegate), which arm collects, what it wrote to its
// own jobstore, how the budget and the open timer gate the pass — so they run on
// the scripted git boundary (scriptedLaneRepo), where a lane is collectible via
// the Unchanged arm (tip == recorded base) and the per-lane git failures the
// sweep reacts to are injected at the boundary. These four stay on real git
// (wtRepo) because their subject IS git's own behavior:
//
//   - TestP3Sweep_CollectsUnlockedMergedForeignLanePastGrace — a real ff-merge
//     ancestry verdict over real commits
//   - TestP3Sweep_CherryOnlyMergedNeverCollected — real patch-equivalence
//     divergence, which only real `git cherry` and a real ancestry answer express
//   - TestP3Sweep_TwoConcurrentPassesCollectOnce — two passes racing the real
//     registry, serialized by git's own index locking
//   - TestP3CloseResidue_JoinsInFlightOpenPass — concurrent open+close git ops on
//     one real repository

// unlockLane releases a lane's serf:dlg lock directly with git, simulating the
// foreign session's close-time unlock that leaves the lane as P3 residue.
func (r *wtRepo) unlockLane(t *testing.T, path string) {
	t.Helper()
	wtGit(t, r.mainRoot, "worktree", "unlock", path)
}

// seedForeignUnlockedLane creates a delegate lane whose job record this session
// does NOT own (createDelegateWorktree records no job), then unlocks it — the
// shape a foreign session leaves behind at its clean close. Returns the delegate
// id, path, and recorded base SHA.
func (r *wtRepo) seedForeignUnlockedLane(t *testing.T) (delegateID, lanePath, baseSHA string) {
	t.Helper()
	delegateID = jobstore.NewDelegateID()
	path, _, base, _, _, err := r.s.createDelegateWorktree(context.Background(), delegateID)
	if err != nil {
		t.Fatalf("createDelegateWorktree: %v", err)
	}
	r.unlockLane(t, path)
	return delegateID, path, base
}

// commitAndFastForwardMerge commits work in the lane, then fast-forwards main to
// the lane tip so the lane's commits are ancestry-reachable from refs/heads/main.
func (r *wtRepo) commitAndFastForwardMerge(t *testing.T, delegateID, lanePath string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(lanePath, "work.txt"), []byte("done\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, lanePath, "add", "work.txt")
	wtGit(t, lanePath, "commit", "-m", "lane work")
	wtGit(t, r.mainRoot, "merge", "--ff-only", delegateID)
}

func (r *wtRepo) ageBeyondGrace(t *testing.T, delegateID string) {
	t.Helper()
	ageSidecar(t, r.metaDir(t, r.canonicalMain(t)), delegateID, laneGrace+time.Minute)
}

func (r *wtRepo) lanePresent(path string) bool {
	_, err := os.Stat(filepath.Join(path, ".git"))
	return err == nil
}

// rawStoreMentions reports whether the delegate id appears anywhere in sess's
// durable jobs.jsonl — used to prove a foreign lane earned NO Disposed mark. The
// jobstore is serf's own state and stays a real file under either harness.
func rawStoreMentions(t *testing.T, sess *Session, delegateID string) bool {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sess.jobManager.dir, "jobs.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return false
		}
		t.Fatalf("read store: %v", err)
	}
	return strings.Contains(string(b), delegateID)
}

// TestP3Sweep_CollectsUnlockedMergedForeignLanePastGrace (spec test 21/22 core):
// an unlocked, ancestry-merged foreign delegate lane past grace is collected by
// the residue sweep (worktree + branch + sidecar gone).
//
// REAL git: the collectibility verdict is a real ancestry answer over a real
// ff-merge.
func TestP3Sweep_CollectsUnlockedMergedForeignLanePastGrace(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, path, _ := r.seedForeignUnlockedLane(t)
	r.commitAndFastForwardMerge(t, id, path)
	r.ageBeyondGrace(t, id)

	r.s.runLaneResidueSweep(context.Background())

	if r.lanePresent(path) {
		t.Error("merged unlocked foreign lane past grace was not collected")
	}
	if r.branchExists(t, id) {
		t.Error("lane branch survived residue collection")
	}
	if _, err := worktree.ReadSidecar(r.metaDir(t, r.canonicalMain(t)), id); err == nil {
		t.Error("lane sidecar survived residue collection")
	}
}

// TestP3Sweep_UnchangedForeignLaneCollected: an unlocked, never-advanced
// (tip==base) foreign lane past grace is collectible via the Unchanged arm.
func TestP3Sweep_UnchangedForeignLaneCollected(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, id, path)

	r.s.runLaneResidueSweep(context.Background())

	if r.lanePresent(path) {
		t.Error("unchanged unlocked foreign lane past grace was not collected")
	}
}

// TestP3Sweep_WithinGraceSkipped (spec test 27 grace arm): a fresh sidecar
// (within laneGrace) is left untouched, however collectible otherwise.
func TestP3Sweep_WithinGraceSkipped(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)
	// Unlocked and unchanged, so collectible on every rung BUT grace. No
	// ageBeyondGrace: the sidecar mtime is fresh (< laneGrace).

	r.s.runLaneResidueSweep(context.Background())

	if !r.lanePresent(path) {
		t.Error("within-grace lane was collected; grace must protect the hand-off window")
	}
	if !r.branchExists(t, id) {
		t.Error("within-grace lane branch deleted")
	}
}

// TestP3Sweep_LockedLaneSkipped (spec test 27): a locked lane (a still-live or
// crashed session's lock) is never collected by P3.
func TestP3Sweep_LockedLaneSkipped(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, id, path)
	// Re-lock it (foreign occupancy) — P3 collects UNLOCKED lanes only.
	r.setLaneLock(t, path, "serf:sess:other")

	r.s.runLaneResidueSweep(context.Background())

	if !r.lanePresent(path) {
		t.Error("locked lane was collected; P3 must skip any locked lane")
	}
}

// TestP3Sweep_CherryOnlyMergedNeverCollected (spec test 22): a lane
// patch-equivalent to main but NOT an ancestor of it is never collected by P3.
//
// REAL git: the whole point is the gap between real patch-equivalence and real
// ancestry over a real cherry-pick, which no model of git can express.
func TestP3Sweep_CherryOnlyMergedNeverCollected(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, path, _ := r.seedForeignUnlockedLane(t)

	if err := os.WriteFile(filepath.Join(path, "work.txt"), []byte("patch\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, path, "add", "work.txt")
	wtGit(t, path, "commit", "-m", "lane work")
	laneTip := strings.TrimSpace(wtGit(t, path, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(r.mainRoot, "other.txt"), []byte("other\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	wtGit(t, r.mainRoot, "add", "other.txt")
	wtGit(t, r.mainRoot, "commit", "-m", "diverge main")
	wtGit(t, r.mainRoot, "cherry-pick", laneTip)
	r.ageBeyondGrace(t, id)

	r.s.runLaneResidueSweep(context.Background())

	if !r.lanePresent(path) {
		t.Error("cherry-only-merged lane wrongly collected by P3")
	}
	if !r.branchExists(t, id) {
		t.Error("cherry-only-merged lane branch deleted; must stay resumable")
	}
}

// TestP3Sweep_ManagedNonDelegateUntouched (spec test 27): an unlocked, merged
// MANAGED (non-delegate) worktree carries no DelegateID and is never P3 residue.
func TestP3Sweep_ManagedNonDelegateUntouched(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	path := r.addManagedLane(t, "managed-lane") // unchanged, unlocked, no DelegateID
	r.ageBeyondGrace(t, "managed-lane", path)

	r.s.runLaneResidueSweep(context.Background())

	if !r.lanePresent(path) {
		t.Error("managed non-delegate worktree collected by P3; delegate lanes only")
	}
}

// TestP3Sweep_OwnRecordMarkedForeignNotMarked (spec test 25): a collected lane
// whose record lives in THIS session's store earns a durable Disposed mark; a
// foreign lane (no record) earns none.
func TestP3Sweep_OwnRecordMarkedForeignNotMarked(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)

	ownID, ownPath := r.seedIsolationLane(t) // records an owned delegate job
	r.unlockLane(t, ownPath)
	r.ageBeyondGrace(t, ownID, ownPath)

	foreignID, foreignPath := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, foreignID, foreignPath)

	r.s.runLaneResidueSweep(context.Background())

	if r.lanePresent(ownPath) {
		t.Error("own unlocked unchanged lane past grace not collected")
	}
	if !r.disposedEventPresent(t, ownID) {
		t.Error("own-store record not marked Disposed after collection")
	}
	if r.lanePresent(foreignPath) {
		t.Error("foreign lane not collected")
	}
	if rawStoreMentions(t, r.s, foreignID) {
		t.Error("foreign lane earned a Disposed mark; P3 marks own-store records only")
	}
}

// TestP3Sweep_OrphanBranchAndSidecarReclaimed (spec test 28): a delegate branch
// + sidecar with no registered worktree (a crash between remove and branch -D)
// is reclaimed by the sweep-2 arm.
func TestP3Sweep_OrphanBranchAndSidecarReclaimed(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)
	// Remove the worktree WITHOUT deleting the branch or sidecar — the orphan
	// shape a crash strands.
	metaDir := metaDirForLane(path)
	r.ageBeyondGrace(t, id, path)
	r.removeLane(t, path)

	r.s.runLaneResidueSweep(context.Background())

	if r.branchExists(t, id) {
		t.Error("orphan branch not reclaimed by the sweep-2 arm")
	}
	if _, err := worktree.ReadSidecar(metaDir, id); err == nil {
		t.Error("orphan sidecar not reclaimed by the sweep-2 arm")
	}
}

// TestP3Sweep_OverBudgetSkipsAndReports (spec test 23): a cancelled (expired)
// budget leaves the collectible lane untouched and reports the budget hit.
func TestP3Sweep_OverBudgetSkipsAndReports(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, id, path)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // budget already spent

	budgetHit := r.s.runLaneResidueSweep(ctx)

	if !budgetHit {
		t.Error("expired-budget sweep did not report a budget hit")
	}
	if !r.lanePresent(path) {
		t.Error("expired-budget sweep collected a lane; every lane past the deadline must be skipped")
	}
}

// TestP3CloseResidue_OverBudgetWarns (spec test 23): the close pass emits exactly
// one warning when the shared budget is already spent.
func TestP3CloseResidue_OverBudgetWarns(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, id, path)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	r.s.disposeLaneResidueAtClose(ctx)

	warnings := drainBufferedWarnings(r.s)
	budgetWarns := 0
	for _, w := range warnings {
		if strings.Contains(w, "close budget exhausted") {
			budgetWarns++
		}
	}
	if budgetWarns != 1 {
		t.Errorf("over-budget close pass emitted %d budget warnings, want exactly 1 (%v)", budgetWarns, warnings)
	}
	if !r.lanePresent(path) {
		t.Error("over-budget close pass collected a lane")
	}
}

// drainBufferedWarnings non-blockingly reads every buffered event and returns the
// warning messages.
func drainBufferedWarnings(s *Session) []string {
	var msgs []string
	for {
		select {
		case ev := <-s.Events():
			if ev.Kind == events.EventWarning {
				if wd, ok := ev.Data.(events.WarningData); ok {
					msgs = append(msgs, wd.Message)
				}
			}
		default:
			return msgs
		}
	}
}

// TestP3Sweep_TwoConcurrentPassesCollectOnce (spec test 29): two passes racing on
// one repo collect each lane exactly once; the loser treats the git refusal /
// ENOENT as a skip and never escalates. Run under -race.
//
// REAL git: the race the test exists to exercise is two passes contending for
// one real registry, serialized by git's own index and ref locking.
func TestP3Sweep_TwoConcurrentPassesCollectOnce(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, path, _ := r.seedForeignUnlockedLane(t)
	r.commitAndFastForwardMerge(t, id, path)
	r.ageBeyondGrace(t, id)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.s.runLaneResidueSweep(context.Background())
		}()
	}
	wg.Wait()

	if r.lanePresent(path) {
		t.Error("lane survived two concurrent passes")
	}
	if r.branchExists(t, id) {
		t.Error("lane branch survived two concurrent passes")
	}
}

// TestP3CollectLane_ConcurrentWinnerCompletesRemoval proves that a remove
// error means "lost race" rather than "lane survived" when another collector
// has already removed the worktree. The losing collector must finish the
// branch+sidecar cleanup instead of stranding residue.
func TestP3CollectLane_ConcurrentWinnerCompletesRemoval(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)

	// The remove lands and THEN reports failure, the shape a collector sees when
	// a concurrent winner removed the same worktree first.
	want := filepath.Clean(path)
	r.wrapRunner(func(next worktree.GitRunner, args []string) (string, error) {
		if len(args) >= 3 && args[0] == "worktree" && args[1] == "remove" && filepath.Clean(args[len(args)-1]) == want {
			if _, err := next(args...); err != nil {
				return "", err
			}
			return "", errors.New("scripted git: concurrent collector already removed worktree")
		}
		return next(args...)
	})

	run, err := r.s.worktreeControlRun(context.Background(), r.mainRoot)
	if err != nil {
		t.Fatalf("worktreeControlRun: %v", err)
	}
	metaDir := metaDirForLane(path)
	if err := r.s.collectLane(run, metaDir, id, path, true, r.s.residueSweepPolicy()); err != nil {
		t.Fatalf("collectLane after concurrent removal: %v", err)
	}

	if r.lanePresent(path) {
		t.Error("lane survived concurrent removal")
	}
	if r.branchExists(t, id) {
		t.Error("branch survived concurrent-winner cleanup")
	}
	if _, err := worktree.ReadSidecar(metaDir, id); err == nil {
		t.Error("sidecar survived concurrent-winner cleanup")
	}
}

// TestP3Sweep_Sweep2CollectsSweep1BranchFailure proves the remnant-cleanup
// contract behind concurrent P3 passes: if sweep 1 removes the worktree but
// loses its branch-delete race, sweep 2 must observe the now-orphaned
// branch+sidecar and finish collection in the same pass.
func TestP3Sweep_Sweep2CollectsSweep1BranchFailure(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)
	metaDir := metaDirForLane(path)
	r.ageBeyondGrace(t, id, path)

	// Sweep 1's branch delete loses its race exactly once, stranding the orphan
	// branch + sidecar sweep 2 must then reclaim.
	failedOnce := false
	r.wrapRunner(func(next worktree.GitRunner, args []string) (string, error) {
		if !failedOnce && len(args) == 3 && args[0] == "branch" && args[1] == "-D" && args[2] == id {
			failedOnce = true
			return "", errors.New("scripted git: forced one-shot branch failure")
		}
		return next(args...)
	})

	r.s.runLaneResidueSweep(context.Background())

	if r.lanePresent(path) {
		t.Error("lane survived one-shot branch-delete failure")
	}
	if r.branchExists(t, id) {
		t.Error("sweep 2 did not collect sweep 1's orphaned branch")
	}
	if _, err := worktree.ReadSidecar(metaDir, id); err == nil {
		t.Error("sweep 2 did not collect sweep 1's orphaned sidecar")
	}
}

// TestP3Timer_FiresAtDelayCollectsResidue (spec test 21): nothing before the
// delay; the open-pass timer collects at open+laneSweepDelay.
func TestP3Timer_FiresAtDelayCollectsResidue(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	r := newScriptedLaneRepoWithClock(t, clk)
	id, path := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, id, path)
	// The timer's pass drives the boundary from its own goroutine while this test
	// polls the registry; both go through one mutex.
	obs := r.serializeGitAccess()

	clk.Advance(laneSweepDelay - time.Second)
	if !obs.lanePresent(path) {
		t.Fatal("lane collected before the open-pass delay elapsed")
	}

	clk.Advance(2 * time.Second) // cross laneSweepDelay
	waitForCondition(t, 3*time.Second, "open-pass timer collects the residue lane", func() bool {
		return !obs.lanePresent(path)
	})
}

// TestP3Timer_CancelledByStopDoesNotFire (spec test 21): a stopped timer never
// runs its sweep, even after virtual time passes the delay.
func TestP3Timer_CancelledByStopDoesNotFire(t *testing.T) {
	t.Parallel()
	clk := agenttest.NewFakeClock()
	r := newScriptedLaneRepoWithClock(t, clk)
	id, path := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, id, path)

	// A pass that (incorrectly) fires would drive the boundary from its own
	// goroutine while this test reads the registry; both go through one mutex.
	obs := r.serializeGitAccess()

	r.s.stopLaneResidueSweepTimer()
	clk.Advance(2 * laneSweepDelay)

	// Give any (incorrectly) fired goroutine a chance to run before asserting.
	time.Sleep(50 * time.Millisecond)
	if !obs.lanePresent(path) {
		t.Error("stopped open-pass timer still collected the lane")
	}
}

// TestP3Timer_SubagentSessionNotArmed (spec test 27): subagent sessions never
// arm the P3 open pass.
func TestP3Timer_SubagentSessionNotArmed(t *testing.T) {
	t.Parallel()
	cfg := worktreeTestSessionConfig()
	cfg.spawn.parentSessionID = "parent-session"
	r := newScriptedLaneRepoWithConfig(t, cfg)

	r.s.mu.Lock()
	armed := r.s.laneSweepTimer != nil
	r.s.mu.Unlock()
	if armed {
		t.Error("subagent session armed the P3 open-pass timer")
	}
}

// TestP3CloseResidue_SubagentSessionNoOp: a subagent session's close runs no P3
// residue pass (leaves foreign residue for the top-level session's passes).
func TestP3CloseResidue_SubagentSessionNoOp(t *testing.T) {
	t.Parallel()
	cfg := worktreeTestSessionConfig()
	cfg.spawn.parentSessionID = "parent-session"
	r := newScriptedLaneRepoWithConfig(t, cfg)
	id, path := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, id, path)

	r.s.disposeLaneResidueAtClose(context.Background())

	if !r.lanePresent(path) {
		t.Error("subagent session's close pass collected residue; P3 is top-level only")
	}
}

// TestP3CloseResidue_NonLocalEnvNoOp: a non-local exec env runs no P3 pass.
func TestP3CloseResidue_NonLocalEnvNoOp(t *testing.T) {
	t.Parallel()
	r := newScriptedLaneRepo(t)
	id, path := r.seedForeignUnlockedLane(t)
	r.ageBeyondGrace(t, id, path)
	// Swap in a non-local env: P3 must not run.
	r.s.env = &timeoutEnv{wd: r.mainRoot}

	r.s.disposeLaneResidueAtClose(context.Background())

	if !r.lanePresent(path) {
		t.Error("non-local env ran a P3 close pass")
	}
	// restore a local env so t.Cleanup Close() behaves
	r.s.env = execenv.NewLocalExecutionEnvironment(r.mainRoot)
}

// TestP3CloseResidue_JoinsInFlightOpenPass (spec test 24): a concurrent open pass
// and close residue pass on one session never race on the same lane's git ops and
// never panic. Run under -race.
//
// REAL git: the two passes' git operations must contend for one real repository,
// which is the contention the test exists to prove safe.
func TestP3CloseResidue_JoinsInFlightOpenPass(t *testing.T) {
	t.Parallel()
	r := newWorktreeRepo(t)
	id, path, _ := r.seedForeignUnlockedLane(t)
	r.commitAndFastForwardMerge(t, id, path)
	r.ageBeyondGrace(t, id)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); r.s.fireOpenLaneResidueSweep() }()
	go func() { defer wg.Done(); r.s.disposeLaneResidueAtClose(context.Background()) }()
	wg.Wait()

	if r.lanePresent(path) {
		t.Error("lane survived concurrent open+close residue passes")
	}
}
