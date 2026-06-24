package agent

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/llm"
)

// WS2a: terminal communicate returns to IDLE.
func TestSession_EndTurnResponseGoesIdle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		callID  string
		message string
		input   string
		desc    string
	}{
		{"question", "ask1", "What file would you like me to edit?", "hello", "question"},
		{"declarative", "msg1", "I have completed the task.", "do something", "declarative response"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := newSession(t,
				withSteps(func(req llm.Request) llm.Response {
					return toolCallResponse(communicateCallArgs(tc.callID, map[string]any{
						"end_turn": true,
						"message":  tc.message,
					}))
				}),
				withConfig(SessionConfig{}),
			)
			go func() {
				for range sess.Events() {
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := sess.ProcessInput(ctx, tc.input, nil)
			if err != nil {
				t.Fatalf("ProcessInput: %v", err)
			}

			if got := sess.State(); got != SessionIdle {
				t.Fatalf("state after %s: got %q want %q", tc.desc, got, SessionIdle)
			}
			sess.Close()
		})
	}
}

func TestSession_EndTurnQuestionAllowsNextInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()

	f := &fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return toolCallResponse(communicateCallArgs("ask2", map[string]any{
					"end_turn": true,
					"message":  "What language?",
				}))
			},
			func(req llm.Request) llm.Response {
				return toolCallResponse(communicateCallArgs("msg2", map[string]any{
					"end_turn": true,
					"message":  "Done writing Go code.",
				}))
			},
		},
	}
	c.Register(f)

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// First input: terminal question -> IDLE
	_, err = sess.ProcessInput(ctx, "write code", nil)
	if err != nil {
		t.Fatalf("ProcessInput #1: %v", err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after question: got %q want %q", got, SessionIdle)
	}

	// Second input: IDLE -> PROCESSING -> IDLE
	_, err = sess.ProcessInput(ctx, "Go", nil)
	if err != nil {
		t.Fatalf("ProcessInput #2: %v", err)
	}
	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after answer: got %q want %q", got, SessionIdle)
	}
	sess.Close()
}

// WS2b: MaxTurns → IDLE transition
func TestSession_MaxTurns_SetsStateToIdle(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("ok"), Finish: llm.FinishReason{Reason: "stop"}})
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{MaxTurns: 1})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	go func() {
		for range sess.Events() {
		}
	}()

	ctx := context.Background()
	// First input succeeds (turn 1 of 1).
	_, err = sess.ProcessInput(ctx, "first", nil)
	if err != nil {
		t.Fatalf("first input: %v", err)
	}

	// Second input hits the turn limit but returns nil error.
	_, err = sess.ProcessInput(ctx, "second", nil)
	if err != nil {
		t.Fatalf("turn limit should return nil error, got %v", err)
	}

	if got := sess.State(); got != SessionIdle {
		t.Fatalf("state after MaxTurns: got %q want %q", got, SessionIdle)
	}
	sess.Close()
}

// WS2c: SESSION_END after process_input
func TestSession_SessionEnd_AfterProcessInput(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	c := llm.NewClient()
	c.Register(&fakeAdapter{
		name: "openai",
		steps: []func(req llm.Request) llm.Response{
			func(req llm.Request) llm.Response {
				return wrapCommunicateResponse(llm.Response{Message: llm.Assistant("hello"), Finish: llm.FinishReason{Reason: "stop"}})
			},
		},
	})

	sess, err := NewSession(c, NewOpenAIProfile("gpt-5.2"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}

	var evs []events.SessionEvent
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range sess.Events() {
			evs = append(evs, ev)
		}
	}()

	ctx := context.Background()
	_, err = sess.ProcessInput(ctx, "hi", nil)
	if err != nil {
		t.Fatalf("ProcessInput: %v", err)
	}

	// After ProcessInput returns, exactly one SESSION_END with reason "input_complete" should be emitted.
	// Close() must not emit a second SESSION_END (dedup via sessionEndEmitted flag).
	sess.Close()
	<-done

	endCount := 0
	var inputCompleteEnd bool
	for _, ev := range evs {
		if d, ok := ev.Data.(events.SessionEndData); ok {
			endCount++
			if d.Reason == "input_complete" {
				inputCompleteEnd = true
			}
		}
	}
	if !inputCompleteEnd {
		t.Fatalf("expected SESSION_END with reason=input_complete")
	}
	if endCount != 1 {
		t.Fatalf("expected exactly 1 SESSION_END event, got %d", endCount)
	}
}
