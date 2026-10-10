package main

import (
	"testing"

	"primeradiant.com/evener/agent"
)

// TestRetirementAttemptSummary pins the daemon log line for an automatic
// retirement that did not retire: every blocker with the session and delegate
// it belongs to, the failure, or both.
func TestRetirementAttemptSummary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		attempt agent.RetirementAttempt
		want    string
	}{
		{
			name: "refused names every blocker and its owner",
			attempt: agent.RetirementAttempt{Blockers: []agent.RetirementBlocker{
				{Category: "notification", SessionID: "sess_root"},
				{Category: "watch", SessionID: "sess_child", DelegateID: "dlg_1"},
			}},
			want: "automatic retirement refused: notification (session sess_root), watch (session sess_child, delegate dlg_1)",
		},
		{
			name:    "failed carries the error",
			attempt: agent.RetirementAttempt{Error: "retirement preparation: job store: closed"},
			want:    "automatic retirement failed: retirement preparation: job store: closed",
		},
		{
			name: "an evidence error keeps the blockers it read",
			attempt: agent.RetirementAttempt{
				Blockers: []agent.RetirementBlocker{{Category: "unsupported", SessionID: "sess_root"}},
				Error:    "retirement job evidence: missing",
			},
			want: "automatic retirement failed: retirement job evidence: missing; refused: unsupported (session sess_root)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := retirementAttemptSummary(tt.attempt); got != tt.want {
				t.Fatalf("summary =\n %q\nwant\n %q", got, tt.want)
			}
		})
	}
}
