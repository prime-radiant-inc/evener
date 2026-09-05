package agent

import (
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
)

// A switch refused mid-swap has already locked the target and unlocked the
// lane it was leaving. It gives the target's lock back and leaves the previous
// lane as the leave left it: the refusal means the session is closing, and
// its close unlocks its own lane on the way out — a re-lock landing after that
// would strand the lane under a dead session's marker, which prune skips and
// remove refuses. The session's recorded state stays where it was.
func TestWorktreeSwitch_RefusedMidSwapRestoresThePreviousLocks(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	resA, err := r.create(t, map[string]any{"name": "a"})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	laneA := resA["path"].(string)
	resB, err := r.create(t, map[string]any{"name": "b"})
	if err != nil {
		t.Fatalf("create b: %v", err)
	}
	laneB := resB["path"].(string)
	marker := worktree.FormatSessionMarker(r.s.id)
	if _, locked, reason := sr.laneLocked(t, laneB); !locked || reason != marker {
		t.Fatalf("lane b before the switch: locked=%v %q, want locked with %q", locked, reason, marker)
	}
	if _, locked, _ := sr.laneLocked(t, laneA); locked {
		t.Fatal("lane a is still locked before the switch; the create of b should have left it")
	}
	closeDone := armCloseDuringSwap(r)

	_, err = r.switchOp(t, map[string]any{"name": "a"})
	<-closeDone

	if err == nil {
		t.Fatal("switch succeeded while the session closed under its swap, want a refusal")
	}
	if _, locked, reason := sr.laneLocked(t, laneA); locked {
		t.Errorf("the refused switch left the target %s locked (%q), want it unlocked again", laneA, reason)
	}
	if _, locked, reason := sr.laneLocked(t, laneB); locked && reason == marker {
		t.Errorf("the refused switch left the previous lane %s locked with the closed session's marker %q; nothing will ever release it", laneB, reason)
	}
	r.s.mu.Lock()
	current := r.s.worktreeCurrentPath
	r.s.mu.Unlock()
	if current != laneB {
		t.Errorf("recorded current worktree = %q after the refused switch, want %q unchanged", current, laneB)
	}
	if got := r.s.Meta().WorktreePath; got != laneB {
		t.Errorf("Meta().WorktreePath = %q after the refused switch, want %q unchanged", got, laneB)
	}
}
