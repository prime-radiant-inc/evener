package agent

import (
	"encoding/json"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// canonicalAskToolState builds the ToolState the live ask_user exec records,
// so a restored ask keeps its call identity instead of degrading to the
// legacy text-only path (questionsFromAskCalls only mints TurnID/CallID pairs
// from the canonical state).
func canonicalAskToolState(t *testing.T, args map[string]any) []byte {
	t.Helper()
	normalized, err := normalizeAskArgs(args)
	if err != nil {
		t.Fatalf("normalize ask args: %v", err)
	}
	parsed, err := parseAskQuestions(normalized)
	if err != nil {
		t.Fatalf("parse ask questions: %v", err)
	}
	state, err := json.Marshal(schema.AskUserState{Version: 1, Questions: projectAskUserQuestions(normalized, parsed)})
	if err != nil {
		t.Fatalf("marshal ask state: %v", err)
	}
	return state
}

func fenceAskRound(t *testing.T, turnID string, canonical bool, callIDs ...string) []schema.Turn {
	t.Helper()
	assistant := schema.Turn{Kind: schema.TurnAssistant, TurnID: turnID, Message: llm.Message{Role: llm.RoleAssistant}}
	results := schema.Turn{Kind: schema.TurnToolResults, TurnID: turnID, Message: llm.Message{Role: llm.RoleTool}}
	for _, id := range callIDs {
		call := askUserCall(id, askUserArgsValid())
		assistant.Message.Content = append(assistant.Message.Content, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: &call})
		result := &llm.ToolResultData{Name: "ask_user", ToolCallID: id}
		if canonical {
			result.ToolState = canonicalAskToolState(t, askUserArgsValid())
		}
		results.Message.Content = append(results.Message.Content, llm.ContentPart{Kind: llm.ContentToolResult, ToolResult: result})
	}
	return []schema.Turn{assistant, results}
}

func TestAskFenceRestoreSubtractsCapturedCompositesAndLegacyCount(t *testing.T) {
	t.Parallel()
	for _, tail := range []string{"answer", "assistant_final", "generic_result", "new_ask_round"} {
		t.Run(tail, func(t *testing.T) {
			history := []schema.Turn{{Kind: schema.TurnUserInput}}
			history = append(history, fenceAskRound(t, "ask-turn", true, "first", "second")...)
			history = append(history, schema.Turn{Kind: schema.TurnUserInput, AskResolution: &schema.AskResolution{Calls: []appwire.PendingAskCall{{TurnID: "ask-turn", CallID: "first"}}}})
			switch tail {
			case "assistant_final":
				history = append(history, schema.Turn{Kind: schema.TurnAssistant})
			case "generic_result":
				history = append(history, schema.Turn{Kind: schema.TurnToolResults, Message: llm.Message{Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Name: "other"}}}}})
			case "new_ask_round":
				history = append(history, fenceAskRound(t, "answer-turn", true, "third")...)
			}
			pending, found := deriveRestoredAskPending(history, 0, nil)
			want := 1
			if tail == "new_ask_round" {
				want = 2
			}
			if !found || len(pending) != want || pending[0].CallID != "second" {
				t.Fatalf("pending=%+v found=%v", pending, found)
			}
			if state := deriveRestoredState(history, 0, nil, nil); state != SessionAwaiting {
				t.Fatalf("state=%s", state)
			}
		})
	}
	for _, mixed := range []bool{false, true} {
		history := []schema.Turn{{Kind: schema.TurnUserInput}}
		history = append(history, fenceAskRound(t, "legacy", false, "legacy-call")...)
		if mixed {
			history = append(history, schema.Turn{Kind: schema.TurnSteering, SteeringSource: events.SteeringSourceUser, SteeringKind: events.SteeringKindHumanNote})
			history = append(history, fenceAskRound(t, "named", true, "named-call", "survivor")...)
		}
		resolution := &schema.AskResolution{LegacyCount: 1}
		if mixed {
			resolution.Calls = []appwire.PendingAskCall{{TurnID: "named", CallID: "named-call"}}
		}
		history = append(history, schema.Turn{Kind: schema.TurnUserInput, AskResolution: resolution})
		pending, found := deriveRestoredAskPending(history, 0, nil)
		if mixed {
			if !found || len(pending) != 1 || pending[0].CallID != "survivor" {
				t.Fatalf("mixed pending=%+v found=%v", pending, found)
			}
		} else if found || len(pending) != 0 {
			t.Fatalf("legacy pending=%+v found=%v", pending, found)
		}
	}
}

// TestAskRestoreKeepsAsksPostedAfterADurableAnswer pins the restore scan's
// recency scoping, which the redesigned clear must agree with. Walking
// backward, deriveRestoredAskPending accumulates ask rounds it passes and
// a durable user input survives restore even though a live whole-set clear at
// the input's consumption would have wiped it. The spike's scoped clear
// (clearing exactly the calls the answer was admitted against) makes the live
// session agree with this restore behavior instead of leaving the crash
// window resurrecting questions the live clear removed. This test passes
// today; it is the restore half of the scoped semantics the implementer lane
// inherits, and the anti-regression for the restore changes.
func TestAskRestoreKeepsAsksPostedAfterADurableAnswer(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newSession(t, withConfig(minimalTestSessionConfig(t)), withStateDir(dir))

	call1 := askUserCall("ask-call-early", askUserArgsValid())
	s.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call1}}})
	earlyTurnID := s.history[len(s.history)-1].TurnID
	s.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool,
		Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			Name: "ask_user", ToolCallID: call1.ID, Content: "ack", ToolState: canonicalAskToolState(t, askUserArgsValid())}}}})

	// A durable answer naming the ask it resolves.
	answer := schema.NewTurn(schema.TurnUserInput, llm.Message{Role: llm.RoleUser,
		Content: []llm.ContentPart{{Kind: llm.ContentText, Text: "answer to the early ask"}}})
	answer.AnswerToTurnIDs = []string{earlyTurnID}
	if err := s.appendTurnAfterTranscriptWrite(answer,
		func() error { return s.appendClientMutationTranscriptLocked(answer) },
		func() { s.history = append(s.history, answer) },
	); err != nil {
		t.Fatalf("append answer: %v", err)
	}

	// A later ask, posted after the answer.
	call2 := askUserCall("ask-call-late", askUserArgsValid())
	s.appendTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant,
		Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call2}}})
	s.appendTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool,
		Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{
			Name: "ask_user", ToolCallID: call2.ID, Content: "ack", ToolState: canonicalAskToolState(t, askUserArgsValid())}}}})

	meta, closing := s.Meta(), s.stateDir
	s.Close()
	restored := restoreAskSession(t, meta, closing)

	if !restored.HasPendingAsk() {
		t.Fatal("restore dropped the ask posted after the durable answer — the restore scan is recency-scoped and must keep it")
	}
	_, got := restored.PendingAskSnapshot()
	if got == nil || len(got.Calls) != 1 || got.Calls[0].CallID != "ask-call-late" {
		t.Fatalf("restored pending set = %+v, want exactly the late ask's call", got)
	}
}
