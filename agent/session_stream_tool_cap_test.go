package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/llm"
)

// sendToolCalls streams n complete tool calls, each with a distinct id and
// minimal arguments. Two events per call exercises both the start and end
// accumulation paths.
func sendToolCalls(st *llm.ChanStream, n int, name string) {
	for i := range n {
		id := fmt.Sprintf("call_%d", i)
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: id, Name: name}})
		st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallData{ID: id, Name: name, Arguments: []byte(`{}`)}})
	}
}

// finishToolCallStream closes a successful response with a tool-calls finish.
func finishToolCallStream(st *llm.ChanStream) {
	finish := llm.FinishReason{Reason: llm.FinishReasonToolCalls}
	st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
}

// TestConsumeModelStream_ToolCallCap_AbortsRunawayResponse: the 101st tool call
// in one streamed response ends the attempt before the provider finishes, with
// an error that is neither retryable nor model-fallback-eligible. The scripted
// stream would otherwise finish cleanly, so the sentinel can only come from the
// cap.
func TestConsumeModelStream_ToolCallCap_AbortsRunawayResponse(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}

	st := llm.NewChanStream(nil)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer st.CloseSend()
		sendToolCalls(st, maxToolCallsPerResponse+1, "capped_probe")
		// Never reached by the consumer: if the cap did not fire, this text and
		// the finish would make the runaway a successful response.
		st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: "after-the-cap"})
		finishToolCallStream(st)
	}()

	_, _, err := sess.consumeModelStream(context.Background(), req, st)
	<-producerDone

	if _, ok := errors.AsType[*responseToolCallLimitError](err); !ok {
		t.Fatalf("err = %v (%T), want *responseToolCallLimitError", err, err)
	}
	if got := llm.Classify(err); got != llm.ErrorClassPermanent {
		t.Fatalf("llm.Classify(err) = %v, want %v (the retry chain must not replay the runaway)", got, llm.ErrorClassPermanent)
	}
	if modelFallbackEligible(err, llm.DefaultRetryPolicy()) {
		t.Fatal("modelFallbackEligible = true, want false: a fallback would replay the runaway request")
	}
}

// TestConsumeModelStream_ToolCallCap_AtLimitSucceeds keeps the boundary honest:
// exactly maxToolCallsPerResponse tool calls is a legitimate response.
func TestConsumeModelStream_ToolCallCap_AtLimitSucceeds(t *testing.T) {
	t.Parallel()
	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}

	st := llm.NewChanStream(nil)
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		defer st.CloseSend()
		sendToolCalls(st, maxToolCallsPerResponse, "ok_tool")
		finishToolCallStream(st)
	}()

	resp, _, err := sess.consumeModelStream(context.Background(), req, st)
	<-producerDone

	if err != nil {
		t.Fatalf("err = %v, want nil for a response at the tool-call limit", err)
	}
	if got := len(resp.Response.ToolCalls()); got != maxToolCallsPerResponse {
		t.Fatalf("response tool calls = %d, want %d", got, maxToolCallsPerResponse)
	}
}

// TestProcessInput_ToolCallCap_DispatchesNothingAndDoesNotFallBack drives the
// cap through a real turn: a runaway primary response must execute none of its
// tool calls, must not be retried, and must not be replayed against the
// configured fallback model.
func TestProcessInput_ToolCallCap_DispatchesNothingAndDoesNotFallBack(t *testing.T) {
	t.Parallel()
	a := &scriptedStreamAdapter{
		provider: "openai",
		script: map[string]func(*llm.ChanStream){
			"primary": func(st *llm.ChanStream) {
				sendToolCalls(st, maxToolCallsPerResponse+1, "capped_probe")
				finishToolCallStream(st)
			},
			"fallback-b": func(st *llm.ChanStream) {
				st.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "t0", Delta: "fallback answered"})
				finish := llm.FinishReason{Reason: llm.FinishReasonStop}
				st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
			},
		},
	}
	policy := llm.RetryPolicy{MaxRetries: 10, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	sess := newSession(t,
		withAdapter(a),
		withProfile(NewOpenAIProfile("primary")),
		withConfig(SessionConfig{
			LLMRetryPolicy: &policy,
			LLMSleep:       func(context.Context, time.Duration) error { return nil },
			ModelFallbacks: []string{"fallback-b"},
		}),
	)
	dispatched := 0
	sess.registerTool("capped_probe", "test-only: counts dispatches", map[string]any{"type": "object"},
		func(context.Context, any) (any, error) {
			dispatched++
			return "ok", nil
		})
	drainSessionEvents(sess)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: scripted in-process adapter, no real I/O; only fires on a genuine hang.
	defer cancel()
	_, err := sess.ProcessInput(ctx, "go", nil)

	if _, ok := errors.AsType[*responseToolCallLimitError](err); !ok {
		t.Fatalf("ProcessInput err = %v (%T), want *responseToolCallLimitError", err, err)
	}
	if dispatched != 0 {
		t.Errorf("dispatched %d tool calls, want 0: a capped response's calls must never execute", dispatched)
	}
	if got := a.Models(); len(got) != 1 || got[0] != "primary" {
		t.Errorf("streamed models = %v, want exactly [primary]: the cap error must not retry or fall back", got)
	}
	// The capped round must persist nothing about the runaway: no salvaged
	// assistant draft, and no provider-failure steering that would tell the next
	// round to reuse that draft.
	for _, turn := range sessionHistory(sess) {
		switch turn.Kind {
		case schema.TurnAssistant:
			t.Errorf("capped turn persisted a salvaged assistant turn: %q", turn.Message.Text())
		case schema.TurnSteering:
			t.Errorf("capped turn persisted a failure-steering turn: %q", turn.Message.Text())
		}
	}
}
