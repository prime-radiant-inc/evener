package fuzzcov

import (
	"strings"
	"testing"
)

func TestRunReportsUnsupportedMode(t *testing.T) {
	var stdout, stderr strings.Builder
	code := Run([]string{"-modules", "."}, nil, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("Run() stdout = %q, want empty", got)
	}
	const want = "evener-fuzzcov: the only mode is -gap-only -registry <file>; the coverage reporter was removed\n"
	if got := stderr.String(); got != want {
		t.Fatalf("Run() stderr = %q, want %q", got, want)
	}
}
