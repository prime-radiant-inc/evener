package agent

import (
	"context"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

func armLiveAskExecution(s *Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execution.turnID = "turn-live"
}

func appendAsk(s *Session, question string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendAskPendingLocked([]askQuestion{{Question: question, Options: []string{"yes", "no"}}}, []byte(`{"questions":[{}]}`), "call-"+question)
}

// appendFenceAsk records a same-execution ask with the real transcript append
// door, then runs the real ask tool's live append. No provider scheduling is
// needed to place this specific admission-to-consumption interleaving.
func appendFenceAsk(t *testing.T, s *Session, callID, turnID string) {
	t.Helper()
	call := askUserCall(callID, askUserArgsValid())
	assistant := schema.NewTurn(schema.TurnAssistant, llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentPart{{Kind: llm.ContentToolCall, ToolCall: &call}}})
	assistant.TurnID = turnID
	result := schema.NewTurn(schema.TurnToolResults, llm.Message{Role: llm.RoleTool, Content: []llm.ContentPart{{Kind: llm.ContentToolResult, ToolResult: &llm.ToolResultData{Name: "ask_user", ToolCallID: callID, ToolState: canonicalAskToolState(t, askUserArgsValid())}}}})
	result.TurnID = turnID
	for _, turn := range []schema.Turn{assistant, result} {
		if err := s.appendTurnAfterTranscriptWrite(turn, func() error {
			_, err := s.recordTranscriptLocked(turn, transcript.DoorDurable, transcript.PlaceInTurn(turnID))
			return err
		}, func() { s.history = append(s.history, turn) }); err != nil {
			t.Fatal(err)
		}
	}
	s.mu.Lock()
	previous := s.execution.turnID
	s.execution.turnID = turnID
	s.mu.Unlock()
	res := s.reg.ExecuteCall(context.WithValue(tripwireCtx(t), ctxToolCallID, callID), s.env, call)
	s.mu.Lock()
	s.execution.turnID = previous
	s.mu.Unlock()
	if res.IsError {
		t.Fatalf("append ask failed: %+v", res)
	}
}

func TestAskFenceScopedAnswerPreservesPostAdmissionAppend(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"queue", "steer_inline", "steer_carrier"} {
		t.Run(mode, func(t *testing.T) {
			s, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
			ctx := seedDurableAdmissionAsk(t, s)
			s.SetBootGeneration("boot-current")
			pair := s.PendingAskCalls()
			if mode == "queue" {
				_, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "scoped-answer", Input: clientMutationInput("answer-sentinel", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := s.AcceptClientMutationSteer(appwire.TurnSteerParams{ClientMutationID: "scoped-answer", Input: clientMutationInput("answer-sentinel", nil, nil), ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration})
				if err != nil {
					t.Fatal(err)
				}
			}
			appendFenceAsk(t, s, "ask-after-admission", pair.Calls[0].TurnID)
			if appended := s.PendingAskCalls(); len(appended.Calls) != 2 || appended.CalllessCount != 0 {
				t.Fatalf("append scenario missing exact call identity: %+v", appended)
			}
			adapter.appendSteps(func(llm.Request) llm.Response { return agenttest.FinalResponse("done-sentinel") })
			if mode == "steer_inline" {
				if !s.injectDrainedSteering() {
					t.Fatal("steer not delivered")
				}
			} else if _, ran, err := s.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
				t.Fatalf("ran=%v error=%v", ran, err)
			}
			assertFenceSurvivor(t, s, "ask-after-admission")
			liveCalls := s.PendingAskCalls().Calls
			if liveCalls[0].TurnID != pair.Calls[0].TurnID {
				t.Fatalf("surviving ask changed execution identity: %+v", liveCalls)
			}
			meta, dir := s.Meta(), s.stateDir
			s.Close()
			restored := restoreAskSession(t, meta, dir)
			assertFenceSurvivor(t, restored, "ask-after-admission")
			if !reflect.DeepEqual(restored.PendingAskCalls().Calls, liveCalls) {
				t.Fatalf("restore changed surviving composite: live=%+v restored=%+v", liveCalls, restored.PendingAskCalls().Calls)
			}
		})
	}
}

func assertFenceSurvivor(t *testing.T, s *Session, callID string) {
	t.Helper()
	state := s.PendingAskCalls()
	if s.askPendingCount() != 1 || len(state.Calls) != 1 || state.Calls[0].CallID != callID {
		t.Fatalf("survivors=%+v questionCount=%d", state, s.askPendingCount())
	}
}
