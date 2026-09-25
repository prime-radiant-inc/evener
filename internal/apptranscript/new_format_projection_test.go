package apptranscript

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/schema/schematest"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// newFormatAssistantEntry is text, text, thinking, text, a communicate call
// and a read_file call: every ASSISTANT rule in one entry.
func newFormatAssistantEntry(format int) schema.Turn {
	return schema.Turn{
		Format:    format,
		TurnID:    "t_1",
		RoundID:   "r_1",
		Kind:      schema.TurnAssistant,
		Timestamp: time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC),
		Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "a"},
			{Kind: llm.ContentText, Text: "b"},
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "think"}},
			{Kind: llm.ContentText, Text: "c"},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c1", Name: "communicate", Arguments: json.RawMessage(`{"message":"m"}`)}},
			{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "c2", Name: "read_file", Arguments: json.RawMessage(`{"path":"x"}`)}},
		}},
	}
}

func TestNewFormatAssistantProjectsTextRunsReasoningAndHidesCommunicate(t *testing.T) {
	items, parts := ProjectEntryParts("t_1", 3, newFormatAssistantEntry(schema.TurnFormatIdentity), map[string]string{}, nil, nil)
	if !reflect.DeepEqual(parts, []int{0, 2, 3, 5}) {
		t.Fatalf("parts = %v, want [0 2 3 5]", parts)
	}
	want := []struct{ typ, id, text string }{
		{"agentMessage", "item_assistant_3_0", "ab"},
		{"reasoning", "item_reasoning_3_2", "think"},
		{"agentMessage", "item_assistant_3_3", "c"},
		{"commandExecution", "item_tool_3_5", ""},
	}
	if len(items) != len(want) {
		t.Fatalf("items = %+v, want %d items", items, len(want))
	}
	for i, w := range want {
		got := items[i]
		if got.Type != w.typ || got.ID != w.id || got.Text != w.text {
			t.Fatalf("item %d = {%s %s %q}, want {%s %s %q}", i, got.Type, got.ID, got.Text, w.typ, w.id, w.text)
		}
		if got.RoundID != "r_1" {
			t.Fatalf("item %d RoundID = %q, want r_1", i, got.RoundID)
		}
		if got.TurnID != "t_1" {
			t.Fatalf("item %d TurnID = %q, want t_1", i, got.TurnID)
		}
	}
	// Messages and calls carry the entry's recorded instant; reasoning does not.
	for i, stamped := range []bool{true, false, true, true} {
		if (items[i].StartedAt != nil) != stamped {
			t.Fatalf("item %d StartedAt = %v, want set %v", i, items[i].StartedAt, stamped)
		}
	}
	if items[3].CallID != "c2" || items[3].ToolName != "read_file" {
		t.Fatalf("tool item = %+v, want the read_file call c2", items[3])
	}
}

func TestNewFormatReasoningUsesSummaryWhenTextIsEmpty(t *testing.T) {
	entry := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Summary: []string{"sum"}}},
	}}}
	items, parts := ProjectEntryParts("t_1", 0, entry, nil, nil, nil)
	if len(items) != 1 || items[0].Type != "reasoning" || items[0].Text != "sum" || !reflect.DeepEqual(parts, []int{0}) {
		t.Fatalf("items = %+v parts = %v, want one reasoning item \"sum\" at part 0", items, parts)
	}
}

func TestNewFormatReasoningJoinsEveryThinkingPartIntoOneItem(t *testing.T) {
	entry := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "x"},
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "one"}},
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Summary: []string{"two", "three"}}},
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{}},
	}}}
	items, parts := ProjectEntryParts("t_1", 0, entry, nil, nil, nil)
	if !reflect.DeepEqual(parts, []int{0, 1}) || len(items) != 2 {
		t.Fatalf("items = %+v parts = %v, want agentMessage part 0 and reasoning part 1", items, parts)
	}
	if items[1].Type != "reasoning" || items[1].ID != "item_reasoning_0_1" || items[1].Text != "one\n\ntwo\n\nthree" {
		t.Fatalf("reasoning = %+v, want every thinking part joined", items[1])
	}
}

func TestNewFormatReasoningAndTextWithNothingToShowProjectNothing(t *testing.T) {
	entry := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: ""},
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{}},
		{Kind: llm.ContentThinking},
	}}}
	if items, parts := ProjectEntryParts("t_1", 0, entry, nil, nil, nil); len(items) != 0 || len(parts) != 0 {
		t.Fatalf("items = %+v parts = %v, want nothing", items, parts)
	}
}

// Redacted thinking shows what the legacy projection shows for it, inside the
// entry's one reasoning item, so an entry whose only thinking is redacted
// still has its reasoning item.
func TestNewFormatRedactedThinkingJoinsTheReasoningItem(t *testing.T) {
	redacted := llm.ContentPart{Kind: llm.ContentRedThinking, Thinking: &llm.ThinkingData{Redacted: true}}
	legacy, _ := ProjectTurnParts("turn_0", 0, schema.Turn{Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{redacted}}}, nil, nil, nil)
	if len(legacy) != 1 {
		t.Fatalf("legacy redacted projection = %+v, want one item", legacy)
	}
	onlyRedacted := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		{Kind: llm.ContentText, Text: "x"},
		redacted,
	}}}
	items, parts := ProjectEntryParts("t_1", 0, onlyRedacted, nil, nil, nil)
	if !reflect.DeepEqual(parts, []int{0, 1}) || items[1].Type != "reasoning" || items[1].ID != "item_reasoning_0_1" || items[1].Text != legacy[0].Text {
		t.Fatalf("items = %+v parts = %v, want reasoning %q at part 1", items, parts, legacy[0].Text)
	}
	mixed := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnAssistant, Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{
		redacted,
		{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "visible"}},
	}}}
	items, parts = ProjectEntryParts("t_1", 0, mixed, nil, nil, nil)
	if len(items) != 1 || !reflect.DeepEqual(parts, []int{0}) || items[0].Text != legacy[0].Text+"\n\nvisible" {
		t.Fatalf("items = %+v parts = %v, want one reasoning item joining both parts", items, parts)
	}
}

// A legacy entry keeps today's projection: one agentMessage per text part,
// the communicate call as an agentMessage, and no round id.
func TestNewFormatLegacyEntryProjectsAsToday(t *testing.T) {
	items, parts := ProjectEntryParts("turn_3", 3, newFormatAssistantEntry(0), map[string]string{}, nil, nil)
	todayItems, todayParts := ProjectTurnParts("turn_3", 3, newFormatAssistantEntry(0), map[string]string{}, nil, nil)
	if !reflect.DeepEqual(items, todayItems) || !reflect.DeepEqual(parts, todayParts) {
		t.Fatalf("ProjectEntryParts of a legacy entry differs from ProjectTurnParts")
	}
	if !reflect.DeepEqual(parts, []int{0, 1, 2, 3, 4, 5}) {
		t.Fatalf("parts = %v, want [0 1 2 3 4 5]", parts)
	}
	want := []struct{ typ, id, text string }{
		{"agentMessage", "item_assistant_3_0", "a"},
		{"agentMessage", "item_assistant_3_1", "b"},
		{"reasoning", "item_reasoning_3_2", "think"},
		{"agentMessage", "item_assistant_3_3", "c"},
		{"agentMessage", "item_assistant_3_4", "m"},
		{"commandExecution", "item_tool_3_5", ""},
	}
	if len(items) != len(want) {
		t.Fatalf("items = %+v, want %d items", items, len(want))
	}
	for i, w := range want {
		got := items[i]
		if got.Type != w.typ || got.ID != w.id || got.Text != w.text || got.RoundID != "" {
			t.Fatalf("item %d = {%s %s %q round %q}, want {%s %s %q round \"\"}", i, got.Type, got.ID, got.Text, got.RoundID, w.typ, w.id, w.text)
		}
	}
}

func TestNewFormatCommunicateProjectsTheDeliveredMessage(t *testing.T) {
	at := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	entry := schema.Turn{
		Format:      schema.TurnFormatIdentity,
		TurnID:      "t_1",
		Kind:        schema.TurnCommunicate,
		Timestamp:   at,
		Communicate: &schema.CommunicateInfo{CallID: "c1", EndTurn: true, Message: "hello there"},
	}
	items, parts := ProjectEntryParts("t_1", 7, entry, nil, nil, nil)
	ms := at.UnixMilli()
	want := appwire.ThreadItem{
		Type:      "agentMessage",
		ID:        "item_assistant_7_0",
		TurnID:    "t_1",
		Text:      "hello there",
		CallID:    "c1",
		Status:    appwire.TurnStatusCompleted,
		StartedAt: &ms,
	}
	if len(items) != 1 || !reflect.DeepEqual(items[0], want) || !reflect.DeepEqual(parts, []int{0}) {
		t.Fatalf("items = %+v parts = %v, want [%+v] at part 0", items, parts, want)
	}
	if item, ok := CommunicateItem("t_1", 7, entry); !ok || !reflect.DeepEqual(item, want) {
		t.Fatalf("CommunicateItem = %+v, %v; want %+v", item, ok, want)
	}
	if _, ok := CommunicateItem("t_1", 7, schema.Turn{Kind: schema.TurnCommunicate, Format: schema.TurnFormatIdentity}); ok {
		t.Fatal("a COMMUNICATE entry with no payload projected an item")
	}
}

func TestNewFormatNoticeProjectsItsAnnouncement(t *testing.T) {
	for _, tc := range []struct {
		notice schema.NoticeInfo
		kind   appwire.ThreadItemEventKind
	}{
		{schema.NoticeInfo{Kind: schema.NoticeToolRepair, ToolRepair: &schema.ToolRepairNotice{ToolName: "edit_file", CallID: "c1", Changes: []string{"drop_unknown:artifacts:dropped artifacts"}}}, appwire.ThreadItemEventKindToolRepair},
		{schema.NoticeInfo{Kind: schema.NoticeGoalEnded, GoalEnded: &schema.GoalEndedNotice{Status: "complete", Iterations: 2}}, appwire.ThreadItemEventKindGoalEnded},
		{schema.NoticeInfo{Kind: schema.NoticeTurnLimit, TurnLimit: &schema.TurnLimitNotice{MaxTurns: 3}}, appwire.ThreadItemEventKindTurnLimit},
		{schema.NoticeInfo{Kind: schema.NoticeSkillActivated, SkillActivated: &schema.SkillActivatedNotice{Name: "brainstorming"}}, appwire.ThreadItemEventKindSkillActivated},
	} {
		t.Run(string(tc.notice.Kind), func(t *testing.T) {
			notice := tc.notice
			entry := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnNotice, Notice: &notice}
			items, parts := ProjectEntryParts("t_1", 4, entry, nil, nil, nil)
			if len(items) != 1 || !reflect.DeepEqual(parts, []int{0}) {
				t.Fatalf("items = %+v parts = %v, want one item at part 0", items, parts)
			}
			item := items[0]
			if item.Type != "systemMessage" || item.EventKind != tc.kind || item.Status != appwire.TurnStatusCompleted {
				t.Fatalf("item = %+v, want a completed %s systemMessage", item, tc.kind)
			}
			if item.Text == "" || item.Description == "" || item.TurnID != "t_1" || item.ID == "" {
				t.Fatalf("item = %+v, want text, description, turn and id", item)
			}
			if direct, ok := NoticeItem("t_1", 4, entry); !ok || !reflect.DeepEqual(direct, item) {
				t.Fatalf("NoticeItem = %+v, %v; want %+v", direct, ok, item)
			}
		})
	}
	for _, bad := range []*schema.NoticeInfo{
		nil,
		{Kind: schema.NoticeGoalEnded},
		{Kind: "unknown_notice", GoalEnded: &schema.GoalEndedNotice{Status: "complete"}},
	} {
		entry := schema.Turn{Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnNotice, Notice: bad}
		if items, parts := ProjectEntryParts("t_1", 4, entry, nil, nil, nil); len(items) != 0 || len(parts) != 0 {
			t.Fatalf("notice %+v projected %+v / %v, want nothing", bad, items, parts)
		}
	}
}

func TestNewFormatStatusEntriesAndFoldCopiesProjectNothing(t *testing.T) {
	ordinal := uint64(2)
	foldCopy := newFormatAssistantEntry(schema.TurnFormatIdentity)
	foldCopy.OriginalOrdinal = &ordinal
	for name, entry := range map[string]schema.Turn{
		"completion": {Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnCompletion, Completion: &schema.TurnCompletionInfo{}},
		"reopen":     {Format: schema.TurnFormatIdentity, TurnID: "t_1", Kind: schema.TurnReopen},
		"fold copy":  foldCopy,
	} {
		if items, parts := ProjectEntryParts("t_1", 9, entry, map[string]string{}, nil, nil); len(items) != 0 || len(parts) != 0 {
			t.Fatalf("%s projected %+v / %v, want nothing", name, items, parts)
		}
	}
}

// The transcript-only kinds project by the read model's rules through
// ProjectEntryParts only: today's consumers, through ProjectTurnParts, still
// pass over them (TestProjectTurnPartsProjectsNothingForTranscriptOnlyKinds),
// and a new-format ASSISTANT entry still projects as today there.
func TestNewFormatRulesApplyOnlyThroughProjectEntryParts(t *testing.T) {
	for _, sample := range schematest.TranscriptOnlySamples() {
		items, parts := ProjectEntryParts("t_1", 1, sample, map[string]string{}, nil, nil)
		switch sample.Kind {
		case schema.TurnCommunicate, schema.TurnNotice:
			if len(items) != 1 || !reflect.DeepEqual(parts, []int{0}) {
				t.Errorf("%s projected %v / %v, want one item at part 0", sample.Kind, items, parts)
			}
		default:
			if len(items) != 0 || len(parts) != 0 {
				t.Errorf("%s projected %v", sample.Kind, items)
			}
		}
	}
	newFormat, parts := ProjectTurnParts("turn_3", 3, newFormatAssistantEntry(schema.TurnFormatIdentity), map[string]string{}, nil, nil)
	legacy, legacyParts := ProjectTurnParts("turn_3", 3, newFormatAssistantEntry(0), map[string]string{}, nil, nil)
	if !reflect.DeepEqual(newFormat, legacy) || !reflect.DeepEqual(parts, legacyParts) {
		t.Fatalf("ProjectTurnParts applied the new-format rules:\n got %+v\nwant %+v", newFormat, legacy)
	}
}
