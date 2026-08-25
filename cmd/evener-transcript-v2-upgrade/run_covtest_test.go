package transcriptv2upgrade

import (
	"strings"
	"testing"
)

func TestRunReportsAnEmptyProjectsRoot(t *testing.T) {
	var stdout, stderr strings.Builder
	code := Run([]string{"-root", t.TempDir()}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("Run() code = %d, want 0; stderr = %q", code, stderr.String())
	}
	const want = "candidates=0 eligible=0 upgraded=0 skipped_current=0 skipped_old=0 removed_api_calls=0 errors=0\n"
	if got := stdout.String(); got != want {
		t.Fatalf("Run() stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("Run() stderr = %q, want empty", got)
	}
}
