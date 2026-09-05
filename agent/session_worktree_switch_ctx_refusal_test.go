package agent

import (
	"context"
	"testing"
)

// The close that refuses the swap also cancels the request context the op's
// git runner is bound to; the target's unlock must not go through it.
func TestWorktreeSwitch_RefusedMidSwapUnlocksTheTargetOnACancelledRequestContext(t *testing.T) {
	sr := newScriptedLaneRepo(t)
	r := sr.wt()
	resA, err := r.create(t, map[string]any{"name": "a"})
	if err != nil {
		t.Fatalf("create a: %v", err)
	}
	laneA := resA["path"].(string)
	if _, err := r.create(t, map[string]any{"name": "b"}); err != nil {
		t.Fatalf("create b: %v", err)
	}
	contextBoundScriptedGit(sr)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeDone := armCloseDuringSwap(r, cancel)

	_, err = r.s.worktreeSwitchByName(ctx, "a")
	<-closeDone

	if err == nil {
		t.Fatal("switch succeeded while the session closed under its swap, want a refusal")
	}
	if _, locked, reason := sr.laneLocked(t, laneA); locked {
		t.Errorf("the refused switch left the target %s locked (%q), want it unlocked again", laneA, reason)
	}
}
