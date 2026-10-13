package apptranscript

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

// An approval NOTICE projects as a systemMessage of its own kind, whose text
// says what was decided and whose raw detail carries the decision, so a client
// draws "Allowed: …" or "Denied: …" without parsing prose (S16).
func TestApprovalDecisionNoticeProjectsItsDecision(t *testing.T) {
	texts := map[bool]string{}
	for _, approved := range []bool{true, false} {
		entry := schema.Turn{Kind: schema.TurnNotice, Format: schema.TurnFormatIdentity, Notice: &schema.NoticeInfo{
			Kind: schema.NoticeApprovalDecision,
			ApprovalDecision: &schema.ApprovalDecisionNotice{
				EscalationID: "esc_1", Approved: approved, Tool: "write_file", Kind: "file_tool", DeniedPath: "/Users/j/sites/docs",
			},
		}}
		items, parts := ProjectEntryParts("turn_4", 9, entry, nil, nil, nil)
		if len(items) != 1 || len(parts) != 1 {
			t.Fatalf("approved=%v projected %d items, want one", approved, len(items))
		}
		item := items[0]
		if item.Type != "systemMessage" || item.EventKind != appwire.ThreadItemEventKindApprovalDecision ||
			!strings.Contains(item.Text, "write_file") || !strings.Contains(item.Text, "/Users/j/sites/docs") {
			t.Fatalf("approved=%v item = %+v, want an approval decision naming the tool and path", approved, item)
		}
		texts[approved] = item.Text
		var raw map[string]map[string]any
		if err := json.Unmarshal(item.Raw, &raw); err != nil {
			t.Fatalf("raw %s: %v", item.Raw, err)
		}
		got := raw["approvalDecision"]
		if got["escalationId"] != "esc_1" || got["approved"] != approved || got["tool"] != "write_file" || got["kind"] != "file_tool" || got["deniedPath"] != "/Users/j/sites/docs" {
			t.Fatalf("approved=%v raw = %s", approved, item.Raw)
		}
	}
	if texts[true] == texts[false] {
		t.Fatalf("an allow and a deny read the same: %q", texts[true])
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
