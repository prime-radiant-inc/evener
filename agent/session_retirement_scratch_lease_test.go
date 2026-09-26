//go:build unix

package agent

import (
	"context"
	"os"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
	"primeradiant.com/evener/llm"
)

// TestRetirementReleaseOfASharedChildKeepsTheParentScratchLease pins the round
// 11 finding. A child spawned with neither a working dir nor a box of its own
// runs on its parent's very environment object: prepareSubagentEnvironment
// hands the parent's env through untouched and recordEnvironmentOwnership
// records it in parentSharedEnv. When that child is released for retirement its
// current environment is therefore the LIVE parent's object, and the parent is
// still working in it. Releasing the lease there takes the scratch off an
// environment the parent still owns; only the parent's own release may do that.
//
// The assertion is behavioural: a competing lock acquisition on the parent's
// scratch lease (the real OS-level flock) must still fail while the parent
// holds it. The parent stays live and is never retired here, so the outcome
// cannot be confused with the parent's own intended release.
func TestRetirementReleaseOfASharedChildKeepsTheParentScratchLease(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	parent := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	shared, ok := parent.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("parent env = %T, want a local environment", parent.currentEnv())
	}
	// The parent's first command mints its scratch and takes the lease.
	if _, err := shared.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand to mint the parent's scratch: %v", err)
	}
	parentScratch := heldParentScratch(t, shared)
	t.Cleanup(func() { _ = os.RemoveAll(parentScratch) })

	ctx := context.WithValue(context.Background(), ctxDelegationAllowance, 0)
	prepared, err := parent.prepareSubagentRun(ctx, "child task", "", "", 0, "", "", nil, nil)
	if err != nil {
		t.Fatalf("prepareSubagentRun: %v", err)
	}
	t.Cleanup(func() {
		releasePreparedTreeSlot(prepared)
		prepared.disposeUnadopted()
	})
	child := prepared.sub.sess
	if child.currentEnv() != parent.currentEnv() {
		t.Fatal("the spawned child did not land on the parent's environment; this test would prove nothing")
	}
	if child.ownsEnv || child.parentSharedEnv == nil {
		t.Fatal("the spawned child did not record the parent's environment as shared; this test would prove nothing")
	}

	// A real reachable production release path for one resident child runtime.
	// It must settle the child's own resources only: its current environment is
	// the parent's own object, whose lease is the parent's to release.
	if err := child.releaseChildRuntimeForRetirement(context.Background()); err != nil {
		t.Fatalf("releaseChildRuntimeForRetirement: %v", err)
	}

	if !scratchLeaseHeld(t, parentScratch) {
		t.Errorf("the retired shared child released the parent's scratch %s lease while the parent is still working in it", parentScratch)
	}
}

// TestRetirementReleaseRetainsOwnEnvironmentScratch pins the positive direction
// the trap section warns about: a ROOT has parentSharedEnv nil, so the ownership
// guard must skip nothing and the root's own current and parked scratch leases
// must still be released by retirement, with the directories kept for the
// handoff and the manifest left unreleased.
func TestRetirementReleaseRetainsOwnEnvironmentScratch(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	t.Cleanup(func() { root.Close() })
	rootLocal, ok := root.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("root env = %T, want a local environment", root.currentEnv())
	}
	if _, err := rootLocal.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand to mint the root's scratch: %v", err)
	}
	currentScratch := heldParentScratch(t, rootLocal)
	t.Cleanup(func() { _ = os.RemoveAll(currentScratch) })

	// A root that entered a worktree parks its launch environment: the parked
	// object is the root's own, not a parent's shared one.
	parked := execenv.NewLocalExecutionEnvironment(t.TempDir())
	if _, err := parked.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand on the parked environment: %v", err)
	}
	parkedScratch := parked.SessionScratchDir()
	if parkedScratch == "" {
		t.Fatal("the parked environment minted no session scratch")
	}
	t.Cleanup(func() { _ = os.RemoveAll(parkedScratch) })
	root.mu.Lock()
	root.worktreeRestoreEnv = parked
	root.mu.Unlock()

	root.releaseRetirementScratch()

	for name, scratch := range map[string]string{"current": currentScratch, "parked": parkedScratch} {
		if _, err := os.Stat(scratch); err != nil {
			t.Errorf("retirement removed the root's %s scratch %s, want it kept for the handoff: %v", name, scratch, err)
		}
		if scratchLeaseHeld(t, scratch) {
			t.Errorf("the root's own %s scratch %s lease is still held after retirement", name, scratch)
		}
	}

	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("the root has no scratch retention owner")
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Released {
		t.Error("retirement wrote the Released tombstone")
	}
}

// TestRetirementReleaseOfAnOwningChildReleasesItsOwnScratch is the child half of
// the positive direction: a child whose environment was built FOR it records no
// parentSharedEnv, so retirement must release that clone's lease and keep the
// directory. Only the child's own environment is settled; nothing here is the
// parent's.
func TestRetirementReleaseOfAnOwningChildReleasesItsOwnScratch(t *testing.T) {
	t.Parallel()
	client := llm.NewClient()
	client.Register(&fakeAdapter{name: "openai"})
	parent := newSession(t, withClient(client), withDir(t.TempDir()), withoutGitSnapshot())
	parentLocal, ok := parent.currentEnv().(*execenv.LocalExecutionEnvironment)
	if !ok {
		t.Fatalf("parent env = %T, want a local environment", parent.currentEnv())
	}

	childEnv := parentLocal.WithWorkingDirectory(t.TempDir())
	child, err := NewSession(client, parent.currentProfile(), childEnv, SessionConfig{
		MaxSubagentDepth: 1,
		testOnly:         testConfig{skipGitSnapshot: true},
	})
	if err != nil {
		t.Fatalf("NewSession on the child's clone: %v", err)
	}
	child.ownsEnv = true
	if _, err := childEnv.ExecCommand(context.Background(), "true", 5000, "", nil); err != nil {
		t.Fatalf("ExecCommand to mint the child's scratch: %v", err)
	}
	childScratch := childEnv.SessionScratchDir()
	if childScratch == "" {
		t.Fatal("the child minted no session scratch, so there is nothing to release")
	}
	t.Cleanup(func() { _ = os.RemoveAll(childScratch) })
	if !scratchLeaseHeld(t, childScratch) {
		t.Fatal("the child's scratch lease is not held before retirement")
	}

	if err := child.releaseChildRuntimeForRetirement(context.Background()); err != nil {
		t.Fatalf("releaseChildRuntimeForRetirement: %v", err)
	}

	if _, err := os.Stat(childScratch); err != nil {
		t.Errorf("retirement removed the owning child's scratch %s, want it kept for the handoff: %v", childScratch, err)
	}
	if scratchLeaseHeld(t, childScratch) {
		t.Errorf("the owning child's scratch %s lease is still held after retirement", childScratch)
	}
}
