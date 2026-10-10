package schema

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

func TestNewTurnFieldsRoundTripAndStayInKeyOrder(t *testing.T) {
	ordinal := uint64(7)
	turn := Turn{
		Communicate:     &CommunicateInfo{CallID: "c1", EndTurn: true, Message: "hi"},
		Completion:      &TurnCompletionInfo{Status: TurnInterrupted, CompletedAt: time.Unix(5, 0).UTC(), DurationMS: 12},
		Format:          TurnFormatIdentity,
		Kind:            TurnCompletion,
		Message:         llm.Message{Role: llm.RoleUser},
		Model:           "gpt-5.4",
		Notice:          &NoticeInfo{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete", Iterations: 2}},
		OriginalOrdinal: &ordinal,
		RoundID:         "r_1",
		Timestamp:       time.Unix(4, 0).UTC(),
		TurnID:          "t_1",
		TurnKind:        TurnSpanExecution,
	}
	data, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	var back Turn
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, turn) {
		t.Fatalf("round trip = %+v, want %+v", back, turn)
	}
	// The public line projection re-marshals through sorted maps and relies on
	// the struct's field order matching the sort.
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	sorted, err := json.Marshal(keys)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sorted, data) {
		t.Fatalf("field order differs from key order:\n got %s\nwant %s", data, sorted)
	}
}

func TestNoticePayloadsRoundTrip(t *testing.T) {
	for _, notice := range []NoticeInfo{
		{Kind: NoticeToolRepair, ToolRepair: &ToolRepairNotice{ToolName: "read_file", CallID: "c1", Changes: []string{"renamed path"}}},
		{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "blocked", Reason: "stuck", Iterations: 3}},
		{Kind: NoticeTurnLimit, TurnLimit: &TurnLimitNotice{MaxTurns: 4, MaxToolRoundsPerInput: 9}},
		{Kind: NoticeSkillActivated, SkillActivated: &SkillActivatedNotice{Name: "tdd"}},
		{Kind: NoticeApprovalDecision, ApprovalDecision: &ApprovalDecisionNotice{EscalationID: "esc_1", Approved: true, Tool: "write_file", Kind: "file_tool", DeniedPath: "/etc/hosts"}},
	} {
		data, err := json.Marshal(notice)
		if err != nil {
			t.Fatal(err)
		}
		var back NoticeInfo
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(back, notice) {
			t.Fatalf("round trip = %+v, want %+v", back, notice)
		}
	}
}

// Validate enforces NoticeInfo's own doc contract: exactly one payload,
// matching Kind. The read side (internal/apptranscript/notice.go) drops any
// mismatch silently, so the write side must refuse to record one.
func TestNoticeInfoValidate(t *testing.T) {
	valid := []NoticeInfo{
		{Kind: NoticeToolRepair, ToolRepair: &ToolRepairNotice{ToolName: "read_file"}},
		{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete"}},
		{Kind: NoticeTurnLimit, TurnLimit: &TurnLimitNotice{MaxTurns: 4}},
		{Kind: NoticeSkillActivated, SkillActivated: &SkillActivatedNotice{Name: "tdd"}},
		{Kind: NoticeApprovalDecision, ApprovalDecision: &ApprovalDecisionNotice{EscalationID: "esc_1", Approved: true}},
	}
	for _, notice := range valid {
		if err := notice.Validate(); err != nil {
			t.Errorf("Validate(%+v) = %v, want nil", notice, err)
		}
	}
	invalid := []struct {
		name   string
		notice NoticeInfo
	}{
		{"zero payloads", NoticeInfo{Kind: NoticeGoalEnded}},
		{"two payloads", NoticeInfo{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "complete"}, TurnLimit: &TurnLimitNotice{MaxTurns: 4}}},
		{"mismatched kind", NoticeInfo{Kind: NoticeGoalEnded, TurnLimit: &TurnLimitNotice{MaxTurns: 4}}},
		{"approval without its payload", NoticeInfo{Kind: NoticeApprovalDecision}},
		{"approval payload under another kind", NoticeInfo{Kind: NoticeGoalEnded, ApprovalDecision: &ApprovalDecisionNotice{EscalationID: "esc_1"}}},
	}
	for _, tc := range invalid {
		if err := tc.notice.Validate(); err == nil {
			t.Errorf("%s: Validate(%+v) = nil, want an error", tc.name, tc.notice)
		}
	}
}

func TestLegacyTurnCarriesNoIdentity(t *testing.T) {
	data, err := json.Marshal(NewTurn(TurnUserInput, llm.User("hi")))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"format", "turn_id", "turn_kind", "round_id", "original_ordinal", "model", "completion", "communicate", "notice"} {
		if strings.Contains(string(data), `"`+key+`"`) {
			t.Fatalf("a turn without identity marshals %q: %s", key, data)
		}
	}
}

func TestTranscriptOnlyKinds(t *testing.T) {
	for _, kind := range []TurnKind{TurnCompletion, TurnReopen, TurnCommunicate, TurnNotice} {
		if !kind.TranscriptOnly() {
			t.Errorf("%s is not transcript-only", kind)
		}
	}
	for _, kind := range []TurnKind{TurnUserInput, TurnSteering, TurnAssistant, TurnTool, TurnToolResults, TurnSystem, TurnCheckpoint, TurnSummary, TurnModelSwitch, TurnFailure, TurnHookCompleted, TurnEnvironment, TurnNotesContext, TurnAttentionResolution} {
		if kind.TranscriptOnly() {
			t.Errorf("%s is transcript-only", kind)
		}
	}
}
