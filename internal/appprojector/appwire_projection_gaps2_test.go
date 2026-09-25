package appprojector

import (
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
