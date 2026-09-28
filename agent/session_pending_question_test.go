package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"primeradiant.com/evener/agent/execenv"
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
	want := &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Options: []string{"Postgres", "SQLite"}, Count: 3}
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
	want := &appwire.PendingQuestion{Question: "Which datastore for the ingest path?", Options: []string{"Postgres", "SQLite"}, Count: 1}
	if got := sess.PendingQuestion(); !reflect.DeepEqual(got, want) {
		t.Fatalf("live PendingQuestion = %+v, want %+v", got, want)
	}
	meta, dir := sess.Meta(), sess.stateDir
	sess.Close()

	restored, err := RestoreSessionFromMeta(newAskRestoreClient(), NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), meta, dir)
	if err != nil {
		t.Fatalf("RestoreSessionFromMeta: %v", err)
	}
	defer restored.Close()
	if got := restored.PendingQuestion(); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored PendingQuestion = %+v, want %+v", got, want)
	}
}
