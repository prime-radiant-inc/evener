package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/llm"
)

// failureJSON is a failure summary as the wire carries it.
func failureJSON(t *testing.T, failure *appwire.ThreadFailure) string {
	t.Helper()
	raw, err := json.Marshal(failure)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// A session resting on a failed turn summarizes it for its row (S1c): the
// failure's headline and its structured cause, and never its message ("sign-in
// rejected" here), which can quote a provider's error body. The next turn to
// start clears it.
func TestRestingFailure_SummarizesTheFailedTurnUntilTheNextTurn(t *testing.T) {
	t.Parallel()
	sess := failingThenRecoveringSession(t, t.TempDir())
	defer sess.Close()
	if got := sess.RestingFailure(); got != nil {
		t.Fatalf("a session that has not failed rests on %s", failureJSON(t, got))
	}
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	want := `{"title":"Provider error","cause":{"kind":"provider","provider":"openai","model":"test-model","status":403}}`
	if got := failureJSON(t, sess.RestingFailure()); got != want {
		t.Fatalf("RestingFailure = %s, want %s", got, want)
	}
	if _, err := sess.ProcessInput(ctx, "second", nil); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	if got := sess.RestingFailure(); got != nil {
		t.Fatalf("after the next clean turn RestingFailure = %s, want none", failureJSON(t, got))
	}
}

// A question still pending outranks the failure: the session reads awaiting on
// the wire, and its row names the question, not the failure (S1c).
func TestRestingFailure_NoneWhileAQuestionWaits(t *testing.T) {
	t.Parallel()
	sess := failedCarrierWithPendingQuestion(t, t.TempDir(), SessionConfig{})
	defer sess.Close()
	if got := sess.RestingFailure(); got != nil {
		t.Fatalf("RestingFailure with a pending question = %s, want none", failureJSON(t, got))
	}
}

// A daemon restarted after a failed turn summarizes the failure the live
// session did: restore rebuilds the history the summary reads (S1c).
func TestRestingFailure_SurvivesRestore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	sess := failingThenRecoveringSession(t, dir)
	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "first", nil); err == nil {
		t.Fatal("first turn succeeded, want the scripted provider failure")
	}
	live := sess.RestingFailure()
	if live == nil {
		t.Fatal("the live session summarizes no failure")
	}
	restored, _ := restoreClosedSession(t, sess, dir, "test-model")
	if got, want := failureJSON(t, restored.RestingFailure()), failureJSON(t, live); got != want {
		t.Fatalf("restored RestingFailure = %s, want the live %s", got, want)
	}
}

// A failure recorded with no diagnostic (a legacy entry) still reads Failed on
// the wire, and has nothing to summarize.
func TestRestingFailure_NoneForAFailureWithNoDiagnostic(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	sess.mu.Lock()
	sess.history = append(sess.history, schema.NewTurn(schema.TurnUserInput, llm.User("go")), schema.NewTurn(schema.TurnFailure, llm.System("it broke")))
	sess.mu.Unlock()
	if got := sess.RestingWireState(); got != appwire.ThreadStatusSystemError {
		t.Fatalf("RestingWireState = %q, want %q", got, appwire.ThreadStatusSystemError)
	}
	if got := sess.RestingFailure(); got != nil {
		t.Fatalf("RestingFailure = %s, want none: the failure recorded no diagnostic", failureJSON(t, got))
	}
}
