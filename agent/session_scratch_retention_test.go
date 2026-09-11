package agent

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/sandbox"
)

// TestRetirementColdDelegateScratchManifest proves the root-owned manifest
// round-trips through the agent layer: a pinned allocation referenced only by a
// cold consumer is reacquired by prepareRetainedScratch and adopted by the
// consumer's binding at its original absolute path with its bytes intact.
func TestRetirementColdDelegateScratchManifest(t *testing.T) {
	dir := t.TempDir()
	root := newQueuePersistTestSession(t, dir)
	defer root.Close()
	owner, ok := root.scratchRetentionOwner()
	if !ok {
		t.Fatal("root had no scratch retention owner")
	}

	base := t.TempDir()
	scratch, err := sandbox.NewSessionScratch(base, dir)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(scratch.Dir, "cold.bin")
	want := []byte("cold-delegate-artifact")
	if err := os.WriteFile(artifact, want, 0o600); err != nil {
		t.Fatal(err)
	}
	ref := sandbox.ScratchReference{Dir: scratch.Dir, Kind: sandbox.ScratchKindUnsandboxed}
	if err := scratch.Pin(owner, ref); err != nil {
		t.Fatal(err)
	}
	manifest, err := sandbox.LoadScratchRetention(owner)
	if err != nil {
		t.Fatal(err)
	}
	binding := sandbox.ScratchBinding{
		BindingID:      "E0",
		OwnerSessionID: owner.RootSessionID,
		WorkingDir:     dir,
		Slots:          map[string]sandbox.ScratchSlot{sandbox.ScratchKindUnsandboxed: {Dir: scratch.Dir, OwnsLease: true}},
	}
	if err := sandbox.UpdateScratchBindings(owner, manifest.Revision,
		[]sandbox.ScratchBinding{binding},
		[]sandbox.ScratchConsumerBinding{{SessionID: "cold-child", CurrentBindingID: "E0"}}); err != nil {
		t.Fatal(err)
	}
	// Release the live lease: a cold consumer's allocation must restore from the
	// manifest, not from a live handle.
	if err := scratch.Retain(); err != nil {
		t.Fatal(err)
	}

	if err := root.prepareRetainedScratch(); err != nil {
		t.Fatalf("prepareRetainedScratch: %v", err)
	}
	if id, ok := root.retainedScratchBindingFor("cold-child", "current"); !ok || id != "E0" {
		t.Fatalf("cold consumer binding = %q ok=%v, want E0", id, ok)
	}
	if _, ok := root.retainedScratchBindingFor("missing-child", "current"); ok {
		t.Fatal("unknown consumer resolved a binding")
	}

	env := execenv.NewLocalExecutionEnvironment(dir)
	if err := root.adoptRetainedScratch(env, "E0"); err != nil {
		t.Fatalf("adoptRetainedScratch: %v", err)
	}
	if got := env.SessionScratchDir(); filepath.Clean(got) != filepath.Clean(scratch.Dir) {
		t.Fatalf("restored scratch dir = %q, want original %q", got, scratch.Dir)
	}
	got, err := os.ReadFile(artifact)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("artifact lost at original path: bytes=%q err=%v", got, err)
	}
	// A second adoption of the same binding must fail rather than duplicate the
	// lease onto another environment.
	other := execenv.NewLocalExecutionEnvironment(dir)
	if err := root.adoptRetainedScratch(other, "E0"); err == nil {
		t.Fatal("adopted an already-transferred binding onto a second environment")
	}
	env.RetainSessionScratch()
}
