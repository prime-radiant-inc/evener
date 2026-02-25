package agent

import "testing"

func TestWithSubmitResultRequiredDataKeys_AddsRequiredKeysToSchema(t *testing.T) {
	p := WithSubmitResultRequiredDataKeys(NewOpenAIProfile("gpt-5.2"), []string{"components"})

	var submitResultFound bool
	for _, td := range p.ToolDefinitions() {
		if td.Name != "submit_result" {
			continue
		}
		submitResultFound = true

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
	}
	if !submitResultFound {
		t.Fatal("submit_result tool not found")
	}
}

func TestWithSubmitResultRequiredDataKeys_PlanDocIsString(t *testing.T) {
	p := WithSubmitResultRequiredDataKeys(NewOpenAIProfile("gpt-5.2"), []string{"plan_doc"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "submit_result" {
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
	t.Fatal("submit_result tool not found")
}

func TestDefSubmitResult_DefaultSchema_NoDecisionField(t *testing.T) {
	// Default submit_result schema should NOT include the decision field.
	// Decision is only needed for orchestration (toil) and gives the model
	// an escape hatch to rationalize giving up in standalone mode.
	td := defSubmitResult()
	props, _ := td.Parameters["properties"].(map[string]any)
	output, _ := props["output"].(map[string]any)
	outProps, _ := output["properties"].(map[string]any)

	if _, exists := outProps["decision"]; exists {
		t.Fatal("default submit_result schema should not have decision field")
	}

	// output.required should not include "decision"
	required, _ := output["required"].([]string)
	for _, r := range required {
		if r == "decision" {
			t.Fatal("default submit_result output.required should not include decision")
		}
	}
}

func TestWithSubmitResultRequiredDataKeys_AddsDecisionField(t *testing.T) {
	// When orchestration keys are set (toil mode), decision field must be present.
	p := WithSubmitResultRequiredDataKeys(NewOpenAIProfile("gpt-5.2"), []string{"components"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "submit_result" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)

		decisionSchema, exists := outProps["decision"].(map[string]any)
		if !exists {
			t.Fatal("orchestrated submit_result schema should have decision field")
		}
		if decisionSchema["type"] != "string" {
			t.Fatalf("decision.type=%v, want string", decisionSchema["type"])
		}

		required, _ := output["required"].([]string)
		hasDecision := false
		for _, r := range required {
			if r == "decision" {
				hasDecision = true
			}
		}
		if !hasDecision {
			t.Fatal("orchestrated submit_result output.required should include decision")
		}
		return
	}
	t.Fatal("submit_result tool not found")
}

func TestWithSubmitResultRequiredDataKeys_TasksSchemaHasItems(t *testing.T) {
	p := WithSubmitResultRequiredDataKeys(NewOpenAIProfile("gpt-5.2"), []string{"tasks"})

	for _, td := range p.ToolDefinitions() {
		if td.Name != "submit_result" {
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
	t.Fatal("submit_result tool not found")
}
