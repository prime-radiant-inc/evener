package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

func TestVoiceSpeechTextRequiresExactSuccessfulPendingQuestion(t *testing.T) {
	t.Parallel()
	base := appwire.ThreadItem{
		Type: "commandExecution", ToolName: "ask_user", Status: appwire.TurnStatusCompleted,
		TurnID: "execution", CallID: "call", Text: "UNTRUSTED-TEXT-17", ArgumentsJSON: `{"question":"ORIGINAL-ARGUMENTS-19"}`,
		Raw: json.RawMessage(`{"version":1,"questions":[{"question":"EXECUTED-QUESTION-23","options":[{"label":"OPTION-A-29"},{"label":"OPTION-B-31"}]}]}`),
	}
	pending := appwire.PendingAskState{Version: 1, InstanceID: "instance", Revision: 1, Calls: []appwire.PendingAskCall{{TurnID: "execution", CallID: "call"}}}
	for _, name := range []string{"exact", "resolved", "different_call", "different_execution", "different_instance", "unknown", "failed", "in_progress", "prevalidation", "error", "other_tool", "other_type", "missing_canonical", "legacy", "invalid_json", "empty_questions", "empty_question", "empty_option", "future_state", "future_pending"} {
		t.Run(name, func(t *testing.T) {
			item := base
			state := appwire.ClonePendingAsk(&pending)
			switch name {
			case "resolved":
				state.Calls = nil
			case "different_call":
				state.Calls[0].CallID = "another-call"
			case "different_execution":
				state.Calls[0].TurnID = "another-execution"
			case "different_instance":
				state.InstanceID = "another-instance"
			case "unknown":
				state = nil
			case "failed":
				item.Status = appwire.TurnStatusFailed
			case "in_progress":
				item.Status = appwire.TurnStatusInProgress
			case "prevalidation":
				item.PrevalOnly = true
			case "error":
				item.Error = "execution-failure"
			case "other_tool":
				item.ToolName = "delegate"
			case "other_type":
				item.Type = "systemMessage"
			case "missing_canonical":
				item.Raw = nil
			case "legacy":
				item.Raw = json.RawMessage(`{"questions":[{"question":"LEGACY-QUESTION-37"}]}`)
			case "invalid_json":
				item.Raw = json.RawMessage(`{"version":`)
			case "empty_questions":
				item.Raw = json.RawMessage(`{"version":1,"questions":[]}`)
			case "empty_question":
				item.Raw = json.RawMessage(`{"version":1,"questions":[{"question":" "}]}`)
			case "empty_option":
				item.Raw = json.RawMessage(`{"version":1,"questions":[{"question":"QUESTION-41","options":[{}]}]}`)
			case "future_state":
				item.Raw = json.RawMessage(`{"version":2,"questions":[{"question":"FUTURE-QUESTION-43"}]}`)
			case "future_pending":
				state.Version = 2
			}
			text := voiceSpeechText(item, state, "instance")
			if name == "exact" {
				if !strings.Contains(text, "EXECUTED-QUESTION-23") || strings.Contains(text, "UNTRUSTED-TEXT-17") || strings.Contains(text, "ORIGINAL-ARGUMENTS-19") {
					t.Fatal("question selection used noncanonical public content")
				}
			} else if text != "" {
				t.Fatalf("ineligible %s source became speech", name)
			}
		})
	}
}

func TestVoiceAskPageDriftUsesHistoryRecovery(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		info appwire.ErrorInfo
		want bool
	}{
		{appwire.ErrorAskFenceMismatch, true},
		{appwire.ErrorTranscriptItemCursorStale, true},
		{appwire.ErrorAskFenceUnsupportedOnDaemon, false},
		{appwire.ErrorConflict, false},
	} {
		err := appwire.WireError{Code: appwire.CodeConflict, Data: appwire.ErrorData{EvenerErrorInfo: tc.info}}
		if got := voiceHistoryCursorStale(err); got != tc.want {
			t.Errorf("history recovery for %s=%v, want %v", tc.info, got, tc.want)
		}
	}
}
