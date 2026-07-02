package providercfg

import (
	"strings"
	"testing"
)

// Marshal emits the struct verbatim, api_key included — the secrets-never-
// reach-disk guarantee lives in WriteFile, which scrubs struct-held keys and
// restores only what the on-disk file already carried (see mutate_test.go and
// TestWriteFile_PreservesOnDiskAPIKeyAndScrubsInjected).
func TestMarshalDescriptorsOnly(t *testing.T) {
	cfg := Config{Default: "openai", Instances: []InstanceConfig{
		{Name: "openai", Type: "openai", APIStyle: StyleResponses, APIKey: "$OPENAI_KEY"},
		{Name: "vllm", Type: "openai", APIStyle: StyleChatCompletions, BaseURL: "https://vllm.local/v1", Quirks: "vllm-quirk"},
	}}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `api_key = "$OPENAI_KEY"`) {
		t.Fatalf("Marshal dropped the api_key it was given:\n%s", data)
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
		if found.Quirks != orig.Quirks {
			t.Errorf("instance %q: Quirks = %q, want %q", orig.Name, found.Quirks, orig.Quirks)
		}
	}
}
