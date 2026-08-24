package devtoolingtest

import (
	"bytes"
	"strings"
	"testing"
)

// TestCovRunBadFlag covers the flag-parse error path of Run (main.go:21).
func TestCovRunBadFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"-bogus"}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run([-bogus]) code = %d, want 2", code)
	}
}

// TestCovRunNoSuites covers the no-suites (NArg()==0) path of Run.
func TestCovRunNoSuites(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(nil, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run(nil) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: evener test-dev-tooling") {
		t.Fatalf("stderr should contain usage: %q", stderr.String())
	}
}
