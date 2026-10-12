package dev

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// The dispatcher lives entirely in Run(), so its contract is pinned the
// real-process way: build the actual evener binary and invoke `evener dev`
// to read its exit code and stderr directly. Not `go run`, which reports 1
// whatever the child exited with, erasing exactly the codes under test. The
// binary is built once per package run (buildEvenerDev).
func runEvenerDev(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration: builds and runs the evener binary")
	}
	return execEvenerDev(t, args...)
}

// execEvenerDev runs `evener dev args...` and returns its exit code and each
// stream separately.
func execEvenerDev(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	cmd := exec.Command(buildEvenerDev(t), append([]string{"dev"}, args...)...) //nolint:noctx // one short-lived run, reaped by Run — buildEvenerDev is the shard runner's shared helper
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	if err == nil {
		return 0, out.String(), errOut.String()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("running evener dev: %v", err)
	}
	return exit.ExitCode(), out.String(), errOut.String()
}

func TestDispatchRejectsMissingSubcommand(t *testing.T) {
	code, stdout, stderr := runEvenerDev(t)
	if code != 2 {
		t.Errorf("no subcommand exits %d, want 2", code)
	}
	if stderr == "" || stdout != "" {
		t.Errorf("usage belongs on stderr only: stdout %q, stderr %q", stdout, stderr)
	}
}

func TestDispatchRejectsUnknownSubcommand(t *testing.T) {
	code, stdout, stderr := runEvenerDev(t, "launder-money")
	if code != 2 || stdout != "" {
		t.Errorf("unknown subcommand = exit %d, stdout %q; want 2 with nothing on stdout", code, stdout)
	}
	if !strings.Contains(stderr, "launder-money") {
		t.Errorf("unknown subcommand not named in stderr: %q", stderr)
	}
}
