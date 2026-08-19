package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// The dispatcher lives entirely in main(), so its contract is pinned the
// real-process way: build the actual binary and read its exit code and
// stderr directly. Not `go run`, which reports 1 whatever the child exited
// with, erasing exactly the codes under test. The per-test build rides the
// build cache.
func runEvenerDev(t *testing.T, args ...string) (int, string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: builds and runs the evener-dev binary")
	}
	cmd := exec.Command(buildEvenerDev(t), args...) //nolint:noctx // one short-lived run, reaped by Run — buildEvenerDev is the shard runner's shared helper
	var errOut strings.Builder
	cmd.Stderr = &errOut
	err := cmd.Run()
	if err == nil {
		return 0, errOut.String()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("running evener-dev: %v", err)
	}
	return exit.ExitCode(), errOut.String()
}

func TestDispatchRejectsMissingSubcommand(t *testing.T) {
	code, stderr := runEvenerDev(t)
	if code != 2 {
		t.Errorf("no subcommand exits %d, want 2", code)
	}
	if !strings.Contains(stderr, "usage: evener-dev") {
		t.Errorf("usage missing from stderr: %q", stderr)
	}
	if !strings.Contains(stderr, "module-lint") {
		t.Errorf("usage does not name module-lint: %q", stderr)
	}
}

func TestDispatchRejectsUnknownSubcommand(t *testing.T) {
	code, stderr := runEvenerDev(t, "launder-money")
	if code != 2 {
		t.Errorf("unknown subcommand exits %d, want 2", code)
	}
	if !strings.Contains(stderr, "launder-money") {
		t.Errorf("unknown subcommand not named in stderr: %q", stderr)
	}
}
