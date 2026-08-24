package main

import (
	"strings"
	"testing"
)

// TestCovDispatchEmpty covers dispatch (main.go:30) with no args: prints
// usage to stderr and returns 2.
func TestCovDispatchEmpty(t *testing.T) {
	var stderr strings.Builder
	code := dispatch(nil, nil, nil, &stderr)
	if code != 2 {
		t.Fatalf("dispatch(nil) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage: evener-dev") {
		t.Fatalf("stderr should contain usage, got %q", stderr.String())
	}
}

// TestCovDispatchHelp covers the -h/--help/help branch: prints usage to
// stdout and returns 0.
func TestCovDispatchHelp(t *testing.T) {
	for _, arg := range []string{"-h", "--help", "help"} {
		t.Run(arg, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := dispatch([]string{arg}, nil, &stdout, &stderr)
			if code != 0 {
				t.Fatalf("dispatch([%s]) code = %d, want 0", arg, code)
			}
			if !strings.Contains(stdout.String(), "Usage: evener-dev") {
				t.Fatalf("stdout should contain usage, got %q", stdout.String())
			}
		})
	}
}

// TestCovDispatchUnknown covers the default branch: names the unknown
// subcommand in stderr, prints usage, and returns 2.
func TestCovDispatchUnknown(t *testing.T) {
	var stderr strings.Builder
	code := dispatch([]string{"nonexistent-cmd"}, nil, nil, &stderr)
	if code != 2 {
		t.Fatalf("dispatch([unknown]) code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "nonexistent-cmd") {
		t.Fatalf("stderr should name the unknown subcommand, got %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage: evener-dev") {
		t.Fatalf("stderr should contain usage, got %q", stderr.String())
	}
}

// TestCovUsage covers usage (main.go:65) directly.
func TestCovUsage(t *testing.T) {
	var sb strings.Builder
	usage(&sb)
	out := sb.String()
	if !strings.Contains(out, "Usage: evener-dev") {
		t.Fatalf("usage missing header: %q", out)
	}
	if !strings.Contains(out, "module-lint") {
		t.Fatalf("usage missing module-lint: %q", out)
	}
	if !strings.Contains(out, "agent-shards") {
		t.Fatalf("usage missing agent-shards: %q", out)
	}
	if !strings.Contains(out, "fuzz-harvest") {
		t.Fatalf("usage missing fuzz-harvest: %q", out)
	}
	if !strings.Contains(out, "transcript-v2-upgrade") {
		t.Fatalf("usage missing transcript-v2-upgrade: %q", out)
	}
}
