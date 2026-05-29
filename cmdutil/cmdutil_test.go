package cmdutil

import (
	"strings"
	"testing"
)

func TestMaxRoundsToConfig(t *testing.T) {
	tests := []struct {
		name     string
		cli      int
		wantConf int
	}{
		{"not specified", -1, 0},   // 0 → applyDefaults sets to 200
		{"unlimited", 0, -1},       // -1 → no limit in session loop
		{"explicit limit", 50, 50}, // pass through
		{"explicit limit 1", 1, 1}, // edge case
		{"very negative", -999, 0}, // any negative → not specified
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MaxRoundsToConfig(tt.cli)
			if got != tt.wantConf {
				t.Fatalf("MaxRoundsToConfig(%d) = %d, want %d", tt.cli, got, tt.wantConf)
			}
		})
	}
}

func TestResolveReasoningEffort(t *testing.T) {
	tests := []struct {
		name    string
		cli     string
		env     string
		wantSet bool
		wantVal string
		wantErr bool
	}{
		{name: "unset", cli: "", env: "", wantSet: false},
		{name: "env medium", cli: "", env: "medium", wantSet: true, wantVal: "medium"},
		{name: "cli overrides env", cli: "HIGH", env: "low", wantSet: true, wantVal: "high"},
		{name: "cli none clears", cli: "none", env: "high", wantSet: true, wantVal: ""},
		{name: "env none clears", cli: "", env: "none", wantSet: true, wantVal: ""},
		{name: "xhigh", cli: "xhigh", env: "", wantSet: true, wantVal: "xhigh"},
		{name: "invalid", cli: "banana", env: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveReasoningEffort(tt.cli, tt.env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got.Set != tt.wantSet {
				t.Fatalf("Set=%v want %v", got.Set, tt.wantSet)
			}
			if got.Value != tt.wantVal {
				t.Fatalf("Value=%q want %q", got.Value, tt.wantVal)
			}
		})
	}
}

func TestResolveModelRef_QualifiedModelSuppliesProvider(t *testing.T) {
	got, err := ResolveModelRef("openai/gpt-5.2", "", "", "")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "openai" || got.Model != "gpt-5.2" {
		t.Fatalf("got provider=%q model=%q, want openai/gpt-5.2", got.Provider, got.Model)
	}
	if got.Qualified() != "openai/gpt-5.2" {
		t.Fatalf("Qualified()=%q", got.Qualified())
	}
}

func TestResolveModelRef_EnvModelSuppliesProvider(t *testing.T) {
	got, err := ResolveModelRef("", "Anthropic/claude-opus-4-6", "", "")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Fatalf("got provider=%q model=%q, want anthropic/claude-opus-4-6", got.Provider, got.Model)
	}
}

func TestResolveModelRef_RejectsBareStartupModel(t *testing.T) {
	_, err := ResolveModelRef("gpt-5.2", "", "", "")
	if err == nil {
		t.Fatal("expected error for bare startup model")
	}
	if !strings.Contains(err.Error(), "provider/model") {
		t.Fatalf("error=%q, want provider/model guidance", err.Error())
	}
}

func TestResolveModelRef_ResumeMetaSuppliesBareModelProvider(t *testing.T) {
	got, err := ResolveModelRef("", "", "anthropic", "claude-opus-4-6")
	if err != nil {
		t.Fatalf("ResolveModelRef: %v", err)
	}
	if got.Provider != "anthropic" || got.Model != "claude-opus-4-6" {
		t.Fatalf("got provider=%q model=%q, want anthropic/claude-opus-4-6", got.Provider, got.Model)
	}
}

func TestParseAllowedDecisions_CommaSeparated(t *testing.T) {
	got := parseAllowedDecisions("approved,changes_requested")
	if len(got) != 2 || got[0] != "approved" || got[1] != "changes_requested" {
		t.Fatalf("got %v, want [approved changes_requested]", got)
	}
}

func TestParseAllowedDecisions_JSONArray(t *testing.T) {
	got := parseAllowedDecisions(`["pass","fail"]`)
	if len(got) != 2 || got[0] != "pass" || got[1] != "fail" {
		t.Fatalf("got %v, want [pass fail]", got)
	}
}

func TestParseAllowedDecisions_Empty(t *testing.T) {
	if got := parseAllowedDecisions(""); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

func TestParseAllowedDecisions_Whitespace(t *testing.T) {
	got := parseAllowedDecisions(" approved , changes_requested ")
	if len(got) != 2 || got[0] != "approved" || got[1] != "changes_requested" {
		t.Fatalf("got %v, want [approved changes_requested]", got)
	}
}

func TestSelectProfile_NoSchema(t *testing.T) {
	p, err := SelectProfile("openai", "gpt-5.2", "")
	if err != nil {
		t.Fatalf("SelectProfile: %v", err)
	}
	// Default permissive output schema should still have the message/data/artifacts shape.
	var found bool
	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		found = true
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)
		if _, ok := outProps["data"]; !ok {
			t.Fatalf("default output schema missing data: %#v", outProps)
		}
		if _, ok := outProps["message"]; !ok {
			t.Fatalf("default output schema missing message: %#v", outProps)
		}
	}
	if !found {
		t.Fatal("communicate tool not found")
	}
}

func TestSelectProfile_ValidSchema(t *testing.T) {
	schemaJSON := `{"type":"object","properties":{"plan":{"type":"string"}},"required":["plan"],"additionalProperties":false}`
	p, err := SelectProfile("openai", "gpt-5.2", schemaJSON)
	if err != nil {
		t.Fatalf("SelectProfile: %v", err)
	}
	var found bool
	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		found = true
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)
		if _, ok := outProps["plan"]; !ok {
			t.Fatalf("output.properties missing plan after schema replacement: %#v", outProps)
		}
		// The original permissive fields must be gone.
		if _, ok := outProps["data"]; ok {
			t.Fatal("expected data to be gone after schema replacement")
		}
		if _, ok := outProps["message"]; ok {
			t.Fatal("expected message to be gone after schema replacement")
		}
	}
	if !found {
		t.Fatal("communicate tool not found")
	}
}

func TestSelectProfile_InvalidJSON(t *testing.T) {
	_, err := SelectProfile("openai", "gpt-5.2", "{not json")
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
	if !strings.Contains(err.Error(), "invalid --output-schema") {
		t.Fatalf("error message %q, want to contain 'invalid --output-schema'", err.Error())
	}
}

func TestSelectProfile_WhitespaceOnly(t *testing.T) {
	// Whitespace-only input is treated like empty — no override applied.
	p, err := SelectProfile("openai", "gpt-5.2", "   ")
	if err != nil {
		t.Fatalf("SelectProfile: %v", err)
	}
	for _, td := range p.ToolDefinitions() {
		if td.Name != "communicate" {
			continue
		}
		props, _ := td.Parameters["properties"].(map[string]any)
		output, _ := props["output"].(map[string]any)
		outProps, _ := output["properties"].(map[string]any)
		if _, ok := outProps["data"]; !ok {
			t.Fatalf("default output schema missing data (whitespace should be no-op): %#v", outProps)
		}
		return
	}
	t.Fatal("communicate tool not found")
}

// TestSelectProfile_NormalizesProviderCase verifies that mixed-case and
// whitespace-padded provider strings are normalized to lowercase before
// being used as the profile id. The registered adapter names are
// lowercase, so a profile with id="OLLAMA" or "  ollama  " would later
// fail to find a matching provider in the LLM client. Mirrors the same
// invariant llm.normalizeProviderName enforces on the client side.
//
// Hermeticity: SelectProfile calls queryModelContextWindow for kimi /
// glm / openrouter when their API keys are present, which would issue
// real HTTP requests. Clear those env vars so the test takes the
// fast/empty path regardless of the test runner's environment.
func TestSelectProfile_NormalizesProviderCase(t *testing.T) {
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("GLM_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")

	cases := []struct {
		input  string
		wantID string
	}{
		{"ollama", "ollama"},
		{"OLLAMA", "ollama"},
		{"Ollama", "ollama"},
		{"Kimi", "kimi"},
		{"OpenRouter", "openrouter"},
		{"  OLLAMA  ", "ollama"},
		{"\tollama\n", "ollama"},
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			p, err := SelectProfile(tc.input, "some-model", "")
			if err != nil {
				t.Fatalf("SelectProfile(%q): %v", tc.input, err)
			}
			if p.ID() != tc.wantID {
				t.Fatalf("profile ID = %q, want %q (mixed-case/whitespace provider must be normalized)", p.ID(), tc.wantID)
			}
		})
	}
}

// TestSelectProfile_GeminiIDIsGoogle verifies that both "google" and "gemini"
// prefixes yield a profile whose ID() is "google" — the canonical provider key
// after the id/adapter-name unification (PRI-1880).
func TestSelectProfile_GeminiIDIsGoogle(t *testing.T) {
	cases := []struct {
		provider string
	}{
		{"google"},
		{"gemini"},
		{"GEMINI"},
		{"Google"},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			p, err := SelectProfile(tc.provider, "gemini-2.5-pro", "")
			if err != nil {
				t.Fatalf("SelectProfile(%q): %v", tc.provider, err)
			}
			if got := p.ID(); got != "google" {
				t.Fatalf("profile ID = %q, want \"google\"", got)
			}
		})
	}
}

func TestStringSliceFlag(t *testing.T) {
	var f StringSliceFlag
	if err := f.Set("a"); err != nil {
		t.Fatal(err)
	}
	if err := f.Set("b"); err != nil {
		t.Fatal(err)
	}
	if f.String() != "a,b" {
		t.Fatalf("String() = %q, want %q", f.String(), "a,b")
	}
	if len(f) != 2 || f[0] != "a" || f[1] != "b" {
		t.Fatalf("values = %v, want [a b]", []string(f))
	}
}
