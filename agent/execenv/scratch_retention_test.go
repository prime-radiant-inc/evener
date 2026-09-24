package execenv

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"primeradiant.com/evener/agent/sandbox"
)

// TestScratchRetentionBindingMoveConcurrentMint drives the supported
// dual-allocation/clone topology against a manifest that already holds the
// pre-move E0/A record. Allocation A moves from E0 to E1 through the real
// update path while a racing writer mints B into E0; the move must preserve
// both current slots — E1/sandbox=A and E0/unsandboxed=B — under unchanged
// root/owner identities. Stale-revision and persistence-failure refusals must
// leave a fresh slot untouched and no lease released before its committed
// reference/mapping.
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

	// Seed the manifest with the pre-move E0/A record through the real writer.
	seed := sandbox.ScratchBinding{
		BindingID:      "E0",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     workspace,
		Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindSandbox: {Dir: a.Dir, OwnsLease: true}},
	}
	consumerR := sandbox.ScratchConsumerBinding{SessionID: "R", CurrentBindingID: "E0"}
	if err := sandbox.UpsertScratchBinding(owner, seed, consumerR); err != nil {
		t.Fatalf("seed E0/A: %v", err)
	}
	// A racing writer mints B into E0 before the move commits.
	racer := sandbox.ScratchBinding{
		BindingID:      "E0",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     workspace,
		Slots: map[string]sandbox.ScratchSlot{
			sandbox.ScratchKindSandbox:     {Dir: a.Dir, OwnsLease: true},
			sandbox.ScratchKindUnsandboxed: {Dir: b.Dir, OwnsLease: true},
		},
	}
	if err := sandbox.UpsertScratchBinding(owner, racer, consumerR); err != nil {
		t.Fatalf("racing mint B: %v", err)
	}
	// A stale record that still names only A must not erase the racing B.
	if err := sandbox.UpsertScratchBinding(owner, seed, consumerR); err != nil {
		t.Fatalf("stale retry: %v", err)
	}
	if got := manifestBinding(t, owner, "E0").Slots[sandbox.ScratchKindUnsandboxed]; filepath.Clean(got.Dir) != filepath.Clean(b.Dir) || !got.OwnsLease {
		t.Fatalf("stale retry erased B: %+v", got)
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
	// The plan's single-transaction move: E1 takes A's owning slot, E0 keeps B.
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
	// The lease is an *os.File inside the lease object, and *os.File's
	// finalizer closes the fd — releasing the flock — the moment its holder
	// becomes unreachable. Pin the holders past the contention assertions
	// above: otherwise aggressive GC can legitimately release the leases
	// between the committed UpdateScratchBindings and the
	// OpenRetainedSessionScratch calls, making this test nondeterministic
	// without any product change. KeepAlive is liveness only; the assertions
	// still require both opens to fail.
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	runtime.KeepAlive(e0)
	runtime.KeepAlive(e1)
}

// TestPinOwnedScratchRetriesLockContention pins the round-8 contention
// contract at the mint pin: the manifest's fail-fast update lock can refuse
// the pin while a concurrent in-process writer holds it, and that refusal is
// transient by construction — never a durability verdict — so the pin must
// retry it a bounded number of times instead of recording a sticky retention
// failure that poisons every later preparation of a live environment. The
// holder is released deterministically between the first failed attempt and
// the retry.
func TestPinOwnedScratchRetriesLockContention(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-pin-retry"}
	e := NewLocalExecutionEnvironment(workspace)
	scratch, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Retain() })
	if err := e.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{
		BindingID:      "b-pin-retry",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     workspace,
	}); err != nil {
		t.Fatal(err)
	}
	e.ownedSessionTmp = scratch

	takeLock := make(chan struct{})
	lockTaken := make(chan struct{})
	releaseLock := make(chan struct{})
	lockReleased := make(chan struct{})
	go func() {
		<-takeLock
		_ = sandbox.WithScratchRetentionLock(owner, func() error {
			close(lockTaken)
			<-releaseLock
			return nil
		})
		close(lockReleased)
	}()
	e.scratchPinProbe = func(attempt int) {
		switch attempt {
		case 1:
			close(takeLock)
			<-lockTaken
		case 2:
			close(releaseLock)
			<-lockReleased
		}
	}
	if err := e.PinOwnedScratch(); err != nil {
		t.Fatalf("pin lost to transient lock contention: %v", err)
	}
	if sticky := e.ScratchRetentionError(); sticky != nil {
		t.Fatalf("a transient lock refusal was recorded sticky: %v", sticky)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	var stored sandbox.ScratchBinding
	found := false
	for _, binding := range manifest.Bindings {
		if binding.BindingID == "b-pin-retry" {
			stored, found = binding, true
			break
		}
	}
	if !found {
		t.Fatalf("the retried pin never published the binding: %+v", manifest.Bindings)
	}
	slot, ok := stored.Slots[sandbox.ScratchKindSandbox]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(scratch.Dir) {
		t.Fatalf("the retried pin lost the owned slot: %+v", stored.Slots)
	}
}

// TestPinOwnedScratchWithoutOwnedHandlesIsANoOp pins round 58's second
// Medium: the pin's early return demanded live handles before it would
// no-op, so an environment that owns no scratch at all still submitted its
// installed binding's stale slots, and PinScratchBinding's validation
// rejected them before the slotless republish the reinstall needed. With no
// owned live handle there is nothing to pin: the pin is a true no-op.
func TestPinOwnedScratchWithoutOwnedHandlesIsANoOp(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-stale-slots"}
	e := NewLocalExecutionEnvironment(workspace)
	t.Cleanup(func() { e.Cleanup(); e.DisposeSandboxScratch(); e.DisposeUnsandboxedScratch() })
	// The reinstall-after-move shape: the environment keeps its binding
	// identity — whose row still names the allocation that moved away — but
	// owns no live scratch handle of any kind.
	staleDir := filepath.Join(base, "moved-away")
	if err := e.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{
		BindingID:      "b-stale-slots",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     workspace,
		Slots: map[string]sandbox.ScratchSlot{
			sandbox.ScratchKindUnsandboxed: {Dir: staleDir, OwnsLease: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.PinOwnedScratch(); err != nil {
		t.Fatalf("the pin submitted stale slots with no owned handles: %v", err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range manifest.Bindings {
		if binding.BindingID == "b-stale-slots" {
			t.Fatalf("the no-op pin published the stale-slotted binding: %+v", binding)
		}
	}
}

// TestPinOwnedScratchRecoversFromExhaustedLockContention pins round 58's
// third Medium: contention the pin's retry bound cannot clear was recorded
// as a sticky durability error that a later successful pin never cleared —
// though the pin's own contract says a lock race must not permanently poison
// a live environment's retention state. Once the holder releases and a pin
// succeeds, preparation must recover.
func TestPinOwnedScratchRecoversFromExhaustedLockContention(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-lock-held-recovery"}
	e := NewLocalExecutionEnvironment(workspace)
	scratch, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Retain() })
	if err := e.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{
		BindingID:      "b-lock-held-recovery",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     workspace,
	}); err != nil {
		t.Fatal(err)
	}
	e.ownedSessionTmp = scratch

	takeLock := make(chan struct{})
	lockTaken := make(chan struct{})
	releaseLock := make(chan struct{})
	lockReleased := make(chan struct{})
	go func() {
		<-takeLock
		_ = sandbox.WithScratchRetentionLock(owner, func() error {
			close(lockTaken)
			<-releaseLock
			return nil
		})
		close(lockReleased)
	}()
	// The holder keeps the manifest lock past the pin's whole retry bound:
	// every attempt is refused and the retry gives up on the last one. The
	// probe stays installed across both pins, so the close is guarded.
	started := false
	e.scratchPinProbe = func(attempt int) {
		if !started {
			started = true
			close(takeLock)
		}
		<-lockTaken
	}
	err = e.PinOwnedScratch()
	if err == nil || !errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
		t.Fatalf("the pin under a held lock must report lock contention, got %v", err)
	}
	if sticky := e.ScratchRetentionError(); sticky == nil || !errors.Is(sticky, sandbox.ErrScratchRetentionLockHeld) {
		t.Fatalf("the exhausted contention was not recorded: %v", sticky)
	}
	close(releaseLock)
	<-lockReleased
	if err := e.PinOwnedScratch(); err != nil {
		t.Fatalf("the pin after the holder released: %v", err)
	}
	if sticky := e.ScratchRetentionError(); sticky != nil {
		t.Fatalf("a recovered lock race still poisons preparation: %v", sticky)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	var stored sandbox.ScratchBinding
	found := false
	for _, binding := range manifest.Bindings {
		if binding.BindingID == "b-lock-held-recovery" {
			stored, found = binding, true
			break
		}
	}
	if !found {
		t.Fatalf("the recovered pin never published the binding: %+v", manifest.Bindings)
	}
	slot, ok := stored.Slots[sandbox.ScratchKindSandbox]
	if !ok || filepath.Clean(slot.Dir) != filepath.Clean(scratch.Dir) {
		t.Fatalf("the recovered pin lost the owned slot: %+v", stored.Slots)
	}
}

// TestPinOwnedScratchNoHandlesClearsARecoverableSticky pins round 59's second
// Medium: the no-owned-handle no-op returned before the recovery clear, so a
// prior lock-held or released-manifest error stayed sticky on an environment
// whose ownership has since moved on — every later pin no-ops, nothing can
// ever clear the record, and preparation is blocked forever over state that
// no longer holds.
func TestPinOwnedScratchNoHandlesClearsARecoverableSticky(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-sticky-nohandles"}
	e := NewLocalExecutionEnvironment(workspace)
	scratch, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = scratch.Retain() })
	if err := e.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{
		BindingID:      "b-sticky-nohandles",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     workspace,
	}); err != nil {
		t.Fatal(err)
	}
	e.ownedSessionTmp = scratch

	takeLock := make(chan struct{})
	lockTaken := make(chan struct{})
	releaseLock := make(chan struct{})
	lockReleased := make(chan struct{})
	go func() {
		<-takeLock
		_ = sandbox.WithScratchRetentionLock(owner, func() error {
			close(lockTaken)
			<-releaseLock
			return nil
		})
		close(lockReleased)
	}()
	started := false
	e.scratchPinProbe = func(attempt int) {
		if !started {
			started = true
			close(takeLock)
		}
		<-lockTaken
	}
	err = e.PinOwnedScratch()
	if err == nil || !errors.Is(err, sandbox.ErrScratchRetentionLockHeld) {
		t.Fatalf("the pin under a held lock must report lock contention, got %v", err)
	}
	if sticky := e.ScratchRetentionError(); sticky == nil {
		t.Fatal("the exhausted contention was not recorded")
	}
	// Ownership moves on: the handle is handed off, so the environment owns
	// nothing live and every later pin takes the no-op path.
	close(releaseLock)
	<-lockReleased
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}
	if err := e.PinOwnedScratch(); err != nil {
		t.Fatalf("the pin with no owned handles: %v", err)
	}
	if sticky := e.ScratchRetentionError(); sticky != nil {
		t.Fatalf("transferred ownership left the recoverable race sticky with nothing left to clear it: %v", sticky)
	}
}

func manifestBinding(t *testing.T, owner sandbox.ScratchOwner, bindingID string) sandbox.ScratchBinding {
	t.Helper()
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, binding := range manifest.Bindings {
		if binding.BindingID == bindingID {
			return binding
		}
	}
	t.Fatalf("binding %q missing: %+v", bindingID, manifest.Bindings)
	return sandbox.ScratchBinding{}
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

// TestScratchRetentionErrorClearsWhenTheReleasedManifestRepins pins round 27's
// Medium: a pin that raced a terminal release recorded
// ErrScratchRetentionReleased as a sticky error, and the sticky record outlived
// the recovery — a later reset reinitialized the manifest and the very
// republish this environment performs succeeded, yet preparation kept failing
// on the stale error. A successful pin under the manifest that now exists
// proves the released race healed, so the sticky released error must clear
// with it; other pin failures stay sticky (round 9).
func TestScratchRetentionErrorClearsWhenTheReleasedManifestRepins(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-released-repin"}
	env := NewLocalExecutionEnvironment(workspace)
	env.sandboxTmpBase = base
	if err := env.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{BindingID: "E0", OwnerSessionID: "root-released-repin", WorkingDir: workspace}); err != nil {
		t.Fatal(err)
	}
	dir := env.unsandboxedScratchDir()
	if dir == "" {
		t.Fatal("environment minted no scratch")
	}
	t.Cleanup(func() { env.RetainSessionScratch() })
	// The pin races the terminal release: writers refuse a released manifest,
	// so the pin fails and the failure is recorded sticky for preparation.
	if err := sandbox.ReleaseScratchRetention(owner); err != nil {
		t.Fatal(err)
	}
	if err := env.PinOwnedScratch(); !errors.Is(err, sandbox.ErrScratchRetentionReleased) {
		t.Fatalf("pin over the released manifest: %v", err)
	}
	if env.ScratchRetentionError() == nil {
		t.Fatal("the released pin failure was not recorded sticky")
	}
	// The reset reinitializes the manifest — the pin the pair's death leaves
	// in place is exactly the identity the republish writes — and the
	// republish succeeds against it.
	if _, _, err := sandbox.ResetScratchRetentionIfReleased(owner); err != nil {
		t.Fatalf("reset the released manifest: %v", err)
	}
	if err := env.PinOwnedScratch(); err != nil {
		t.Fatalf("repin over the reset manifest: %v", err)
	}
	if err := env.ScratchRetentionError(); err != nil {
		t.Fatalf("preparation still fails on the stale released-pin error after a successful repin: %v", err)
	}
}

// TestScratchRetentionAdoptionPublishesTheDestinationPin proves the lazy-mint
// pin race is closed: when AdoptSessionScratch moves a freshly minted
// unsandboxed allocation to a destination that holds a retention binding, the
// destination's pin must be persisted even though the source's own post-mint
// pin runs in the move window and observes no leased handle.
func TestScratchRetentionAdoptionPublishesTheDestinationPin(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-adopt"}
	source := NewLocalExecutionEnvironment(workspace)
	target := NewLocalExecutionEnvironment(workspace)
	if err := source.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{BindingID: "E0", OwnerSessionID: "root-adopt", WorkingDir: workspace}); err != nil {
		t.Fatal(err)
	}
	if err := target.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{BindingID: "E1", OwnerSessionID: "root-adopt", WorkingDir: workspace}); err != nil {
		t.Fatal(err)
	}

	// The source just minted an unsandboxed allocation and is about to pin it.
	tmp, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	source.scratchMu.Lock()
	source.unsandboxedScratch = tmp
	source.scratchMu.Unlock()
	// The source's own post-mint pin runs in the move window, after its scratch
	// fields have been taken and before the target installs them, so it finds no
	// leased handle to publish.
	restore := source.ObserveScratchMoveWindowForTesting(func() {
		_ = source.PinOwnedScratch()
	})
	defer restore()

	target.AdoptSessionScratch(source)
	t.Cleanup(func() {
		target.RetainSessionScratch()
		source.RetainSessionScratch()
	})

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	var refPinned bool
	for _, ref := range manifest.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(tmp.Dir) {
			refPinned = true
		}
	}
	if !refPinned {
		t.Fatalf("adopted allocation was never pinned: %+v", manifest.References)
	}
	binding := manifestBinding(t, owner, "E1")
	slot, ok := binding.Slots[sandbox.ScratchKindUnsandboxed]
	if !ok || !slot.OwnsLease || filepath.Clean(slot.Dir) != filepath.Clean(tmp.Dir) {
		t.Fatalf("destination binding slot = %+v ok=%v, want it owning %q", slot, ok, tmp.Dir)
	}
}

// unownedScratchReferences returns the canonical directories owner's retention
// manifest references that no binding owns with a lease-owning slot. Every path
// in that set is durable state nothing can attribute: the reference keeps the
// allocation retained while no binding maps it into any environment, so the
// next restore has nothing to adopt and the allocation is never collected
// either. It is the consequence a partial PinOwnedScratch publication used to
// leave behind, and it is what these tests assert is impossible.
func unownedScratchReferences(t *testing.T, owner sandbox.ScratchOwner) []string {
	t.Helper()
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatalf("load retention manifest: %v", err)
	}
	owned := make(map[string]struct{}, len(manifest.Bindings))
	for _, binding := range manifest.Bindings {
		for _, slot := range binding.Slots {
			if !slot.OwnsLease {
				continue
			}
			owned[filepath.Clean(slot.Dir)] = struct{}{}
		}
	}
	var unowned []string
	for _, ref := range manifest.References {
		if _, ok := owned[filepath.Clean(ref.Dir)]; !ok {
			unowned = append(unowned, filepath.Clean(ref.Dir))
		}
	}
	return unowned
}

// scratchRetentionManifestDir is the retention directory the sandbox package
// derives from an owner's state dir. The package keeps the name private, so a
// cross-package test that has to change the directory's mode spells it out.
func scratchRetentionManifestDir(owner sandbox.ScratchOwner) string {
	return filepath.Join(owner.StateDir, "scratch-retention")
}

// TestScratchRetentionPartiallyPinnedOwnedScratchLeavesNoUnownedReference is the
// round-19 regression test for PinOwnedScratch's non-atomic publication. The
// environment already published its first allocation and its binding; a second
// owned allocation is then pinned into a manifest the writer cannot fsync after
// committing it, so the pin step reports a failure that the caller aborts on.
// Until the fix, the pin loop had already committed that allocation's manifest
// reference, and because the loop returned before UpsertScratchBindingOnly the
// binding that should own it was never published: the new allocation was
// referenced, retained, and owned by nobody. Either loop order reaches the same
// state — the preexisting reference is a no-op re-pin while the new one is the
// only pin that can commit — so the assertion is deterministic.
//
// The failure is a real filesystem condition, not a hook. A retention directory
// whose mode is 0o300 stays usable for the owner's known paths (read a known
// manifest, create the temp file, rename it over the manifest) but denies the
// read permission os.Open needs, which is exactly how
// atomicWritePrivateFile's trailing directory fsync fails — the documented case
// where writeScratchRetention reports an error after the rename committed.
func TestScratchRetentionPartiallyPinnedOwnedScratchLeavesNoUnownedReference(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a write-only retention directory cannot be created as root")
	}
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-pin-owned"}
	env := NewLocalExecutionEnvironment(workspace)
	if err := env.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{
		BindingID:      "E0",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     workspace,
	}); err != nil {
		t.Fatal(err)
	}
	first, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Cleanup() })
	env.ownedSessionTmp = first
	if err := env.PinOwnedScratch(); err != nil {
		t.Fatalf("publish the first owned allocation: %v", err)
	}
	if unowned := unownedScratchReferences(t, owner); len(unowned) != 0 {
		t.Fatalf("unowned references after a complete publication = %v, want none", unowned)
	}

	second, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Cleanup() })
	env.unsandboxedScratch = second
	retentionDir := scratchRetentionManifestDir(owner)
	if err := os.Chmod(retentionDir, 0o300); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(retentionDir, 0o700) })

	if err := env.PinOwnedScratch(); err == nil {
		t.Fatal("PinOwnedScratch reported success although the manifest could not be synced")
	}
	if err := env.ScratchRetentionError(); err == nil {
		t.Fatal("the failed pin was recorded as a success")
	}
	if unowned := unownedScratchReferences(t, owner); len(unowned) != 0 {
		t.Fatalf("references owned by no binding after a failed pin = %v, want none: every reference a pin step made durable must be owned by the published binding", unowned)
	}
}

// TestScratchRetentionFailedBindingPublicationLeavesNoUnownedReference drives
// the other half of the same finding: every pin succeeded and the final binding
// publication failed. The publication's own validator rejects a merged binding
// whose recorded slot names a directory no reference pins — the state this
// finding is about, and one an environment can genuinely carry, because
// SetScratchRetentionBinding installs the persisted binding an adoption or a
// cold resume read out of the manifest. Until the fix the environment's pin was
// already durable when the publication failed, so the manifest ended up holding
// a reference with no binding at all, which restore refuses as a contradictory
// graph.
func TestScratchRetentionFailedBindingPublicationLeavesNoUnownedReference(t *testing.T) {
	base, workspace := t.TempDir(), t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-publish-failed"}
	env := NewLocalExecutionEnvironment(workspace)
	if err := env.SetScratchRetentionBinding(owner, sandbox.ScratchBinding{
		BindingID:      "E0",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     workspace,
		Slots: map[string]sandbox.ScratchSlot{
			sandbox.ScratchKindSandbox: {Dir: filepath.Join(workspace, "never-pinned"), OwnsLease: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	minted, err := sandbox.NewSessionScratch(base, workspace)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = minted.Cleanup() })
	env.unsandboxedScratch = minted

	if err := env.PinOwnedScratch(); err == nil {
		t.Fatal("PinOwnedScratch reported success although the binding could not be published")
	}
	if err := env.ScratchRetentionError(); err == nil {
		t.Fatal("the failed publication was recorded as a success")
	}
	if unowned := unownedScratchReferences(t, owner); len(unowned) != 0 {
		t.Fatalf("references owned by no binding after a failed publication = %v, want none: a pin that cannot be published must not become durable", unowned)
	}
}

// TestSetScratchRetentionBindingResetsPendingOnlyForNewIdentity pins the
// round-13 dead-guard fix: the pending-marker reset must compare against the
// binding identity being REPLACED, so a genuinely different identity
// re-derives contention for every kind while the same identity re-installed
// (a transfer or a re-adoption) keeps the markers it is owed.
func TestSetScratchRetentionBindingResetsPendingOnlyForNewIdentity(t *testing.T) {
	workspace := t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "root-test-session"}
	e := NewLocalExecutionEnvironment(workspace)
	t.Cleanup(func() { e.Cleanup(); e.DisposeSandboxScratch() })

	first := sandbox.ScratchBinding{BindingID: "E0", OwnerSessionID: owner.RootSessionID, WorkingDir: workspace}
	if err := e.SetScratchRetentionBinding(owner, first); err != nil {
		t.Fatal(err)
	}
	e.MarkRetainedSlotPending(sandbox.ScratchKindSandbox)
	if kinds := e.RetentionPendingKinds(); len(kinds) != 1 {
		t.Fatalf("fixture expected the pending marker recorded, got %v", kinds)
	}

	// A different logical identity re-derives contention for every kind this
	// cycle: its pending markers must not outlive the binding they describe.
	second := sandbox.ScratchBinding{BindingID: "E1", OwnerSessionID: owner.RootSessionID, WorkingDir: workspace}
	if err := e.SetScratchRetentionBinding(owner, second); err != nil {
		t.Fatal(err)
	}
	if kinds := e.RetentionPendingKinds(); len(kinds) != 0 {
		t.Fatalf("a different binding identity must reset the pending markers, got %v", kinds)
	}

	// The same identity re-installed is a transfer or a re-adoption of one
	// logical environment: its markers travel with it (round 12).
	e.MarkRetainedSlotPending(sandbox.ScratchKindSandbox)
	if err := e.SetScratchRetentionBinding(owner, second); err != nil {
		t.Fatal(err)
	}
	if kinds := e.RetentionPendingKinds(); len(kinds) != 1 {
		t.Fatalf("the same identity re-installed must keep the pending markers, got %v", kinds)
	}
}
