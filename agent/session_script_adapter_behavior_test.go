package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

func textResponse(text string) llm.Response {
	return llm.Response{Message: llm.Assistant(text)}
}

// scriptedText reports with Errorf, not Fatalf, because tests also call it
// from goroutines of their own.
func scriptedText(t *testing.T, a *sessionScriptAdapter, sessionID string) string {
	t.Helper()
	resp, err := a.Complete(context.Background(), llm.Request{SessionID: sessionID, Model: "m"})
	if err != nil {
		t.Errorf("Complete(%s): %v", sessionID, err)
	}
	if resp.Provider != "openai" || resp.Model != "m" {
		t.Errorf("Complete(%s) = provider %q model %q, want them filled in", sessionID, resp.Provider, resp.Model)
	}
	return resp.Message.Text()
}

// Each session walks its own script in order, however the calls interleave;
// a session with no script left, or none, gets otherwise.
func TestSessionScriptAdapterKeepsAScriptPerSession(t *testing.T) {
	t.Parallel()
	a := newSessionScriptAdapter("openai").
		script("parent", answerStep(textResponse("p1")), answerStep(textResponse("p2"))).
		script("child", answerStep(textResponse("c1")))
	a.otherwise = answerStep(textResponse("other"))
	for _, call := range []struct{ session, want string }{
		{"child", "c1"},
		{"parent", "p1"},
		{"child", "other"},
		{"parent", "p2"},
		{"stranger", "other"},
		{"parent", "other"},
	} {
		if got := scriptedText(t, a, call.session); got != call.want {
			t.Fatalf("%s call = %q, want %q", call.session, got, call.want)
		}
	}
	if got := scriptedText(t, newSessionScriptAdapter("openai"), "parent"); got != "done" {
		t.Fatalf("call with nothing scripted = %q, want done", got)
	}
}

// A step runs with the adapter unlocked: while one session's call is
// blocked in its step, another session's call completes.
func TestSessionScriptAdapterHoldsNoLockDuringAStep(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	a := newSessionScriptAdapter("openai").
		script("child", func(context.Context, llm.Request) (llm.Response, error) {
			close(entered)
			<-release
			return textResponse("child"), nil
		}).
		script("parent", answerStep(textResponse("parent")))
	childDone := make(chan string, 1)
	go func() { childDone <- scriptedText(t, a, "child") }()
	<-entered
	parentDone := make(chan string, 1)
	go func() { parentDone <- scriptedText(t, a, "parent") }()
	select {
	case got := <-parentDone:
		if got != "parent" {
			t.Fatalf("parent call = %q, want parent", got)
		}
	// TRIPWIRE: an unlocked call returns at once; only a held lock waits.
	case <-time.After(30 * time.Second):
		t.Fatal("the parent's call waited on the child's blocked step")
	}
	close(release)
	if got := <-childDone; got != "child" {
		t.Fatalf("child call = %q, want child", got)
	}
}

// A step sees its call's context: cancelling the call ends a step that waits
// on it, and Complete returns the context's error.
func TestSessionScriptAdapterPassesTheCallsContext(t *testing.T) {
	t.Parallel()
	a := newSessionScriptAdapter("openai").script("child", awaitCancelStep)
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := a.Complete(ctx, llm.Request{SessionID: "child"})
		errs <- err
	}()
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Complete after cancel = %v, want context.Canceled", err)
		}
	// TRIPWIRE: a cancelled step returns at once.
	case <-time.After(30 * time.Second):
		t.Fatal("cancelling the call did not end its step")
	}
}
