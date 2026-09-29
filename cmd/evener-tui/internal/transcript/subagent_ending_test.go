package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The TUI says a subagent's ending as the web and phone do: its cause, else
// its reason code in words, never snake_case.
func TestSubagentEndingText(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  SubagentRunInfo
		want string
	}{
		{"cause first", SubagentRunInfo{Outcome: "failed", Reason: "run_error", Error: "\n provider returned 500\nretry-after: 30"}, "provider returned 500"},
		{"code in words", SubagentRunInfo{Outcome: "failed", Reason: "ended_without_report"}, "ended without reporting"},
		{"start failure", SubagentRunInfo{Outcome: "failed", Reason: "launch_failed"}, "couldn't start"},
		{"unknown failed code", SubagentRunInfo{Outcome: "failed", Reason: "quota_window_closed"}, "failed"},
		{"unknown exhausted code", SubagentRunInfo{Outcome: "exhausted", Reason: "memory_budget_exhausted"}, "ran out of budget"},
		{"prose reason", SubagentRunInfo{Outcome: "failed", Reason: "2 high CVEs"}, "2 high CVEs"},
		{"nothing", SubagentRunInfo{Outcome: "completed"}, ""},
	} {
		if got := SubagentEndingText(tc.run); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The TUI's words match appwire-client's delegateEndingText table, which the
// web and phone use, so the three surfaces say an ending the same way.
func TestSubagentEndingWordsMatchTheClientTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "appwire-client", "typescript", "delegateDetails.ts"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for code, words := range subagentEndingWords {
		entry := `["` + code + `", "` + words + `"]`
		if !strings.Contains(source, entry) {
			t.Errorf("delegateDetails.ts has no entry %s", entry)
		}
	}
	if got, want := strings.Count(source, `", "`), len(subagentEndingWords)+len(subagentOutcomeWords); got != want {
		t.Errorf("delegateDetails.ts has %d word entries, the TUI %d: the tables differ", got, want)
	}
}
