package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
)

// Issue #570: one assistant round may contain multiple terminal communicate
// calls. The terminal result capture must be atomic per call — a call's
// message/reply/output and its structured value are accepted together, so no
// call can populate another call's structured slot and no captured result can
// mix one call's message with another's structured output.
//
// The communicate tool is deliberately not ReadOnly (it mutates session
// terminal state), so same-round calls execute serially in assistant content
// order through execToolBatch's ordered-group algorithm (session_tool_round.go).
// The tables below drive the real session registry (ExecuteCall — argument
// validation, middleware, the actual handler) in that deterministic order; no
// sleeps and no timing races. A concurrent case exercises handler completion
// racing through the same setter under s.mu.

// communicateOutputShapes enumerates the structured-output shapes competing
// terminal calls can carry: absent (default envelope, nothing meaningful),
// object, array, scalar, and explicit null (the acceptance-criteria table from
// the issue). A non-nil custom schema replaces communicate's output schema the
// way provider.WithCommunicateOutputSchema does for delegates, enabling the
// non-object shapes; valueFor renders a per-call value of that shape, tagged
// with the call's identity so a mix is directly observable.
var communicateOutputShapes = []struct {
	name     string
	custom   map[string]any
	valueFor func(call string) any
}{
	{name: "absent"},
	{name: "object", valueFor: func(call string) any { return map[string]any{"source": call} }},
	{name: "array", custom: map[string]any{"type": "array"}, valueFor: func(call string) any { return []any{call} }},
	{name: "scalar", custom: map[string]any{"type": "string"}, valueFor: func(call string) any { return call }},
	{name: "explicit null", custom: map[string]any{"type": "null"}, valueFor: func(call string) any { return json.RawMessage(`null`) }},
}

// newCompetingCommunicateSession builds a session whose communicate tool
// carries customSchema (when non-nil), exactly as the delegate runtime builds
// result-schema delegates (WithCommunicateOutputSchema before NewSession, so
// registerCommunicateTool captures the custom definition).
func newCompetingCommunicateSession(t *testing.T, custom map[string]any) *Session {
	t.Helper()
	profile := provider.NewOpenAIProfile("gpt-5.2")
	if custom != nil {
		profile = provider.WithCommunicateOutputSchema(profile, custom)
	}
	return newSession(t, withProfile(profile), withoutGitSnapshot())
}

// execCommunicateCall drives one terminal communicate call through the real
// session registry path and returns its decoded response.
func execCommunicateCall(t *testing.T, sess *Session, id string, args map[string]any) map[string]any {
	t.Helper()
	res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
		ID:        id,
		Name:      "communicate",
		Arguments: mustMarshalJSON(t, args),
		Type:      "function",
	})
	if res.IsError {
		t.Fatalf("communicate %s error: %s", id, res.Output)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(res.Output), &resp); err != nil {
		t.Fatalf("communicate %s response not JSON: %v (%s)", id, err, res.Output)
	}
	return resp
}

// communicateArgsFor builds terminal communicate arguments carrying message
// and, under the default envelope, output as given (the envelope is required,
// so callers pass the empty envelope for "no structured output"); under a
// custom schema output is passed through verbatim.
func communicateArgsFor(custom map[string]any, message string, value any) map[string]any {
	args := map[string]any{"message": message, "end_turn": true}
	if custom == nil {
		if envelope, ok := value.(map[string]any); ok {
			args["output"] = envelope
		} else {
			args["output"] = communicateDefaultEnvelope("", nil)
		}
		return args
	}
	args["output"] = value
	return args
}

// communicateDefaultEnvelope builds a default-envelope output value.
func communicateDefaultEnvelope(message string, data map[string]any) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	return map[string]any{"message": message, "data": data, "artifacts": []string{}}
}

// TestCommunicate_CompetingCallsNeverMixCapture drives the core #570 scenario
// through the real execution path in BOTH orderings: two terminal calls where
// one carries no explicit structured output (default-envelope empty) and the
// other carries each structured-output shape. Whichever call completes first
// must win BOTH slots — its message and its structured value (or none) — and
// the loser's structured value must never fill the winner's slot. It also
// asserts the per-call tool responses: the first terminal call reports
// accepted:true, the loser accepted:false.
func TestCommunicate_CompetingCallsNeverMixCapture(t *testing.T) {
	t.Parallel()
	for _, shape := range communicateOutputShapes {
		for _, reverse := range []bool{false, true} {
			name := shape.name
			if reverse {
				name += "/reversed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				sess := newCompetingCommunicateSession(t, shape.custom)

				// Under the default envelope, the plain call carries the empty
				// envelope (no explicit structured value) and the structured
				// call's value must be a meaningful envelope; under a custom
				// schema both calls carry the shape's values.
				var plainValue, structValue any
				if shape.custom == nil {
					structValue = communicateDefaultEnvelope("B", map[string]any{"source": "B"})
				} else {
					plainValue = shape.valueFor("A")
					structValue = shape.valueFor("B")
				}

				firstID, firstMsg, firstVal := "callA", "A", plainValue
				secondID, secondMsg, secondVal := "callB", "B", structValue
				if reverse {
					firstID, firstMsg, firstVal = "callB", "B", structValue
					secondID, secondMsg, secondVal = "callA", "A", plainValue
				}

				respFirst := execCommunicateCall(t, sess, firstID, communicateArgsFor(shape.custom, firstMsg, firstVal))
				if accepted, _ := respFirst["accepted"].(bool); !accepted {
					t.Fatalf("first call %s reported accepted=false, want true (first terminal call wins): %v", firstID, respFirst)
				}
				respSecond := execCommunicateCall(t, sess, secondID, communicateArgsFor(shape.custom, secondMsg, secondVal))
				if accepted, _ := respSecond["accepted"].(bool); accepted {
					t.Fatalf("second call %s reported accepted=true, want false (a rejected call must not report accepted): %v", secondID, respSecond)
				}

				if !sess.Communicated() {
					t.Fatal("Communicated() = false, want true after a terminal communicate")
				}
				if got := sess.CommunicateOutput(); got == "" {
					t.Fatal("CommunicateOutput() empty, want the first call's output")
				}
				// The captured message must be the FIRST call's, and the
				// structured slot must hold the first call's value (absent when
				// it carried none) — never the loser's.
				sess.mu.Lock()
				capturedText := sess.comm.text
				capturedStructured, capturedPresent := sess.comm.structured, sess.comm.structured != nil
				sess.mu.Unlock()
				if capturedText != firstMsg {
					t.Fatalf("captured message = %q, want %q (first call wins the message slot)", capturedText, firstMsg)
				}
				if shape.custom == nil && firstVal == nil {
					if capturedPresent {
						t.Fatalf("captured structured = %#v, want absent (first call carried no explicit structured output; the loser's value must not fill its slot)", capturedStructured)
					}
					return
				}
				if !capturedPresent || !communicateJSONEqual(capturedStructured, firstVal) {
					t.Fatalf("captured structured = %#v (present=%v), want first call's value %#v", capturedStructured, capturedPresent, firstVal)
				}
			})
		}
	}
}

// TestCommunicate_SingleCallSemanticsPreserved pins the single-call contract
// the atomic setter must preserve exactly: message, reply/canonical output,
// and the raw structured value with its present flag.
func TestCommunicate_SingleCallSemanticsPreserved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		custom      map[string]any
		message     string
		output      any
		wantPresent bool
	}{
		{
			name:        "default envelope with data",
			message:     "with data",
			output:      communicateDefaultEnvelope("with data", map[string]any{"k": 1}),
			wantPresent: true, // meaningful node output → raw output captured
		},
		{
			name:    "default envelope empty",
			message: "plain",
			output:  communicateDefaultEnvelope("", nil),
			// all envelope fields empty → not meaningful → structured not captured
			wantPresent: false,
		},
		{
			name:        "custom schema object",
			custom:      map[string]any{"type": "object"},
			message:     "obj",
			output:      map[string]any{"source": "single"},
			wantPresent: true,
		},
		{
			name:        "custom schema explicit null",
			custom:      map[string]any{"type": "null"},
			message:     "null",
			output:      json.RawMessage(`null`),
			wantPresent: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := newCompetingCommunicateSession(t, tc.custom)
			execCommunicateCall(t, sess, "c1", communicateArgsFor(tc.custom, tc.message, tc.output))
			if !sess.Communicated() {
				t.Fatal("Communicated() = false, want true")
			}
			structured, present := sess.communicateStructuredResult()
			if present != tc.wantPresent {
				t.Fatalf("structured present = %v, want %v (value %#v)", present, tc.wantPresent, structured)
			}
			if present {
				if eq, ab, bb := jsonEqual(t, structured, tc.output); !eq {
					t.Fatalf("structured = %s, want %s", ab, bb)
				}
			}
		})
	}
}

// TestCommunicate_ConcurrentTerminalCaptureIsAtomic exercises concurrent
// handler completion against one session (race-enabled runs stress the same
// lock): many goroutines drive terminal communicate handlers whose structured
// values embed their call identity; afterwards the captured result must pair
// a message with that same call's structured value (or with none), never a mix.
func TestCommunicate_ConcurrentTerminalCaptureIsAtomic(t *testing.T) {
	t.Parallel()
	const rounds = 32
	sess := newCompetingCommunicateSession(t, map[string]any{"type": "object"})

	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range rounds {
		wg.Go(func() {
			<-start
			args := map[string]any{
				"message":  strconv.Itoa(i),
				"end_turn": true,
				"output":   map[string]any{"call": strconv.Itoa(i)},
			}
			res := sess.reg.ExecuteCall(context.Background(), sess.env, llm.ToolCallData{
				ID:        "c" + strconv.Itoa(i),
				Name:      "communicate",
				Arguments: mustMarshalJSON(t, args),
				Type:      "function",
			})
			if res.IsError {
				t.Errorf("communicate c%d error: %s", i, res.Output)
			}
		})
	}
	close(start)
	wg.Wait()

	sess.mu.Lock()
	text := sess.comm.text
	structured := sess.comm.structured
	sess.mu.Unlock()
	if text == "" {
		t.Fatal("no terminal communicate result captured")
	}
	if structured == nil {
		return // message-only winner: no structured value, no mix possible
	}
	m, ok := structured.(map[string]any)
	if !ok {
		t.Fatalf("structured = %#v, want map with call identity", structured)
	}
	if call, _ := m["call"].(string); call != text {
		t.Fatalf("captured mix: message %q paired with structured %v (call %q)", text, structured, call)
	}
}

// --- helpers ---

// mustMarshalJSON marshals v or fails the test. (The same package's mustJSON
// returns string; this returns the json.RawMessage ExecuteCall takes.)
func mustMarshalJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

// communicateJSONEqual compares two values by their JSON encodings so
// json.RawMessage and map[string]any representations of the same value
// compare equal.
func communicateJSONEqual(a, b any) bool {
	ab, err := json.Marshal(a)
	if err != nil {
		return false
	}
	bb, err := json.Marshal(b)
	if err != nil {
		return false
	}
	return bytes.Equal(ab, bb)
}
