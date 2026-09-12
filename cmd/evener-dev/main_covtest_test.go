package dev

import (
	"strings"
	"testing"
)

const devUsage = "usage: evener dev <subcommand> [args]\nsubcommands: agent-shards covstmt module-lint\n"

func TestRunRejectsMissingAndUnknownSubcommands(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{name: "missing", wantStderr: devUsage},
		{
			name:       "unknown",
			args:       []string{"does-not-exist"},
			wantStderr: "evener dev: unknown subcommand \"does-not-exist\"\n" + devUsage,
		},
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
			if got := stderr.String(); got != tc.wantStderr {
				t.Fatalf("Run(%q) stderr = %q, want %q", tc.args, got, tc.wantStderr)
			}
		})
	}
}
