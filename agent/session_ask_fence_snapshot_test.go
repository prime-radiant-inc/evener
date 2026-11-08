package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
)

func TestAskFenceSnapshotMetadataUsesFullLiveArguments(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                string
		mutate              func(*schema.AskUserQuestion)
		multiple            bool
		complete, truncated bool
	}{
		{name: "complete", complete: true},
		{name: "detail", mutate: func(q *schema.AskUserQuestion) { q.Options[0].Detail = "decision detail" }},
		{name: "recommended", mutate: func(q *schema.AskUserQuestion) { q.Options[0].Recommended = true }},
		{name: "multi_select", mutate: func(q *schema.AskUserQuestion) { q.MultiSelect = true }},
		{name: "why", mutate: func(q *schema.AskUserQuestion) { q.Why = "decision reason" }},
		{name: "if_unanswered", mutate: func(q *schema.AskUserQuestion) { q.IfUnanswered = "fallback" }},
		{name: "two_questions", multiple: true},
		{name: "question_cut", mutate: func(q *schema.AskUserQuestion) { q.Question = strings.Repeat("界", 201) }, truncated: true},
		{name: "label_cut", mutate: func(q *schema.AskUserQuestion) { q.Options[0].Label = strings.Repeat("界", 81) }, truncated: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestSession(t)
			armLiveAskExecution(s)
			s.SetBootGeneration("snapshot-boot")
			q := schema.AskUserQuestion{Question: "question", Options: []schema.AskUserOption{{Label: "one"}, {Label: "two"}}}
			if tc.mutate != nil {
				tc.mutate(&q)
			}
			questions := []schema.AskUserQuestion{q}
			if tc.multiple {
				questions = append(questions, q)
			}
			args, err := json.Marshal(struct {
				Questions []schema.AskUserQuestion `json:"questions"`
			}{questions})
			if err != nil {
				t.Fatal(err)
			}
			var input map[string]any
			if err := json.Unmarshal(args, &input); err != nil {
				t.Fatal(err)
			}
			// Input requires detail even when empty, unlike persisted state.
			for i, rawQuestion := range input["questions"].([]any) {
				for j, option := range rawQuestion.(map[string]any)["options"].([]any) {
					option.(map[string]any)["detail"] = questions[i].Options[j].Detail
				}
			}
			res := s.reg.ExecuteCall(context.WithValue(tripwireCtx(t), ctxToolCallID, "displayed-call"), s.env, askUserCall("displayed-call", input))
			if res.IsError {
				t.Fatalf("fixture invalid %+v", res)
			}
			excerpt, state := s.PendingAskSnapshot()
			if excerpt == nil || excerpt.Complete == nil || excerpt.Truncated == nil || *excerpt.Complete != tc.complete || *excerpt.Truncated != tc.truncated || excerpt.TurnID != "turn-live" || excerpt.CallID != "displayed-call" || excerpt.Count != len(questions) || state.BootGeneration != "snapshot-boot" || state.CompositeTotal != 1 || state.CalllessCount != 0 {
				t.Fatalf("excerpt=%+v state=%+v", excerpt, state)
			}
			*excerpt.Complete = !*excerpt.Complete
			*excerpt.Truncated = !*excerpt.Truncated
			excerpt.Options[0] = "mutated"
			state.Calls[0].CallID = "mutated"
			again, calls := s.PendingAskSnapshot()
			if *again.Complete != tc.complete || *again.Truncated != tc.truncated || again.Options[0] == "mutated" || calls.Calls[0].CallID != "displayed-call" {
				t.Fatal("snapshot aliases session")
			}
		})
	}
}

func TestAskFenceSnapshotExhaustiveAndDisplayedLegacyHasNoBorrowedIdentity(t *testing.T) {
	t.Parallel()
	s := newTestSession(t)
	s.SetBootGeneration("current")
	composites := appwire.MaxPendingAskComposites + 1
	s.mu.Lock()
	s.setAskPendingLocked([]askQuestion{{Question: "first legacy"}})
	for i := range composites {
		s.execution.turnID = fmt.Sprintf("turn-%d", i)
		s.appendAskPendingLocked([]askQuestion{{Question: "named"}}, []byte(`{"questions":[{"question":"named"}]}`), fmt.Sprintf("call-%d", i))
	}
	s.mu.Unlock()
	excerpt, state := s.PendingAskSnapshot()
	if excerpt.TurnID != "" || excerpt.CallID != "" || excerpt.Complete != nil || excerpt.Truncated != nil || excerpt.Count != composites+1 || state.CompositeTotal != composites || len(state.Calls) != composites || state.CalllessCount != 1 || state.CallsTruncated {
		t.Fatalf("owner clamped or borrowed displayed identity excerpt=%+v state=%+v", excerpt, state)
	}
	capture, err := captureAskFence(excerpt, state, &state.Revision, state.BootGeneration)
	if err != nil || len(capture.Resolution.Calls) != composites || capture.Resolution.LegacyCount != 1 {
		t.Fatalf("capture=%+v error=%v", capture, err)
	}
}

func TestAskFenceSnapshotRestoreOmitsLiveOnlyTruthFlags(t *testing.T) {
	t.Parallel()
	s := newDurableAdmissionAskSession(t, SessionConfig{})
	seedDurableAdmissionAsk(t, s)
	live, _ := s.PendingAskSnapshot()
	if live.Complete == nil || live.Truncated == nil {
		t.Fatal("live flags absent")
	}
	meta, dir := s.Meta(), s.stateDir
	s.Close()
	restored := restoreAskSession(t, meta, dir)
	question, state := restored.PendingAskSnapshot()
	if question == nil || question.Complete != nil || question.Truncated != nil || question.TurnID != live.TurnID || question.CallID != live.CallID || state.CompositeTotal != 1 {
		t.Fatalf("restored truth flags fabricated question=%+v state=%+v", question, state)
	}
}
