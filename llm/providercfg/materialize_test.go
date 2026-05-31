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
}
