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

		required, ok := data["required"].([]string)
		if !ok {
			t.Fatalf("data.required not []string: %#v", data["required"])
		}
		if len(required) != 1 || required[0] != "components" {
			t.Fatalf("data.required=%v, want [components]", required)
		}
		dataProps, _ := data["properties"].(map[string]any)
		if _, ok := dataProps["components"]; !ok {
			t.Fatalf("data.properties missing components: %#v", dataProps)
		}
	}
	if !communicateFound {
		t.Fatal("communicate tool not found")
	}
}

