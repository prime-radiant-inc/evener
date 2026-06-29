package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

// gitleaksScan runs `gitleaks detect --no-git` over dir as the final pre-commit
// barrier — the same engine the repo's make secret-scan target uses, so the
// writer and the repo gate cannot drift. It reports whether the corpus is clean
// and whether gitleaks was available at all (when absent, the caller skips the
// step and the in-process abort gate remains the protection).
func gitleaksScan(dir string, stderr io.Writer) (clean, available bool) {
	bin, err := exec.LookPath("gitleaks")
	if err != nil {
		return false, false
	}
	cmd := exec.CommandContext(context.Background(), bin, "detect", "--no-git", "--redact", "--source", dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true, true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		// gitleaks exits non-zero when it finds leaks; surface its report.
		fmt.Fprintf(stderr, "gitleaks: %s\n", out) //nolint:errcheck
		return false, true
	}
	// A non-exit error (failed to start) is not a clean result; report and treat
	// the barrier as unavailable so the in-process gate stays the protection.
	fmt.Fprintf(stderr, "gitleaks run error: %v\n", err) //nolint:errcheck
	return false, false
}
