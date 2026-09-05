package execenv

import (
	"path/filepath"
	"testing"
)

// Session close runs the environment's Cleanup while a file tool may still be
// mid-operation (the tool join comes later), so teardown has to honor the
// layer's in-use count: a held layer stays open until its operation completes
// and is closed then; a drained one is closed at once.
func TestCleanupLeavesAHeldFileToolLayerOpenUntilItsOperationCompletes(t *testing.T) {
	env := readConfinedEnvAt(t, t.TempDir())
	scratch := env.SessionScratchDir()
	if _, err := env.WriteFile(filepath.Join(scratch, "probe"), "held\n"); err != nil {
		t.Fatalf("write_file into the scratch: %v", err)
	}
	// An operation in flight across the teardown.
	held := env.sandbox()
	if held == nil {
		t.Fatal("a confined env built no file-tool layer")
	}

	env.Cleanup()

	if held.closed.Load() {
		t.Errorf("Cleanup closed a layer an operation still holds")
	}
	if b, err := held.readFile("read_file", filepath.Join(scratch, "probe")); err != nil || string(b) != "held\n" {
		t.Errorf("the in-flight operation failed after Cleanup: got %q err %v", b, err)
	}
	held.release()
	if !held.closed.Load() {
		t.Errorf("the held layer was not closed once its operation completed")
	}
}
