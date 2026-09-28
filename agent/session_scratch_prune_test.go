package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/clock"
	"primeradiant.com/evener/agent/sandbox"
)

// pinTestScratch mints a fresh scratch directory under base and pins it to
// owner as an unsandboxed allocation, returning the handle. The caller owns
// the handle's lease until it Retains it.
func pinTestScratch(t *testing.T, owner sandbox.ScratchOwner, base, workDir string) *sandbox.SessionScratch {
	t.Helper()
	s, err := sandbox.NewSessionScratch(base, workDir)
	if err != nil {
		t.Fatalf("NewSessionScratch: %v", err)
	}
	if err := s.Pin(owner, sandbox.ScratchReference{Dir: s.Dir, Kind: sandbox.ScratchKindUnsandboxed}); err != nil {
		t.Fatalf("Pin: %v", err)
	}
	return s
}

// newPruneTestRoot builds a root session whose state dir is its own working
// dir, so the retention manifest and the session's durable files share one
// tree exactly as a real daemon lays them out. It returns the root and the
// retention owner that tree names.
func newPruneTestRoot(t *testing.T) (*Session, sandbox.ScratchOwner) {
	t.Helper()
	root := newQueuePersistTestSession(t, t.TempDir())
	t.Cleanup(root.Close)
	root.delegateRootSessionID = ""
	return root, sandbox.ScratchOwner{StateDir: root.stateDir, RootSessionID: root.id}
}

// bindSurvivorAndGone publishes two unsandboxed bindings for owner — one whose
// allocation survives and one whose allocation the caller removes out of band —
// and returns both handles. It is the shared setup for the prune tests.
func bindSurvivorAndGone(t *testing.T, owner sandbox.ScratchOwner, base, workDir string) (survivor, gone *sandbox.SessionScratch) {
	t.Helper()
	survivor = pinTestScratch(t, owner, base, workDir)
	t.Cleanup(func() { _ = os.RemoveAll(survivor.Dir) })
	gone = pinTestScratch(t, owner, base, workDir)
	t.Cleanup(func() { _ = os.RemoveAll(gone.Dir) })

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{
			{BindingID: "E-survivor", OwnerSessionID: owner.RootSessionID, WorkingDir: workDir,
				Slots: map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: survivor.Dir, OwnsLease: true}}},
			{BindingID: "E-gone", OwnerSessionID: owner.RootSessionID, WorkingDir: workDir,
				Slots: map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: gone.Dir, OwnsLease: true}}},
		},
		[]sandbox.ScratchConsumerBinding{
			{SessionID: "consumer-survivor", CurrentBindingID: "E-survivor"},
			{SessionID: "consumer-gone", CurrentBindingID: "E-gone"},
		}); err != nil {
		t.Fatal(err)
	}
	if err := survivor.Retain(); err != nil {
		t.Fatal(err)
	}
	// Remove the gone allocation's directory out of band, exactly as a tmp
	// reaper or a manual rm would.
	if err := os.RemoveAll(gone.Dir); err != nil {
		t.Fatal(err)
	}
	return survivor, gone
}

// TestPrepareRetainedScratchPrunesMissingAllocation proves a restore heals a
// retention manifest that references an allocation whose directory was removed
// out of band — a tmp reaper, an operator's rm, a crashed reclaimer. The
// missing reference and the binding slot that named it are dropped, the
// surviving allocation still restores, and retirement readiness passes
// afterward instead of failing closed forever.
func TestPrepareRetainedScratchPrunesMissingAllocation(t *testing.T) {
	workDir := t.TempDir()
	base := t.TempDir()
	root, owner := newPruneTestRoot(t)
	survivor, gone := bindSurvivorAndGone(t, owner, base, workDir)

	// Before the fix this failed closed on the missing allocation and wedged
	// every later restore of the root.
	if err := root.prepareRetainedScratch(); err != nil {
		t.Fatalf("prepareRetainedScratch wedged on a missing allocation: %v", err)
	}

	pruned, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	survivorKept := false
	for _, ref := range pruned.References {
		switch filepath.Clean(ref.Dir) {
		case filepath.Clean(gone.Dir):
			t.Fatalf("prune kept the missing reference %q", gone.Dir)
		case filepath.Clean(survivor.Dir):
			survivorKept = true
		}
	}
	if !survivorKept {
		t.Fatalf("prune dropped the surviving reference %q", survivor.Dir)
	}
	for _, binding := range pruned.Bindings {
		if binding.BindingID != "E-gone" {
			continue
		}
		if _, ok := binding.Slots[sandbox.ScratchKindUnsandboxed]; ok {
			t.Fatal("prune kept the missing allocation's binding slot")
		}
	}
	// The wedge class this heals: retirement readiness previously failed closed
	// forever on the missing directory.
	if err := root.validateRetainedScratchPresent(); err != nil {
		t.Fatalf("retirement readiness still wedged after the prune: %v", err)
	}
}

// TestRetirementPreparationPrunesMissingAllocation proves retirement
// readiness reconciles the same missing allocation rather than failing closed
// on it: a live root whose directory vanished out of band stays retirable. The
// bare fixture has no transcript for the later reconstruction step, so the
// assertion pins what this change owns — preparation no longer reports the
// scratch wedge, and the missing reference is pruned.
func TestRetirementPreparationPrunesMissingAllocation(t *testing.T) {
	workDir := t.TempDir()
	base := t.TempDir()
	root, owner := newPruneTestRoot(t)
	_, gone := bindSurvivorAndGone(t, owner, base, workDir)

	// The wedge without the prune: readiness fails closed on the missing
	// directory.
	if err := root.validateRetainedScratchPresent(); err == nil {
		t.Fatal("fixture did not reproduce the missing-directory wedge")
	}

	c, err := NewRetirementController(0, clock.Real())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachRoot(root); err != nil {
		t.Fatal(err)
	}
	claim, _, err := c.TryClaim(true)
	if err != nil || claim == nil {
		t.Fatalf("claim: %v %v", claim, err)
	}
	if _, err := c.Prepare(context.Background(), claim); err != nil && strings.Contains(err.Error(), "retained scratch") {
		t.Fatalf("retirement preparation wedged on the missing allocation: %v", err)
	}
	pruned, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range pruned.References {
		if filepath.Clean(ref.Dir) == filepath.Clean(gone.Dir) {
			t.Fatal("retirement preparation did not prune the missing reference")
		}
	}
}

// TestPruneMissingScratchReferencesNoOpOnHealthyManifest proves the prune is a
// no-op for a healthy manifest: no durable write, no revision bump, no
// reference or slot churn. The restore path only reaches the prune when a
// reference's directory is gone, so a healthy manifest must never be rewritten.
func TestPruneMissingScratchReferencesNoOpOnHealthyManifest(t *testing.T) {
	workDir := t.TempDir()
	base := t.TempDir()
	owner := sandbox.ScratchOwner{StateDir: t.TempDir(), RootSessionID: "prune-noop-root"}

	intact := pinTestScratch(t, owner, base, workDir)
	t.Cleanup(func() { _ = os.RemoveAll(intact.Dir) })

	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{{
			BindingID: "E0", OwnerSessionID: owner.RootSessionID, WorkingDir: workDir,
			Slots: map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: intact.Dir, OwnsLease: true}},
		}},
		[]sandbox.ScratchConsumerBinding{{SessionID: owner.RootSessionID, CurrentBindingID: "E0"}}); err != nil {
		t.Fatal(err)
	}
	before, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if err := intact.Retain(); err != nil {
		t.Fatal(err)
	}

	_, written, err := sandbox.PruneMissingScratchReferences(owner)
	if err != nil {
		t.Fatalf("PruneMissingScratchReferences: %v", err)
	}
	if written {
		t.Fatal("prune reported a write for a healthy manifest")
	}
	after, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("an intact manifest was rewritten: revision %d -> %d", before.Revision, after.Revision)
	}
	if len(after.References) != 1 || filepath.Clean(after.References[0].Dir) != filepath.Clean(intact.Dir) {
		t.Fatalf("intact manifest references changed: %+v", after.References)
	}
}
