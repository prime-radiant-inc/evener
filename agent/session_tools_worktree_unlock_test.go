package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/worktree"
)

// unlockThroughTool drives the registered manage_worktree tool, so the test
// covers the schema/dispatch wiring as well as the operation body.
func (r *wtRepo) unlockThroughTool(t *testing.T, id string) (map[string]any, error) {
	t.Helper()
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), map[string]any{"operation": "unlock", "id": id})
	if err != nil {
		return nil, err
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unlock result is %T, want map[string]any", out)
	}
	return m, nil
}

// attachResidentUnlockChild installs a resident runtime for a seeded delegate,
// the shape a delegate presents after its last generation finished but before
// the idle release evicted it.
func attachResidentUnlockChild(t *testing.T, r *wtRepo, delegateID string) (*Session, string) {
	t.Helper()
	child := newSession(t)
	c := r.s.delegateController
	c.mu.Lock()
	aggregate := c.durable[delegateID]
	if aggregate == nil {
		c.mu.Unlock()
		t.Fatalf("delegate %s has no durable aggregate", delegateID)
	}
	childID := aggregate.Descriptor.ChildSessionID
	live := c.live[delegateID]
	if live == nil {
		live = &delegateLiveState{}
		c.live[delegateID] = live
	}
	// A finished generation has no open binding; the resident pointer is what
	// the unlock release must evict.
	live.binding = nil
	live.runtime = child
	c.evidenceVersion++
	c.mu.Unlock()

	r.s.subagents.mu.Lock()
	r.s.subagents.subs[childID] = &subagent{id: childID, sess: child}
	r.s.subagents.mu.Unlock()
	t.Cleanup(func() { r.s.subagents.removeSession(childID, child) })
	return child, childID
}

func assertUnlockLaneStillLocked(t *testing.T, r *wtRepo, lanePath, delegateID string) {
	t.Helper()
	registered, locked, reason := r.laneLocked(t, lanePath)
	if !registered {
		t.Fatalf("lane %s is no longer registered", lanePath)
	}
	if !locked {
		t.Fatalf("lane %s lost its lock", lanePath)
	}
	m, ok := worktree.ParseMarker(reason)
	if !ok || m.DelegateID != delegateID || m.SessionID != r.s.id {
		t.Fatalf("lane lock reason = %q, want this parent's evener:dlg marker for %s", reason, delegateID)
	}
}

func TestManageWorktreeUnlock_ReleasesDelegateLaneWithoutRetiring(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	dirtyPath := filepath.Join(lanePath, "uncommitted-recovery.txt")
	if err := os.WriteFile(dirtyPath, []byte("work the parent must recover\n"), 0o600); err != nil {
		t.Fatalf("seed uncommitted lane work: %v", err)
	}
	assertUnlockLaneStillLocked(t, r, lanePath, delegateID)

	res, err := r.unlockThroughTool(t, delegateID)
	if err != nil {
		t.Fatalf("unlock: %v", err)
	}
	if res["status"] != "unlocked" || res["id"] != delegateID || res["path"] != lanePath {
		t.Fatalf("unlock result = %#v, want status unlocked id/path for %s at %s", res, delegateID, lanePath)
	}
	registered, locked, _ := r.laneLocked(t, lanePath)
	if !registered {
		t.Fatal("unlock deregistered the lane")
	}
	if locked {
		t.Fatal("unlock left the delegate lane locked")
	}
	if _, err := os.Stat(lanePath); err != nil {
		t.Fatalf("lane directory is gone after unlock: %v", err)
	}
	if !r.branchExists(t, delegateID) {
		t.Fatal("unlock deleted the delegate branch")
	}
	aggregate := delegateAggregateSnapshot(t, r.s.delegateController, delegateID)
	if !aggregate.Resumable || aggregate.NotResumableReason != "" {
		t.Fatalf("unlock closed resumability: resumable=%t reason=%q", aggregate.Resumable, aggregate.NotResumableReason)
	}
	if r.stableDisposalClosurePresent(t, delegateID) {
		t.Fatal("unlock marked the delegate disposed")
	}

	// The issue's outcome: after unlock the parent can switch in and read the
	// delegate's uncommitted work. switch takes the session marker, proving the
	// delegate's lock really is gone rather than merely hidden.
	rt := r.s.reg.Get("manage_worktree")
	if rt == nil {
		t.Fatal("registry is missing manage_worktree")
	}
	out, err := rt.Exec(t.Context(), r.s.currentEnv(), map[string]any{"operation": "switch", "path": lanePath})
	if err != nil {
		t.Fatalf("switch into the unlocked lane: %v", err)
	}
	if status := out.(map[string]any)["status"]; status != "switched" {
		t.Fatalf("switch status = %v, want switched", status)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked || reason != worktree.FormatSessionMarker(r.s.id) {
		t.Fatalf("lane after switch = locked=%t reason=%q, want this session's marker", locked, reason)
	}
	got, err := os.ReadFile(dirtyPath)
	if err != nil || string(got) != "work the parent must recover\n" {
		t.Fatalf("uncommitted lane work after switch = %q err=%v", got, err)
	}
	if _, err := rt.Exec(t.Context(), r.s.currentEnv(), map[string]any{"operation": "exit"}); err != nil {
		t.Fatalf("exit the recovered lane: %v", err)
	}
}

func TestManageWorktreeUnlock_RefusesWhenCallerIsNotTheParent(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	other := newSession(t)

	if _, err := other.worktreeUnlock(context.Background(), delegateID); err == nil || !strings.Contains(err.Error(), "not a direct worktree-isolated delegate of this session") {
		t.Fatalf("unlock from a non-parent error = %v, want the ownership refusal", err)
	}
	assertUnlockLaneStillLocked(t, r, lanePath, delegateID)
}

func TestManageWorktreeUnlock_RefusesADelegateMarkerOwnedByAnotherParent(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	foreign := worktree.FormatDelegateMarker(delegateID, "some-other-parent")
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", foreign, lanePath)

	_, err := r.unlockThroughTool(t, delegateID)
	if err == nil || !strings.Contains(err.Error(), "recorded owner is not this parent") {
		t.Fatalf("unlock on a foreign lock error = %v, want the parent-marker refusal", err)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked || reason != foreign {
		t.Fatalf("foreign lock = locked=%t reason=%q, want it untouched", locked, reason)
	}
}

func TestManageWorktreeUnlock_RefusesASessionMarker(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	wtGit(t, r.mainRoot, "worktree", "unlock", lanePath)
	wtGit(t, r.mainRoot, "worktree", "lock", "--reason", "evener:some-other-session", lanePath)

	_, err := r.unlockThroughTool(t, delegateID)
	if err == nil || !strings.Contains(err.Error(), "not locked with its own evener:dlg: marker") {
		t.Fatalf("unlock on a session marker error = %v, want the state-machine refusal", err)
	}
	_, locked, reason := r.laneLocked(t, lanePath)
	if !locked || reason != "evener:some-other-session" {
		t.Fatalf("session lock = locked=%t reason=%q, want it untouched", locked, reason)
	}
}

func TestManageWorktreeUnlock_RefusesARunningResidentChild(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	_, childID := attachResidentUnlockChild(t, r, delegateID)
	sub := r.s.subagents.get(childID)
	sub.mu.Lock()
	sub.running = true
	sub.mu.Unlock()
	t.Cleanup(func() {
		sub.mu.Lock()
		sub.running = false
		sub.mu.Unlock()
	})

	_, err := r.unlockThroughTool(t, delegateID)
	if err == nil || !strings.Contains(err.Error(), "became active while unlocking") {
		t.Fatalf("unlock with a running resident child error = %v, want the gate refusal", err)
	}
	assertUnlockLaneStillLocked(t, r, lanePath, delegateID)
	if r.s.subagents.get(childID) == nil {
		t.Fatal("refused unlock dropped the resident child")
	}
}

func TestManageWorktreeUnlock_RefusesAnUntrackedResidentRuntime(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	child := newSession(t)
	c := r.s.delegateController
	c.mu.Lock()
	live := c.live[delegateID]
	live.binding = nil
	live.runtime = child
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		if live := c.live[delegateID]; live != nil && live.runtime == child {
			live.runtime = nil
		}
		c.mu.Unlock()
	})

	_, err := r.unlockThroughTool(t, delegateID)
	if err == nil || !strings.Contains(err.Error(), "not tracked in the subagent registry") {
		t.Fatalf("unlock with an untracked resident runtime error = %v, want the fail-closed refusal", err)
	}
	assertUnlockLaneStillLocked(t, r, lanePath, delegateID)
}

func TestManageWorktreeUnlock_RefusesWhileRuntimeReleaseIsInFlight(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	child := newSession(t)
	c := r.s.delegateController
	entries := []delegateRuntimeReclamationEntry{{delegateID: delegateID, runtime: child}}
	c.beginRuntimeReleaseInFlight(entries)
	t.Cleanup(func() { c.endRuntimeReleaseInFlight(entries) })

	_, err := r.unlockThroughTool(t, delegateID)
	if err == nil || !strings.Contains(err.Error(), "became active while unlocking") {
		t.Fatalf("unlock while a runtime release is in flight error = %v, want the handoff refusal", err)
	}
	assertUnlockLaneStillLocked(t, r, lanePath, delegateID)
}

func TestClaimIdleRuntimeReleaseRefusesUnderLaneHandoff(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, _, _ := r.seedIsolationLane(t)
	attachResidentUnlockChild(t, r, delegateID)
	c := r.s.delegateController
	if admission := c.beginSubtreeLaneHandoff(delegateID); admission != laneHandoffAcquired {
		t.Fatalf("begin handoff admission = %v, want acquired", admission)
	}
	defer c.endSubtreeLaneHandoff(delegateID)

	claim, retryable, err := c.ClaimIdleRuntimeRelease(delegateID)
	if err != nil {
		t.Fatalf("claim idle runtime release under handoff: %v", err)
	}
	if claim != nil || !retryable {
		t.Fatalf("claim under handoff = (%v, retryable=%t), want nil and retryable", claim, retryable)
	}
}

// TestManageWorktreeUnlock_EvictsBeforeUnlockAndRetriesAfterSpentRelease covers
// #2066 (never hand the lane off while the child is resident) and #4530 (a
// release that consumed its one-time teardown pass and reported failure must
// not make every later unlock impossible).
func TestManageWorktreeUnlock_EvictsBeforeUnlockAndRetriesAfterSpentRelease(t *testing.T) {
	r := newWorktreeRepo(t)
	delegateID, lanePath, _ := r.seedIsolationLane(t)
	child, childID := attachResidentUnlockChild(t, r, delegateID)

	lockedAtReleaseFailure := false
	restoreFault := setRetirementReleaseFault(func(point string) error {
		if point == "transcript_close" {
			// Observe the lock AT the failure, not after any compensating
			// re-lock: #2066 is about the window before the lane is released.
			_, locked, _ := r.laneLocked(t, lanePath)
			lockedAtReleaseFailure = locked
			return errors.New("injected resident-release failure")
		}
		return nil
	})
	_, err := r.unlockThroughTool(t, delegateID)
	restoreFault()
	if err == nil || !strings.Contains(err.Error(), "resident runtime failed") {
		t.Fatalf("unlock with a failing resident release error = %v, want the release failure", err)
	}
	if !lockedAtReleaseFailure {
		t.Fatal("unlock released the lane before the resident runtime release failed")
	}
	if !child.retirementTeardownSpent() {
		t.Fatal("first release failure did not consume the child's one-time teardown pass")
	}
	// #2066: the lock must still be held while the child is retained.
	assertUnlockLaneStillLocked(t, r, lanePath, delegateID)
	if r.s.subagents.get(childID) == nil {
		t.Fatal("failed unlock dropped the resident child")
	}
	c := r.s.delegateController
	c.mu.Lock()
	live := c.live[delegateID]
	resident := live != nil && live.runtime == child
	c.mu.Unlock()
	if !resident {
		t.Fatal("failed unlock dropped the controller's resident runtime pointer")
	}

	// The first attempt consumed the child's single teardown pass. #4530: the
	// retry must treat that spent pass as already-released and proceed.
	res, err := r.unlockThroughTool(t, delegateID)
	if err != nil {
		t.Fatalf("retry after a spent release pass: %v", err)
	}
	if res["status"] != "unlocked" {
		t.Fatalf("retry result = %#v, want status unlocked", res)
	}
	_, locked, _ := r.laneLocked(t, lanePath)
	if locked {
		t.Fatal("retry left the delegate lane locked")
	}
	if r.s.subagents.get(childID) != nil {
		t.Fatal("retry left the released child in the subagent registry")
	}
	c.mu.Lock()
	live = c.live[delegateID]
	resident = live != nil && live.runtime != nil
	c.mu.Unlock()
	if resident {
		t.Fatal("retry left the controller's resident runtime pointer in place")
	}
	aggregate := delegateAggregateSnapshot(t, c, delegateID)
	if !aggregate.Resumable {
		t.Fatalf("retry closed resumability: reason=%q", aggregate.NotResumableReason)
	}
}

func TestDelegateHasResidentDescendants(t *testing.T) {
	r := newWorktreeRepo(t)
	parentID, lanePath, _ := r.seedIsolationLane(t)
	const nestedID = "dlg_nested_unlock_fixture"
	seedNestedIdleDelegate(t, r.s.delegateController, nestedID, parentID, lanePath)

	c := r.s.delegateController
	if c.delegateHasResidentDescendants(parentID) {
		t.Fatal("idle descendant without a runtime reported resident")
	}
	child := newSession(t)
	c.mu.Lock()
	c.live[nestedID] = &delegateLiveState{runtime: child}
	c.mu.Unlock()
	if !c.delegateHasResidentDescendants(parentID) {
		t.Fatal("descendant with a resident runtime was not reported")
	}
	c.mu.Lock()
	c.live[nestedID].runtime = nil
	c.mu.Unlock()
	if c.delegateHasResidentDescendants(parentID) {
		t.Fatal("descendant runtime cleared but still reported resident")
	}

	// A nested isolated delegate has its own lane; its resident runtime does not
	// occupy the parent's lane and must not block unlocking it.
	nestedChild := newSession(t)
	c.mu.Lock()
	nested := c.durable[nestedID]
	nested.Descriptor.Isolation = "worktree"
	nested.Descriptor.WorkingDir = filepath.Join(t.TempDir(), "nested-lane")
	c.live[nestedID] = &delegateLiveState{runtime: nestedChild}
	c.mu.Unlock()
	t.Cleanup(func() {
		c.mu.Lock()
		if live := c.live[nestedID]; live != nil && live.runtime == nestedChild {
			live.runtime = nil
		}
		c.mu.Unlock()
	})
	if c.delegateHasResidentDescendants(parentID) {
		t.Fatal("nested isolated delegate's own-lane runtime blocked the parent lane")
	}
}
