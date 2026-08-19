package agent

import (
	"context"
	"fmt"
	"testing"
	"time"

	"primeradiant.com/evener/llm"
)

// TestConsumeModelStream_PerResponseToolCallBound is a RED test for GitHub
// issue #94: "Nothing bounds a single assistant response — runaway model
// streams until user interrupts."
//
// Root cause (from the issue's investigation): consumeModelStream's event loop
// (agent/session_stream.go) has no in-stream per-response tool-call guard —
// neither cycle detection nor a high raw-call backstop. All four existing
// guards watch between rounds or on silence/failure, and a loud never-
// terminating response touches none of them:
//
//   - MaxToolRoundsPerInput counts tool ROUNDS; a response that never
//     completes never finishes a round.
//   - PhaseSilentStall fires only on ZERO content events; every tool-call
//     emission calls noteContent(), so a runaway reads as healthy activity.
//   - RetryStream's cap rule applies only to a stream that FAILED; a runaway
//     never fails, it just never ends.
//   - The tool breaker (agent/internal/tool/breaker.go) keys on tool RESULTS;
//     nothing executes during the response (dispatch is after StreamEventFinish).
//
// This test reproduces the incident's exact shape — a single streamed response
// containing many tool calls built from a short repeating cycle (A,B,C),
// byte-identical, many times over — and asserts that a per-response bound
// fires and stops the stream before all of them are consumed. With the current
// code no such bound exists, so consumeModelStream happily drains every event
// and returns nil: the test fails (RED) because it expects a bound.
func TestConsumeModelStream_PerResponseToolCallBound(t *testing.T) {
	t.Parallel()

	const (
		cycleLen   = 3  // A,B,C — the incident's distinct-signature count
		repeat     = 20 // 20 × 3 = 60 tool calls in one response
		totalCalls = cycleLen * repeat
		// A per-response bound must fire well before the incident's 228 calls.
		// Any sane bound (cycle detection trips ~15, backstop in the hundreds)
		// stops the stream before totalCalls. We assert the stream did NOT
		// complete all of them.
	)

	sess := newSession(t)
	req := llm.Request{Provider: "openai", Model: "gpt-5.2"}
	st := llm.NewChanStream(nil)

	// Build three distinct tool-call argument blocks (the cycle members),
	// then emit them as stream events repeat times in a single response.
	type cycleMember struct {
		id   string
		name string
		args []byte
	}
	cycle := []cycleMember{
		{id: "call_a", name: "manage_worktree", args: []byte(`{"action":"create","path":"/tmp/a"}`)},
		{id: "call_b", name: "task_list", args: []byte(`{"action":"view"}`)},
		{id: "call_c", name: "communicate", args: []byte(`{"message":"status","end_turn":false}`)},
	}

	// Emit the tool-call stream events. Each member is sent as Start → Delta
	// (args) → End, exactly the shape a real provider stream carries.
	go func() {
		defer st.CloseSend()
		for r := 0; r < repeat; r++ {
			for _, m := range cycle {
				// Unique IDs across the whole stream so the accumulator
				// records each as a distinct call (matching the incident,
				// where every call had a unique id even when args repeated).
				id := fmt.Sprintf("%s_%d", m.id, r)
				st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: id, Name: m.name}})
				st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: id, Arguments: m.args}})
				st.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallData{ID: id, Name: m.name, Arguments: m.args}})
			}
		}
		// A healthy stream terminates with a Finish event. A runaway never
		// does, but we emit it so that, IF a bound existed and stopped the
		// stream early, the absence of Finish is attributable to the bound
		// rather than to a malformed test stream. (The bound would return
		// before reaching this CloseSend-driven finish in the error path.)
		finish := llm.FinishReason{Reason: "tool_calls"}
		st.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish})
	}()

	type outcome struct {
		resp sessionModelResponse
		obs  attemptObservation
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		resp, obs, err := sess.consumeModelStream(context.Background(), req, st)
		done <- outcome{resp, obs, err}
	}()

	var got outcome
	select {
	case got = <-done:
		// returned
	case <-time.After(30 * time.Second): // TRIPWIRE: in-process ChanStream with no real I/O; only fires on a genuine hang.
		t.Fatal("consumeModelStream did not finish")
	}

	// RED assertion: a per-response tool-call bound (cycle detection or a raw
	// call backstop) MUST fire and stop the stream before it consumes all
	// totalCalls. With the current code no bound exists, so consumeModelStream
	// drains every event and returns a response carrying all totalCalls tool
	// calls — this branch is what makes the test RED.
	stoppedByBound := got.err != nil
	callsConsumed := len(got.resp.Response.ToolCalls())

	if !stoppedByBound {
		if callsConsumed >= totalCalls {
			t.Errorf("RED for issue #94: consumeModelStream consumed all %d tool calls in one response "+
				"and returned nil — no per-response bound fired. A runaway model (the incident: 228 calls, "+
				"76 repetitions of a 3-call cycle) is unbounded. Expected a bound (cycle detection or raw-call "+
				"backstop) to stop the stream; got err=nil, calls=%d.",
				totalCalls, callsConsumed)
		} else {
			t.Errorf("consumeModelStream stopped early (calls=%d < %d) but returned no error; "+
				"expected either the bound's error or all calls. err=%v",
				callsConsumed, totalCalls, got.err)
		}
	}

	// If a bound did fire (it will not, today), it must be a non-nil error that
	// the turn loop can classify — not a silent swallow. This guards against a
	// future implementation that stops the stream but loses the signal.
	if stoppedByBound && got.err == nil {
		t.Errorf("bound returned a nil error; expected a non-nil error so the turn loop can classify the stop")
	}
}
