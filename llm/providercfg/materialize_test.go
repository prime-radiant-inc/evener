package providercfg

import (
	"strings"
	"testing"
)

func TestMarshalDescriptorsOnly(t *testing.T) {
	cfg := Config{Default: "openai", Instances: []InstanceConfig{
		{Name: "openai", Type: "openai", APIStyle: StyleResponses, APIKey: "sk-LEAK"},
		{Name: "vllm", Type: "openai", APIStyle: StyleChatCompletions, BaseURL: "https://vllm.local/v1"},
	}}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sk-LEAK") || strings.Contains(string(data), "api_key") {
		t.Fatalf("Marshal leaked a secret:\n%s", data)
	}
	got, err := Load(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.Default != "openai" || len(got.Instances) != 2 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	// Assert each instance's descriptor fields survive Marshal/Load intact.
	for _, orig := range cfg.Instances {
		var found *InstanceConfig
		for i := range got.Instances {
			if got.Instances[i].Name == orig.Name {
				found = &got.Instances[i]
				break
			}
		}
		if found == nil {
			t.Fatalf("instance %q missing after round-trip", orig.Name)
		}
		if found.Type != orig.Type {
			t.Errorf("instance %q: Type = %q, want %q", orig.Name, found.Type, orig.Type)
		}
		if found.APIStyle != orig.APIStyle {
			t.Errorf("instance %q: APIStyle = %q, want %q", orig.Name, found.APIStyle, orig.APIStyle)
		}
		if found.BaseURL != orig.BaseURL {
			t.Errorf("instance %q: BaseURL = %q, want %q", orig.Name, found.BaseURL, orig.BaseURL)
		}
	}
}
