package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/agent/internal/worktree"
)

// Resume re-lock (auto-delegate-lane-disposal spec §P3 "Session resume re-locks
// its own undisposed lanes"). A clean close UNLOCKS this session's KEPT delegate
// lanes; P3 (the residue sweep) collects unlocked delegate lanes on the
// invariant "unlocked ⇒ no live owner". A resumed session with undisposed KEPT
// lanes would therefore expose its own live lanes to another session's P3 sweep.
// This post-init resume step restores the invariant: it re-takes the evener:dlg:
// lock on each of THIS session's own, undisposed, still-present isolation lanes,
// routing the decision through the same EvDelegateRevive lock core revival uses
// (Unlocked→lock, OwnDelegate→adopt, foreign→leave untouched). A failed re-lock
// warns once and is retried once — at the P3 open timer for a top-level session,
// or at a dedicated one-shot timer for a restored subagent coordinator (which
// has no P3 open pass to piggyback on).

// reLockOutcome is the result of one lane's resume re-lock attempt.
type reLockOutcome int

const (
	// reLockDone: the lane now carries this session's evener:dlg: lock (freshly
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
	work, admitted := s.beginEnvWork("resume-lane-relock")
	if !admitted {
		return
	}
	defer s.endEnvWork(work)
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
	if s == nil || s.delegateController == nil {
		return nil
	}
	var lanes []isolationLane
	for _, delegate := range s.delegateController.ownedStableWorktreeSnapshots(s) {
		if !delegate.resumable || delegate.descriptor.WorkingDir == "" {
			continue
		}
		lanes = append(lanes, isolationLane{delegateID: delegate.delegateID, path: delegate.descriptor.WorkingDir})
	}
	return lanes
}

// reLockOwnLane re-takes this session's evener:dlg: lock on one own undisposed
// lane, routing the decision through worktree.Decide(EvDelegateRevive, …) — the
// SAME core delegate revival uses, so an already-re-locked lane later classifies
// OwnDelegate→adopt there. A missing lane directory or a foreign lock is a clean
// skip; only a present, unverifiable-or-lock-failing lane is a retriable failure.
func (s *Session) reLockOwnLane(local *execenv.LocalExecutionEnvironment, lane isolationLane) reLockOutcome {
	lanePath := filepath.Clean(lane.path)
	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		return reLockSkipped // lane directory gone (disposed/pruned): nothing to re-lock
	}
	controlEnv, _, done, ok := laneControlEnv(local, lanePath)
	if !ok {
		return reLockSkipped // no longer part of a git repository
	}
	defer done()
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

// reapplyIsolationLaneLockForSend re-establishes the evener:dlg: lock on a
// worktree-isolated delegate's lane before its send revives it (issue #481
// review). The lock core is the SAME EvDelegateRevive core revival and resume
// re-lock use: unlocked → lock, the delegate's own dlg marker → adopt, a plain
// session marker or a foreign lock → refuse the send. restoreIdleForSend calls
// it before the child is restored into the lane, so a delegate can never start
// work in a lane it does not hold — including after a manage_worktree unlock
// left the lane loose while the delegate stayed resumable.
//
// Non-isolated delegates are a no-op; a worktree-isolated delegate whose lane is
// absent or is not a linked managed worktree FAILS CLOSED (issue #481 review):
// treating an invalid lane as success would let restoreIdle create the child
// environment at the bare path, using an unlocked ordinary directory. The lane's
// basename is the authoritative delegate identity for its sidecar and marker (a
// lane is named for the delegate that created it), so it is what the sidecar
// read and the marker use. The relock is admitted on the session's env-work
// close fence so a close cannot tear the environment down under it.
func (s *Session) reapplyIsolationLaneLockForSend(descriptor delegatestore.Descriptor) error {
	if descriptor.Isolation != "worktree" {
		return nil
	}
	lanePath := filepath.Clean(strings.TrimSpace(descriptor.WorkingDir))
	if lanePath == "" || lanePath == "." {
		return errors.New("delegate isolation lane path is unset; refusing to start the delegate")
	}
	if !laneWorktreePresent(lanePath) {
		return fmt.Errorf("delegate isolation lane %s is absent or not a linked managed worktree; refusing to start the delegate in it", lanePath)
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return fmt.Errorf("delegate isolation lane %s requires a local execution environment; refusing to start the delegate in it", lanePath)
	}
	work, admitted := s.beginEnvWork("delegate-lane-relock")
	if !admitted {
		return fmt.Errorf("delegate isolation lane %s: session is closing; refusing to start the delegate in it", lanePath)
	}
	defer s.endEnvWork(work)
	delegateID := filepath.Base(lanePath)
	sc, err := worktree.ReadSidecar(metaDirForLane(lanePath), delegateID)
	if err != nil {
		return fmt.Errorf("delegate isolation lane %s is unreadable; refusing to start the delegate in it: %w", lanePath, err)
	}
	originalRoot := strings.TrimSpace(sc.OriginalRoot)
	if originalRoot == "" {
		return fmt.Errorf("delegate isolation lane %s sidecar has no original_root; refusing to start the delegate in it", lanePath)
	}
	if laneMain, conflict := delegateLaneProvenanceConflict(local, lanePath, originalRoot); conflict {
		return fmt.Errorf("delegate isolation lane %s resolves to main root %s but its sidecar records %s; refusing to start the delegate in it on a provenance mismatch", lanePath, laneMain, originalRoot)
	}
	controlEnv, err := s.delegateDisposeControlEnv(originalRoot)
	if err != nil {
		return fmt.Errorf("delegate isolation lane %s control environment: %w", lanePath, err)
	}
	defer disposeUnadoptedScratch(controlEnv)
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)

	locked, reason, lsErr := lockStateOf(run, lanePath)
	if lsErr != nil {
		return fmt.Errorf("delegate isolation lane %s lock state could not be verified; refusing to start the delegate in it: %w", lanePath, lsErr)
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, delegateID)
	}
	switch worktree.Decide(worktree.EvDelegateRevive, st) {
	case worktree.ActLock:
		marker := worktree.FormatDelegateMarker(delegateID, s.id)
		if _, err := run("worktree", "lock", "--reason", marker, lanePath); err != nil {
			return fmt.Errorf("delegate isolation lane %s could not be re-locked; refusing to start the delegate in it: %w", lanePath, err)
		}
		return nil
	case worktree.ActAdopt:
		return nil // already carries this delegate's own dlg marker
	default: // ActRefuse: a session marker or a foreign lock is not ours to touch
		return fmt.Errorf("delegate isolation lane %s is locked by another owner (%s); refusing to start the delegate in it", lanePath, reason)
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
	work, ok := s.beginEnvWork("lane-relock-retry")
	if !ok {
		// Admission was refused for this one-shot firing (a real TryClaim
		// preparing window, or close). Do not consume the only firing: re-arm
		// the existing retry timer so a later firing still retries the retained
		// pending re-lock. The arming helper is closing-gated, so a
		// stop-during-close stays stopped.
		s.armLaneReLockRetryTimer()
		return
	}
	defer s.endEnvWork(work)
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
