package agent

import (
	"time"

	"primeradiant.com/evener/agent/internal/goal"
	"primeradiant.com/evener/agent/schema"
)

// restoreDormantBlockedGoal loads a terminal-blocked goal as dormant-resumable
// (spec §7): objective, stopReason, budgets, autoReparks, stage,
// deadlineFinalDelivered, and the ledger window restore; waits and pendingWake
// do not (cleared and disarmed at block). Nothing is armed and the terminal
// report stays suppressed — the persisted stop doubles as the no-reemit
// marker. The blocked chip/status projects and /goal resume serves from the
// restored snapshot unchanged.
func (s *Session) restoreDormantBlockedGoal(g *schema.GoalSnapshot) {
	persisted := goalRestoreToStore(g, s.sclock().Now())
	persisted.Waits = nil
	persisted.PendingWake = nil
	s.getOrCreateGoalStore().RestoreSnapshot(persisted)
}

// seedRestoredGoalLatch mirrors a restored persisted TerminalPending latch
// into the session-local gate mirror (spec §1 R7 M-I1): the gate reads the
// local mirror for lock-order reasons, so a latch that was set before the
// restart must survive it. Dormant-blocked goals never carry the latch
// (restoreDormantBlockedGoal keeps waits/backlog empty and the latch clear):
// the seed applies to non-terminal restores only — a persisted latch on a
// dormant-blocked goal stays inert data until /goal resume (Task 9 owns the
// resume-latch interaction).
func (s *Session) seedRestoredGoalLatch() {
	full, ok := s.getOrCreateGoalStore().GoalSnapshot()
	if !ok || !full.TerminalPending {
		return
	}
	if full.Status == goal.StatusComplete || full.Status == goal.StatusBlocked {
		return
	}
	s.mu.Lock()
	s.goalTerminalPending = true
	s.mu.Unlock()
}

// restoreGoalAttachScan re-validates every restored wait predicate immediately
// (spec §7 attach-scan at restore): classify every live lease — predicate
// truth across ALL kinds (job/delegate retained-terminal catch-up, approval
// liveness, child terminality, file baseline delta, HTTP match at the poll
// leg) plus expiry for EVERY kind — and claim fires into pendingWake before
// any kick-or-arm decision, so a restart never silently strands a fired
// wait. Substrate validation failures (job gone, file unstatable, child
// unknown) drop the lease with the cause persisted: the restored backlog (or
// the next turn tail when nothing else stands) delivers the honest loss
// notice (spec §2). A live-but-already-true predicate that fires here arms
// the §5 same-target cooldown like any predicate fire.
func (s *Session) restoreGoalAttachScan() {
	now := s.sclock().Now()
	s.goalUpdateMu.Lock()
	store := s.getOrCreateGoalStore()
	full, ok := store.GoalSnapshot()
	if !ok || (full.Status != goal.StatusWaiting && full.Status != goal.StatusActive) {
		s.goalUpdateMu.Unlock()
		return
	}
	var live []goal.Wait
	for _, w := range full.Waits {
		if w.Live() {
			live = append(live, w)
		}
	}
	// Lock discipline: ClassifyWaits copies the substrate field out under the
	// store lock and evaluates outside it (never held across Substrate
	// calls); the claim loop below runs serialized under goalUpdateMu. The
	// child-terminal pre-read is itself a controller read outside both.
	batch := store.ClassifyWaits(live, now, s.childTerminalTrigger)
	_, losses := store.ClaimClassified(batch, now)
	for _, cause := range losses {
		store.RecordLoss(cause, now)
	}
	s.goalUpdateMu.Unlock()
}

// goalWaitNextFire computes the single coalesced fire instant (spec §2
// four-way min, the single source stated once here and referenced from the
// gate, timer, and restore sites — never the two- or three-way subsets):
// min(earliest live wait deadline, goal deadline, projected
// maxParkedTotal-crossing, next poll due). ok is false when nothing is
// armed (no live waits and no pending poll obligation while waiting).
func goalWaitNextFire(full goal.GoalSnapshot, now time.Time) (fire time.Time, ok bool) {
	if full.Status != goal.StatusWaiting {
		return time.Time{}, false
	}
	var earliest time.Time
	for _, w := range full.Waits {
		if !w.Live() {
			continue
		}
		if earliest.IsZero() || w.Lease.Deadline.Before(earliest) {
			earliest = w.Lease.Deadline
		}
	}
	if earliest.IsZero() {
		return time.Time{}, false
	}
	fire = earliest
	if !full.Budgets.Deadline.IsZero() && full.Budgets.Deadline.Before(fire) {
		fire = full.Budgets.Deadline
	}
	if full.Budgets.MaxParkedTotal > 0 {
		remaining := full.Budgets.MaxParkedTotal - full.Budgets.ParkedTotal
		remaining = max(remaining, 0)
		if crossing := now.Add(remaining); crossing.Before(fire) {
			fire = crossing
		}
	}
	if pollDue := now.Add(goal.GoalWaitPollInterval); pollDue.Before(fire) {
		fire = pollDue
	}
	return fire, true
}
