package migrate

import (
	"bytes"
	"strings"
	"testing"
)

// TestCovRun covers Run (main.go:100), the thin wrapper around run. Existing
// tests call run directly, so Run itself is uncovered. We use the
// positional-args rejection path: it is fast (no filesystem walk) and
// exercises the full Run → run → flags.Parse path.
func TestCovRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"extra-arg"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run([extra]) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments are not accepted") {
		t.Fatalf("stderr should reject positional args: %q", stderr.String())
	}
}

// TestCovRunBadFlag covers the bad-flag path through Run.
func TestCovRunBadFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-bogus-flag"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run([-bogus]) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "flag provided but not defined") {
		t.Fatalf("stderr should mention undefined flag: %q", stderr.String())
	}
}
