package transcript

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// The TUI's words match appwire-client's delegateEndingText tables, which
// the web and phone use, so the three surfaces say an ending the same way:
// each table read from delegateDetails.ts entry by entry, both ways.
func TestSubagentEndingWordsMatchTheClientTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "appwire-client", "typescript", "delegateDetails.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []struct {
		name string
		want map[string]string
	}{
		{"ENDING_WORDS", subagentEndingWords},
		{"OUTCOME_WORDS", subagentOutcomeWords},
	} {
		got := clientWordTable(t, string(raw), table.name)
		if !reflect.DeepEqual(got, table.want) {
			t.Errorf("%s: delegateDetails.ts has %v, the TUI %v", table.name, got, table.want)
		}
	}
}

// clientWordTable reads `const NAME = new Map([ ["code", "words"], … ]);`
// from delegateDetails.ts.
func clientWordTable(t *testing.T, source, name string) map[string]string {
	t.Helper()
	start := strings.Index(source, "const "+name+" = new Map([")
	if start < 0 {
		t.Fatalf("delegateDetails.ts has no %s table", name)
	}
	end := strings.Index(source[start:], "]);")
	if end < 0 {
		t.Fatalf("delegateDetails.ts's %s table has no end", name)
	}
	entries := regexp.MustCompile(`\["([^"]+)", "([^"]+)"\]`).FindAllStringSubmatch(source[start:start+end], -1)
	table := make(map[string]string, len(entries))
	for _, entry := range entries {
		table[entry[1]] = entry[2]
	}
	return table
}
