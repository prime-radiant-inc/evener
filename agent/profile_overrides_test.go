package agent

import (
	"context"
	"testing"

	"primeradiant.com/serf/llm"
)

func TestWithCommunicateRequiredDataKeys_AddsRequiredKeysToSchema(t *testing.T) {
	p := WithCommunicateRequiredDataKeys(NewOpenAIProfile("gpt-5.2"), []string{"components"})

	var communicateFound bool
	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		communicateFound = true

		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)
		data, _ := outProps["data"].(map[string]any)

		if data["additionalProperties"] != false {
			t.Fatalf("data.additionalProperties=%v, want false", data["additionalProperties"])
		}

		required, ok := data["required"].([]string)
		if !ok {
			t.Fatalf("data.required not []string: %#v", data["required"])
		}
		if len(required) != 1 || required[0] != "components" {
			t.Fatalf("data.required=%v, want [components]", required)
		}
		dataProps, _ := data["properties"].(map[string]any)
		compSchema, ok := dataProps["components"].(map[string]any)
		if !ok {
			t.Fatalf("data.properties missing components: %#v", dataProps)
		}
		if compSchema["type"] != "array" {
			t.Fatalf("components.type=%v, want %q", compSchema["type"], "array")
		}
		items, ok := compSchema["items"].(map[string]any)
		if !ok {
			t.Fatalf("components.items missing or not object: %#v", compSchema["items"])
		}
		if items["type"] != "object" {
			t.Fatalf("components.items.type=%v, want %q", items["type"], "object")
		}

		// After removing decision side-effect, decision should NOT be in output
		if _, exists := outProps["decision"]; exists {
			t.Fatal("WithCommunicateRequiredDataKeys should not add decision field")
		}
	}
	if !communicateFound {
		t.Fatal("communicate tool not found")
	}
}

func TestWithCommunicateRequiredDataKeys_PlanDocIsString(t *testing.T) {
	p := WithCommunicateRequiredDataKeys(NewOpenAIProfile("gpt-5.2"), []string{"plan_doc"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)
		data, _ := outProps["data"].(map[string]any)
		dataProps, _ := data["properties"].(map[string]any)
		planDoc, _ := dataProps["plan_doc"].(map[string]any)
		if planDoc["type"] != "string" {
			t.Fatalf("plan_doc.type=%v, want %q", planDoc["type"], "string")
		}
		return
	}
	t.Fatal("communicate tool not found")
}

func TestDefCommunicate_DefaultSchema_NoDecisionField(t *testing.T) {
	// Default communicate schema should NOT include the decision field.
	// Decision is only needed for orchestration (toil) and gives the model
	// an escape hatch to rationalize giving up in standalone mode.
	td := defCommunicate()
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

func TestWithCommunicateRequiredDataKeys_TasksSchemaHasItems(t *testing.T) {
	p := WithCommunicateRequiredDataKeys(NewOpenAIProfile("gpt-5.2"), []string{"tasks"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)
		data, _ := outProps["data"].(map[string]any)
		dataProps, _ := data["properties"].(map[string]any)
		tasks, _ := dataProps["tasks"].(map[string]any)
		if tasks["type"] != "array" {
			t.Fatalf("tasks.type=%v, want %q", tasks["type"], "array")
		}
		items, ok := tasks["items"].(map[string]any)
		if !ok {
			t.Fatalf("tasks.items missing or not object: %#v", tasks["items"])
		}
		if items["type"] != "object" {
			t.Fatalf("tasks.items.type=%v, want %q", items["type"], "object")
		}
		return
	}
	t.Fatal("communicate tool not found")
}

func TestWithCommunicateRequiredDataKeys_StoryResultsIsObject(t *testing.T) {
	// Regression: the suffix heuristic was typing "story_results" as array
	// because it ends in "s". Keys ending in "_results" should be objects
	// (keyed by ID), not arrays.
	p := WithCommunicateRequiredDataKeys(NewOpenAIProfile("gpt-5.2"), []string{"story_results"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)
		data, _ := outProps["data"].(map[string]any)
		dataProps, _ := data["properties"].(map[string]any)
		sr, _ := dataProps["story_results"].(map[string]any)
		if sr["type"] != "object" {
			t.Fatalf("story_results.type=%v, want %q", sr["type"], "object")
		}
		if sr["additionalProperties"] != false {
			t.Fatalf("story_results.additionalProperties=%v, want false", sr["additionalProperties"])
		}
		return
	}
	t.Fatal("communicate tool not found")
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
	// Regression test: NewToolRegistry registers the profile's communicate
	// definition (with decision). Then re-registering with the base definition
	// but checking for an existing entry first should preserve decision.
	p := WithAllowedDecisions(NewOpenAIProfile("gpt-5.2"), []string{"approved", "rejected"})
	reg := p.NewToolRegistry()

	// Registry should have communicate with decision from profile.
	existing := reg.Get("communicate")
	if existing == nil {
		t.Fatal("communicate not found in registry after NewToolRegistry")
	}

	// Simulate registerCoreTools pattern (the fix):
	// Use existing definition from registry instead of base.
	resultToolDef := defCommunicateNamed("communicate")
	if ex := reg.Get("communicate"); ex != nil {
		resultToolDef = ex.Definition
	}

	// Re-register with the preserved definition + an executor.
	err := reg.Register(RegisteredTool{
		Tool: llm.Tool{Definition: resultToolDef},
		Exec: func(ctx context.Context, env ExecutionEnvironment, args map[string]any) (any, error) {
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

func TestWithAllowedDecisions_WithRequiredDataKeys_BothApplied(t *testing.T) {
	p := NewOpenAIProfile("gpt-5.2")
	p = WithCommunicateRequiredDataKeys(p, []string{"plan_doc"})
	p = WithAllowedDecisions(p, []string{"ready_for_review"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)

		// Decision should be present with enum
		decisionSchema, exists := outProps["decision"].(map[string]any)
		if !exists {
			t.Fatal("expected decision field")
		}
		enumSlice := toStringSlice(decisionSchema["enum"])
		if len(enumSlice) != 1 || enumSlice[0] != "ready_for_review" {
			t.Fatalf("decision.enum=%v, want [ready_for_review]", enumSlice)
		}

		// Data keys should also be present
		data, _ := outProps["data"].(map[string]any)
		dataProps, _ := data["properties"].(map[string]any)
		if _, ok := dataProps["plan_doc"]; !ok {
			t.Fatal("expected plan_doc in data.properties")
		}

		// Output should be required at top level
		topRequired, _ := td.Parameters["required"].([]string)
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
