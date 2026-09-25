package schema

import (
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
	if string(sorted) != string(data) {
		t.Fatalf("field order differs from key order:\n got %s\nwant %s", data, sorted)
	}
}

func TestNoticePayloadsRoundTrip(t *testing.T) {
	for _, notice := range []NoticeInfo{
		{Kind: NoticeToolRepair, ToolRepair: &ToolRepairNotice{ToolName: "read_file", CallID: "c1", Changes: []string{"renamed path"}}},
		{Kind: NoticeGoalEnded, GoalEnded: &GoalEndedNotice{Status: "blocked", Reason: "stuck", Iterations: 3}},
		{Kind: NoticeTurnLimit, TurnLimit: &TurnLimitNotice{MaxTurns: 4, MaxToolRoundsPerInput: 9}},
		{Kind: NoticeSkillActivated, SkillActivated: &SkillActivatedNotice{Name: "tdd"}},
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
