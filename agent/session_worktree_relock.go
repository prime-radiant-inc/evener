package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/worktree"
	"primeradiant.com/evener/agent/schema"
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

// delegateLaneLockPlan is the EvDelegateRevive decision for one isolation
// lane's git lock — the shared classification the resume re-lock and delegate
// revival both apply, so the two cannot diverge on what an unlocked or
// foreign-held lane means.
type delegateLaneLockPlan int

const (
	// delegateLaneGone: the lane directory no longer exists (disposed or
	// collected). Nothing to lock; revival's environment reconstruction fails
	// loudly into the construction-failed closure.
	delegateLaneGone delegateLaneLockPlan = iota
	// delegateLaneNoRepo: the directory exists but no longer belongs to a git
	// repository, so there is no lock to manage.
	delegateLaneNoRepo
	// delegateLaneUnlocked: the lane carries no lock — Decide(EvDelegateRevive)
	// maps it to lock, re-taking this session's delegate marker.
	delegateLaneUnlocked
	// delegateLaneAdopt: the lane already carries this session's own delegate
	// marker.
	delegateLaneAdopt
	// delegateLaneRefuse: the lane carries another session's marker (or a
	// plain session marker) — not this session's to touch.
	delegateLaneRefuse
	// delegateLaneUnverifiable: the lane is present but its lock state could
	// not be read.
	delegateLaneUnverifiable
)

// inspectDelegateLaneLock classifies one isolation lane's git lock through the
// shared EvDelegateRevive core. A present lane whose lock state cannot be read
// reports delegateLaneUnverifiable with the error.
func (s *Session) inspectDelegateLaneLock(local *execenv.LocalExecutionEnvironment, lane isolationLane) (delegateLaneLockPlan, error) {
	lanePath := filepath.Clean(lane.path)
	if _, err := os.Stat(filepath.Join(lanePath, ".git")); err != nil {
		return delegateLaneGone, nil
	}
	controlEnv, _, done, ok := laneControlEnv(local, lanePath)
	if !ok {
		return delegateLaneNoRepo, nil
	}
	defer done()
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)
	locked, reason, lsErr := lockStateOf(run, lanePath)
	if lsErr != nil {
		return delegateLaneUnverifiable, lsErr
	}
	st := worktree.Unlocked
	if locked {
		st = worktree.ClassifyReason(reason, s.id, lane.delegateID)
	}
	switch worktree.Decide(worktree.EvDelegateRevive, st) {
	case worktree.ActLock:
		return delegateLaneUnlocked, nil
	case worktree.ActAdopt:
		return delegateLaneAdopt, nil
	default:
		return delegateLaneRefuse, nil
	}
}

// lockDelegateLane takes this session's evener:dlg: marker on an unlocked
// lane, through the same control env the other lock-taking paths use.
func (s *Session) lockDelegateLane(local *execenv.LocalExecutionEnvironment, lane isolationLane) error {
	lanePath := filepath.Clean(lane.path)
	controlEnv, _, done, ok := laneControlEnv(local, lanePath)
	if !ok {
		return errors.New("delegate lane is no longer part of a git repository")
	}
	defer done()
	run := s.newWorktreeGitRunner(context.Background(), controlEnv)
	_, err := run("worktree", "lock", "--reason", worktree.FormatDelegateMarker(lane.delegateID, lane.markerSessionID), lanePath)
	return err
}

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
			s.emit(events.WarningData{
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
		// This session spawned these lanes, so their markers name it.
		lanes = append(lanes, isolationLane{delegateID: delegate.delegateID, path: delegate.descriptor.WorkingDir, markerSessionID: s.id})
	}
	return lanes
}

// reLockOwnLane re-takes this session's evener:dlg: lock on one own undisposed
// lane, routing the decision through worktree.Decide(EvDelegateRevive, …) — the
// SAME core delegate revival uses, so an already-re-locked lane later classifies
// OwnDelegate→adopt there. A missing lane directory or a foreign lock is a clean
// skip; only a present, unverifiable-or-lock-failing lane is a retriable failure.
func (s *Session) reLockOwnLane(local *execenv.LocalExecutionEnvironment, lane isolationLane) reLockOutcome {
	plan, inspectErr := s.inspectDelegateLaneLock(local, lane)
	if inspectErr != nil {
		return reLockFailed // present but unverifiable: an exposed lane; retry
	}
	switch plan {
	case delegateLaneAdopt:
		return reLockDone // already carries our own dlg marker (lock held across resume)
	case delegateLaneGone, delegateLaneNoRepo, delegateLaneRefuse:
		// Gone (disposed/pruned), lockless to manage, or someone else's —
		// not ours to touch, spec §9 Guards.
		return reLockSkipped
	case delegateLaneUnlocked:
		if err := s.lockDelegateLane(local, lane); err != nil {
			return reLockFailed // still unlocked and exposed: retry
		}
		return reLockDone
	default: // delegateLaneUnverifiable always carries inspectErr above
		return reLockFailed
	}
}

// reviveRevalidateDelegateLane re-establishes this session's evener:dlg:
// marker on an isolated delegate's lane before a revive hands the lane back to
// a child runtime — retained runtimes included. Close-time disposal leaves a
// kept changed lane unlocked ("switch-able until revived", spec §9 Guards),
// and prune's orphan arm (#5471) and the documented manual unlock can release
// a resumable delegate's lane; an unlocked lane is re-locked, a lane already
// carrying this session's marker is adopted, and a lane held by anyone else —
// or removed outright — is a refusal: revival never runs a child in a
// checkout another session owns, and never builds one on a directory that is
// no longer there, because the caller's environment reconstruction cannot be
// relied on to notice (an unsandboxed clone does not validate its working
// directory).
func (s *Session) reviveRevalidateDelegateLane(local *execenv.LocalExecutionEnvironment, lane isolationLane) error {
	plan, inspectErr := s.inspectDelegateLaneLock(local, lane)
	if inspectErr != nil {
		return fmt.Errorf("revalidating delegate lane %s at %s: %w", lane.delegateID, lane.path, inspectErr)
	}
	switch plan {
	case delegateLaneAdopt:
		return nil
	case delegateLaneGone:
		return fmt.Errorf("delegate lane %s at %s is gone; refusing revival into a removed checkout", lane.delegateID, lane.path)
	case delegateLaneUnlocked:
		if err := s.lockDelegateLane(local, lane); err != nil {
			// A sibling launch sharing this lane can take it between this
			// launch's inspection and its lock attempt — the per-child guards
			// serialize each child's own drives, not two children sharing an
			// ancestor's lane — and git then refuses the loser's lock as
			// already locked. The lane is not lost to the tree: re-inspect,
			// and a lane now carrying the tree's own marker is an adoption.
			// Any other state keeps the original error.
			replan, recheckErr := s.inspectDelegateLaneLock(local, lane)
			if recheckErr == nil && replan == delegateLaneAdopt {
				return nil
			}
			return fmt.Errorf("re-locking delegate lane %s at %s: %w", lane.delegateID, lane.path, err)
		}
		return nil
	case delegateLaneNoRepo:
		return fmt.Errorf("delegate lane %s at %s is no longer part of a git repository", lane.delegateID, lane.path)
	default: // delegateLaneRefuse
		return fmt.Errorf("delegate lane %s at %s is held by another session", lane.delegateID, lane.path)
	}
}

// validateDrivenDelegateLane re-checks the lane a notification-driven drive
// turn is about to run in before it hands the retained child another turn:
// the child's own when it is worktree-isolated, and otherwise the isolated
// ancestor's lane it shares. The releases that threaten revival (close-time
// disposal, prune's orphan arm, the documented manual unlock) threaten the
// drive just the same: an unlocked lane is re-locked, and a lane held by
// anyone else or removed outright refuses the launch — the drive reports
// not-launched (its notifications stay queued) and the lane is never touched.
func (s *Session) validateDrivenDelegateLane(sub *subagent) bool {
	if s == nil || sub == nil || sub.sess == nil || s.delegateController == nil {
		return true
	}
	delegateID := sub.sess.owningDelegateID
	if delegateID == "" {
		return true
	}
	if err := s.revalidateLaneForLaunch(delegateID); err != nil {
		s.emit(events.WarningData{
			Message: fmt.Sprintf("delegate %s's lane failed revalidation before a notification turn: %v", delegateID, err),
		})
		return false
	}
	return true
}

// revalidateLaneForLaunch resolves the lane a delegate's next turn edits and
// re-establishes the tree's claim on it before any launch hands the turn
// over: the delegate's own when it is worktree-isolated, and otherwise the
// nearest isolated ancestor's — the checkout a non-isolated descendant
// shares. The launch itself is activity of the session the lane's marker
// names — the parent the orphan reclaim consults — so it is recorded in that
// session's evidence before the lane is adopted or re-locked; a launch that
// interleaves with a prune leaves the release recheck seeing a live parent. A
// delegate with no lane of this tree's own runs in this session's checkout,
// which no reclaim can release, and a launch on a non-local environment has
// no git worktree to validate.
func (s *Session) revalidateLaneForLaunch(delegateID string) error {
	if s == nil || s.delegateController == nil || delegateID == "" {
		return nil
	}
	local, ok := s.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		return nil // local git worktrees are a local-env-only feature
	}
	lane, ok := s.delegateController.isolatedLaneForDelegate(delegateID)
	if !ok {
		return nil
	}
	s.refreshActivityEvidence(lane.markerSessionID)
	return s.reviveRevalidateDelegateLane(local, lane)
}

// refreshActivityEvidence records activity in the last-activity signal the
// session listings and the orphan reclaim both read — the named session's
// transcript mtime (the doctor's LastActivity). An automatic launch runs on
// the tree's machinery without running a turn of that session's own, so the
// evidence would otherwise stay stale while the tree is demonstrably alive,
// and the orphan arm could judge the parent long-inactive and release a
// child's lane under the running turn. Best-effort: a session with nothing
// persisted has no evidence to refresh, and the arm disarms without a state
// directory anyway.
func (s *Session) refreshActivityEvidence(sessionID string) {
	if s == nil || sessionID == "" {
		return
	}
	now := s.sclock().Now()
	_ = os.Chtimes(schema.SessionTranscriptPath(s.stateDir, sessionID), now, now)
}

// startLaneEvidenceTicker keeps the lane-marker parent's activity evidence
// fresh for the life of this delegate child's turn. Phase reports fire at
// transitions only — a long foreground shell call reports once before it
// starts and then stays silent — so per-report refreshes cannot cover a
// turn's quiet stretches, and the evidence the orphan reclaim reads would go
// stale under running work, letting a foreign prune release the lane
// mid-task. The tick reuses the activity publication floor: no turn keeps
// the evidence staler than the noisiest legitimate reporter makes it. The
// returned stop joins the goroutine; a session that is no delegate child, or
// carries no controller, starts nothing.
func (s *Session) startLaneEvidenceTicker(ctx context.Context) (stop func()) {
	if s == nil || s.owningDelegateID == "" || s.delegateController == nil {
		return func() {}
	}
	tickerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := s.sclock().NewTicker(delegateActivityPublishInterval)
		defer ticker.Stop()
		for {
			select {
			case <-tickerCtx.Done():
				return
			case <-ticker.C():
				s.delegateController.refreshLaneParentActivityEvidence(s.owningDelegateID)
			}
		}
	}()
	return func() {
		cancel()
		<-done
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
			s.emit(events.WarningData{
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
