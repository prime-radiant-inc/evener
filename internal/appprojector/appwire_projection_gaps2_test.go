package appprojector

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/appwire"
)

// TestRecordAssistantMessageEmpty covers the empty-text early return in
// recordAssistantMessage.
func TestRecordAssistantMessageEmpty(t *testing.T) {
	p := &AppEventProjector{}
	p.recordAssistantMessage("turn1", "   ")
	if p.lastAssistantTurnID != "" || p.lastAssistantText != "" {
		t.Fatal("empty text should not record")
	}
}

// TestRecordAssistantMessageNonEmpty covers the normal recording path.
func TestRecordAssistantMessageNonEmpty(t *testing.T) {
	p := &AppEventProjector{}
	p.recordAssistantMessage("turn1", "hello")
	if p.lastAssistantTurnID != "turn1" || p.lastAssistantText != "hello" {
		t.Fatal("should record turn and text")
	}
}

// TestSystemAnnouncementItemEmptyTextNonPlugin covers the text=="" &&
// eventKind != PluginLoaded early return in systemAnnouncementItem.
func TestSystemAnnouncementItemEmptyTextNonPlugin(t *testing.T) {
	p := &AppEventProjector{threadID: "t1"}
	out := p.systemAnnouncementItem(appwire.ThreadItemEventKindHookCompleted, "desc", "", nil, nil)
	if out != nil {
		t.Fatal("empty text with non-plugin kind should return nil")
	}
}

// TestSystemAnnouncementItemEmptyDescAndTextPlugin covers the
// description=="" && text=="" return for PluginLoaded kind (text was
// trimmed to "" but kind is PluginLoaded, so we pass that guard, then
// both desc and text empty hits the second nil return).
func TestSystemAnnouncementItemEmptyDescAndTextPlugin(t *testing.T) {
	p := &AppEventProjector{threadID: "t1"}
	out := p.systemAnnouncementItem(appwire.ThreadItemEventKindPluginLoaded, "  ", "  ", nil, nil)
	if out != nil {
		t.Fatal("empty desc and text should return nil")
	}
}

// TestRoundTimingsRawError covers the json.Marshal error return (nil) in
// roundTimingsRaw. json.Marshal of a map with a nil channel or func value
// fails. RoundTimings fields are durations (which marshal fine), so we
// instead test the nil-return path by checking that a normal call
// succeeds and verify the function is exercised. The error branch is
// unreachable with valid RoundTimings fields — documented as such.
func TestRoundTimingsRaw(t *testing.T) {
	data := events.RoundTimings{Round: 1}
	raw := roundTimingsRaw(data)
	if raw == nil {
		t.Fatal("roundTimingsRaw should produce non-nil for valid data")
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("roundTimingsRaw should produce valid JSON: %v", err)
	}
	rt, ok := payload["roundTimings"]
	if !ok {
		t.Fatal("should have roundTimings key")
	}
	m, ok := rt.(map[string]any)
	if !ok {
		t.Fatal("roundTimings should be an object")
	}
	if m["round"] != float64(1) {
		t.Fatalf("round should be 1, got %v", m["round"])
	}
}

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
	got := toolCallRepairedAnnouncement(events.ToolCallRepairedData{ToolName: "shell"})
	if !strings.Contains(got, "Repaired shell") {
		t.Fatalf("no changes should say 'Repaired shell', got %q", got)
	}
}

// TestToolCallRepairedAnnouncementNoName covers the fallbackLabel path
// with an empty tool name.
func TestToolCallRepairedAnnouncementNoName(t *testing.T) {
	got := toolCallRepairedAnnouncement(events.ToolCallRepairedData{})
	if !strings.Contains(got, "Repaired tool call") {
		t.Fatalf("empty name should use fallback 'tool call', got %q", got)
	}
}

// TestPluginLoadedAnnouncementNoName covers the no-name format.
func TestPluginLoadedAnnouncementNoName(t *testing.T) {
	got := pluginLoadedAnnouncement(events.PluginLoadedData{})
	if !strings.Contains(got, "Loaded plugin (") {
		t.Fatalf("no name should say 'Loaded plugin (', got %q", got)
	}
}

// TestHookEndAnnouncement covers hookEndAnnouncement through hookInfoFromEvent.
func TestHookEndAnnouncement(t *testing.T) {
	data := events.HookEndData{Event: "pre-tool", HookType: "command", PluginName: "p"}
	got := hookEndAnnouncement(data)
	if got == "" {
		t.Fatal("hookEndAnnouncement should produce non-empty string")
	}
}

// TestMergeAppwireDelegateInfoNewerActivityInPrimary covers line 1081:
// the primary branch (incoming has higher revision) where
// current.LatestActivityAt is newer than merged (incoming) activity.
func TestMergeAppwireDelegateInfoNewerActivityInPrimary(t *testing.T) {
	current := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 1,
		LatestActivityAt:   "2024-01-03T00:00:00Z",
	}
	incoming := appwire.EvenerDelegateInfo{
		DelegateID:         "dlg_1",
		ProjectionRevision: 2,
		LatestActivityAt:   "2024-01-01T00:00:00Z",
	}
	merged, changed := mergeAppwireDelegateInfo(current, incoming)
	if !changed {
		t.Fatal("higher revision should trigger merge")
	}
	if merged.LatestActivityAt != "2024-01-03T00:00:00Z" {
		t.Fatalf("should keep current's newer activity, got %q", merged.LatestActivityAt)
	}
}
