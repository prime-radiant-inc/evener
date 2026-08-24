package fuzzcov

import (
	"strings"
	"testing"
)

// TestCovRun covers Run (main.go:41), the thin wrapper around runCLI that
// formats errors to stderr and escalates the exit code to 2. Existing
// tests call runCLI directly, so Run itself is uncovered.
func TestCovRun(t *testing.T) {
	// No flags: runCLI returns an error, Run prints it and returns 2.
	var stdout, stderr strings.Builder
	code := Run(nil, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run(nil) code = %d, want 2", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("Run(nil) should write to stderr")
	}
	if !strings.Contains(stderr.String(), "evener-fuzzcov:") {
		t.Fatalf("stderr should contain 'evener-fuzzcov:', got %q", stderr.String())
	}

	// Bad flag: same escalation.
	stdout.Reset()
	stderr.Reset()
	code = Run([]string{"-bogus"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run([-bogus]) code = %d, want 2", code)
	}
	if stderr.Len() == 0 {
		t.Fatal("Run([-bogus]) should write to stderr")
	}
}
