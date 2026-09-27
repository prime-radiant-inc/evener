package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"primeradiant.com/evener/agent/internal/tool"
	"primeradiant.com/evener/llm"
)

// registerSchemaTool registers a probe tool carrying params so the real
// validator, repair pipeline, and explain layer all run.
func registerSchemaTool(t *testing.T, name string, params map[string]any) *tool.RegisteredTool {
	t.Helper()
	def := llm.ToolDefinition{
		Name:        name,
		Description: "probe",
		Parameters:  params,
	}
	reg := tool.NewRegistry()
	if err := reg.Register(regTool(def)); err != nil {
		t.Fatalf("register: %v", err)
	}
	return reg.Get(name)
}

// nestedOneOfParams is the issue #624 shape: a oneOf nested inside a property
// (properties.opts.oneOf), which plugin/MCP schemas can carry.
func nestedOneOfParams() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	}
}

// Issue #624 end to end: the real validator's KeywordLocation must be walked
// back to the enclosing property-level oneOf and its branches surfaced,
// instead of the generic present-field "wrong type or value" on the container.
func TestPrepareToolCall_NestedOneOfNamesConstraint(t *testing.T) {
	t.Parallel()
	rt := registerSchemaTool(t, "nested_oneof", nestedOneOfParams())
	call := llm.ToolCallData{
		ID:        "nested",
		Name:      "nested_oneof",
		Arguments: json.RawMessage(`{"opts":{"z":1}}`),
	}
	res := prepareToolCall(call, rt, []string{"nested_oneof"}, "nested_oneof", "communicate", "")
	if res.PrevalErr == "" {
		t.Fatal("expected a prevalidation error for the nested oneOf failure")
	}
	if strings.Contains(res.PrevalErr, "wrong type or value") {
		t.Fatalf("nested oneOf misreported as a wrong value (issue #624): %q", res.PrevalErr)
	}
	if !strings.Contains(res.PrevalErr, "oneOf constraint") {
		t.Fatalf("error does not name the oneOf constraint: %q", res.PrevalErr)
	}
	for _, want := range []string{`"opts"`, `send all of "x"`, `send all of "y"`} {
		if !strings.Contains(res.PrevalErr, want) {
			t.Fatalf("error missing %q: %q", want, res.PrevalErr)
		}
	}
}

// A oneOf nested inside an array item's schema (properties.opts.items.oneOf)
// must not be reported as a missing argument for the array index (issue #624
// review, finding 2), and its Example must render the item structurally.
func TestPrepareToolCall_NestedOneOfInArrayItem(t *testing.T) {
	t.Parallel()
	rt := registerSchemaTool(t, "nested_item_oneof", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"oneOf": []any{
						map[string]any{"required": []any{"x"}},
						map[string]any{"required": []any{"y"}},
					},
				},
			},
		},
	})
	call := llm.ToolCallData{
		ID:        "nested-item",
		Name:      "nested_item_oneof",
		Arguments: json.RawMessage(`{"opts":[{"z":1}]}`),
	}
	res := prepareToolCall(call, rt, []string{"nested_item_oneof"}, "nested_item_oneof", "communicate", "")
	if strings.Contains(res.PrevalErr, "missing required argument") {
		t.Fatalf("array-item nested oneOf reported as a missing argument (issue #624 review): %q", res.PrevalErr)
	}
	for _, want := range []string{"oneOf constraint", `"opts[0]"`, `Example: {"opts": [{"x": "..."}]}`} {
		if !strings.Contains(res.PrevalErr, want) {
			t.Fatalf("error missing %q: %q", want, res.PrevalErr)
		}
	}
}

// A property name containing "/" is JSON-Pointer escaped as "~1" in the
// validator's locations; the explain layer must decode it (issue #624 review,
// finding 5).
func TestPrepareToolCall_NestedOneOfDecodesPointerEscapes(t *testing.T) {
	t.Parallel()
	rt := registerSchemaTool(t, "nested_ptr", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"a/b": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	})
	call := llm.ToolCallData{
		ID:        "nested-ptr",
		Name:      "nested_ptr",
		Arguments: json.RawMessage(`{"a/b":{"z":1}}`),
	}
	res := prepareToolCall(call, rt, []string{"nested_ptr"}, "nested_ptr", "communicate", "")
	for _, want := range []string{"oneOf constraint", `"a/b"`} {
		if !strings.Contains(res.PrevalErr, want) {
			t.Fatalf("error missing %q: %q", want, res.PrevalErr)
		}
	}
}

// A nested oneOf whose arguments match more than one branch must be reported as
// an over-match (oneOf means exactly-one), not as branch requirements the
// arguments already satisfy (issue #624 review, finding 1).
func TestPrepareToolCall_NestedOneOfOverMatch(t *testing.T) {
	t.Parallel()
	rt := registerSchemaTool(t, "nested_overmatch", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"opts": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"a"}},
					map[string]any{"required": []any{"a"}},
				},
			},
		},
	})
	call := llm.ToolCallData{
		ID:        "nested-overmatch",
		Name:      "nested_overmatch",
		Arguments: json.RawMessage(`{"opts":{"a":"x"}}`),
	}
	res := prepareToolCall(call, rt, []string{"nested_overmatch"}, "nested_overmatch", "communicate", "")
	if !strings.Contains(res.PrevalErr, `argument "opts" matched more than one branch`) {
		t.Fatalf("nested over-match not named end to end: %q", res.PrevalErr)
	}
	if strings.Contains(res.PrevalErr, "Branch 1 requires") || strings.Contains(res.PrevalErr, "Example:") {
		t.Fatalf("over-match must not enumerate branches or add an Example: %q", res.PrevalErr)
	}
}

// An object property named "0" must be reported as an object key, not an array
// index (issue #624 review, finding 4).
func TestPrepareToolCall_NestedOneOfNumericPropertyName(t *testing.T) {
	t.Parallel()
	rt := registerSchemaTool(t, "nested_numeric", map[string]any{
		"type": "object",
		"properties": map[string]any{
			"0": map[string]any{
				"type": "object",
				"oneOf": []any{
					map[string]any{"required": []any{"x"}},
					map[string]any{"required": []any{"y"}},
				},
			},
		},
	})
	call := llm.ToolCallData{
		ID:        "nested-numeric",
		Name:      "nested_numeric",
		Arguments: json.RawMessage(`{"0":{"z":1}}`),
	}
	res := prepareToolCall(call, rt, []string{"nested_numeric"}, "nested_numeric", "communicate", "")
	if strings.Contains(res.PrevalErr, `argument "[0]"`) {
		t.Fatalf("numeric object property rendered as an array index: %q", res.PrevalErr)
	}
	for _, want := range []string{`argument "0"`, `Example: {"0": {"x": "..."}}`} {
		if !strings.Contains(res.PrevalErr, want) {
			t.Fatalf("error missing %q: %q", want, res.PrevalErr)
		}
	}
}
