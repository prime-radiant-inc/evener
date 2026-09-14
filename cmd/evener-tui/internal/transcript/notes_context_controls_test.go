package transcript

import (
	"strings"
	"testing"
	"unicode"

	"primeradiant.com/evener/appwire"
)

// A reloaded NOTES_CONTEXT turn arrives as a systemMessage item whose text is
// the notes block, and that renders as MsgSystem. A session written before the
// write-path strip would otherwise print its controls in the transcript, so the
// reducer strips them for that event kind alone (roborev's sixth round).
func TestApplyThreadItemStripsReloadedNotesContext(t *testing.T) {
	const payload = "<shared-notes>\nHuman: legacy\x1b]0;owned\x07\u009b31m note\n</shared-notes>"

	r := NewTranscriptReducer(nil, map[string]int{}, map[string]int{})
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:      "systemMessage",
		ID:        "item_notes_context_1",
		EventKind: appwire.ThreadItemEventKindNotesContext,
		Text:      payload,
	}, 1, true)

	if len(r.messages) != 1 {
		t.Fatalf("reduced %d messages, want 1", len(r.messages))
	}
	got := r.messages[0].Text
	for _, runeValue := range got {
		if unicode.IsControl(runeValue) && runeValue != '\n' && runeValue != '\t' {
			t.Fatalf("reloaded notes-context message = %q carries control rune %U", got, runeValue)
		}
	}
	if !strings.Contains(got, "legacy") || !strings.Contains(got, "note") {
		t.Fatalf("reloaded notes-context message lost its text: %q", got)
	}
}

// The scope limit: a system message that is not a notes-context item keeps its
// bytes, since other system text is not notes-derived.
func TestApplyThreadItemLeavesOtherSystemMessagesAlone(t *testing.T) {
	const payload = "hook output \x1b[31mred\x1b[0m"
	r := NewTranscriptReducer(nil, map[string]int{}, map[string]int{})
	r.ApplyThreadItem(appwire.ThreadItem{
		Type: "systemMessage",
		ID:   "item_hook_1",
		Text: payload,
	}, 1, true)

	if len(r.messages) != 1 {
		t.Fatalf("reduced %d messages, want 1", len(r.messages))
	}
	if r.messages[0].Text != payload {
		t.Fatalf("non-notes system message = %q, want %q", r.messages[0].Text, payload)
	}
}
