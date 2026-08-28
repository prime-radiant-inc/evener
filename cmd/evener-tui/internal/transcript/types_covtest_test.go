package transcript

import (
	"testing"
)

// TestCovSubagentDisplayStatus covers SubagentDisplayStatus (types.go:90),
// which has four branches: terminal+outcome, terminal+no-outcome,
// non-terminal, and empty status.
func TestCovSubagentDisplayStatus(t *testing.T) {
	cases := []struct {
		name string
		run  SubagentRunInfo
		want string
	}{
		{
			name: "terminal with outcome",
			run:  SubagentRunInfo{Status: "running", Outcome: "completed", Terminal: true},
			want: "completed",
		},
		{
			name: "terminal with blank outcome",
			run:  SubagentRunInfo{Status: "running", Outcome: "  ", Terminal: true},
			want: "running",
		},
		{
			name: "non-terminal ignores outcome",
			run:  SubagentRunInfo{Status: "idle", Outcome: "completed", Terminal: false},
			want: "idle",
		},
		{
			name: "empty status non-terminal",
			run:  SubagentRunInfo{Status: "", Terminal: false},
			want: "",
		},
		{
			name: "whitespace status trimmed",
			run:  SubagentRunInfo{Status: "  pending  ", Terminal: false},
			want: "pending",
		},
		{
			name: "terminal with empty status and outcome",
			run:  SubagentRunInfo{Status: "", Outcome: "failed", Terminal: true},
			want: "failed",
		},
		{
			name: "terminal with whitespace outcome falls back to status",
			run:  SubagentRunInfo{Status: " stopped ", Outcome: "  ", Terminal: true},
			want: "stopped",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SubagentDisplayStatus(c.run); got != c.want {
				t.Errorf("SubagentDisplayStatus() = %q, want %q", got, c.want)
			}
		})
	}
}
