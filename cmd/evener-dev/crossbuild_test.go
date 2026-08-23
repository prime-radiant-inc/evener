package dev

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCrossCompileWindows pins issue #182: the evener binary (which includes
// the dev subcommand) must build cleanly for GOOS=windows with CGO disabled.
// agentshards.go historically called the unix-only syscall.Kill and used
// syscall.SysProcAttr.Setpgid without a build tag, so a cross-compile died
// with `undefined: syscall.Kill` and `unknown field Setpgid`. This test fails
// until that file is split by build tags.
//
// It runs a real `go build` (cache-friendly, ~fast), so it is skipped under
// -short. The env is forced to CGO_ENABLED=0 GOOS=windows regardless of host
// OS, so the cross-compile is exercised on linux/darwin hosts (the common
// case). On a Windows host the build is a native compile and the env vars are
// a no-op, which is fine.
func TestCrossCompileWindows(t *testing.T) {
	if testing.Short() {
		t.Skip("integration: cross-compiles evener for windows")
	}

	// Resolve the cmd/evener directory (parent of this package's directory)
	// from this test file's location, not from the test's working directory.
	// This is robust against `go test -C` and worktree runs where the working
	// dir may differ.
	pkgDir := filepath.Dir(testFile(t))
	evenerDir := filepath.Join(pkgDir, "..", "evener")
	if fi, err := os.Stat(evenerDir); err != nil || !fi.IsDir() {
		// Fall back to the working directory.
		wd, derr := os.Getwd()
		if derr != nil {
			t.Fatalf("resolving evener dir: stat %q: %v; getwd: %v", evenerDir, err, derr)
		}
		evenerDir = filepath.Join(wd, "..", "evener")
	}

	out := filepath.Join(t.TempDir(), "evener.exe")
	cmd := exec.Command("go", "build", "-o", out, ".")
	cmd.Dir = evenerDir
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=windows",
	)
	combined, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("cross-build evener for GOOS=windows failed (issue #182):\n%v\n%s", err, combined)
	}
}

// testFile returns the path of this test's source file via runtime.Caller.
func testFile(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: cannot locate test source file")
	}
	return file
}
