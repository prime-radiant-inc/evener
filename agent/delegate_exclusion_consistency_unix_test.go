//go:build unix

package agent

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/mcpconfig"
)

// TestCLIClassificationEvidenceTreatsAnUnreadableLayerAsUnknown is H2: the
// evidence read must not be a plain whole-file read of a repository-controlled
// path. A named pipe there blocks forever, and a read that blocks while a caller
// holds s.mu wedges the session.
//
// The assertion is on REACHING an answer, not on the file's contents: a FIFO with
// no writer is exactly the shape that hangs os.ReadFile, so the property under
// test is that the call returns at all, and that its answer leaves the names
// eligible rather than excluding them.
func TestCLIClassificationEvidenceTreatsAnUnreadableLayerAsUnknown(t *testing.T) {
	// Not parallel: hermeticInfraEnv calls t.Setenv.
	hermeticInfraEnv(t)
	root := realTempDirForTest(t)
	// A real git root, so the recorded path actually matches what discovery
	// resolves and the read below is reached. Without it the path check rejects
	// the evidence first and this test would pass without exercising the read.
	//
	// The canonical root above is load-bearing for that same reason: git reports
	// the RESOLVED root, so on a symlinked temp directory (Darwin's /var) an
	// unresolved fixture root makes the recorded and resolved paths differ, and
	// the check returns at the comparison without reading — the exact false pass
	// this test was built to rule out.
	runInfraGit(t, root, "init", "-q")
	layer := filepath.Join(root, ".evener", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(layer), 0o755); err != nil {
		t.Fatal(err)
	}
	// A REGULAR file first, and real evidence for it, so both layers verify
	// before the FIFO replaces this one. The evidence has to be discovery's own:
	// a hand-built project-only value leaves the global layer unrecorded, and
	// hermeticInfraEnv resolves a nonempty global path, so the comparison would
	// return false at the GLOBAL layer and never reach this read at all — the
	// test would pass with the read hanging.
	if err := os.WriteFile(layer, []byte(`{"mcpServers":{"svc":{"command":"/bin/a"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	env := execenv.NewLocalExecutionEnvironment(root)
	_, _, evidence, _, err := mcpconfig.DiscoverWithCLIOnlyAndEvidence(env, nil, nil)
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if evidence.Project.Path != layer {
		t.Fatalf("fixture is wrong: evidence path %q, want %q", evidence.Project.Path, layer)
	}
	if err := os.Remove(layer); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(layer, 0o600); err != nil {
		t.Skipf("this platform cannot create a named pipe: %v", err)
	}
	s := &Session{env: env}

	result := make(chan bool, 1)
	go func() {
		result <- s.cliMCPClassificationCurrent(cliMCPClassificationEvidence{layers: evidence})
	}()
	select {
	case current := <-result:
		if current {
			t.Errorf("an unreadable project layer verified its evidence; unknown must mean eligible")
		}
	case <-time.After(10 * time.Second): // TRIPWIRE: a FIFO read that ignores the cap, not a slow filesystem
		t.Fatal("the evidence check blocked on a named pipe: the project layer is read without execenv.ReadFileCapped")
	}
}
