package dev

import (
	"strings"
	"testing"
)

// TestRepoAuditUsageNamesTheRunnableBinary pins that the usage line names
// `evener-dev dev`, the executable and subcommand that dispatch repo-audit.
// Plain `evener` does not.
func TestRepoAuditUsageNamesTheRunnableBinary(t *testing.T) {
	var stdout, stderr strings.Builder
	if code := repoAuditRun([]string{"extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("repoAuditRun with an argument = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage: evener-dev dev repo-audit") {
		t.Fatalf("usage does not name the evener-dev dev dispatch: %q", stderr.String())
	}
}
