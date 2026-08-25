package devtoolingtest

import (
	"strings"
	"testing"
)

func TestRunRequiresAtLeastOneSuite(t *testing.T) {
	var stdout, stderr strings.Builder
	code := Run(nil, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run(nil) code = %d, want 2", code)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("Run(nil) stdout = %q, want empty", got)
	}
	const want = "usage: evener test-dev-tooling [-scripts-dir dir] [-kill-grace d] suite...\n"
	if got := stderr.String(); got != want {
		t.Fatalf("Run(nil) stderr = %q, want %q", got, want)
	}
}
