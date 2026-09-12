package goal_test

import (
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/internal/goal"
)

// Round-19 fix wave, finding 1 (approval-cancel misreported, roborev on rev
// bd2fda6): a missing live ask ALWAYS fired "approval answered", but
// askPending clears on interrupt and on every accepted turn entry, so a
// cleared-but-unanswered ask is indistinguishable from an answered one at the
// substrate. The wake excerpt must not claim an answer the predicate cannot
// verify: the fire drives (no strand) with a neutral excerpt, and the reply
// turn's content - not the trigger text - carries the verdict.

// TestFixWave19_ApprovalClearedAskFiresNeutral pins the neutral excerpt: an
// until_approval wait whose live ask disappears (answered OR cancelled - the
// substrate cannot distinguish) fires for re-validation WITHOUT asserting an
// answer, while a still-live ask parks.
func TestFixWave19_ApprovalClearedAskFiresNeutral(t *testing.T) {
	sub := &fakeSubstrate{approvals: map[string]bool{"ship it?\x00gen1": true}}
	s := goal.NewStore()
	s.Set("x", waveBClock())
	s.SetSubstrate(sub)
	w, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilApproval, Target: "ship it?", AskGeneration: "gen1", Timeout: time.Minute}, waveBClock())
	if !ok {
		t.Fatalf("precondition: live ask must park: %q", s.LastRejectReason())
	}
	full, _ := s.GoalSnapshot()
	var live []goal.Wait
	for _, lw := range full.Waits {
		if lw.Live() {
			live = append(live, lw)
		}
	}
	// Live ask still matching: every live lease parks.
	if batch := s.ClassifyWaits(live, waveBClock(), nil); len(batch) != 1 || batch[0].Disposition != goal.WaitPark {
		t.Fatalf("live ask must park: %+v", batch)
	}
	// The ask disappears - answered, interrupted, or superseded by a cleared
	// turn entry. The substrate cannot distinguish, so the fire must not claim
	// an answer.
	delete(sub.approvals, "ship it?\x00gen1")
	batch := s.ClassifyWaits(live, waveBClock(), nil)
	if len(batch) != 1 || batch[0].Disposition != goal.WaitFire {
		t.Fatalf("cleared ask must fire for re-validation (no strand): %+v", batch)
	}
	if strings.Contains(batch[0].Trigger, "answered") {
		t.Fatalf("fire trigger %q must not claim an answer the predicate cannot verify", batch[0].Trigger)
	}
	if !strings.Contains(batch[0].Trigger, w.Lease.Label) {
		t.Fatalf("fire trigger %q must carry the lease label %q", batch[0].Trigger, w.Lease.Label)
	}
}

// TestFixWave19_SetTerminalDrainsBacklog pins finding 2 (terminal backlog
// carried into retarget, roborev on rev bd2fda6): SetTerminal cleared waits
// but preserved PendingWake "for the gate to drain" - but the gate
// short-circuits terminals before draining, so the backlog rode into the next
// Set as Superseded: one no-op continuation with stale context on the new
// objective. A terminal goal must carry no backlog into a replacement
// objective.
func TestFixWave19_SetTerminalDrainsBacklog(t *testing.T) {
	s := goal.NewStore()
	s.Set("old objective", waveBClock())
	w, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, waveBClock())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	if _, ok := s.ClaimFire(w.Lease.WaitID, "timer fired", waveBClock()); !ok {
		t.Fatal("precondition: claim should consume the lease into the backlog")
	}
	// A wake continuation (as update_goal complete would observe after the
	// wake turn drove) terminalizes with the backlog still standing.
	if !s.SetTerminal(goal.StatusComplete, "", waveBClock()) {
		t.Fatal("precondition: SetTerminal should complete the goal")
	}
	if full, _ := s.GoalSnapshot(); len(full.PendingWake) != 0 {
		t.Fatalf("terminal goal must carry no backlog: %+v", full.PendingWake)
	}
	// Retarget after the terminal carries nothing: no Superseded no-op drives
	// on the new objective.
	s.Set("new objective", waveBClock())
	if full, _ := s.GoalSnapshot(); len(full.PendingWake) != 0 {
		t.Fatalf("retarget after terminal must carry no backlog: %+v", full.PendingWake)
	}
}

// TestFixWave19_SetCarriesLiveBacklog pins the preserved non-terminal retarget
// race: Set on a LIVE goal still carries claimed backlog marked Superseded
// (the claim-then-kick interleave needs the single no-op evaluation). Only
// the TERMINAL backlog is now always empty.
func TestFixWave19_SetCarriesLiveBacklog(t *testing.T) {
	s := goal.NewStore()
	s.Set("old objective", waveBClock())
	w, ok := s.RegisterWait(goal.WaitKind{Kind: goal.WaitUntilTime, Timeout: time.Minute}, waveBClock())
	if !ok {
		t.Fatal("precondition: registration should succeed")
	}
	if _, ok := s.ClaimFire(w.Lease.WaitID, "timer fired", waveBClock()); !ok {
		t.Fatal("precondition: claim should consume the lease into the backlog")
	}
	s.Set("new objective", waveBClock())
	full, _ := s.GoalSnapshot()
	if len(full.PendingWake) != 1 || !full.PendingWake[0].Superseded {
		t.Fatalf("live retarget must carry the claim marked Superseded: %+v", full.PendingWake)
	}
}
