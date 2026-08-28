package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/agent/internal/tool/repair"
	"primeradiant.com/evener/llm"
)

// issue627OutputArgs is the exact communicate argument shape recorded in
// session 034FxZS24JiHCpndeJz7Xr turn 91 (and retried identically at turns
// 93/95/97): output carries data and artifacts but no nested `message` key.
// The schema's output.required rejected it with the generic
// `argument "output" has the wrong type or value` — even though the call
// followed the documented envelope and the executor treats a missing
// output.message as an empty string.
const issue627OutputArgs = `{"end_turn":true,"message":"ADVERSARIAL TEST-QUALITY REVIEW — final report","output":{"artifacts":[],"data":{"command":"go test ./agent","must_fix":["inject testOnly.sandboxProber"]}}}`

// issue627ArgsMap is the same shape as a parsed argument map, for tests that
// call repair.ExplainSchemaError directly.
func issue627ArgsMap() map[string]any {
	return map[string]any{
		"end_turn": true,
		"message":  "final report",
		"output": map[string]any{
			"artifacts": []any{},
			"data":      map[string]any{"command": "go test"},
		},
	}
}

// registerCommunicateForIssue627 registers the default communicate tool the
// way a real session does, returning the registry and the resolved tool.
func registerCommunicateForIssue627(t *testing.T) (*tool.Registry, *tool.RegisteredTool) {
	t.Helper()
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(tool.DefCommunicate())); err != nil {
		t.Fatalf("register communicate: %v", err)
	}
	rt := reg.Get("communicate")
	return reg, rt
}

// TestPrepareToolCall_CommunicateOutputEnvelopeHealed is the red test for
// issue #627: an output object that follows the documented envelope but omits
// a nested default-envelope key (here `message`) must be healed before schema
// validation — the runtime treats those keys as optional with defaults, so
// the schema's required list must not strand a call whose visible text rides
// in the top-level message.
func TestPrepareToolCall_CommunicateOutputEnvelopeHealed(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)

	call := llm.ToolCallData{ID: "issue627", Name: "communicate", Arguments: json.RawMessage(issue627OutputArgs)}
	res := prepareToolCall(call, rt, []string{"communicate"}, "communicate", "communicate", "")
	if res.PrevalErr != "" {
		t.Fatalf("documented-shape communicate call rejected (issue #627): %s", res.PrevalErr)
	}
	if len(res.Changes) == 0 {
		t.Fatal("expected the missing envelope keys to be recorded as repairs")
	}
	var got map[string]any
	if err := json.Unmarshal(res.Call.Arguments, &got); err != nil {
		t.Fatalf("unmarshal healed args: %v", err)
	}
	out, ok := got["output"].(map[string]any)
	if !ok {
		t.Fatalf("healed args lost the output object: %v", got)
	}
	for _, key := range []string{"message", "data", "artifacts"} {
		if _, ok := out[key]; !ok {
			t.Fatalf("healed output missing %q key: %v", key, out)
		}
	}
}

// TestExecTool_CommunicateIssue627ShapeSucceeds drives the same call through
// the full execTool path (prevalidation, hooks, dispatch) to prove the
// delegate-report scenario from the issue — an agent delivering its final
// report with structured data — no longer fails four times in a row.
func TestExecTool_CommunicateIssue627ShapeSucceeds(t *testing.T) {
	s := newSession(t, withoutGitSnapshot())
	s.stateDir = t.TempDir()

	res := s.execTool(context.Background(), llm.ToolCallData{
		ID:        "issue627-exec",
		Name:      "communicate",
		Arguments: json.RawMessage(issue627OutputArgs),
	}, "")
	if res.IsError {
		t.Fatalf("communicate with issue-627 output shape failed: %s", res.FullOutput)
	}
	if !strings.Contains(res.FullOutput, `"accepted":true`) {
		t.Fatalf("communicate result not accepted: %s", res.FullOutput)
	}
}

// TestPrepareToolCall_CommunicateOutputExampleShowsFullShape: when a
// communicate call still fails validation (a custom output schema that
// genuinely requires a key the model omitted), the model-facing error must
// state the actual violated constraint — which nested key is missing — and
// show the full accepted output shape, not the generic `wrong type or value`
// with an example that renders `output` as a bare `{}`.
func TestPrepareToolCall_CommunicateOutputExampleShowsFullShape(t *testing.T) {
	reg := tool.NewRegistry()
	def := tool.DefCommunicateNamed("communicate")
	params := tool.CloneSchemaMap(def.Parameters)
	props := params["properties"].(map[string]any)
	outputSchema := props["output"].(map[string]any)
	outProps := outputSchema["properties"].(map[string]any)
	// `decision` is enum-constrained, so the fill must not invent a value for
	// it; the call must be rejected with the missing key named.
	outProps["decision"] = map[string]any{"type": "string", "enum": []string{"approved", "rejected"}}
	outputSchema["required"] = append(outputSchema["required"].([]string), "decision")
	if err := reg.Register(regTool(llm.ToolDefinition{Name: "communicate", Description: "d", Parameters: params})); err != nil {
		t.Fatalf("register: %v", err)
	}
	rt := reg.Get("communicate")

	call := llm.ToolCallData{ID: "issue627-custom", Name: "communicate",
		Arguments: json.RawMessage(issue627OutputArgs)}
	res := prepareToolCall(call, rt, []string{"communicate"}, "communicate", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatal("expected prevalidation failure for enum-constrained missing decision")
	}
	if strings.Contains(res.PrevalErr, "wrong type or value") {
		t.Fatalf("generic wrong-type-or-value message hides the actual constraint (issue #627): %s", res.PrevalErr)
	}
	if !strings.Contains(res.PrevalErr, "output.decision") {
		t.Fatalf("error does not name the missing nested key output.decision: %s", res.PrevalErr)
	}
}

// TestPrepareToolCall_CustomOutputSchemaNotSilentlyFilled pins the fill's
// blast radius: only the default communicate envelope (whose missing keys the
// executor documents as empty defaults) may be zero-filled. A custom output
// schema — the shape a delegate's result_schema takes via
// WithCommunicateOutputSchema — that requires a key the model omitted must
// fail loudly, never be healed into validity with invented content.
func TestPrepareToolCall_CustomOutputSchemaNotSilentlyFilled(t *testing.T) {
	reg := tool.NewRegistry()
	def := tool.DefCommunicateNamed("communicate")
	params := tool.CloneSchemaMap(def.Parameters)
	props := params["properties"].(map[string]any)
	props["output"] = map[string]any{
		"type":       "object",
		"properties": map[string]any{"summary": map[string]any{"type": "string"}},
		"required":   []string{"summary"},
	}
	if err := reg.Register(regTool(llm.ToolDefinition{Name: "communicate", Description: "d", Parameters: params})); err != nil {
		t.Fatalf("register: %v", err)
	}
	rt := reg.Get("communicate")

	call := llm.ToolCallData{ID: "issue627-custom-plain", Name: "communicate",
		Arguments: json.RawMessage(`{"end_turn":true,"message":"report","output":{"detail":"x"}}`)}
	res := prepareToolCall(call, rt, []string{"communicate"}, "communicate", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatalf("custom output schema was silently healed (missing required summary): changes %+v", res.Changes)
	}
	if !strings.Contains(res.PrevalErr, "output.summary") {
		t.Fatalf("error does not name the missing custom key: %s", res.PrevalErr)
	}
}

// TestExplainSchemaError_CommunicateOutputNamesMissingNestedKey pins the
// model-facing message for the issue #627 failure shape: a present `output`
// object that omits a nested required property must be explained as exactly
// that — never as a wrong type or value on `output` itself. Uses the real
// DefCommunicateNamed parameters (not a hand-mirrored schema) so drift in
// definitions.go fails here.
func TestExplainSchemaError_CommunicateOutputNamesMissingNestedKey(t *testing.T) {
	params := tool.DefCommunicateNamed("communicate").Parameters
	msg := repair.ExplainSchemaError("communicate", params, issue627ArgsMap(), "output", "required")
	if strings.Contains(msg, "wrong type or value") {
		t.Fatalf("present output object misreported as wrong type or value (issue #627): %q", msg)
	}
	if !strings.Contains(msg, "output.message") {
		t.Fatalf("message does not name the missing nested key: %q", msg)
	}
	if !strings.Contains(msg, "Example:") {
		t.Fatalf("message lost the example tail: %q", msg)
	}
}

// TestExplainSchemaError_ExampleShowsNestedOutputShape pins the Example tail:
// for a schema whose required `output` property is itself an object with
// required keys, the example must show that nested shape rather than a bare
// `{}` — the recovery hint the issue reports as incomplete. It also guards
// the shared schema against mutation: DefCommunicateNamed stores the nested
// required list as a []string, which asStringSlice returns by reference, so
// an example renderer that sorts it in place would corrupt
// t.Definition.Parameters for every later message (and across registry
// clones that share it).
func TestExplainSchemaError_ExampleShowsNestedOutputShape(t *testing.T) {
	params := tool.DefCommunicateNamed("communicate").Parameters
	outputSchema := params["properties"].(map[string]any)["output"].(map[string]any)
	required, _ := outputSchema["required"].([]string)
	before := strings.Join(required, ",")

	msg := repair.ExplainSchemaError("communicate", params, issue627ArgsMap(), "output", "required")
	if !strings.Contains(msg, `"output": {"artifacts": [], "data": {}, "message": "..."}`) {
		t.Fatalf("example does not show the accepted output shape: %q", msg)
	}
	if after := strings.Join(required, ","); after != before {
		t.Fatalf("schema's required list mutated by example rendering: %s -> %s", before, after)
	}
}

// TestPrepareToolCall_SameShapeNonResultToolNotFilled pins the identity half
// of the fill gate: a tool that is NOT the session's result tool keeps its
// envelope-shaped required output failing loudly, even when its schema is
// byte-identical to the default communicate envelope. The fill is a property
// of the result tool's documented contract, not of any schema that happens to
// share its shape (an MCP or plugin tool could plausibly look like this).
func TestPrepareToolCall_SameShapeNonResultToolNotFilled(t *testing.T) {
	reg := tool.NewRegistry()
	// submit_report: envelope-shaped schema on a differently-named tool.
	if err := reg.Register(regTool(tool.DefCommunicateNamed("submit_report"))); err != nil {
		t.Fatalf("register: %v", err)
	}
	rt := reg.Get("submit_report")

	call := llm.ToolCallData{ID: "r2-identity", Name: "submit_report",
		Arguments: json.RawMessage(`{"end_turn":true,"message":"m","output":{"data":{"a":1}}}`)}
	// The session's result tool is communicate, not submit_report.
	res := prepareToolCall(call, rt, []string{"submit_report"}, "submit_report", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatalf("envelope-shaped schema on a non-result tool was silently filled: changes %+v args %s", res.Changes, res.Call.Arguments)
	}
	if !strings.Contains(res.PrevalErr, "output.message") {
		t.Fatalf("error does not name the missing envelope key: %s", res.PrevalErr)
	}
}

// TestPrepareToolCall_SupersetEnvelopeNotFilled pins the exact-match half of
// the fill gate: usesDefaultCommunicateOutputEnvelope must reject a superset
// envelope (the WithAllowedDecisions shape — message/data/artifacts plus an
// enum-constrained decision), so those calls keep failing loudly on the key
// the model was required to choose.
func TestPrepareToolCall_SupersetEnvelopeNotFilled(t *testing.T) {
	def := tool.DefCommunicateNamed("communicate")
	params := tool.CloneSchemaMap(def.Parameters)
	props := params["properties"].(map[string]any)
	outputSchema := props["output"].(map[string]any)
	outProps := outputSchema["properties"].(map[string]any)
	outProps["decision"] = map[string]any{"type": "string", "enum": []string{"approved", "rejected"}}
	outputSchema["required"] = append(outputSchema["required"].([]string), "decision")

	if usesDefaultCommunicateOutputEnvelope(llm.ToolDefinition{Parameters: params}) {
		t.Fatal("superset envelope must not match the default envelope predicate")
	}

	reg := tool.NewRegistry()
	if err := reg.Register(regTool(llm.ToolDefinition{Name: "communicate", Description: "d", Parameters: params})); err != nil {
		t.Fatalf("register: %v", err)
	}
	rt := reg.Get("communicate")

	call := llm.ToolCallData{ID: "r2-superset", Name: "communicate",
		Arguments: json.RawMessage(`{"end_turn":true,"message":"m","output":{"data":{"a":1}}}`)}
	res := prepareToolCall(call, rt, []string{"communicate"}, "communicate", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatalf("superset envelope was silently filled: changes %+v args %s", res.Changes, res.Call.Arguments)
	}
	if !strings.Contains(res.PrevalErr, "decision") {
		t.Fatalf("error does not name the enum-constrained key: %s", res.PrevalErr)
	}
}

// TestPrepareToolCall_FillNotRecordedWhenValidationStillFails pins the
// telemetry contract: the fill is committed only when validation passes. A
// call that still fails after filling (here: end_turn missing) must report
// zero changes, so no ToolCallRepaired event claims arguments bytes that were
// never applied.
func TestPrepareToolCall_FillNotRecordedWhenValidationStillFails(t *testing.T) {
	_, rt := registerCommunicateForIssue627(t)

	call := llm.ToolCallData{ID: "r2-phantom", Name: "communicate",
		Arguments: json.RawMessage(`{"message":"m","output":{"data":{"a":1}}}`)}
	res := prepareToolCall(call, rt, []string{"communicate"}, "communicate", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatal("expected validation failure (end_turn missing)")
	}
	if len(res.Changes) != 0 {
		t.Fatalf("phantom ToolCallRepaired changes recorded for a call that failed validation: %+v", res.Changes)
	}
}

// TestExplainSchemaError_NestedFieldExampleUsesContainerSchema pins the
// container-collision fix: when a nested object property (tasks[0].meta) is
// missing required keys, the Example must render that nested field's own
// shape — never a same-named top-level property's.
func TestExplainSchemaError_NestedFieldExampleUsesContainerSchema(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"meta": map[string]any{"type": "object", "required": []string{"top_level_marker"}, "properties": map[string]any{"top_level_marker": map[string]any{"type": "string"}}},
			"tasks": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"meta": map[string]any{"type": "object", "required": []string{"nested_marker"}, "properties": map[string]any{"nested_marker": map[string]any{"type": "string"}}},
					},
					"required": []string{"meta"},
				},
			},
		},
		"required": []string{"tasks"},
	}
	args := map[string]any{"tasks": []any{map[string]any{"meta": map[string]any{}}}}
	msg := repair.ExplainSchemaError("probe_tool", params, args, "tasks/0/meta", "required")
	if strings.Contains(msg, "top_level_marker") {
		t.Fatalf("example rendered the top-level meta's shape for a nested field: %q", msg)
	}
	if !strings.Contains(msg, `"meta": {"nested_marker": "..."}`) {
		t.Fatalf("example does not show the nested field's own shape: %q", msg)
	}
}
