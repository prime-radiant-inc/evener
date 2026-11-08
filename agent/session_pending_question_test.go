package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// The first question of the pending ask names the Needs you row (S1b): its
// text, its option labels in the order the call listed them, and how many
// questions wait in all. A session with nothing pending reports none, and the
// answer clears it.
func TestPendingQuestion_NamesTheFirstQuestionOfThePendingAsk(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})
	if got := sess.PendingQuestion(); got != nil {
		t.Fatalf("a session with no ask reports %+v", got)
	}
	for _, call := range []llm.ToolCallData{askUserCall("c1", askUserArgsValid()), askUserCall("c2", askUserArgsTwoQuestions())} {
		if res := sess.reg.ExecuteCall(context.Background(), sess.env, call); res.IsError {
			t.Fatalf("ask_user %s errored: %s", call.ID, res.Output)
		}
	}
	want := &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Options: []string{"Postgres", "SQLite"}, Count: 3, Complete: new(false), Truncated: new(false)}
	if got := sess.PendingQuestion(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PendingQuestion = %+v, want %+v", got, want)
	}
	sess.clearAskPending()
	if got := sess.PendingQuestion(); got != nil {
		t.Fatalf("after the answer PendingQuestion = %+v, want none", got)
	}
}

// A question leaves the session cut to the wire's bounds
// (appwire.BoundedPendingQuestion): a long, many-line question is one line.
func TestPendingQuestion_LeavesTheSessionBounded(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})
	sess.mu.Lock()
	sess.askPending = []askQuestion{{Question: "Keep or drop\nthe implied options?\n\n" + strings.Repeat("Fourteen descriptions mention flags. ", 20)}}
	sess.mu.Unlock()
	got := sess.PendingQuestion()
	if got == nil || !strings.HasPrefix(got.Question, "Keep or drop the implied options? Fourteen") ||
		utf8.RuneCountInString(got.Question) > appwire.MaxQuestionTextRunes {
		t.Fatalf("PendingQuestion = %+v, want one line cut to %d runes", got, appwire.MaxQuestionTextRunes)
	}
}

// A restored session names the same question the live one did: restore
// rebuilds the pending set through the parse the live call used (S1b).
func TestPendingQuestion_SurvivesRestore(t *testing.T) {
	t.Parallel()
	// The durable-admission suite's harness: one live ask_user round
	// (askUserArgsValid) in a session with a state dir.
	sess := newDurableAdmissionAskSession(t, SessionConfig{})
	seedDurableAdmissionAsk(t, sess)
	want := &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Options: []string{"Postgres", "SQLite"}, Count: 1, Complete: new(false), Truncated: new(false)}
	for _, entry := range readPersistedTranscriptEntries(t, sess.stateDir, sess.ID()) {
		if entry.Turn.Kind != schema.TurnAssistant {
			continue
		}
		for _, call := range assistantToolCalls(entry.Turn.Message) {
			if call.Name == "ask_user" && call.ID == "ask1" {
				if want.TurnID != "" {
					t.Fatal("ask has multiple recorded executions")
				}
				want.TurnID, want.CallID = entry.Turn.TurnID, call.ID
			}
		}
	}
	if want.TurnID == "" || want.CallID == "" {
		t.Fatal("ask has no recorded execution identity")
	}
	if got := sess.PendingQuestion(); !reflect.DeepEqual(got, want) {
		t.Fatalf("live PendingQuestion = %+v, want %+v", got, want)
	}
	meta, dir := sess.Meta(), sess.stateDir
	sess.Close()

	restored := restoreAskSession(t, meta, dir)
	got := restored.PendingQuestion()
	if got != nil && (got.Complete != nil || got.Truncated != nil) {
		t.Errorf("restored truth fields must be absent: complete=%v truncated=%v", got.Complete, got.Truncated)
	}
	restoredWant := *want
	restoredWant.Complete, restoredWant.Truncated = nil, nil
	if !reflect.DeepEqual(got, &restoredWant) {
		t.Fatalf("restored PendingQuestion = %+v, want %+v", got, &restoredWant)
	}
}

func TestQuestionsFromAskCallsOutsideHistory(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		history []schema.Turn
		index   int
	}{
		{name: "at-end", history: []schema.Turn{{Kind: schema.TurnUserInput}}, index: 1},
		{name: "empty", index: 0},
		{name: "before-start", history: []schema.Turn{{Kind: schema.TurnUserInput}}, index: -1},
		{name: "past-end", history: []schema.Turn{{Kind: schema.TurnUserInput}}, index: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := questionsFromAskCalls(tc.history, tc.index, map[string]bool{"x": true}); got != nil {
				t.Fatalf("questions from absent result: %+v", got)
			}
		})
	}
}
