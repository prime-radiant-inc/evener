//go:build !evenerfuzz

package sandboxtest

import (
	"context"
	"os/exec"
	"time"

	"primeradiant.com/evener/gittest"
)

func requireGitHarness(t TestingT) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git binary not available")
	}
}

func gitHarness(t TestingT, dir string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = gittest.Env()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}
