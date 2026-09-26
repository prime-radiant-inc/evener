package apptranscript

import (
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
)

// TestRepairChangePhraseAliasNoDetail covers the alias branch where
// strings.Cut fails (no → separator in detail), hitting the "renamed a
// field to %q" fallback.
func TestRepairChangePhraseAliasNoDetail(t *testing.T) {
	got := repairChangePhrase("alias:fieldname")
	if !strings.Contains(got, "renamed a field to") {
		t.Fatalf("alias without detail should fall back, got %q", got)
	}
	if !strings.Contains(got, "fieldname") {
		t.Fatalf("should mention field name, got %q", got)
	}
}

// TestRepairChangePhraseCoerceType covers the coerce_type branch.
func TestRepairChangePhraseCoerceType(t *testing.T) {
	got := repairChangePhrase("coerce_type:count")
	if !strings.Contains(got, "adjusted the") || !strings.Contains(got, "type") {
		t.Fatalf("coerce_type should mention type adjustment, got %q", got)
	}
}

// TestRepairChangePhraseDropUnknown covers the drop_unknown branch.
func TestRepairChangePhraseDropUnknown(t *testing.T) {
	got := repairChangePhrase("drop_unknown:artifacts")
	if !strings.Contains(got, "removed the unrecognized") {
		t.Fatalf("drop_unknown should mention removal, got %q", got)
	}
}

// TestRepairChangePhraseUnicodeRepair covers the unicode_repair branch.
func TestRepairChangePhraseUnicodeRepair(t *testing.T) {
	got := repairChangePhrase("unicode_repair:")
	if !strings.Contains(got, "fixed an invalid character") {
		t.Fatalf("unicode_repair should mention invalid character, got %q", got)
	}
}

// TestRepairChangePhraseUnknownKindNoField covers the default branch
// with an empty field, hitting the "adjusted the arguments" return.
func TestRepairChangePhraseUnknownKindNoField(t *testing.T) {
	got := repairChangePhrase("unknown_kind")
	if !strings.Contains(got, "adjusted the arguments") {
		t.Fatalf("unknown kind with no field should say 'adjusted the arguments', got %q", got)
	}
}

// TestRepairChangePhraseUnknownKindWithField covers the default branch
// with a field, hitting the "adjusted the %q field" return.
func TestRepairChangePhraseUnknownKindWithField(t *testing.T) {
	got := repairChangePhrase("unknown_kind:myfield")
	if !strings.Contains(got, "adjusted the") || !strings.Contains(got, "myfield") {
		t.Fatalf("unknown kind with field should name the field, got %q", got)
	}
}

// TestRepairChangePhraseAliasWithDetail covers the alias branch with a
// → separator in the detail, hitting the "renamed %q to %q" path.
func TestRepairChangePhraseAliasWithDetail(t *testing.T) {
	got := repairChangePhrase("alias:newfield:oldfield→newfield")
	if !strings.Contains(got, "renamed") {
		t.Fatalf("alias with detail should mention rename, got %q", got)
	}
	if !strings.Contains(got, "oldfield") || !strings.Contains(got, "newfield") {
		t.Fatalf("should mention old and new names, got %q", got)
	}
}

func TestRepairChangePhraseDefaultCommunicateEnvelopeRepairs(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want string
	}{
		{
			raw:  "synthesize:output:synthesized default envelope",
			want: "created the required output object",
		},
		{
			raw:  "copy:message:copied output.message",
			want: "copied nested output.message to the required message",
		},
		{
			raw:  "promote_json_object:output:promoted JSON object string",
			want: "converted the output JSON string to an object",
		},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			if got := repairChangePhrase(tc.raw); got != tc.want {
				t.Fatalf("repairChangePhrase(%q) = %q, want %q", tc.raw, got, tc.want)
			}
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

// TestToolCallRepairedAnnouncementNoChanges covers the "Repaired %s" path
// with no changes.
func TestToolCallRepairedAnnouncementNoChanges(t *testing.T) {
	got := ToolRepairAnnouncement(schema.ToolRepairNotice{ToolName: "shell"}).Text
	if !strings.Contains(got, "Repaired shell") {
		t.Fatalf("no changes should say 'Repaired shell', got %q", got)
	}
}

// TestToolCallRepairedAnnouncementNoName covers the fallback name path
// with an empty tool name.
func TestToolCallRepairedAnnouncementNoName(t *testing.T) {
	got := ToolRepairAnnouncement(schema.ToolRepairNotice{}).Text
	if !strings.Contains(got, "Repaired tool call") {
		t.Fatalf("empty name should use fallback 'tool call', got %q", got)
	}
}
