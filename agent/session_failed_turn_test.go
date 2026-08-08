package agent

import (
	"context"
	"testing"
	"time"

	"github.com/spf13/afero"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/llm"
)

// transcriptFailureTurns returns every TurnFailure entry recorded in a
// session's transcript.
func transcriptFailureTurns(t *testing.T, path string) []schema.Turn {
	t.Helper()
	data, err := readTranscriptFull(path)
	if err != nil {
		t.Fatalf("readTranscriptFull: %v", err)
	}
	var out []schema.Turn
	for _, entry := range data.Entries {
		if entry.Turn.Kind == schema.TurnFailure {
			out = append(out, entry.Turn)
		}
	}
	return out
}

// kata mcgh: a terminal provider failure must be written to the transcript,
// not merely broadcast live. Before this, the transcript stopped at the
// USER_INPUT entry and a reload showed a prompt with no answer and no error —
// indistinguishable from a hang.
func TestProviderErrorPersistsFailedTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&streamingAdapter{
		name:      "openai",
		streamErr: llm.ErrorFromHTTPStatus("openai", 403, "access denied", nil, nil),
	})

	policy := llm.RetryPolicy{MaxRetries: 0}
	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir:       dir,
		LLMRetryPolicy: &policy,
		testOnly:       testConfig{metaFS: afero.NewMemMapFs()},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err == nil {
		t.Fatal("expected provider error from ProcessInput")
	}
	tpath := sess.TranscriptPath()
	sess.Close()

	failures := transcriptFailureTurns(t, tpath)
	if len(failures) != 1 {
		t.Fatalf("TurnFailure entries: got %d, want 1", len(failures))
	}
	got := failures[0]
	if got.Error == nil {
		t.Fatal("TurnFailure entry carries no Error diagnostic")
	}
	if got.Error.Message == "" {
		t.Error("TurnFailure diagnostic has an empty Message")
	}
	// The human-readable text also rides the message so every renderer that
	// only reads turn text still shows the failure.
	if got.Message.Text() != got.Error.Message {
		t.Errorf("turn text = %q, want it to match the diagnostic message %q", got.Message.Text(), got.Error.Message)
	}
	if got.Error.Cause == nil {
		t.Fatal("TurnFailure diagnostic has no structured Cause; the live error event carries one")
	}
	if want := "provider"; got.Error.Cause.Kind != want {
		t.Errorf("Cause.Kind = %q, want %q", got.Error.Cause.Kind, want)
	}
	if want := "openai"; got.Error.Cause.Provider != want {
		t.Errorf("Cause.Provider = %q, want %q", got.Error.Cause.Provider, want)
	}
	if want := "gpt-5.2"; got.Error.Cause.Model != want {
		t.Errorf("Cause.Model = %q, want %q", got.Error.Cause.Model, want)
	}
	if want := 403; got.Error.Cause.Status != want {
		t.Errorf("Cause.Status = %d, want %d", got.Error.Cause.Status, want)
	}
}

// A user cancellation is not a failure. The live projector deliberately routes
// it to a warning and lets the interrupted SessionEnd own the turn's terminal
// state, so the transcript must not claim the turn broke either.
func TestCancelledTurnPersistsNoFailedTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response { return finalResponse("hi back") },
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{
		StateDir: dir,
		testOnly: testConfig{metaFS: afero.NewMemMapFs()},
	})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := sess.ProcessInput(ctx, "hi", nil); err != nil {
		t.Fatalf("happy turn: %v", err)
	}

	cancelledCtx, cancelNow := context.WithCancel(context.Background())
	cancelNow()
	if _, err := sess.ProcessInput(cancelledCtx, "again", nil); err == nil {
		t.Fatal("expected context.Canceled from ProcessInput")
	}
	tpath := sess.TranscriptPath()
	sess.Close()

	if failures := transcriptFailureTurns(t, tpath); len(failures) != 0 {
		t.Fatalf("cancellation recorded %d TurnFailure entries, want 0: %+v", len(failures), failures)
	}
}

// A persisted failure is presentational. It must never be replayed to the
// model as conversation, exactly like the TurnModelSwitch marker.
func TestExpandHistoryDropsFailedTurns(t *testing.T) {
	t.Parallel()
	history := []schema.Turn{
		schema.NewTurn(schema.TurnUserInput, llm.User("hi")),
		{
			Kind:    schema.TurnFailure,
			Message: llm.System("provider error: access denied"),
			Error:   &schema.TurnFailureInfo{Message: "provider error: access denied"},
		},
		schema.NewTurn(schema.TurnUserInput, llm.User("still there?")),
	}

	msgs := expandHistory(history, replayScope{})

	if len(msgs) != 2 {
		t.Fatalf("expandHistory produced %d messages, want 2 (the failure marker must be dropped)", len(msgs))
	}
	for _, m := range msgs {
		if m.Text() == "provider error: access denied" {
			t.Fatal("expandHistory sent the failure marker to the model")
		}
	}
}
