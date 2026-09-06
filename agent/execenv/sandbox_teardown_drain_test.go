package execenv

import (
	"path/filepath"
	"sync"
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

// A worktree exit's swap rollback and the session's close can both retain the
// same environment's scratch (the parked restore environment is the swap's
// target). Releasing a lease mutates it, so the retain has to run under the
// scratch lock, or the two releases race on the lease file.
func TestRetainSessionScratchIsSafeToRunConcurrently(t *testing.T) {
	env := readConfinedEnvAt(t, t.TempDir())
	if env.SessionScratchDir() == "" {
		t.Fatal("the env owns no scratch")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(env.RetainSessionScratch)
	}
	wg.Wait()
}

// RetainSessionScratch is what a session's close runs on an environment it must
// not Cleanup — a shared child's, the environment a worktree enter parked, and
// the ones a later enter abandoned. Releasing the lease is only half of what
// those environments hold: each also caches fd-anchored file-tool layers, and a
// retain that left them open would keep a root descriptor per abandoned
// environment for the rest of the process. The retain retires them through the
// same drain a teardown uses.
func TestRetainSessionScratchRetiresTheFileToolLayers(t *testing.T) {
	env := readConfinedEnvAt(t, t.TempDir())
	scratch := env.SessionScratchDir()
	if _, err := env.WriteFile(filepath.Join(scratch, "probe"), "built\n"); err != nil {
		t.Fatalf("write_file into the scratch: %v", err)
	}
	if fds := openRootFdsOf(env); fds == 0 {
		t.Fatal("the file tool built no fd-anchored layer; this test would prove nothing")
	}

	env.RetainSessionScratch()

	if fds := openRootFdsOf(env); fds != 0 {
		t.Errorf("the retain left %d root fds open; an environment nothing will Cleanup keeps its layers forever", fds)
	}
}
