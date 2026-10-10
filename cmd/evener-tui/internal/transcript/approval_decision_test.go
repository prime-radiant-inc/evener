package transcript

import (
	"testing"

	"primeradiant.com/evener/appwire"
)

// A human's Allow or Deny leaves a systemMessage of its own kind in history
// (S16), carrying startedAt like every notice item. The TUI shows it as it
// shows every system message: its description over its text.
func TestApprovalDecisionRendersAsASystemLine(t *testing.T) {
	r := NewTranscriptReducer(nil, map[string]int{}, map[string]int{})
	startedAt := int64(1791297000000)
	r.ApplyThreadItem(appwire.ThreadItem{
		Type:        "systemMessage",
		ID:          "item_approval_decision_9",
		EventKind:   appwire.ThreadItemEventKindApprovalDecision,
		Description: "Approval",
		Text:        "Allowed write_file to access /Users/j/sites/docs",
		StartedAt:   &startedAt,
		Raw:         []byte(`{"approvalDecision":{"escalationId":"esc_1","approved":true,"tool":"write_file","kind":"file_tool","deniedPath":"/Users/j/sites/docs"}}`),
	}, 4, true)
	messages := r.Messages()
	if len(messages) != 1 || messages[0].Kind != MsgSystem {
		t.Fatalf("messages = %+v, want one system line", messages)
	}
	if messages[0].Text != "Approval\nAllowed write_file to access /Users/j/sites/docs" {
		t.Fatalf("system line = %q", messages[0].Text)
	}
}
