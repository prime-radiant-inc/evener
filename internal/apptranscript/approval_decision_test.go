package apptranscript

import (
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// An approval NOTICE projects as a systemMessage of its own kind, whose text
// says what was decided and whose raw detail carries the decision, so a client
// draws "Allowed: …" or "Denied: …" without parsing prose (S16).
func TestApprovalDecisionNoticeProjectsItsDecision(t *testing.T) {
	for _, tc := range []struct {
		approved bool
		text     string
	}{
		{true, "Allowed write_file to access /Users/j/sites/docs"},
		{false, "Denied write_file access to /Users/j/sites/docs"},
	} {
		entry := schema.Turn{Kind: schema.TurnNotice, Format: schema.TurnFormatIdentity, Notice: &schema.NoticeInfo{
			Kind: schema.NoticeApprovalDecision,
			ApprovalDecision: &schema.ApprovalDecisionNotice{
				EscalationID: "esc_1", Approved: tc.approved, Tool: "write_file", Kind: "file_tool", DeniedPath: "/Users/j/sites/docs",
			},
		}}
		items, parts := ProjectEntryParts("turn_4", 9, entry, nil, nil, nil)
		if len(items) != 1 || len(parts) != 1 {
			t.Fatalf("approved=%v projected %d items, want one", tc.approved, len(items))
		}
		item := items[0]
		if item.Type != "systemMessage" || item.EventKind != appwire.ThreadItemEventKindApprovalDecision || item.Text != tc.text || item.Description != "Approval" {
			t.Fatalf("approved=%v item = %+v", tc.approved, item)
		}
		var raw map[string]map[string]any
		if err := json.Unmarshal(item.Raw, &raw); err != nil {
			t.Fatalf("raw %s: %v", item.Raw, err)
		}
		got := raw["approvalDecision"]
		if got["escalationId"] != "esc_1" || got["approved"] != tc.approved || got["tool"] != "write_file" || got["kind"] != "file_tool" || got["deniedPath"] != "/Users/j/sites/docs" {
			t.Fatalf("approved=%v raw = %s", tc.approved, item.Raw)
		}
	}
}

// A notice whose kind says approval but carries no payload shows nothing,
// like every other mismatched notice.
func TestApprovalDecisionNoticeWithoutPayloadShowsNothing(t *testing.T) {
	entry := schema.Turn{Kind: schema.TurnNotice, Format: schema.TurnFormatIdentity, Notice: &schema.NoticeInfo{Kind: schema.NoticeApprovalDecision}}
	if items, _ := ProjectEntryParts("turn_4", 9, entry, nil, nil, nil); len(items) != 0 {
		t.Fatalf("payload-less approval notice projected %+v", items)
	}
}

// A notice says when it happened, as a communicate message does: its item's
// startedAt is the entry's recorded instant, so approval history can say
// when the human decided. An entry with no timestamp leaves it unset.
func TestNoticeItemCarriesTheEntryTimestamp(t *testing.T) {
	at := time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC)
	entry := schema.Turn{Kind: schema.TurnNotice, Format: schema.TurnFormatIdentity, Timestamp: at, Notice: &schema.NoticeInfo{
		Kind:             schema.NoticeApprovalDecision,
		ApprovalDecision: &schema.ApprovalDecisionNotice{EscalationID: "esc_1", Approved: true, Tool: "write_file", Kind: "file_tool", DeniedPath: "/tmp/x"},
	}}
	item, ok := NoticeItem("turn_4", 9, entry)
	if !ok || item.StartedAt == nil || *item.StartedAt != at.UnixMilli() {
		t.Fatalf("item = %+v, want startedAt %d", item, at.UnixMilli())
	}
	entry.Timestamp = time.Time{}
	if item, ok := NoticeItem("turn_4", 9, entry); !ok || item.StartedAt != nil {
		t.Fatalf("untimed entry item = %+v, want no startedAt", item)
	}
}
