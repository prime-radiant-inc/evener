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
