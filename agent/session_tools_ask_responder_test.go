package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// TestParseAskUserCallArgumentsBatchForm checks that a batch-form ask_user
// call's raw arguments (as events.ToolCallStartData.ArgumentsJSON captures
// them, before normalizeAskArgs runs) parse into full questions, including
// each option's detail text — the piece askQuestion and PendingQuestion
// deliberately omit, per session_tools_ask.go's own doc comments.
func TestParseAskUserCallArgumentsBatchForm(t *testing.T) {
	raw := `{"questions":[{"header":"DB choice","question":"Which database?","options":[{"label":"Postgres","detail":"Battle-tested, relational"},{"label":"SQLite","detail":"Zero ops, embedded"}]}]}`
	got, err := ParseAskUserCallArguments([]byte(raw))
	if err != nil {
		t.Fatalf("ParseAskUserCallArguments: %v", err)
	}
	want := []AskUserQuestion{
		{
			Header:   "DB choice",
			Question: "Which database?",
			Options: []AskUserOption{
				{Label: "Postgres", Detail: "Battle-tested, relational"},
				{Label: "SQLite", Detail: "Zero ops, embedded"},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestParseAskUserCallArgumentsShorthandForm checks that the single-question
// shorthand (question+options at top level, no "questions" array) is
// normalized the same way the live tool call is before it parses — a model
// can use either form.
func TestParseAskUserCallArgumentsShorthandForm(t *testing.T) {
	raw := `{"question":"Ship today?","options":[{"label":"Yes","detail":"Ready now"},{"label":"No","detail":"Needs more time"}]}`
	got, err := ParseAskUserCallArguments([]byte(raw))
	if err != nil {
		t.Fatalf("ParseAskUserCallArguments: %v", err)
	}
	if len(got) != 1 || got[0].Question != "Ship today?" || len(got[0].Options) != 2 {
		t.Fatalf("got %+v", got)
	}
	if got[0].Options[0].Detail != "Ready now" {
		t.Fatalf("option detail lost: %+v", got[0].Options[0])
	}
}

// TestParseAskUserCallArgumentsInvalidJSON: malformed arguments are an
// error, not a silent empty result — a caller (the ask-responder loop) must
// be able to tell "nothing was pending" apart from "the call's own
// arguments were unparseable".
func TestParseAskUserCallArgumentsInvalidJSON(t *testing.T) {
	if _, err := ParseAskUserCallArguments([]byte("not json")); err == nil {
		t.Fatal("want an error for invalid JSON")
	}
}

// TestParseAskUserCallArgumentsSemanticViolation propagates
// normalizeAskArgs/parseAskQuestions-style semantic errors (duplicate
// labels), the same rule the live Exec enforces.
func TestParseAskUserCallArgumentsSemanticViolation(t *testing.T) {
	raw := `{"questions":[{"question":"Which?","options":[{"label":"A","detail":"x"},{"label":"A","detail":"y"}]}]}`
	if _, err := ParseAskUserCallArguments([]byte(raw)); err == nil {
		t.Fatal("want an error for duplicate option labels")
	}
}

// TestPendingAskArguments_AvailableWithNoEventConsumer proves an external
// ask-responder (evener run --ask-responder) can read the full pending
// ask_user questions from durable session state, with NOBODY ever reading
// Session.Events() — the event channel is best-effort (session_events.go:
// "a full buffer drops"), so a caller for whom missing one would leave state
// permanently wrong (a real pending question the responder never sees) must
// not depend on it. registerAskTool's Exec records each call's own
// arguments into this state the moment it runs, independent of whether
// anything ever drains the event stream.
func TestPendingAskArguments_AvailableWithNoEventConsumer(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
		},
	}
	sess := newSession(t, withAdapter(f))
	// No goroutine, no select, nothing: sess.Events() is never called.

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "pick a db", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	pending := sess.PendingAskArguments()
	if len(pending) != 1 {
		t.Fatalf("PendingAskArguments = %d entries, want 1", len(pending))
	}
	questions, err := ParseAskUserCallArguments(pending[0])
	if err != nil {
		t.Fatalf("ParseAskUserCallArguments(PendingAskArguments()[0]): %v", err)
	}
	if len(questions) != 1 || questions[0].Question != "Which datastore for the ingest path?" {
		t.Fatalf("questions = %+v", questions)
	}
	if len(questions[0].Options) != 2 || questions[0].Options[0].Detail == "" {
		t.Fatalf("option detail lost: %+v", questions[0].Options)
	}
}

// TestPendingAskArguments_OneEntryPerCallInOrder: two ask_user calls in one
// round record two entries, in call order, each parseable to its own
// question — a responder answering the whole round needs every call's
// questions, not just the first.
func TestPendingAskArguments_OneEntryPerCallInOrder(t *testing.T) {
	t.Parallel()
	ask1 := askUserCall("ask1", askUserArgsValid())
	ask2 := askUserCall("ask2", askUserArgsTwoQuestions())
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask1, ask2) },
		},
	}
	sess := newSession(t, withAdapter(f))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "pick a db and a name", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	pending := sess.PendingAskArguments()
	if len(pending) != 2 {
		t.Fatalf("PendingAskArguments = %d entries, want 2", len(pending))
	}
	q1, err := ParseAskUserCallArguments(pending[0])
	if err != nil || len(q1) != 1 {
		t.Fatalf("first entry = %+v, %v", q1, err)
	}
	q2, err := ParseAskUserCallArguments(pending[1])
	if err != nil || len(q2) != 2 {
		t.Fatalf("second entry = %+v, %v", q2, err)
	}
}

// TestPendingAskArguments_ClearedWithAskPending: answering the question
// clears PendingAskArguments the same moment it clears askPending, so a
// caller reading it after the reply never sees stale questions.
func TestPendingAskArguments_ClearedWithAskPending(t *testing.T) {
	t.Parallel()
	ask := askUserCall("ask1", askUserArgsValid())
	comm := communicateCall("c1", "Using Postgres.")
	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask) },
			func(req llm.Request) llm.Response { return toolCallResponse(comm) },
		},
	}
	sess := newSession(t, withAdapter(f))

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "pick a db", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if len(sess.PendingAskArguments()) != 1 {
		t.Fatal("want the pending call recorded before the reply")
	}
	if _, err := sess.ProcessInput(ctx, "Postgres", nil); err != nil {
		t.Fatalf("ProcessInput (reply): %v", err)
	}
	if got := sess.PendingAskArguments(); len(got) != 0 {
		t.Fatalf("PendingAskArguments after the reply = %+v, want empty", got)
	}
}
