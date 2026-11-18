package agent

import (
	"encoding/json"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/apptranscript"
	"primeradiant.com/evener/llm"
)

// An admitted answer that resolves several ask_user calls of one execution
// carries admission's per-call evidence on the wire: the exact (turn, call)
// pairs it consumed, which the turn-granular answer link cannot express. A
// client orders answer recaps by these calls (#6323).
func TestAskResolutionProjectsCallLevelEvidence(t *testing.T) {
	t.Parallel()
	s, adapter := newDurableAdmissionAskSessionWithAdapter(t, SessionConfig{})
	ctx := seedDurableAdmissionAsk(t, s)
	s.SetBootGeneration("current")
	_, first := s.PendingAskSnapshot()
	// A second call in the SAME execution: one answer link, two calls.
	appendFenceAsk(t, s, "ask2", first.Calls[0].TurnID)
	_, pair := s.PendingAskSnapshot()
	if len(pair.Calls) != 2 || pair.Calls[0].CallID != "ask1" || pair.Calls[1].CallID != "ask2" {
		t.Fatalf("pending calls = %+v", pair.Calls)
	}
	if pair.Calls[0].TurnID != pair.Calls[1].TurnID {
		t.Fatalf("calls are not siblings of one execution: %+v", pair.Calls)
	}
	adapter.appendSteps(func(llm.Request) llm.Response { return agenttest.FinalResponse("done-sentinel") })
	if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "both-answer", Input: clientMutationInput("Postgres", nil, nil),
		ExpectedAskRevision: &pair.Revision, ExpectedAskBootGeneration: pair.BootGeneration,
	}); err != nil {
		t.Fatal(err)
	}
	if _, ran, err := s.ProcessPendingUserInput(ctx, nil); err != nil || !ran {
		t.Fatalf("ran=%v error=%v", ran, err)
	}
	turn := assertAskEvidence(t, s, "both-answer", pair.Calls, 0)
	if !reflect.DeepEqual(turn.AnswerToTurnIDs, []string{pair.Calls[0].TurnID}) {
		t.Fatalf("answer link = %v, want the single execution turn", turn.AnswerToTurnIDs)
	}

	items, _ := apptranscript.ProjectEntryParts(turn.TurnID, 0, turn, nil, nil, nil)
	if len(items) != 1 {
		t.Fatalf("answer projected %d items", len(items))
	}
	raw, err := json.Marshal(items[0])
	if err != nil {
		t.Fatal(err)
	}
	var projected struct {
		Observation map[string]json.RawMessage `json:"observation"`
	}
	if err := json.Unmarshal(raw, &projected); err != nil {
		t.Fatal(err)
	}
	encoded, ok := projected.Observation["resolvedAskCalls"]
	if !ok {
		t.Fatalf("answer observation carries no call-level resolution: %s", raw)
	}
	var got []appwire.PendingAskCall
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, pair.Calls) {
		t.Fatalf("resolvedAskCalls = %+v, want the admitted calls in order %+v", got, pair.Calls)
	}

	// This verifies the wire join: every serialized (turn, call) pair is a
	// pair the ask rows themselves project, so a consumer that joins on the
	// pair reaches a real row. The join is on the pair and not the call id
	// alone, because a row projected under a different turn than the stamp
	// would satisfy a call-id-only check and point at the wrong row. Which
	// consumers adopt the pair for recap selection is the recap follow-up,
	// not this test.
	projectedAskCalls := map[appwire.PendingAskCall]bool{}
	for _, entry := range answerLinkRecords(t, s) {
		if entry.Kind != schema.TurnAssistant {
			continue
		}
		askItems, _ := apptranscript.ProjectEntryParts(entry.TurnID, 0, entry, nil, nil, nil)
		for _, item := range askItems {
			if item.ToolName == "ask_user" {
				projectedAskCalls[appwire.PendingAskCall{TurnID: item.TurnID, CallID: item.CallID}] = true
			}
		}
	}
	for _, call := range got {
		if !projectedAskCalls[call] {
			t.Fatalf("resolved call %+v matches no projected ask row (rows=%v)", call, projectedAskCalls)
		}
	}
}
