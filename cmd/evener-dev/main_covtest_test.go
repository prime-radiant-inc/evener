package dev

import (
	"strings"
	"testing"
)

func TestRunRejectsMissingAndUnknownSubcommands(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// wantNamed is what the diagnostic must name, if anything.
		wantNamed string
	}{
		{name: "missing"},
		{name: "unknown", args: []string{"does-not-exist"}, wantNamed: "does-not-exist"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			code := Run(tc.args, nil, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("Run(%q) code = %d, want 2", tc.args, code)
			}
			if got := stdout.String(); got != "" {
				t.Fatalf("Run(%q) stdout = %q, want empty", tc.args, got)
			}
			got := stderr.String()
			if got == "" {
				t.Fatalf("Run(%q) wrote no usage to stderr", tc.args)
			}
			if !strings.Contains(got, tc.wantNamed) {
				t.Fatalf("Run(%q) stderr = %q, want %q named", tc.args, got, tc.wantNamed)
			}
		})
	}
}
