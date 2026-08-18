package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The dispatcher lives entirely in main(), so its contract is pinned the
// real-process way: run the actual binary via `go run` and read its exit
// code and stderr.
func runSerfDev(t *testing.T, args ...string) (int, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: builds and runs the serf-dev binary")
	}
	cmd := exec.Command("go", append([]string{"run", "."}, args...)...) //nolint:noctx // one short-lived build-and-run, reaped by Wait
	var errOut strings.Builder
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err == nil {
		return 0, errOut.String()
	}
	exit, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("go run ./cmd/serf-dev: %v", err)
	}
	return exit.ExitCode(), errOut.String()
}

func TestDispatchRejectsMissingSubcommand(t *testing.T) {
	code, stderr := runSerfDev(t)
	if code != 2 {
		t.Errorf("no subcommand exits %d, want 2", code)
	}
	if !strings.Contains(stderr, "usage: serf-dev") {
		t.Errorf("usage missing from stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "module-lint") {
		t.Errorf("usage does not name module-lint: %q", stderr)
	}
}

func TestDispatchRejectsUnknownSubcommand(t *testing.T) {
	code, stderr := runSerfDev(t, "launder-money")
	if code != 2 {
		t.Errorf("unknown subcommand exits %d, want 2", code)
	}
	if !strings.Contains(stderr, "launder-money") {
		t.Errorf("unknown subcommand not named in stderr: %q", stderr)
	}
}
