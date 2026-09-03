package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

func TestPrepareToolCall_HealsDefaultCommunicateEnvelopeIssue831(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)

	outputString := `{"message":"output-only text","data":{"status":"ok"},"artifacts":["artifact:result"]}`
	// Exact output string from session 034HvTCI5LrwbM2ZZpBMqN, where the
	// provider encoded the object required at communicate.output as a string.
	observedDoubleEncoded := `{"data":{"baseline_tests":"go test ./agent/ -run 'SessionName|SessionNamer|Rename' -count=1 — ok","files":["agent/session_namer.go","agent/internal/contextmgr/context_manager.go (read-only, root-cause evidence)","cmd/evener-tui/ (terminal title feature)"],"worktree":"session-auto-namer"}}`

	for _, tc := range []struct {
		name          string
		arguments     string
		wantMessage   string
		wantOutputMsg string
		wantChanges   bool
	}{
		{
			name:          "complete object",
			arguments:     `{"end_turn":true,"message":"top-level","output":{"message":"nested","data":{},"artifacts":[]}}`,
			wantMessage:   "top-level",
			wantOutputMsg: "nested",
		},
		{
			name:          "object fills documented defaults",
			arguments:     `{"end_turn":true,"message":"top-level","output":{}}`,
			wantMessage:   "top-level",
			wantOutputMsg: "",
			wantChanges:   true,
		},
		{
			name:          "absent output",
			arguments:     `{"end_turn":true,"message":"top-level"}`,
			wantMessage:   "top-level",
			wantOutputMsg: "",
			wantChanges:   true,
		},
		{
			name:          "null output",
			arguments:     `{"end_turn":true,"message":"top-level","output":null}`,
			wantMessage:   "top-level",
			wantOutputMsg: "",
			wantChanges:   true,
		},
		{
			name:          "output-only object text",
			arguments:     `{"end_turn":true,"output":{"message":"output-only text","data":{},"artifacts":[]}}`,
			wantMessage:   "output-only text",
			wantOutputMsg: "output-only text",
			wantChanges:   true,
		},
		{
			name:          "clean JSON object string",
			arguments:     `{"end_turn":true,"output":` + jsonQuote(outputString) + `}`,
			wantMessage:   "output-only text",
			wantOutputMsg: "output-only text",
			wantChanges:   true,
		},
		{
			name:          "observed double encoded shape",
			arguments:     `{"end_turn":true,"message":"top-level","output":` + jsonQuote(observedDoubleEncoded) + `}`,
			wantMessage:   "top-level",
			wantOutputMsg: "",
			wantChanges:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := prepareToolCall(llm.ToolCallData{ID: "issue831", Name: "communicate", Arguments: json.RawMessage(tc.arguments)}, rt, []string{"communicate"}, "communicate", "communicate", "")
			if res.PrevalErr != "" {
				t.Fatalf("prepareToolCall rejected healed default envelope: %s", res.PrevalErr)
			}
			if got := len(res.Changes) > 0; got != tc.wantChanges {
				t.Fatalf("has changes = %t, want %t: %+v", got, tc.wantChanges, res.Changes)
			}
			var got map[string]any
			if err := json.Unmarshal(res.Call.Arguments, &got); err != nil {
				t.Fatalf("unmarshal repaired call: %v", err)
			}
			if got["message"] != tc.wantMessage {
				t.Fatalf("message = %#v, want %q", got["message"], tc.wantMessage)
			}
			output, ok := got["output"].(map[string]any)
			if !ok {
				t.Fatalf("output = %#v, want object", got["output"])
			}
			if output["message"] != tc.wantOutputMsg {
				t.Fatalf("output.message = %#v, want %q", output["message"], tc.wantOutputMsg)
			}
			if _, ok := output["data"].(map[string]any); !ok {
				t.Fatalf("output.data = %#v, want object", output["data"])
			}
			if _, ok := output["artifacts"].([]any); !ok {
				t.Fatalf("output.artifacts = %#v, want array", output["artifacts"])
			}
		})
	}
}

func TestPrepareToolCall_RejectsInvalidDefaultCommunicateOutputStringsIssue831(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)
	const depthBeyondLimit = 65
	tooDeep := strings.Repeat(`{"nested":`, depthBeyondLimit) + `{}` + strings.Repeat(`}`, depthBeyondLimit)

	for _, tc := range []struct {
		name      string
		output    string
		arguments []byte
	}{
		{name: "malformed", output: `{"message":`},
		{name: "trailing", output: `{"message":"x","data":{},"artifacts":[]} trailing`},
		{name: "scalar", output: `true`},
		{name: "array", output: `[]`},
		{name: "unknown nested field", output: `{"message":"x","data":{},"artifacts":[],"secret":"do not log"}`},
		{name: "wrong nested type", output: `{"message":"x","data":[],"artifacts":[]}`},
		{name: "oversized", output: strings.Repeat(" ", 2*1024*1024+1)},
		{name: "over depth", output: tooDeep},
		{name: "invalid UTF-8", arguments: []byte("{\"end_turn\":true,\"message\":\"top-level\",\"output\":\"{\xff}\"}")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arguments := tc.arguments
			if arguments == nil {
				arguments = []byte(`{"end_turn":true,"message":"top-level","output":` + jsonQuote(tc.output) + `}`)
			}
			res := prepareToolCall(llm.ToolCallData{ID: "issue831-invalid", Name: "communicate", Arguments: arguments}, rt, []string{"communicate"}, "communicate", "communicate", "")
			if res.PrevalErr == "" {
				t.Fatalf("invalid output string was accepted: arguments %q changes %+v", arguments, res.Changes)
			}
			if !strings.Contains(res.PrevalErr, "passed as an object") {
				t.Fatalf("error lacks JSON-string object guidance: %q", res.PrevalErr)
			}
			if len(res.Changes) != 0 {
				t.Fatalf("failed repair recorded changes %+v", res.Changes)
			}
		})
	}
}

func TestPrepareToolCall_PromotedCommunicateOutputGuidanceOnlyNamesOutputFailuresIssue831(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)
	const outputGuidance = "the decoded object did not satisfy the communicate output schema"

	for _, tc := range []struct {
		name         string
		arguments    string
		wantGuidance bool
	}{
		{
			name:         "unrelated top-level schema failure",
			arguments:    `{"end_turn":"not-a-bool","message":"top-level","output":"{\"message\":\"valid nested\",\"data\":{},\"artifacts\":[]}"}`,
			wantGuidance: false,
		},
		{
			name:         "output descendant schema failure",
			arguments:    `{"end_turn":true,"message":"top-level","output":"{\"message\":\"nested\",\"data\":[],\"artifacts\":[]}"}`,
			wantGuidance: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := prepareToolCall(llm.ToolCallData{ID: "issue831-guidance", Name: "communicate", Arguments: json.RawMessage(tc.arguments)}, rt, []string{"communicate"}, "communicate", "communicate", "")
			if res.PrevalErr == "" {
				t.Fatalf("invalid call was accepted: %s", tc.arguments)
			}
			if got := strings.Contains(res.PrevalErr, outputGuidance); got != tc.wantGuidance {
				t.Fatalf("output guidance = %t, want %t: %q", got, tc.wantGuidance, res.PrevalErr)
			}
		})
	}
}

func TestPrepareToolCall_DefaultCommunicateObjectStillRejectsInvalidNestedFieldsIssue831(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)
	for _, arguments := range []string{
		`{"end_turn":true,"message":"top-level","output":{"message":"nested","data":{},"artifacts":[],"unexpected":true}}`,
		`{"end_turn":true,"message":"top-level","output":{"message":"nested","data":[],"artifacts":[]}}`,
	} {
		res := prepareToolCall(llm.ToolCallData{ID: "issue831-invalid-object", Name: "communicate", Arguments: json.RawMessage(arguments)}, rt, []string{"communicate"}, "communicate", "communicate", "")
		if res.PrevalErr == "" {
			t.Fatalf("invalid output object was accepted: %s", arguments)
		}
	}
}

func TestPrepareToolCall_SameKeysStricterCommunicateOutputSchemaIsNotHealedIssue831(t *testing.T) {
	def := tool.DefCommunicateNamed("communicate")
	params := tool.CloneSchemaMap(def.Parameters)
	output := params["properties"].(map[string]any)["output"].(map[string]any)
	output["properties"].(map[string]any)["message"] = map[string]any{"type": "string", "enum": []string{"allowed"}}

	reg := tool.NewRegistry()
	if err := reg.Register(regTool(llm.ToolDefinition{Name: "communicate", Parameters: params})); err != nil {
		t.Fatalf("register: %v", err)
	}
	rt := reg.Get("communicate")
	for _, arguments := range []string{
		`{"end_turn":true,"message":"top-level","output":null}`,
		`{"end_turn":true,"output":{"message":"allowed","data":{},"artifacts":[]}}`,
		`{"end_turn":true,"output":"{\"message\":\"allowed\",\"data\":{},\"artifacts\":[]}"}`,
	} {
		res := prepareToolCall(llm.ToolCallData{ID: "issue831-strict", Name: "communicate", Arguments: json.RawMessage(arguments)}, rt, []string{"communicate"}, "communicate", "communicate", "")
		if res.PrevalErr == "" {
			t.Fatalf("same-keys stricter schema was healed: %s", arguments)
		}
		if len(res.Changes) != 0 {
			t.Fatalf("same-keys stricter schema recorded repairs: %+v", res.Changes)
		}
	}
}

func TestSession_DefaultCommunicateEnvelopeRepairsEmitBoundedTelemetryIssue831(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	repairedCh := drainRepairedEvents(sess)

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "issue831-telemetry",
		Name:      "communicate",
		Arguments: json.RawMessage(`{"end_turn":false,"output":"{\"message\":\"sensitive message\",\"data\":{},\"artifacts\":[]}"}`),
	}, "")
	if res.IsError {
		t.Fatalf("communicate: %s", res.FullOutput)
	}
	sess.Close()
	repaired := <-repairedCh
	if len(repaired) != 1 {
		t.Fatalf("repaired events = %+v, want one", repaired)
	}
	if repaired[0].ToolName != "communicate" || len(repaired[0].Changes) == 0 {
		t.Fatalf("repair event = %+v, want communicate changes", repaired[0])
	}
	for _, change := range repaired[0].Changes {
		if strings.Contains(change, "sensitive message") || !strings.Contains(change, "output") {
			t.Fatalf("telemetry leaked a value or lacks path: %q", change)
		}
	}
}

func TestSession_FailedCommunicateOutputPromotionDoesNotEmitUnappliedJSONRepairIssue831(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	observed := make(chan struct {
		repaired []events.ToolCallRepairedData
		end      *events.ToolCallEndData
	}, 1)
	go func() {
		var got struct {
			repaired []events.ToolCallRepairedData
			end      *events.ToolCallEndData
		}
		for event := range sess.Events() {
			switch data := event.Data.(type) {
			case events.ToolCallRepairedData:
				if data.CallID == "issue831-unapplied" {
					got.repaired = append(got.repaired, data)
				}
			case events.ToolCallEndData:
				if data.CallID == "issue831-unapplied" {
					data := data
					got.end = &data
				}
			}
		}
		observed <- got
	}()

	// The broken outer escape is repairable, but the promoted nested output has
	// trailing text and must be rejected. No repaired bytes reach dispatch.
	arguments := json.RawMessage(`{"end_turn":true,"message":"\uX","output":"{\"message\":\"nested\",\"data\":{},\"artifacts\":[]} trailing"}`)
	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "issue831-unapplied", Name: "communicate", Arguments: arguments}, "")
	if !res.IsError || !strings.Contains(res.FullOutput, "passed as an object") {
		t.Fatalf("result = %+v, want nested output promotion failure", res)
	}

	sess.Close()
	got := <-observed
	if len(got.repaired) != 0 {
		t.Fatalf("unapplied JSON repair emitted ToolCallRepaired: %+v", got.repaired)
	}
	if got.end == nil {
		t.Fatal("missing ToolCallEnd event")
	}
	if !got.end.PrevalOnly || got.end.Error != res.FullOutput {
		t.Fatalf("end = %+v, want prevalidation failure %q", got.end, res.FullOutput)
	}
	if got.end.ArgumentsJSON != string(arguments) {
		t.Fatalf("end arguments = %q, want original unapplied bytes %q", got.end.ArgumentsJSON, arguments)
	}
}

func jsonQuote(s string) string {
	return string(mustMarshalIssue831JSON(s))
}

func mustMarshalIssue831JSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
