package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
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
	{name: "object", custom: map[string]any{"type": "object"}, valueFor: func(call string) any { return map[string]any{"source": call} }},
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
				// The captured message must be the FIRST call's, the canonical
				// output the FIRST call's canonicalization, and the structured
				// slot the FIRST call's value (absent when it carried none) —
				// never the loser's.
				sess.mu.Lock()
				capturedText := sess.comm.text
				capturedOutput := sess.comm.output
				capturedStructured, capturedPresent := sess.comm.structured, sess.comm.structured != nil
				sess.mu.Unlock()
				if capturedText != firstMsg {
					t.Fatalf("captured message = %q, want %q (first call wins the message slot)", capturedText, firstMsg)
				}
				if wantOutput := canonicalNodeOutputText(communicateEffectiveOutput(firstMsg, firstVal)); capturedOutput != wantOutput {
					t.Fatalf("captured canonical output = %q, want %q (first call's canonicalization)", capturedOutput, wantOutput)
				}
				if got := sess.CommunicateOutput(); got != capturedOutput {
					t.Fatalf("CommunicateOutput() = %q, want the first call's canonical output %q", got, capturedOutput)
				}
				if firstVal == nil {
					// absent row: the first call carried no explicit structured
					// output (default envelope), so the slot must stay empty —
					// the loser's value must not fill it.
					if capturedPresent {
						t.Fatalf("captured structured = %#v, want absent (first call carried no explicit structured output; the loser's value must not fill its slot)", capturedStructured)
					}
				} else if !capturedPresent || !communicateJSONEqual(capturedStructured, firstVal) {
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
		// wantReply is the handler's reply: the canonical output text when the
		// node output is meaningful, else the message.
		wantReply string
	}{
		{
			name:        "default envelope with data",
			message:     "with data",
			output:      communicateDefaultEnvelope("with data", map[string]any{"k": 1}),
			wantPresent: true, // meaningful node output → raw output captured
			// meaningful node output → reply is the canonical output text
			wantReply: canonicalNodeOutputText(communicateDefaultEnvelope("with data", map[string]any{"k": 1})),
		},
		{
			name:    "default envelope empty",
			message: "plain",
			output:  communicateDefaultEnvelope("", nil),
			// all envelope fields empty → not meaningful → structured not captured
			wantPresent: false,
			// node output not meaningful → reply falls back to the message
			wantReply: "plain",
		},
		{
			name:        "custom schema object",
			custom:      map[string]any{"type": "object"},
			message:     "obj",
			output:      map[string]any{"source": "single"},
			wantPresent: true,
			// non-envelope object under a custom schema: message is not
			// defaulted into the node output (no message field present), and
			// hasMeaningfulNodeOutput is false for a map without
			// decision/message/data/artifacts keys → reply stays the message
			wantReply: "obj",
		},
		{
			name:        "custom schema explicit null",
			custom:      map[string]any{"type": "null"},
			message:     "null",
			output:      json.RawMessage(`null`),
			wantPresent: true,
			wantReply:   "null",
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
			// Pin the exact captured text, reply, and canonical output — the
			// single-call contract the atomic setter must preserve exactly.
			sess.mu.Lock()
			text, reply, output := sess.comm.text, sess.comm.reply, sess.comm.output
			sess.mu.Unlock()
			if text != tc.message {
				t.Fatalf("captured text = %q, want %q", text, tc.message)
			}
			if wantReply := tc.wantReply; reply != wantReply {
				t.Fatalf("captured reply = %q, want %q", reply, wantReply)
			}
			if wantOutput := canonicalNodeOutputText(communicateEffectiveOutput(tc.message, tc.output)); output != wantOutput {
				t.Fatalf("captured canonical output = %q, want %q", output, wantOutput)
			}
			if got := sess.CommunicateOutput(); got != output {
				t.Fatalf("CommunicateOutput() = %q, want %q", got, output)
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

// TestCommunicate_CrossShapeCompetingCallsNeverMix drives the two shapes that
// are otherwise indistinguishable in a same-shape pair — explicit null and an
// object value — against each other in BOTH orderings, asserting the winner's
// exact captured value. A same-shape explicit-null pair cannot detect a mix
// (both values are the identical null); pairing it with a distinguishable value
// makes any cross-call leak observable. A session carries one communicate
// definition, so the schema accepts both shapes (object|null union).
func TestCommunicate_CrossShapeCompetingCallsNeverMix(t *testing.T) {
	t.Parallel()
	schema := map[string]any{"type": []any{"object", "null"}}
	nullValue := json.RawMessage(`null`)
	objectValue := map[string]any{"source": "O"}

	for _, reverse := range []bool{false, true} {
		name := "null first"
		if reverse {
			name = "object first"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			sess := newCompetingCommunicateSession(t, schema)

			nullArgs := map[string]any{"message": "N", "end_turn": true, "output": nullValue}
			objectArgs := map[string]any{"message": "O", "end_turn": true, "output": objectValue}

			firstID, firstArgs, firstMsg := "callN", nullArgs, "N"
			secondID, secondArgs := "callO", objectArgs
			if reverse {
				firstID, firstArgs, firstMsg = "callO", objectArgs, "O"
				secondID, secondArgs = "callN", nullArgs
			}

			respFirst := execCommunicateCall(t, sess, firstID, firstArgs)
			if accepted, _ := respFirst["accepted"].(bool); !accepted {
				t.Fatalf("first call %s reported accepted=false, want true: %v", firstID, respFirst)
			}
			respSecond := execCommunicateCall(t, sess, secondID, secondArgs)
			if accepted, _ := respSecond["accepted"].(bool); accepted {
				t.Fatalf("second call %s reported accepted=true, want false: %v", secondID, respSecond)
			}

			sess.mu.Lock()
			capturedText := sess.comm.text
			capturedStructured := sess.comm.structured
			sess.mu.Unlock()
			if capturedText != firstMsg {
				t.Fatalf("captured message = %q, want %q (first call wins the message slot)", capturedText, firstMsg)
			}
			if firstMsg == "N" {
				// the null call won: explicit null must be captured verbatim,
				// not the loser's object value and not "absent"
				if !communicateJSONEqual(capturedStructured, nullValue) {
					t.Fatalf("captured structured = %#v, want the winner's explicit null", capturedStructured)
				}
			} else if !communicateJSONEqual(capturedStructured, objectValue) {
				t.Fatalf("captured structured = %#v, want the winner's object value %#v", capturedStructured, objectValue)
			}
		})
	}
}

// TestCommunicate_ConcurrentTerminalCaptureIsAtomic exercises concurrent
// handler completion against one session (race-enabled runs stress the same
// lock): many goroutines drive terminal communicate handlers whose structured
// values embed their call identity; afterwards the captured result must pair
// a message with that same call's structured value, never a mix. Every call
// carries a structured value (the custom object schema requires output), so
// the winner's capture is always present and identity-checkable.
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
	// Every call carries a structured value (the object schema requires
	// output), so the winner's capture is present and must carry the same
	// call identity as the message.
	m, ok := structured.(map[string]any)
	if !ok {
		t.Fatalf("structured = %#v, want map with call identity", structured)
	}
	if call, _ := m["call"].(string); call != text {
		t.Fatalf("captured mix: message %q paired with structured %v (call %q)", text, structured, call)
	}
}

// --- helpers ---

// communicateEffectiveOutput mirrors the handler's effective-output
// computation (session_tools_communicate.go): the normalized node output with
// an empty message defaulted from the top-level message.
func communicateEffectiveOutput(message string, output any) any {
	effective := normalizeNodeOutput(output)
	if strings.TrimSpace(effective.Message) == "" {
		effective.Message = message
	}
	return effective
}

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
