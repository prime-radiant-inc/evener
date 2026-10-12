package apptranscript

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

// Each repair kind reads as its own plain phrase that names the argument
// involved, and none leaks the repair engine's "kind:field:detail" encoding
// into the reader's transcript (kata k4v8).
func TestRepairChangePhraseNamesTheFieldWithoutLeakingTheEncoding(t *testing.T) {
	seen := map[string]string{}
	for _, tc := range []struct {
		raw   string
		names []string
	}{
		{raw: "alias:fieldname", names: []string{"fieldname"}},
		{raw: "alias:newfield:oldfield→newfield", names: []string{"oldfield", "newfield"}},
		{raw: "coerce_type:count", names: []string{"count"}},
		{raw: "drop_unknown:artifacts", names: []string{"artifacts"}},
		{raw: "unicode_repair:"},
		{raw: "unknown_kind"},
		{raw: "unknown_kind:myfield", names: []string{"myfield"}},
		{raw: "synthesize:output:synthesized default envelope"},
		{raw: "copy:message:copied output.message"},
		{raw: "promote_json_object:output:promoted JSON object string"},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got := repairChangePhrase(tc.raw)
			if got == "" {
				t.Fatal("repair phrase is empty")
			}
			for _, name := range tc.names {
				if !strings.Contains(got, name) {
					t.Errorf("repairChangePhrase(%q) = %q, want it to name %q", tc.raw, got, name)
				}
			}
			kind, rest, _ := strings.Cut(tc.raw, ":")
			if strings.Contains(got, kind) || strings.ContainsAny(got, ":→") {
				t.Errorf("repairChangePhrase(%q) = %q leaks the repair encoding", tc.raw, got)
			}
			field, _, _ := strings.Cut(rest, ":")
			if fallback := repairChangePhrase("unknown_kind:" + field); kind != "unknown_kind" && got == fallback {
				t.Errorf("repairChangePhrase(%q) = %q, the phrase for a kind this build does not know", tc.raw, got)
			}
			if other, dup := seen[got]; dup {
				t.Errorf("repairChangePhrase(%q) and (%q) both read %q", tc.raw, other, got)
			}
			seen[got] = tc.raw
		})
	}
}

// TestFieldDetailShortParts covers the len < 3 return of "".
func TestFieldDetailShortParts(t *testing.T) {
	if got := fieldDetail([]string{"a", "b"}); got != "" {
		t.Fatalf("less than 3 parts should return empty, got %q", got)
	}
}

// TestFieldDetailFullParts covers the parts[2] return.
func TestFieldDetailFullParts(t *testing.T) {
	if got := fieldDetail([]string{"a", "b", "c"}); got != "c" {
		t.Fatalf("third part should be 'c', got %q", got)
	}
}

// TestToolCallRepairedAnnouncementNoChanges covers the path with no changes:
// the announcement still names the repaired tool.
func TestToolCallRepairedAnnouncementNoChanges(t *testing.T) {
	got := ToolRepairAnnouncement(schema.ToolRepairNotice{ToolName: "shell"}).Text
	if !strings.Contains(got, "shell") {
		t.Fatalf("no changes should still name the tool, got %q", got)
	}
}

// TestToolCallRepairedAnnouncementNoName covers the fallback name path
// with an empty tool name: the sentence has no gap where the name goes.
func TestToolCallRepairedAnnouncementNoName(t *testing.T) {
	got := ToolRepairAnnouncement(schema.ToolRepairNotice{}).Text
	if got == "" || strings.TrimSpace(got) != got || strings.Contains(got, "  ") {
		t.Fatalf("empty name should fall back to a stand-in name, got %q", got)
	}
}
