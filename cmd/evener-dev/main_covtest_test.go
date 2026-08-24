package dev

import (
	"strings"
	"testing"
)

// TestCovRunEmpty covers Run (main.go:21) with no args: prints usage to
// stderr and returns 2. The existing TestDispatchRejectsMissingSubcommand
// is an integration test that skips in -short mode.
func TestCovRunEmpty(t *testing.T) {
	var stderr strings.Builder
	code := Run(nil, nil, nil, &stderr)
	if code != 2 {
		t.Fatalf("Run(nil) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: evener dev") {
		t.Fatalf("stderr should contain usage, got %q", stderr.String())
	}
}

// TestCovRunUnknownSubcommand covers the unknown-subcommand branch of Run.
func TestCovRunUnknownSubcommand(t *testing.T) {
	var stderr strings.Builder
	code := Run([]string{"does-not-exist"}, nil, nil, &stderr)
	if code != 2 {
		t.Fatalf("Run([unknown]) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "does-not-exist") {
		t.Fatalf("stderr should name the unknown subcommand, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "usage: evener dev") {
		t.Fatalf("stderr should contain usage, got %q", stderr.String())
	}
}

// TestCovGolangciCmd covers golangciCmd (modulelint.go:361), which
// constructs an exec.Cmd for golangci-lint in the given module directory.
func TestCovGolangciCmd(t *testing.T) {
	cmd := golangciCmd("/tmp/module")
	if cmd.Dir != "/tmp/module" {
		t.Errorf("cmd.Dir = %q, want /tmp/module", cmd.Dir)
	}
	if cmd.Path == "" {
		t.Error("cmd.Path should not be empty")
	}
	// Verify the args include the expected flags.
	found := false
	for _, arg := range cmd.Args {
		if arg == "--allow-parallel-runners" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("cmd args %v should include --allow-parallel-runners", cmd.Args)
	}
}
