package evener_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// requireFuzzScriptTools skips the calling test, with a stated reason, unless
// git, go, and bash are all resolvable on PATH and the host has a Unix shell.
// scripts/fuzz/fuzz-bisect.sh and scripts/fuzz/fuzz-oracle-audit.sh are bash
// scripts that drive real git and go subprocesses -- there is no scripted
// substitute for any of the three, so a missing one is a clean skip rather
// than a failure.
func requireFuzzScriptTools(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts/fuzz/*.sh require a Unix shell")
	}
	for _, tool := range []string{"git", "go", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is not on PATH: %v", tool, err)
		}
	}
}

// repoScriptPath resolves path (repo-relative, e.g. "scripts/fuzz/fuzz-bisect.sh")
// against the working directory `go test` starts this package's tests from,
// which is this package's own directory (the repo root).
func repoScriptPath(t *testing.T, path string) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return filepath.Join(root, path)
}

// gitOutput runs git args... in dir and returns trimmed stdout, failing the
// test on a nonzero exit. Use this over runGit (runtime_pair_build_test.go)
// when the assertion needs the command's output, not just its success.
func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, exitErr.Stderr)
		}
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(out))
}

// writeExecutableFixture writes an executable file (a stub registry script)
// to path, creating parent directories as needed.
func writeExecutableFixture(t *testing.T, path, content string) {
	t.Helper()
	writeAuditScriptFixture(t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatalf("chmod +x %s: %v", path, err)
	}
}
