package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/worktree"
)

// Resume re-lock (auto-delegate-lane-disposal spec §P3 "Session resume re-locks
// its own undisposed lanes"). A clean close UNLOCKS this session's KEPT delegate
// lanes; P3 (the residue sweep) collects unlocked delegate lanes on the
// invariant "unlocked ⇒ no live owner". A resumed session with undisposed KEPT
// lanes would therefore expose its own live lanes to another session's P3 sweep.
// This post-init resume step restores the invariant: it re-takes the serf:dlg:
// lock on each of THIS session's own, undisposed, still-present isolation lanes,
// routing the decision through the same EvDelegateRevive lock core revival uses
// (Unlocked→lock, OwnDelegate→adopt, foreign→leave untouched). A failed re-lock
// warns once and is retried once — at the P3 open timer for a top-level session,
// or at a dedicated one-shot timer for a restored subagent coordinator (which
// has no P3 open pass to piggyback on).

// reLockOutcome is the result of one lane's resume re-lock attempt.
type reLockOutcome int

const (
	// reLockDone: the lane now carries this session's serf:dlg: lock (freshly
	// locked, or already-own-marker adopted). The P3 invariant holds for it.
	reLockDone reLockOutcome = iota
	// reLockSkipped: nothing to do and nothing exposed — the lane directory is
	// gone (already disposed/pruned; the stat crash net covers it) or it is
	// foreign-locked (someone switched in; not ours to touch, spec §9 Guards).
	reLockSkipped
	// reLockFailed: the re-lock could not be completed (lock state unverifiable
	// or the `worktree lock` git op failed) while the lane is still present and
	// unlocked — an exposed own lane. Warned once and queued for one retry.
	reLockFailed
)

// resumeReLockOwnLanes re-locks this restored session's own undisposed isolation
// lanes (spec §P3 resume re-lock). It runs post-init (the jobstore must exist to
// enumerate owned lanes) on local exec envs only. Each failed re-lock warns and
// is queued into pendingReLock; the retry is armed either implicitly (the P3
// open timer already scheduled for a top-level session) or explicitly (a
// dedicated one-shot for a restored subagent coordinator with no P3 pass).
func (s *Session) resumeReLockOwnLanes() {
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return // env swapping / local git worktrees are a local-env-only feature
	}
	lanes := s.undisposedOwnedLanes()
	if len(lanes) == 0 {
		return
	}
	var pending []isolationLane
	for _, lane := range lanes {
		if s.reLockOwnLane(local, lane) == reLockFailed {
			s.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("delegate lane %s at %s could not be re-locked on resume; it stays exposed to automatic residue collection until a retry succeeds", lane.delegateID, lane.path),
			})
			pending = append(pending, lane)
		}
	}
	if len(pending) == 0 {
		return
	}
	s.mu.Lock()
	s.pendingReLock = pending
	s.mu.Unlock()
	// A restored subagent coordinator runs no P3 open pass (armLaneResidueSweepTimer
	// no-ops for a subagent session), so it needs its own one-shot to drive the
	// retry. A top-level session piggybacks on its already-armed P3 open timer,
	// whose callback retries pendingReLock before the sweep.
	if s.isSubagentSession() {
		s.armLaneReLockRetryTimer()
	}
}

// undisposedOwnedLanes returns the isolation lanes THIS session created that are
// NOT yet marked Disposed — the lanes a resume must re-lock. Disposed records
// (their worktree already removed) are excluded: there is nothing to re-lock and
// the stat crash net already refuses revival into them.
func (s *Session) undisposedOwnedLanes() []isolationLane {
	if s.jobManager == nil || s.jobManager.store == nil {
		return nil
	}
	recs, err := s.jobManager.store.Load()
	if err != nil {
		return nil
	}
	disposed := map[string]bool{}
	for _, r := range recs {
		if r.Disposed && r.DelegateID != "" {
			disposed[r.DelegateID] = true
		}
	}
	var lanes []isolationLane
	for _, lane := range ownedIsolationLanes(recs, s.id) {
		if disposed[lane.delegateID] {
			continue
		}
		lanes = append(lanes, lane)
	}
	return lanes
}

// reLockOwnLane re-takes this session's serf:dlg: lock on one own undisposed
// lane, routing the decision through worktree.Decide(EvDelegateRevive, …) — the
// SAME core delegate revival uses, so an already-re-locked lane later classifies
// OwnDelegate→adopt there. A missing lane directory or a foreign lock is a clean
// skip; only a present, unverifiable-or-lock-failing lane is a retriable failure.
func (s *Session) reLockOwnLane(local *execenv.LocalExecutionEnvironment, lane isolationLane) reLockOutcome {
	lanePath := filepath.Clean(lane.path)
	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		return reLockSkipped // lane directory gone (disposed/pruned): nothing to re-lock
	}
	rootedAtLane := local.WithWorkingDirectory(lanePath)
	mainRoot := execenv.ResolveMainRepoRoot(rootedAtLane, lanePath)
	if mainRoot == "" {
		return reLockSkipped // no longer part of a git repository
	}
	controlEnv := local.WithWorkingDirectory(mainRoot)
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)

	locked, reason, lsErr := lockStateOf(run, lanePath)
	if lsErr != nil {
		return reLockFailed // present but unverifiable: an exposed lane; retry
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, lane.delegateID)
	}
	switch worktree.Decide(worktree.EvDelegateRevive, st) {
	case worktree.ActLock:
		marker := worktree.FormatDelegateMarker(lane.delegateID, s.id)
		if _, err := run("worktree", "lock", "--reason", marker, lanePath); err != nil {
			return reLockFailed // still unlocked and exposed: retry
		}
		return reLockDone
	case worktree.ActAdopt:
		return reLockDone // already carries our own dlg marker (lock held across resume)
	default: // ActRefuse: foreign / a plain session marker — not ours to touch
		return reLockSkipped
	}
}

// retryPendingReLocks re-attempts the resume re-lock for lanes whose first
// attempt failed (spec §P3: "warning + one retry"). It drains pendingReLock
// (a single retry — a lane still failing is not re-queued) and warns once per
// lane still left exposed, naming it. Invoked from the P3 open-timer callback
// (top-level) or the dedicated re-lock retry timer (subagent coordinator).
func (s *Session) retryPendingReLocks() {
	s.mu.Lock()
	pending := s.pendingReLock
	s.pendingReLock = nil
	s.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return
	}
	for _, lane := range pending {
		if s.reLockOwnLane(local, lane) == reLockFailed {
			s.emit(events.EventWarning, events.WarningData{
				Message: fmt.Sprintf("delegate lane %s at %s could not be re-locked on retry; it remains exposed to automatic residue collection", lane.delegateID, lane.path),
			})
		}
	}
}

// armLaneReLockRetryTimer arms the one-shot resume re-lock retry for a restored
// subagent coordinator (spec §P3: "a dedicated one-shot for restored subagent
// coordinators"). A top-level session never uses this — its P3 open timer drives
// the retry instead. Stopped at close alongside the P3 open timer.
func (s *Session) armLaneReLockRetryTimer() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.laneReLockRetryTimer = s.sclock().AfterFunc(laneSweepDelay, s.fireLaneReLockRetry)
	s.mu.Unlock()
}

// fireLaneReLockRetry is the dedicated retry timer's callback. It registers on
// sweepWG under the same closing-gated mu hold Close uses (the beginDispose
// idiom), so a successful Add happens-before Close()'s sweepWG.Wait(): the retry
// either registers before close observes it (and close joins it) or sees closing
// and bails.
func (s *Session) fireLaneReLockRetry() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.sweepWG.Add(1)
	s.mu.Unlock()
	defer s.sweepWG.Done()
	s.retryPendingReLocks()
}

// stopLaneReLockRetryTimer stops the dedicated retry timer if it has not yet
// fired (best-effort: a fired timer is joined via sweepWG instead).
func (s *Session) stopLaneReLockRetryTimer() {
	s.mu.Lock()
	t := s.laneReLockRetryTimer
	s.laneReLockRetryTimer = nil
	s.mu.Unlock()
	if t != nil {
		t.Stop()
	}
}
