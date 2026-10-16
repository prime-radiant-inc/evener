package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/internal/delegatestore"
	"primeradiant.com/evener/llm"
)

func assertDelegateReportMessage(t *testing.T, finish delegateFinish, want string, output any) {
	t.Helper()
	var message string
	if err := json.Unmarshal(finish.packet.Message, &message); err != nil {
		t.Fatal(err)
	}
	if message != want {
		t.Errorf("packet message = %q, want accepted report %q", message, want)
	}
	if output == nil {
		if len(finish.packet.StructuredResult) != 0 {
			t.Errorf("unexpected structured result: %s", finish.packet.StructuredResult)
		}
		return
	}
	var got, expected any
	if err := json.Unmarshal(finish.packet.StructuredResult, &got); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Errorf("structured result = %#v, want %#v", got, expected)
	}
}

func TestDelegateReportMessagePreservesAcceptedCommunicate(t *testing.T) {
	const report = "Importer rejects missing seconds.\nparser.go:42 requires seconds.\nExporter remains unchecked."
	output := map[string]any{"message": "Parser findings", "data": map[string]any{"line": 42}, "artifacts": []string{"parser.go"}}
	for _, method := range []bool{false, true} {
		t.Run(map[bool]string{false: "function", true: "method"}[method], func(t *testing.T) {
			sess := newCompetingCommunicateSession(t, nil)
			execCommunicateCall(t, sess, "winner", communicateArgsFor(nil, "  "+report+"  ", output))
			loser := execCommunicateCall(t, sess, "loser", communicateArgsFor(nil, "Losing report", communicateDefaultEnvelope("Losing summary", map[string]any{"lost": true})))
			if loser["accepted"] != false {
				t.Fatalf("loser accepted: %#v", loser)
			}
			reply, accepted := sess.communicatedReply()
			if !accepted {
				t.Fatal("report not accepted")
			}
			finish := stableDelegateFinish(sess, reply, nil)
			if method {
				finish = (&subagent{sess: sess}).stableDelegateFinish(reply, nil)
			}
			assertDelegateReportMessage(t, finish, report, output)
			if finish.outcome != delegatestore.OutcomeCompleted {
				t.Fatalf("outcome = %s", finish.outcome)
			}
		})
	}
}

func TestDelegateReportMessageShapes(t *testing.T) {
	for _, tc := range []struct {
		name, message, want string
		schema              map[string]any
		output              any
		present, valid      bool
	}{
		{name: "message only", message: "Human only", want: "Human only", output: communicateDefaultEnvelope("", nil)},
		{name: "fallback", want: "Fallback summary", output: communicateDefaultEnvelope("Fallback summary", nil), present: true, valid: true},
		{name: "custom message", message: "Human custom", want: "Human custom", schema: map[string]any{"type": "object"}, output: map[string]any{"message": "Machine custom", "count": 2}, present: true, valid: true},
		{name: "null", message: "Human null", want: "Human null", schema: map[string]any{"type": "null"}, output: json.RawMessage(`null`), present: true, valid: true},
		{name: "invalid", message: "Human invalid", want: "Human invalid", schema: map[string]any{"type": "object", "required": []string{"required_key"}}, output: map[string]any{"message": "Machine invalid"}, present: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			handlerSchema := tc.schema
			if tc.name == "invalid" {
				// Admit the object through the real handler, then exercise the
				// completion boundary's stricter descriptor validation separately.
				handlerSchema = map[string]any{"type": "object"}
			}
			sess := newCompetingCommunicateSession(t, handlerSchema)
			execCommunicateCall(t, sess, "shape", communicateArgsFor(tc.schema, tc.message, tc.output))
			descriptor := delegatestore.Descriptor{}
			if tc.schema != nil {
				descriptor.ResultSchema = mustMarshalJSON(t, tc.schema)
			}
			reply, _ := sess.communicatedReply()
			finish := (&subagent{sess: sess, stableDescriptor: &descriptor}).stableDelegateFinish(reply, nil)
			var expected any
			if tc.present {
				expected = tc.output
			}
			assertDelegateReportMessage(t, finish, tc.want, expected)
			packet := finish.packet
			if !tc.present {
				if packet.StructuredResultValid != nil || packet.StructuredResultSource != "" || packet.StructuredResultReason != "" {
					t.Fatalf("unexpected structured metadata: %#v", packet)
				}
			} else {
				source := delegatestore.StructuredResultSourceDefaultEnvelope
				if tc.schema != nil {
					source = delegatestore.StructuredResultSourceSchema
				}
				if packet.StructuredResultSource != source || packet.StructuredResultValid == nil || *packet.StructuredResultValid != tc.valid {
					t.Fatalf("structured metadata: %#v", packet)
				}
				reason := ""
				if !tc.valid {
					reason = structuredResultReasonSchemaValidationFailed
				}
				if packet.StructuredResultReason != reason {
					t.Fatalf("reason = %q, want %q", packet.StructuredResultReason, reason)
				}
			}
		})
	}
}

func TestDelegateReportMessageNonReportOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		outcome delegatestore.OutcomeStatus
	}{
		{"error", errors.New("run failure"), delegatestore.OutcomeFailed},
		{"cancel", context.Canceled, delegatestore.OutcomeCancelled},
		{"exhaustion", &budgetExhaustionError{Budget: exhaustedBudgetTurns, Limit: 3}, delegatestore.OutcomeExhausted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newCompetingCommunicateSession(t, nil)
			execCommunicateCall(t, sess, "report", communicateArgsFor(nil, "Earlier human report", communicateDefaultEnvelope("Earlier summary", nil)))
			for _, finish := range []delegateFinish{stableDelegateFinish(sess, "Run diagnostic", tc.err), (&subagent{sess: sess}).stableDelegateFinish("Run diagnostic", tc.err)} {
				assertDelegateReportMessage(t, finish, "Run diagnostic", nil)
				if finish.outcome != tc.outcome {
					t.Fatalf("outcome = %s, want %s", finish.outcome, tc.outcome)
				}
			}
		})
	}
	assertDelegateReportMessage(t, stableDelegateFinish(nil, "No session diagnostic", nil), "No session diagnostic", nil)
	inputs := delegateTerminalRunInputs{result: "Existing evidence", communicated: true, structuredResult: "value", structuredResultPresent: true}
	before := inputs
	inputs.captureCommunicateReport(nil)
	if !reflect.DeepEqual(inputs, before) {
		t.Fatal("nil session changed run evidence")
	}
}

func TestDelegateReportMessageReusedRuntime(t *testing.T) {
	fixture := newColdStableDelegateFixture(t, "")
	output := communicateDefaultEnvelope("First summary", map[string]any{"generation": 1})
	fixture.adapter.steps = []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response { return communicateWithStructured("First human report", output) },
	}
	// Exhaust the input and its one delegate nudge's bare-text retries without
	// reporting, then fail the following generation at the provider boundary.
	for i := 0; i < 2*(maxBareTextRetries+1); i++ {
		fixture.adapter.steps = append(fixture.adapter.steps, func(llm.Request) llm.Response {
			return llm.Response{Message: llm.Assistant("Unreported generation")}
		})
	}
	providerErr := llm.ErrorFromHTTPStatus("openai", 401, "fixture failure", nil, nil)
	fixture.client.Register(&failingRequestAdapter{fakeAdapter: fixture.adapter, failAt: len(fixture.adapter.steps), err: providerErr})
	root := restoreSupervisionRoot(t, fixture, nil)
	sub := warmStableSupervisionDelegate(t, root, fixture)
	waitForStableSupervisionRun(t, root, fixture.childID)
	runtime := sub.sess
	reply, _ := runtime.communicatedReply()
	assertDelegateReportMessage(t, sub.stableDelegateFinish(reply, nil), "First human report", output)
	for index, input := range []string{"Next generation without report", "Next generation with provider error"} {
		sendAndAcknowledge(t, root, fixture.delegateID, input)
		waitForStableSupervisionRun(t, root, fixture.childID)
		current := root.subagents.get(fixture.childID)
		if current.sess != runtime {
			t.Fatal("runtime was not reused")
		}
		if runtime.Communicated() {
			t.Fatal("new generation inherited a report")
		}
		current.mu.Lock()
		result, runErr := current.result, current.err
		current.mu.Unlock()
		wantErr := error(errBareTextWithoutResultTool)
		if index == 1 {
			wantErr = providerErr
		}
		if !errors.Is(runErr, wantErr) {
			t.Fatalf("generation %d error = %v, want %v", index+2, runErr, wantErr)
		}
		finish := current.stableDelegateFinish(result, runErr)
		if finish.outcome != delegatestore.OutcomeFailed {
			t.Fatalf("outcome = %s, want failed", finish.outcome)
		}
		if string(finish.packet.Message) == `"First human report"` || len(finish.packet.StructuredResult) != 0 {
			t.Fatalf("inherited packet: %#v", finish.packet)
		}
	}
}
