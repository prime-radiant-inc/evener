package transcriptv2upgrade

import (
	"bytes"
	"strings"
	"testing"
)

// TestCovRun covers Run (main.go:48), the thin wrapper around run. Existing
// tests call run directly, so Run itself is uncovered. A bad flag exercises
// the wrapper without needing real transcript data.
func TestCovRun(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-unknown"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run([-unknown]) code = %d, want 2", code)
	}
}

// TestCovRunEmptyRoot covers the empty-root validation through Run.
func TestCovRunEmptyRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run(nil) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "--root is required") {
		t.Fatalf("stderr should mention --root required: %q", stderr.String())
	}
}

// TestCovRunPositionalArgs covers the positional-args rejection through Run.
func TestCovRunPositionalArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"extra"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run([extra]) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "positional arguments are not accepted") {
		t.Fatalf("stderr should reject positional args: %q", stderr.String())
	}
}
