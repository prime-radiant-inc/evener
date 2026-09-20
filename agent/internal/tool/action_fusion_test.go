package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/llm"
)

// Action Fusion (SoL-Pi auto-research design, mechanism 1): the file-mutation
// tools gain an optional run_after parameter behind a per-session flag. These
// tests pin the two tool-package pieces the mechanism needs: WithRunAfter adds
// exactly the run_after property without mutating its input, and Register
// recompiles the argument schema when a re-registered tool's parameters
// changed (name alone must not pin the stale compiled schema).

func TestWithRunAfterAddsOnlyRunAfterProperty(t *testing.T) {
	t.Parallel()
	for _, base := range []llm.ToolDefinition{DefWriteFile(), DefEditFile(), DefApplyPatch()} {
		baseJSON, err := json.Marshal(base.Parameters)
		if err != nil {
			t.Fatalf("marshal %s base params: %v", base.Name, err)
		}
		fused := WithRunAfter(base)
		// The input definition is untouched.
		afterJSON, err := json.Marshal(base.Parameters)
		if err != nil {
			t.Fatalf("marshal %s base params after WithRunAfter: %v", base.Name, err)
		}
		if !bytes.Equal(afterJSON, baseJSON) {
			t.Fatalf("WithRunAfter mutated its input %s", base.Name)
		}
		props, ok := fused.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("fused %s has no properties", base.Name)
		}
		runAfter, ok := props["run_after"].(map[string]any)
		if !ok {
			t.Fatalf("fused %s lacks run_after", base.Name)
		}
		if typ, _ := runAfter["type"].(string); typ != "string" {
			t.Fatalf("%s run_after type = %v, want string", base.Name, runAfter["type"])
		}
		if desc, _ := runAfter["description"].(string); strings.TrimSpace(desc) == "" {
			t.Fatalf("%s run_after has no description", base.Name)
		}
		// run_after is optional: the required list is unchanged.
		baseRequired, _ := json.Marshal(base.Parameters["required"])
		fusedRequired, _ := json.Marshal(fused.Parameters["required"])
		if !bytes.Equal(fusedRequired, baseRequired) {
			t.Fatalf("WithRunAfter changed %s required: %s -> %s", base.Name, baseRequired, fusedRequired)
		}
		// Every base property survives verbatim.
		baseProps, ok := base.Parameters["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s base has no properties", base.Name)
		}
		for name, want := range baseProps {
			got, ok := props[name]
			if !ok {
				t.Fatalf("fused %s lost property %s", base.Name, name)
			}
			gotJSON, wantJSON := jsonMustMarshal(t, got), jsonMustMarshal(t, want)
			if gotJSON != wantJSON {
				t.Fatalf("fused %s changed property %s:\n got: %s\nwant: %s", base.Name, name, gotJSON, wantJSON)
			}
		}
		if len(props) != len(baseProps)+1 {
			t.Fatalf("fused %s has %d properties, want base %d + run_after only", base.Name, len(props), len(baseProps))
		}
	}
}

func jsonMustMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}

// TestRegisterRecompilesSchemaWhenParamsChange pins the registry seam Action
// Fusion depends on: registerCoreTools re-registers tools over the
// profile-seeded registry, and a changed parameter schema must compile its
// own argument schema — reusing by tool name alone would validate calls
// against the stale schema (run_after would stay rejected by the seeded
// additionalProperties:false schema).
func TestRegisterRecompilesSchemaWhenParamsChange(t *testing.T) {
	t.Parallel()
	exec := func(_ context.Context, _ execenv.ExecutionEnvironment, args map[string]any) (any, error) {
		return "ok:" + args["a"].(string), nil
	}
	v1 := llm.ToolDefinition{
		Name: "fusion_probe",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           map[string]any{"a": map[string]any{"type": "string"}},
			"required":             []string{"a"},
		},
	}
	v2 := llm.ToolDefinition{
		Name: "fusion_probe",
		Parameters: map[string]any{
			"type":                 "object",
			"additionalProperties": false,
			"properties": map[string]any{
				"a":         map[string]any{"type": "string"},
				"run_after": map[string]any{"type": "string"},
			},
			"required": []string{"a"},
		},
	}
	reg := NewRegistry()
	if err := reg.Register(RegisteredTool{Definition: v1, Exec: exec}); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if err := reg.Register(RegisteredTool{Definition: v2, Exec: exec}); err != nil {
		t.Fatalf("register v2: %v", err)
	}
	// The changed schema must now accept the new property.
	res := reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID:        "probe-1",
		Name:      "fusion_probe",
		Arguments: json.RawMessage(`{"a": "x", "run_after": "cat probe"}`),
	})
	if res.IsError {
		t.Fatalf("re-registered schema still rejects the new property: %s", res.Output)
	}
	// And the same-name identical re-registration still reuses the compiled
	// schema (the fast path registerCoreTools relies on).
	if err := reg.Register(RegisteredTool{Definition: v2, Exec: exec}); err != nil {
		t.Fatalf("re-register v2 again: %v", err)
	}
	res = reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID:        "probe-2",
		Name:      "fusion_probe",
		Arguments: json.RawMessage(`{"a": "x", "run_after": "cat probe"}`),
	})
	if res.IsError {
		t.Fatalf("identical re-registration lost the new property: %s", res.Output)
	}
	// A params revert must take effect too: v1 again rejects run_after.
	if err := reg.Register(RegisteredTool{Definition: v1, Exec: exec}); err != nil {
		t.Fatalf("re-register v1: %v", err)
	}
	res = reg.ExecuteCall(context.Background(), nil, llm.ToolCallData{
		ID:        "probe-3",
		Name:      "fusion_probe",
		Arguments: json.RawMessage(`{"a": "x", "run_after": "cat probe"}`),
	})
	if !res.IsError {
		t.Fatalf("reverted schema still accepts run_after: %s", res.Output)
	}
}
