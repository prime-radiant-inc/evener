package agent

import "testing"

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
