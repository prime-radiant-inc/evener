//go:build linux || darwin

package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRunnerCleanupCancellationFixture drives the real run-ui-tests.sh
// cleanup and cancellable xcodebuild wait with fixture processes, so the Linux
// gate covers the trap and stop budget a Mac run reaches only when it is
// cancelled or fails. Linux-only: the macOS lane runs the real journeys, and
// the fixture uses GNU date.
func TestRunnerCleanupCancellationFixture(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux fixture; the macOS qualification lane drives the real runner")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	script, err := filepath.Abs(filepath.Join("testdata", "runner_cleanup_test.sh"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(bash, script).CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 77 {
		t.Skip("fixture prerequisites are missing")
	}
	if err != nil {
		t.Fatalf("runner cleanup fixture: %v\n%s", err, out)
	}
	t.Logf("%s", out)
}
