package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/jobstore"
	"primeradiant.com/serf/agent/internal/worktree"
)

// P3 residue collection (auto-delegate-lane-disposal spec §P3): the
// no-model-in-the-loop pass that reclaims cleanly-closed sessions' unlocked,
// D0-auto-collectible delegate lanes so their original commits become gc-able
// with nobody in the loop. It runs per top-level session on a local exec env,
// once at open+laneSweepDelay and once at close after P0 disposal, over the same
// extracted sweeps `prune` uses — but scoped to delegate sidecars past a grace
// window, judged by the local-refs-only D0-auto predicate, marking only its own
// store's records, and treating every per-lane failure as a lost race.

// armLaneResidueSweepTimer arms the one-shot P3 open-pass timer (spec §P3): a
// top-level session on a local exec env fires a residue sweep laneSweepDelay
// after it opens. Subagent sessions and non-local envs never run P3. The timer
// is stored so close can stop it (a close that begins before the delay cancels
// the pass entirely; a close after it fires joins it via sweepWG).
func (s *Session) armLaneResidueSweepTimer() {
	if s.isSubagentSession() {
		return
	}
	if _, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); !ok {
		return // local exec envs only (spec §P3)
	}
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.laneSweepTimer = s.sclock().AfterFunc(laneSweepDelay, s.fireOpenLaneResidueSweep)
	s.mu.Unlock()
}

// fireOpenLaneResidueSweep is the open timer's callback. It registers on sweepWG
// under the same closing-gated mu hold Close uses (the beginDispose idiom), so a
// successful Add happens-before Close()'s sweepWG.Wait(): the pass either
// registers before close observes it (and close joins it) or sees closing and
// bails (and close has nothing to wait for). No re-lock, ever — the pass only
// touches already-unlocked lanes.
func (s *Session) fireOpenLaneResidueSweep() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.sweepWG.Add(1)
	s.mu.Unlock()
	defer s.sweepWG.Done()
	s.runLaneResidueSweep(context.Background())
}

// stopLaneResidueSweepTimer stops the armed open-pass timer if it has not yet
// fired (best-effort: a timer that already fired is joined via sweepWG instead).
func (s *Session) stopLaneResidueSweepTimer() {
	s.mu.Lock()
	t := s.laneSweepTimer
	s.laneSweepTimer = nil
	s.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}

// disposeLaneResidueAtClose runs the P3 close pass (spec §P3): after this
// session's own P0 disposal, a second residue sweep over the SAME shared close
// budget collects foreign residue (other sessions' cleanly-closed, unlocked,
// merged delegate lanes). Top-level local sessions only. Over budget → the sweep
// stops early and one warning is emitted.
func (s *Session) disposeLaneResidueAtClose(ctx context.Context) {
	if s.isSubagentSession() {
		return
	}
	if _, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment); !ok {
		return
	}
	if s.runLaneResidueSweep(ctx) {
		s.emit(events.EventWarning, events.WarningData{
			Message: "close budget exhausted; delegate-lane residue sweep left remaining foreign lanes for the next open-pass sweep",
		})
	}
}

// runLaneResidueSweep performs one residue-collection pass over this repo's
// worktree root using the extracted prune sweeps under the P3 policy. It returns
// true when ctx was cancelled during the pass (the close budget expired), so the
// close caller can warn. A pass that cannot resolve a local git control env is a
// silent no-op (nothing to collect).
func (s *Session) runLaneResidueSweep(ctx context.Context) (budgetHit bool) {
	// A cancelled cascade budget is a budget hit however far the pass got —
	// including an early exit where the first git call already observed the
	// expiry. The open pass runs on a background ctx, so this stays false there.
	defer func() {
		if ctx.Err() != nil {
			budgetHit = true
		}
	}()
	st := s.worktreeStateSnapshot()
	if st.env == nil || st.mainRepoRoot == "" || st.worktreeRoot == "" {
		return false
	}
	run, err := s.worktreeControlRun(ctx, st.mainRepoRoot)
	if err != nil {
		return false
	}
	projectDir := filepath.Join(st.worktreeRoot, worktree.ProjectID(st.mainRepoRoot))
	metaDir := metaDirForProject(projectDir)

	out, err := run("worktree", "list", "--porcelain")
	if err != nil {
		return false
	}
	managed := managedPorcelainEntries(worktree.ParsePorcelain(out), projectDir)

	policy := s.residueSweepPolicy()
	// Skip-and-continue policy: the sweeps never return an error, so a per-lane
	// refusal/ENOENT (a concurrent collector won the race) is a reported skip and
	// the pass moves on. Sweep 1 collects registered unlocked delegate lanes;
	// sweep 2 reclaims orphan branch+sidecar residue a crash stranded between a
	// worktree remove and its branch delete.
	_, _, _ = s.worktreePruneSweep1(ctx, run, managed, metaDir, policy)
	_, _, _ = s.worktreePruneSweep2(ctx, run, managed, metaDir, policy)
	return false
}

// residueSweepPolicy builds the P3 lane-sweep policy: delegate lanes only, past
// laneGrace (sidecar mtime), judged by the local-refs-only D0-auto predicate
// (never the cherry/remote-tracking arms), skip-and-continue on any per-lane
// failure, and a Disposed mark appended only for lanes whose record lives in
// THIS session's own store (own transiently-unverifiable KEEPs). Foreign records
// get no mark — the crash-net stat covers their revival.
func (s *Session) residueSweepPolicy() laneSweepPolicy {
	owned := s.ownedDelegateIDSet()
	return laneSweepPolicy{
		delegateOnly: true,
		grace:        laneGrace,
		abortOnError: false,
		disposableAt: func(run worktree.GitRunner, lanePath string, sc worktree.Sidecar) (bool, string, error) {
			ok, err := laneAutoCollectible(run, lanePath, sc.BaseSHA, sc.MergeTarget)
			if err != nil {
				return false, "auto-collectible check failed: " + err.Error(), err
			}
			if !ok {
				return false, "not auto-collectible", nil
			}
			return true, "auto-collectible", nil
		},
		disposableBranch: func(run worktree.GitRunner, tip, baseSHA, mergeTarget string) (bool, string, error) {
			ok, err := laneAutoCollectibleBranch(run, tip, baseSHA, mergeTarget)
			if err != nil {
				return false, "auto-collectible check failed: " + err.Error(), err
			}
			if !ok {
				return false, "not auto-collectible", nil
			}
			return true, "auto-collectible", nil
		},
		markDisposed: func(delegateID string) {
			if owned[delegateID] {
				s.markLaneDisposed(delegateID)
			}
		},
	}
}

// ownedDelegateIDSet returns the delegate ids of the isolation lanes THIS
// session created (the same provenance ownedIsolationLanes uses at close). A
// residue sweep marks Disposed only for these — a foreign session's record is
// never in this store, so it is left for that session's own bookkeeping and the
// stat crash net.
func (s *Session) ownedDelegateIDSet() map[string]bool {
	owned := map[string]bool{}
	if s.jobManager == nil || s.jobManager.store == nil {
		return owned
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return owned
	}
	for _, lane := range ownedIsolationLanes(recs, s.id) {
		owned[lane.delegateID] = true
	}
	return owned
}

// markLaneDisposed appends the durable Disposed event for a lane the residue
// sweep collected whose record this session owns. Best-effort: a failed append
// warns but never blocks the collection (the stat crash net still refuses
// revival into the removed lane).
func (s *Session) markLaneDisposed(delegateID string) {
	if s.jobManager == nil {
		return
	}
	if err := s.jobManager.appendEvent(jobstore.Event{
		Kind:       jobstore.EventDelegateDisposed,
		TS:         s.jobManager.now(),
		DelegateID: delegateID,
	}); err != nil {
		s.emit(events.EventWarning, events.WarningData{Message: fmt.Sprintf("delegate lane residue disposal mark failed for %s: %v", delegateID, err)})
	}
}

// laneAutoCollectibleBranch applies the D0-auto predicate to a branch with no
// registered worktree (spec §P3 sweep-2 arm): collectible when its tip equals
// the recorded base SHA (never advanced) or is ancestry-merged into its LOCAL
// merge_target. It never runs the cherry/patch-equivalence arm and never
// consults remote-tracking refs, so an orphan reclaim can never delete commits
// unreachable from a local branch. There is no worktree to check for dirtiness.
func laneAutoCollectibleBranch(run worktree.GitRunner, tip, baseSHA, mergeTarget string) (bool, error) {
	if strings.TrimSpace(tip) == baseSHA {
		return true, nil
	}
	m, err := worktree.MergedAncestryLocal(run, strings.TrimSpace(tip), mergeTarget)
	if err != nil {
		return false, err
	}
	return m.Merged, nil
}
