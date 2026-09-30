package agent

import (
	"context"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/appwire"
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

// TestParseAskUserCallArgumentsCarriesOptionalFields: a responder answers
// on the user's behalf, so it needs everything the model put in the call:
// whether several options may be chosen, the model's recommendation, why the
// answer matters, and the fallback it would take unanswered.
func TestParseAskUserCallArgumentsCarriesOptionalFields(t *testing.T) {
	raw := `{"questions":[{"question":"Which regions?","multi_select":true,"why":"Sets where we deploy.","if_unanswered":"Deploy to us-east only.","options":[{"label":"us-east","detail":"Primary","recommended":true},{"label":"eu-west","detail":"Secondary"}]}]}`
	got, err := ParseAskUserCallArguments([]byte(raw))
	if err != nil {
		t.Fatalf("ParseAskUserCallArguments: %v", err)
	}
	want := []AskUserQuestion{
		{
			Question:     "Which regions?",
			MultiSelect:  true,
			Why:          "Sets where we deploy.",
			IfUnanswered: "Deploy to us-east only.",
			Options: []AskUserOption{
				{Label: "us-east", Detail: "Primary", Recommended: true},
				{Label: "eu-west", Detail: "Secondary"},
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

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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

// TestPendingAskArguments_ClearedByAnsweringSteeringCarrierNotStale:
// processOneInput's steering-carrier entry clear (session_lifecycle.go)
// used to assign s.askPending = nil directly, bypassing the shared
// setAskPendingLocked/clearAskPending path — leaving askPendingCallArgs
// holding the PREVIOUS call's arguments after the clear. Driven through
// the real steering-carrier entry point (AcceptClientMutationSteer,
// claimSteeringCarrierTurn, acceptSteeringCarrierInput — the same
// production path TestAskUser_RestoreResolvesAcrossUserSteer drives, minus
// its restore/assert tail), a new ask_user call the carrier's own turn
// posts must not sit alongside that stale entry.
func TestPendingAskArguments_ClearedByAnsweringSteeringCarrierNotStale(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ask1 := askUserCall("ask1", askUserArgsValid())
	ask2 := askUserCall("ask2", askUserArgsTwoQuestions())
	c := llm.NewClient()
	adapter := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return toolCallResponse(ask1) },
			func(req llm.Request) llm.Response { return toolCallResponse(ask2) },
		},
	}
	c.Register(adapter)
	sess, err := NewSession(c, withTestSessionNamer(c, NewOpenAIProfile("gpt-5.2")), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{StateDir: dir})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer sess.Close()

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "which db should we use?", nil); err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}
	if got := len(sess.PendingAskArguments()); got != 1 {
		t.Fatalf("pre-carrier PendingAskArguments = %d, want 1 (test setup broken)", got)
	}

	if err := sess.ensureClientMutationStore(); err != nil {
		t.Fatalf("ensureClientMutationStore: %v", err)
	}
	if _, err := sess.AcceptClientMutationSteer(appwire.TurnSteerParams{
		ClientMutationID: "steer-1",
		Input:            clientMutationInput("please hold", nil, nil),
	}); err != nil {
		t.Fatalf("AcceptClientMutationSteer: %v", err)
	}
	// Drive processOneInput's own steering-carrier entry clear directly: a
	// context carrying "steer-1"'s identity (now a real journal record, so
	// steeringCarrierClaimAnswersAsk resolves it as answering, not failing
	// closed on an unknown id) through ProcessInputKind, the same context
	// shape ProcessPendingUserInput/acceptUserInputWithSkillSelection build
	// for a claimed carrier (session_client_mutation_queue.go's
	// withQueuedClientMutation), without those callers' own additional
	// clearing paths in between.
	carrierCtx := withQueuedClientMutation(ctx, queuedInput{ClientMutationID: "steer-1", SteeringCarrier: true})
	if _, err := sess.ProcessInputKind(carrierCtx, "", nil, EntryUserInput); err != nil {
		t.Fatalf("ProcessInputKind (carrier turn): %v", err)
	}
	if got := len(adapter.Requests()); got != 2 {
		t.Fatalf("provider saw %d requests, want 2 (test setup broken)", got)
	}
	pending := sess.PendingAskArguments()
	if len(pending) != 1 {
		t.Fatalf("PendingAskArguments after the new ask_user call = %d entries, want 1 (only the new call, no stale leftover)", len(pending))
	}
	questions, err := ParseAskUserCallArguments(pending[0])
	if err != nil || len(questions) != 2 {
		t.Fatalf("new call's questions = %+v, %v, want the two-question call (askUserArgsTwoQuestions)", questions, err)
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

	// TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
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

// TestSetAskPendingLockedAlwaysClearsCallArgs pins setAskPendingLocked's own
// invariant directly, isolated from any masking a later admission clear in
// a full round might otherwise apply: every REPLACEMENT of the pending-ask
// set — a clear (nil), or a restore re-derivation's rebuilt []askQuestion —
// must also clear askPendingCallArgs. A rebuilt slice carries parsed
// questions, never the calls' original JSON, so a live-only cache that no
// longer matches what is pending is worse than an absent one. This is the
// exact property session_lifecycle.go's steering-carrier entry clear and
// session_state.go's restored-failure boundary used to violate by assigning
// s.askPending directly.
func TestSetAskPendingLockedAlwaysClearsCallArgs(t *testing.T) {
	t.Parallel()
	sess := newAskTestSession(t, SessionConfig{})
	res := sess.reg.ExecuteCall(context.Background(), sess.env, askUserCall("c1", askUserArgsValid()))
	if res.IsError {
		t.Fatalf("ask_user call errored: %s", res.Output)
	}
	if got := len(sess.PendingAskArguments()); got != 1 {
		t.Fatalf("PendingAskArguments = %d, want 1 (test setup broken)", got)
	}

	sess.mu.Lock()
	sess.setAskPendingLocked([]askQuestion{{Question: "rebuilt from history, no raw args"}})
	sess.mu.Unlock()

	if got := len(sess.PendingAskArguments()); got != 0 {
		t.Fatalf("PendingAskArguments after setAskPendingLocked = %d entries, want 0 (stale call args from the previous call)", got)
	}
	if got := sess.askPendingCount(); got != 1 {
		t.Fatalf("askPendingCount after setAskPendingLocked = %d, want 1 (the rebuilt question)", got)
	}
}
