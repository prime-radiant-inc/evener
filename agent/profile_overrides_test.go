package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/internal/tool"
	"primeradiant.com/serf/llm"
)

func TestDefCommunicate_DefaultSchema_NoDecisionField(t *testing.T) {
	// Default communicate schema should NOT include the decision field.
	// Decision is only needed for orchestration (toil) and gives the model
	// an escape hatch to rationalize giving up in standalone mode.
	td := tool.DefCommunicate()
	props, _ := td.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)

	if _, exists := outProps["decision"]; exists {
		t.Fatal("default communicate schema should not have decision field")
	}

	// output.required should not include "decision"
	required, _ := output["required"].([]string)
	for _, r := range required {
		if r == "decision" {
			t.Fatal("default communicate output.required should not include decision")
		}
	}
}

func TestWithAllowedDecisions_AddsDecisionWithEnum(t *testing.T) {
	p := WithAllowedDecisions(NewOpenAIProfile("gpt-5.2"), []string{"approved", "changes_requested"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)

		decisionSchema, exists := outProps["decision"].(map[string]any)
		if !exists {
			t.Fatal("expected decision field in output.properties")
		}
		if decisionSchema["type"] != "string" {
			t.Fatalf("decision.type=%v, want string", decisionSchema["type"])
		}
		// enum may be []string (direct) or []any (after JSON round-trip)
		enumSlice := toStringSlice(decisionSchema["enum"])
		if len(enumSlice) != 2 || enumSlice[0] != "approved" || enumSlice[1] != "changes_requested" {
			t.Fatalf("decision.enum=%v, want [approved changes_requested]", enumSlice)
		}

		required, _ := output["required"].([]string)
		hasDecision := false
		for _, r := range required {
			if r == "decision" {
				hasDecision = true
			}
		}
		if !hasDecision {
			t.Fatal("output.required should include decision")
		}
		return
	}
	t.Fatal("communicate tool not found")
}

func TestWithAllowedDecisions_MakesOutputRequired(t *testing.T) {
	p := WithAllowedDecisions(NewOpenAIProfile("gpt-5.2"), []string{"pass", "fail"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		topRequired, ok := td.Parameters["required"].([]string)
		if !ok {
			t.Fatalf("parameters.required not []string: %#v", td.Parameters["required"])
		}
		hasOutput := false
		for _, r := range topRequired {
			if r == "output" {
				hasOutput = true
			}
		}
		if !hasOutput {
			t.Fatal("parameters.required should include output")
		}
		return
	}
	t.Fatal("communicate tool not found")
}

func TestWithAllowedDecisions_NilDecisions_NoOp(t *testing.T) {
	base := NewOpenAIProfile("gpt-5.2")
	p := WithAllowedDecisions(base, nil)

	// Should return same profile (pointer equality)
	if p != base {
		t.Fatal("nil decisions should return profile unchanged")
	}
}

func TestWithAllowedDecisions_RegistryPreservesDecisionSchema(t *testing.T) {
	// Regression test: tool.NewRegistry registers the profile's communicate
	// definition (with decision). Then re-registering with the base definition
	// but checking for an existing entry first should preserve decision.
	p := WithAllowedDecisions(NewOpenAIProfile("gpt-5.2"), []string{"approved", "rejected"})
	reg := newProfileToolRegistry(p)

	// Registry should have communicate with decision from profile.
	existing := reg.Get("communicate")
	if existing == nil {
		t.Fatal("communicate not found in registry after tool.NewRegistry")
	}

	// Simulate registerCoreTools pattern (the fix):
	// Use existing definition from registry instead of base.
	resultToolDef := tool.DefCommunicateNamed("communicate")
	if ex := reg.Get("communicate"); ex != nil {
		resultToolDef = ex.Definition
	}

	// Re-register with the preserved definition + an executor.
	err := reg.Register(tool.RegisteredTool{
		Tool: llm.Tool{Definition: resultToolDef},
		Exec: func(ctx context.Context, env execenv.ExecutionEnvironment, args map[string]any) (any, error) {
			return nil, nil
		},
	})
	if err != nil {
		t.Fatalf("re-register failed: %v", err)
	}

	// Verify decision survived the re-registration.
	final := reg.Get("communicate")
	if final == nil {
		t.Fatal("communicate not found after re-registration")
	}
	props, _ := final.Definition.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)
	if _, exists := outProps["decision"]; !exists {
		t.Fatal("decision field lost during re-registration — schema overwrite bug")
	}
}

func TestWithCommunicateOutputSchema_ReplacesOutput(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"plan": map[string]any{"type": "string"},
		},
		"required": []any{"plan"},
	}
	p := WithCommunicateOutputSchema(NewOpenAIProfile("gpt-5.2"), schema)

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, ok := props["output"].(map[string]any)
		if !ok {
			t.Fatalf("output schema missing or not map: %#v", props["output"])
		}

		// The replacement schema should be present exactly.
		if output["type"] != "object" {
			t.Fatalf("output.type=%v, want object", output["type"])
		}
		if output["additionalProperties"] != false {
			t.Fatalf("output.additionalProperties=%v, want false", output["additionalProperties"])
		}
		outProps, _ := output["properties"].(map[string]any)
		if _, ok := outProps["plan"]; !ok {
			t.Fatalf("output.properties missing plan: %#v", outProps)
		}
		// Default permissive shape (message/data/artifacts) must be gone —
		// schema REPLACES, not merges.
		if _, ok := outProps["message"]; ok {
			t.Fatal("output.properties should not have message after replacement")
		}
		if _, ok := outProps["data"]; ok {
			t.Fatal("output.properties should not have data after replacement")
		}
		if _, ok := outProps["artifacts"]; ok {
			t.Fatal("output.properties should not have artifacts after replacement")
		}
		return
	}
	t.Fatal("communicate tool not found")
}

func TestWithCommunicateOutputSchema_MakesOutputRequired(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string"}},
	}
	p := WithCommunicateOutputSchema(NewOpenAIProfile("gpt-5.2"), schema)

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		topRequired := toStringSlice(td.Parameters["required"])
		hasOutput := false
		for _, r := range topRequired {
			if r == "output" {
				hasOutput = true
			}
		}
		if !hasOutput {
			t.Fatalf("parameters.required=%v, should include output", topRequired)
		}
		return
	}
	t.Fatal("communicate tool not found")
}

func TestWithCommunicateOutputSchema_NilOrEmpty_NoOp(t *testing.T) {
	base := NewOpenAIProfile("gpt-5.2")

	if got := WithCommunicateOutputSchema(base, nil); got != base {
		t.Fatal("nil schema should return profile unchanged")
	}
	if got := WithCommunicateOutputSchema(base, map[string]any{}); got != base {
		t.Fatal("empty schema should return profile unchanged")
	}
}

func TestWithCommunicateOutputSchema_Anthropic(t *testing.T) {
	base := newAnthropicProfile("claude-opus-4-6")
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"summary": map[string]any{"type": "string"}},
		"required":   []any{"summary"},
	}
	p := WithCommunicateOutputSchema(base, schema)

	if p.BehaviorTag() != "anthropic" {
		t.Fatalf("got behavior tag %q, want anthropic", p.BehaviorTag())
	}
	if p.ID() != "anthropic" {
		t.Fatalf("got ID %q, want anthropic", p.ID())
	}
	if p.Model() != "claude-opus-4-6" {
		t.Fatalf("got model %q, want claude-opus-4-6", p.Model())
	}

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)
		if _, ok := outProps["summary"]; !ok {
			t.Fatalf("output.properties missing summary: %#v", outProps)
		}
		return
	}
	t.Fatal("communicate tool not found")
}

// Stacking order: WithCommunicateOutputSchema then WithAllowedDecisions.
// addDecisionToSchema injects "decision" into output.properties, so the
// user-supplied output schema must already have a properties map that can
// accept the new field. Document this in a comment near the function.
func TestWithAllowedDecisions_WithOutputSchema_BothApplied(t *testing.T) {
	base := NewOpenAIProfile("gpt-5.2")
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"plan_doc": map[string]any{"type": "string"},
		},
		"required": []any{"plan_doc"},
	}
	p := WithCommunicateOutputSchema(base, schema)
	p = WithAllowedDecisions(p, []string{"ready_for_review"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)

		// Decision should be present with enum.
		decisionSchema, exists := outProps["decision"].(map[string]any)
		if !exists {
			t.Fatal("expected decision field in output.properties")
		}
		enumSlice := toStringSlice(decisionSchema["enum"])
		if len(enumSlice) != 1 || enumSlice[0] != "ready_for_review" {
			t.Fatalf("decision.enum=%v, want [ready_for_review]", enumSlice)
		}

		// User-supplied plan_doc must survive.
		if _, ok := outProps["plan_doc"]; !ok {
			t.Fatal("user-supplied plan_doc must survive WithAllowedDecisions")
		}

		// Output must be required at top level.
		topReq := toStringSlice(td.Parameters["required"])
		hasOutput := false
		for _, r := range topReq {
			if r == "output" {
				hasOutput = true
			}
		}
		if !hasOutput {
			t.Fatal("parameters.required should include output")
		}
		return
	}
	t.Fatal("communicate tool not found")
}

func TestWithContextWindow_OverridesWhenPositive(t *testing.T) {
	base := newOpenAICompatProfile("kimi", "kimi-k2", 0)
	original := base.ContextWindowSize()

	p := WithContextWindow(base, 262_144)
	if got := p.ContextWindowSize(); got != 262_144 {
		t.Fatalf("ContextWindowSize() = %d, want 262144", got)
	}
	// Original profile must be unchanged (clone semantics).
	if got := base.ContextWindowSize(); got != original {
		t.Fatalf("original profile mutated: ContextWindowSize() = %d, want %d", got, original)
	}
}

func TestWithContextWindow_NonPositiveIsNoOp(t *testing.T) {
	base := newOpenAICompatProfile("kimi", "kimi-k2", 0)
	want := base.ContextWindowSize()

	for _, n := range []int{0, -1, -100} {
		p := WithContextWindow(base, n)
		if got := p.ContextWindowSize(); got != want {
			t.Fatalf("WithContextWindow(base, %d).ContextWindowSize() = %d, want %d (no-op)", n, got, want)
		}
	}
}

func TestWithContextWindow_PreservesAnthropicBehaviorTag(t *testing.T) {
	base := newAnthropicProfile("claude-opus-4-6")
	p := WithContextWindow(base, 500_000)
	if p.BehaviorTag() != "anthropic" {
		t.Fatalf("WithContextWindow changed behavior tag to %q, want anthropic", p.BehaviorTag())
	}
	if got := p.ContextWindowSize(); got != 500_000 {
		t.Fatalf("ContextWindowSize() = %d, want 500000", got)
	}
}
