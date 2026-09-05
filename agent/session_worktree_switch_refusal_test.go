package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
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
	closeDone := armCloseDuringSwap(r, nil)

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

// The mirror of TestWorktreeCreate_CloseWaitsForTheRefusedCreateRollback: the
// refused switch's target unlock runs in a defer, after the swap has left the
// close fence, on a control runner of its own. Its git forks on the process
// table the close reaps, so the close has to wait for it — walk past it and the
// target keeps this session's own marker, which prune skips and nothing
// automatic ever clears.
func TestWorktreeSwitch_CloseWaitsForTheRefusedSwitchTargetUnlock(t *testing.T) {
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

	cleanupObserved := make(chan struct{})
	var cleanupDuringUnlock, holding atomic.Bool
	r.s.cfg.testOnly.envCleanupObserved = func(execenv.ExecutionEnvironment) { close(cleanupObserved) }

	// Hold the rollback at the one command that identifies it — the unlock of
	// the switch target — and watch for the close reaching environment cleanup
	// while it is held. Installed after the setup creates, and the close's own
	// unlock targets lane b (the recorded current worktree), so only the
	// rollback can match. The wait happens before the scripted double is
	// entered, so it never blocks the close's git behind it.
	base := r.s.cfg.testOnly.worktreeGitRunner
	r.s.cfg.testOnly.worktreeGitRunner = func(ctx context.Context, env execenv.ExecutionEnvironment) worktree.GitRunner {
		inner := base(ctx, env)
		return func(args ...string) (string, error) {
			if len(args) == 3 && args[0] == "worktree" && args[1] == "unlock" && args[2] == laneA &&
				holding.CompareAndSwap(false, true) {
				select {
				case <-cleanupObserved:
					cleanupDuringUnlock.Store(true)
				case <-time.After(closeFenceProbe):
				}
			}
			return inner(args...)
		}
	}

	closeDone := armCloseDuringSwap(r, nil)

	_, err = r.switchOp(t, map[string]any{"name": "a"})
	<-closeDone

	if err == nil {
		t.Fatal("switch succeeded while the session closed under its swap, want a refusal")
	}
	if !holding.Load() {
		t.Fatal("the rollback never unlocked the switch target; the test observed nothing")
	}
	if cleanupDuringUnlock.Load() {
		t.Error("the close cleaned the session's environment while the refused switch's target unlock was still running")
	}
	if _, locked, reason := sr.laneLocked(t, laneA); locked {
		t.Errorf("the refused switch left the target %s locked (%q), want it unlocked again", laneA, reason)
	}
}
