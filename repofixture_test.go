package evener_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func copyRepositoryFile(t *testing.T, repoRoot, fixtureRoot, relativePath string, mode os.FileMode) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, relativePath))
	if err != nil {
		t.Fatalf("read repository %s: %v", relativePath, err)
	}
	writeTestFile(t, filepath.Join(fixtureRoot, relativePath), data, mode)
}

func writeTestFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	// Every fixture write lands here, including the scripts/*.sh copies that
	// fixture tests exec by relative path (issue #609). os.WriteFile leaves an open write
	// fd that a concurrent fork inherits until it execs, failing that exec
	// with ETXTBSY (golang/go#22315); holding ForkLock for reading across
	// the write excludes such a fork, as writeExecutable (install_test.go)
	// does for its own callers. Nothing reaching this helper runs parallel
	// today, so taking the lock on every write forecloses the hazard by
	// construction instead of leaning on test ordering.
	syscall.ForkLock.RLock()
	defer syscall.ForkLock.RUnlock()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
