package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/internal/hooks"
	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/agent/plugin"
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
		name          string
		output        string
		arguments     []byte
		wantSubstring string
	}{
		{name: "malformed", output: `{"message":`, wantSubstring: "passed as an object"},
		{name: "trailing", output: `{"message":"x","data":{},"artifacts":[]} trailing`, wantSubstring: "passed as an object"},
		{name: "scalar", output: `true`, wantSubstring: "passed as an object"},
		{name: "array", output: `[]`, wantSubstring: "passed as an object"},
		{name: "unknown nested field", output: `{"message":"x","data":{},"artifacts":[],"secret":"do not log"}`, wantSubstring: "passed as an object"},
		{name: "wrong nested type", output: `{"message":"x","data":[],"artifacts":[]}`, wantSubstring: "passed as an object"},
		{name: "oversized", output: strings.Repeat(" ", 2*1024*1024+1), wantSubstring: "tool arguments too large"},
		{name: "over depth", output: tooDeep, wantSubstring: "passed as an object"},
		{name: "invalid UTF-8", arguments: []byte("{\"end_turn\":true,\"message\":\"top-level\",\"output\":\"{\xff}\"}"), wantSubstring: "not valid UTF-8"},
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
			if !strings.Contains(res.PrevalErr, tc.wantSubstring) {
				t.Fatalf("error lacks %q guidance: %q", tc.wantSubstring, res.PrevalErr)
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

func TestPrepareToolCall_RepairsCommitArgumentsAndChangesAtomicallyIssue831(t *testing.T) {
	def := issue831NumericRepairDefinition()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(def)); err != nil {
		t.Fatalf("register: %v", err)
	}
	raw := json.RawMessage(`{"s":"\uX","n":"NaN"}`)
	res := prepareToolCall(llm.ToolCallData{ID: "issue831-nan", Name: def.Name, Arguments: raw}, reg.Get(def.Name), []string{def.Name}, def.Name, "communicate", "")
	if res.PrevalErr == "" {
		t.Fatal("non-finite numeric repair was accepted")
	}
	if len(res.Changes) != 0 {
		t.Fatalf("uncommitted repair changes = %+v", res.Changes)
	}
	if string(res.Call.Arguments) != string(raw) {
		t.Fatalf("arguments = %q, want original raw bytes %q", res.Call.Arguments, raw)
	}
}

func TestCommitPreparedRepairs_MarshalFailureDoesNotPartiallyCommitIssue831(t *testing.T) {
	raw := json.RawMessage(`{"s":"raw"}`)
	res := prepareResult{Call: llm.ToolCallData{Arguments: raw}}
	changes := []repair.Change{{Kind: repair.ChangeCoerceType, Field: "n", Detail: `"NaN"→NaN`}}
	if err := commitPreparedRepairs(&res, map[string]any{"n": math.NaN()}, changes); err == nil {
		t.Fatal("non-finite arguments unexpectedly marshaled")
	}
	if len(res.Changes) != 0 {
		t.Fatalf("marshal failure committed changes: %+v", res.Changes)
	}
	if string(res.Call.Arguments) != string(raw) {
		t.Fatalf("marshal failure changed arguments to %q, want %q", res.Call.Arguments, raw)
	}
}

func TestSession_NonFiniteNumericRepairDoesNotEmitUnappliedChangesIssue831(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	def := issue831NumericRepairDefinition()
	if err := sess.reg.Register(regTool(def)); err != nil {
		t.Fatalf("register: %v", err)
	}
	repairedCh := drainRepairedEvents(sess)

	res := sess.execTool(context.Background(), llm.ToolCallData{
		ID:        "issue831-nan-session",
		Name:      def.Name,
		Arguments: json.RawMessage(`{"s":"\uX","n":"NaN"}`),
	}, "")
	if !res.IsError || !res.PrevalOnly || !strings.Contains(res.FullOutput, `"n"`) {
		t.Fatalf("result = %+v, want prevalidation type failure for n", res)
	}

	sess.Close()
	if repaired := <-repairedCh; len(repaired) != 0 {
		t.Fatalf("unapplied non-finite repair emitted ToolCallRepaired: %+v", repaired)
	}
}

func TestPrepareToolCall_RejectsInvalidRawCommunicateArgumentsBeforeHealingIssue831(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)
	for _, output := range []struct {
		name string
		tail string
	}{
		{name: "absent output", tail: `}`},
		{name: "null output", tail: `,"output":null}`},
	} {
		for _, tc := range []struct {
			name string
			args func(string) []byte
			want func([]byte) string
		}{
			{
				name: "over limit",
				args: func(tail string) []byte {
					base := `{"end_turn":true,"message":"top-level"` + tail
					return []byte(base + strings.Repeat(" ", tool.MaxToolArgumentBytes+1-len(base)))
				},
				want: func(args []byte) string {
					return fmt.Sprintf("tool arguments too large: %d bytes exceeds the %d byte limit", len(args), tool.MaxToolArgumentBytes)
				},
			},
			{
				name: "invalid UTF-8",
				args: func(tail string) []byte {
					args := append([]byte(`{"end_turn":true,"message":"`), 0xff)
					return append(args, []byte(`"`+tail)...)
				},
				want: func([]byte) string { return "invalid tool arguments JSON: input is not valid UTF-8" },
			},
		} {
			t.Run(output.name+"/"+tc.name, func(t *testing.T) {
				args := tc.args(output.tail)
				res := prepareToolCall(llm.ToolCallData{ID: "issue831-raw", Name: "communicate", Arguments: args}, rt, []string{"communicate"}, "communicate", "communicate", "")
				if res.PrevalErr != tc.want(args) {
					t.Fatalf("prevalidation error = %q, want %q", res.PrevalErr, tc.want(args))
				}
				if len(res.Changes) != 0 {
					t.Fatalf("invalid raw arguments recorded repairs: %+v", res.Changes)
				}
				if string(res.Call.Arguments) != string(args) {
					t.Fatalf("arguments = %q, want original bytes %q", res.Call.Arguments, args)
				}
			})
		}
	}
}

func TestPrepareToolCall_ValidRawCommunicateArgumentsAtLimitAndUTF8StillHealIssue831(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)
	base := `{"end_turn":true,"message":"top-level"}`
	for _, args := range [][]byte{
		[]byte(base + strings.Repeat(" ", tool.MaxToolArgumentBytes-len(base))),
		[]byte(`{"end_turn":true,"message":"✓"}`),
	} {
		res := prepareToolCall(llm.ToolCallData{ID: "issue831-raw-valid", Name: "communicate", Arguments: args}, rt, []string{"communicate"}, "communicate", "communicate", "")
		if res.PrevalErr != "" {
			t.Fatalf("valid arguments rejected: %q", res.PrevalErr)
		}
		if len(res.Changes) == 0 {
			t.Fatalf("valid default envelope was not healed: %q", args)
		}
	}
}

func TestSession_InvalidRawCommunicateArgumentsDoNotHealOrEmitTelemetryIssue831(t *testing.T) {
	for _, output := range []struct {
		name string
		tail string
	}{
		{name: "absent output", tail: `}`},
		{name: "null output", tail: `,"output":null}`},
	} {
		for _, tc := range []struct {
			name string
			args func(string) []byte
			want func([]byte) string
		}{
			{
				name: "over limit",
				args: func(tail string) []byte {
					base := `{"end_turn":true,"message":"top-level"` + tail
					return []byte(base + strings.Repeat(" ", tool.MaxToolArgumentBytes+1-len(base)))
				},
				want: func(args []byte) string {
					return fmt.Sprintf("tool arguments too large: %d bytes exceeds the %d byte limit", len(args), tool.MaxToolArgumentBytes)
				},
			},
			{
				name: "invalid UTF-8",
				args: func(tail string) []byte {
					args := append([]byte(`{"end_turn":true,"message":"`), 0xff)
					return append(args, []byte(`"`+tail)...)
				},
				want: func([]byte) string { return "invalid tool arguments JSON: input is not valid UTF-8" },
			},
		} {
			t.Run(output.name+"/"+tc.name, func(t *testing.T) {
				sess := newSession(t, withoutGitSnapshot())
				sess.stateDir = t.TempDir()
				repairedCh := drainRepairedEvents(sess)
				args := tc.args(output.tail)
				res := sess.execTool(context.Background(), llm.ToolCallData{ID: "issue831-raw-session", Name: "communicate", Arguments: args}, "")
				if !res.IsError || !res.PrevalOnly || res.FullOutput != tc.want(args) {
					t.Fatalf("result = %+v, want bounded prevalidation error %q", res, tc.want(args))
				}
				if len(res.FullOutput) > 256 {
					t.Fatalf("error was not bounded: %d bytes", len(res.FullOutput))
				}

				sess.Close()
				if repaired := <-repairedCh; len(repaired) != 0 {
					t.Fatalf("invalid raw arguments emitted repairs: %+v", repaired)
				}
			})
		}
	}
}

func TestSession_PreToolUseHookDoesNotDecodeOrMergeInvalidRawArgumentsIssue831(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func() []byte
		want func([]byte) string
	}{
		{
			name: "over limit",
			args: func() []byte {
				base := `{"end_turn":true,"message":"top-level"}`
				return []byte(base + strings.Repeat(" ", tool.MaxToolArgumentBytes+1-len(base)))
			},
			want: func(args []byte) string {
				return fmt.Sprintf("tool arguments too large: %d bytes exceeds the %d byte limit", len(args), tool.MaxToolArgumentBytes)
			},
		},
		{
			name: "invalid UTF-8",
			args: func() []byte {
				args := append([]byte(`{"end_turn":true,"message":"`), 0xff)
				return append(args, []byte(`"}`)...)
			},
			want: func([]byte) string { return "invalid tool arguments JSON: input is not valid UTF-8" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sess := newSession(t, withoutGitSnapshot())
			sess.stateDir = t.TempDir()
			var mu sync.Mutex
			var prompts []string
			hookClient := llm.NewClient()
			hookClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
				mu.Lock()
				prompts = append(prompts, req.Messages[0].Text())
				mu.Unlock()
				return llm.Response{Message: llm.Assistant(`{"hookSpecificOutput":{"updatedInput":{"end_turn":true,"message":"hook replacement","output":{"message":"","data":{},"artifacts":[]}}}}`)}
			}})
			runner := hooks.NewRunner(hookClient, "gpt-5.2")
			runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "input=$TOOL_INPUT"})
			sess.hookRunner = runner

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
						got.repaired = append(got.repaired, data)
					case events.ToolCallEndData:
						if data.CallID == "issue831-hook-raw" {
							data := data
							got.end = &data
						}
					}
				}
				observed <- got
			}()

			args := tc.args()
			res := sess.execTool(context.Background(), llm.ToolCallData{ID: "issue831-hook-raw", Name: "communicate", Arguments: args}, "")
			if !res.IsError || !res.PrevalOnly || res.FullOutput != tc.want(args) {
				t.Fatalf("result = %+v, want raw prevalidation error %q", res, tc.want(args))
			}
			if len(res.FullOutput) > 256 {
				t.Fatalf("error was not bounded: %d bytes", len(res.FullOutput))
			}

			sess.Close()
			got := <-observed
			mu.Lock()
			defer mu.Unlock()
			if len(prompts) != 1 || prompts[0] != "input=null" {
				t.Fatalf("hook prompts = %d/%q, want one bounded null input", len(prompts), boundedStringForIssue831(prompts))
			}
			if len(got.repaired) != 0 {
				t.Fatalf("invalid raw hook call emitted repairs: %+v", got.repaired)
			}
			if got.end == nil || got.end.ArgumentsJSON != string(args) {
				t.Fatalf("end = %+v, want original raw arguments", got.end)
			}
		})
	}
}

func TestSession_PreToolUseHookStillReceivesValidSchemaInvalidArgumentsIssue831(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	var mu sync.Mutex
	var prompts []string
	hookClient := llm.NewClient()
	hookClient.Register(&agenttest.ScriptedAdapter{Provider: "openai", Responder: func(req llm.Request) llm.Response {
		mu.Lock()
		prompts = append(prompts, req.Messages[0].Text())
		mu.Unlock()
		return llm.Response{Message: llm.Assistant(`{}`)}
	}})
	runner := hooks.NewRunner(hookClient, "gpt-5.2")
	runner.Add(plugin.HookPreToolUse, plugin.RegisteredHook{Matcher: "*", Type: "prompt", Prompt: "input=$TOOL_INPUT"})
	sess.hookRunner = runner

	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "issue831-hook-schema", Name: "communicate", Arguments: json.RawMessage(`{"end_turn":"not-a-bool","message":"top-level","output":{"message":"","data":{},"artifacts":[]}}`)}, "")
	if !res.IsError || !res.PrevalOnly {
		t.Fatalf("result = %+v, want schema prevalidation failure", res)
	}
	sess.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(prompts) != 1 || prompts[0] == "input=null" || !strings.Contains(prompts[0], `"end_turn":"not-a-bool"`) {
		t.Fatalf("valid schema-invalid input did not reach hook: %q", boundedStringForIssue831(prompts))
	}
}

func boundedStringForIssue831(values []string) string {
	if len(values) == 0 {
		return ""
	}
	const outputLimit = 200
	if len(values[0]) <= outputLimit {
		return values[0]
	}
	return values[0][:outputLimit] + "…"
}

func TestPrepareToolCall_WhitespaceOutputMessageDoesNotBecomeTopLevelMessageIssue831(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)
	raw := json.RawMessage(`{"end_turn":true,"output":"{\"message\":\"   \",\"data\":{},\"artifacts\":[]}"}`)
	res := prepareToolCall(llm.ToolCallData{ID: "issue831-whitespace", Name: "communicate", Arguments: raw}, rt, []string{"communicate"}, "communicate", "communicate", "")
	if res.PrevalErr == "" || !strings.Contains(res.PrevalErr, `"message"`) {
		t.Fatalf("result = %+v, want missing top-level message prevalidation error", res)
	}
	if len(res.Changes) != 0 || string(res.Call.Arguments) != string(raw) {
		t.Fatalf("uncommitted whitespace repair = %+v, arguments = %q", res.Changes, res.Call.Arguments)
	}
}

func TestSession_WhitespaceOutputMessageDoesNotEmitRepairIssue831(t *testing.T) {
	sess := newSession(t, withoutGitSnapshot())
	sess.stateDir = t.TempDir()
	repairedCh := drainRepairedEvents(sess)
	raw := json.RawMessage(`{"end_turn":true,"output":"{\"message\":\"   \",\"data\":{},\"artifacts\":[]}"}`)
	res := sess.execTool(context.Background(), llm.ToolCallData{ID: "issue831-whitespace-session", Name: "communicate", Arguments: raw}, "")
	if !res.IsError || !res.PrevalOnly || !strings.Contains(res.FullOutput, `"message"`) {
		t.Fatalf("result = %+v, want missing top-level message prevalidation error", res)
	}
	sess.Close()
	if repaired := <-repairedCh; len(repaired) != 0 {
		t.Fatalf("uncommitted whitespace repair emitted telemetry: %+v", repaired)
	}
}

func issue831NumericRepairDefinition() llm.ToolDefinition {
	return llm.ToolDefinition{
		Name: "issue831_numeric_repair",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"s": map[string]any{"type": "string"},
				"n": map[string]any{"type": "number"},
			},
			"required": []string{"s", "n"},
		},
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
