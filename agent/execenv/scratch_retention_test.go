package execenv

import (
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestScratchRetentionBindingMoveConcurrentMint drives the supported
// dual-allocation/clone topology: allocation A moves from E0 to E1 while a
// command on the emptied source mints B in the scratch-move window. The
// original manifest must end with two legitimate current slots — E1/sandbox=A
// and E0/unsandboxed=B — under unchanged root/owner identities, with stale
// revisions and a persistence failure refused without overwriting a fresh slot
// or releasing a lease before its committed reference/mapping.
func TestScratchRetentionBindingMoveConcurrentMint(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-test-session"}

	e0 := NewLocalExecutionEnvironment(workspace)
	e1 := NewLocalExecutionEnvironment(workspace)

	a, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	e0.ownedSessionTmp = a

	var b *sandbox.SessionScratch
	e0.ObserveScratchMoveWindowForTesting(func() {
		minted, err := sandbox.NewSessionScratch(base, workspace)
		if err != nil {
			t.Errorf("mint B in the move window: %v", err)
			return
		}
		b = minted
		e0.scratchMu.Lock()
		e0.unsandboxedScratch = minted
		e0.scratchMu.Unlock()
	})
	e1.AdoptSessionScratch(e0)
	if b == nil {
		t.Fatal("scratch-move window did not mint B")
	}

	refA := sandbox.ScratchReference{Dir: a.Dir, Kind: sandbox.ScratchKindSandbox}
	refB := sandbox.ScratchReference{Dir: b.Dir, Kind: sandbox.ScratchKindUnsandboxed}
	if err := a.Pin(owner, refA); err != nil {
		t.Fatalf("pin A: %v", err)
	}
	if err := b.Pin(owner, refB); err != nil {
		t.Fatalf("pin B: %v", err)
	}

	if err := e1.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{BindingID: "E1", OwnerSessionID: owner.RootSessionID, WorkingDir: workspace}); err != nil {
		t.Fatal(err)
	}
	if err := e0.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{BindingID: "E0", OwnerSessionID: owner.RootSessionID, WorkingDir: workspace}); err != nil {
		t.Fatal(err)
	}
	bindingE1, err := e1.ScratchRetentionBinding()
	if err != nil {
		t.Fatal(err)
	}
	bindingE0, err := e0.ScratchRetentionBinding()
	if err != nil {
		t.Fatal(err)
	}
	if bindingE1.Slots[sandbox.ScratchKindSandbox].Dir != a.Dir {
		t.Fatalf("E1 sandbox slot = %+v, want A", bindingE1.Slots)
	}
	if bindingE0.Slots[sandbox.ScratchKindUnsandboxed].Dir != b.Dir {
		t.Fatalf("E0 unsandboxed slot = %+v, want B", bindingE0.Slots)
	}

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	rev := manifest.Revision
	consumers := []sandbox.ScratchConsumerBinding{
		{SessionID: "R", CurrentBindingID: "E1"},
		{SessionID: "C", CurrentBindingID: "E0"},
	}
	if err := sandbox.UpdateScratchBindings(owner, rev, []sandbox.ScratchBinding{bindingE1, bindingE0}, consumers); err != nil {
		t.Fatalf("UpdateScratchBindings: %v", err)
	}
	if err := sandbox.UpdateScratchBindings(owner, rev, []sandbox.ScratchBinding{bindingE1}, nil); err == nil {
		t.Fatal("stale revision was accepted")
	}

	// A persistence failure on a different root must not touch this manifest.
	blocker := filepath.Join(base, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	badOwner := sandbox.ScratchOwner{StateDir: filepath.Join(blocker, "sub"), RootSessionID: owner.RootSessionID}
	if err := sandbox.UpdateScratchBindings(badOwner, 0, []sandbox.ScratchBinding{bindingE1}, nil); err == nil {
		t.Fatal("UpdateScratchBindings succeeded on an unwritable state dir")
	}

	manifest, err = sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Owner != owner {
		t.Fatalf("manifest owner = %+v, want %+v", manifest.Owner, owner)
	}
	slots := make(map[string]string)
	for _, binding := range manifest.Bindings {
		for kind, slot := range binding.Slots {
			if slot.OwnsLease {
				slots[binding.BindingID+"/"+kind] = slot.Dir
			}
		}
	}
	if slots["E1/"+sandbox.ScratchKindSandbox] != a.Dir || slots["E0/"+sandbox.ScratchKindUnsandboxed] != b.Dir {
		t.Fatalf("current slots = %+v, want E1/sandbox=A and E0/unsandboxed=B", slots)
	}
	if len(slots) != 2 {
		t.Fatalf("want exactly two current lease-owning slots, got %+v", slots)
	}
	// A successful UpdateScratchBindings must be committed before either lease
	// is released: both directories are still lease-held, so re-opening them
	// contends rather than silently taking a lease whose mapping is unsettled.
	if _, err := sandbox.OpenRetainedSessionScratch(owner, refA); err == nil {
		t.Fatal("A's lease was released before its committed mapping")
	}
	if _, err := sandbox.OpenRetainedSessionScratch(owner, refB); err == nil {
		t.Fatal("B's lease was released before its committed mapping")
	}
}

// TestScratchRetentionLiveEnvironmentPinsOnMint proves a live environment that
// installed a retention binding pins the allocation it mints and publishes the
// manifest, so the retention path is not inert in production.
func TestScratchRetentionLiveEnvironmentPinsOnMint(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-live"}
	env := NewLocalExecutionEnvironment(workspace)
	env.sandboxTmpBase = base
	if err := env.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{BindingID: "E0", OwnerSessionID: "root-live", WorkingDir: workspace}); err != nil {
		t.Fatal(err)
	}
	dir := env.unsandboxedScratchDir()
	if dir == "" {
		t.Fatal("environment minted no scratch")
	}
	t.Cleanup(func() { env.RetainSessionScratch() })
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.References) != 1 || filepath.Clean(manifest.References[0].Dir) != filepath.Clean(dir) {
		t.Fatalf("minted allocation not pinned: %+v", manifest.References)
	}
	if len(manifest.Bindings) != 1 {
		t.Fatalf("binding not published: %+v", manifest.Bindings)
	}
	slot := manifest.Bindings[0].Slots[sandbox.ScratchKindUnsandboxed]
	if !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(dir) {
		t.Fatalf("minted allocation slot = %+v, want owning slot at %q", slot, dir)
	}
}
