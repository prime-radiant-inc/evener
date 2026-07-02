package providercfg

import (
	"os"
	"strings"
	"testing"
)

const modelsFixtureToml = `
schema = 1
default = "lunaroute"

[instances.lunaroute]
type = "openai"
api_style = "chat-completions"
base_url = "https://gw.lunaroute.com/v1"
api_key = "$LUNAROUTE_API_KEY"

[instances.lunaroute.compat]
thinking_format = "zai"
supports_reasoning_effort = false

[instances.lunaroute.models."glm-5.2-nvfp4"]
context_window = 1048576
max_output_tokens = 131072
reasoning = true
thinking_levels = { minimal = "high", low = "high", medium = "high", high = "high", xhigh = "max" }

[instances.lunaroute.models."glm-5.2-nvfp4".compat]
supports_reasoning_effort = true
max_tokens_field = "max_tokens"
tool_stream = true
`

func TestLoadParsesInstanceCompatAndModels(t *testing.T) {
	cfg, err := Load([]byte(modelsFixtureToml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	inst := cfg.Instances[0]
	if inst.Name != "lunaroute" {
		t.Fatalf("Instances[0].Name = %q, want lunaroute", inst.Name)
	}

	if inst.Compat == nil {
		t.Fatal("inst.Compat = nil, want parsed compat")
	}
	if inst.Compat.ThinkingFormat != "zai" {
		t.Errorf("Compat.ThinkingFormat = %q, want zai", inst.Compat.ThinkingFormat)
	}
	if inst.Compat.SupportsReasoningEffort == nil || *inst.Compat.SupportsReasoningEffort {
		t.Errorf("Compat.SupportsReasoningEffort = %v, want false", inst.Compat.SupportsReasoningEffort)
	}
	if inst.Compat.MaxTokensField != "" {
		t.Errorf("Compat.MaxTokensField = %q, want empty", inst.Compat.MaxTokensField)
	}

	mc, ok := inst.Models["glm-5.2-nvfp4"]
	if !ok {
		t.Fatalf("Models missing glm-5.2-nvfp4; got %v", inst.Models)
	}
	if mc.ContextWindow != 1048576 {
		t.Errorf("ContextWindow = %d, want 1048576", mc.ContextWindow)
	}
	if mc.MaxOutputTokens != 131072 {
		t.Errorf("MaxOutputTokens = %d, want 131072", mc.MaxOutputTokens)
	}
	if mc.Reasoning == nil || !*mc.Reasoning {
		t.Errorf("Reasoning = %v, want true", mc.Reasoning)
	}
	want := map[string]string{
		"minimal": "high", "low": "high", "medium": "high", "high": "high", "xhigh": "max",
	}
	if len(mc.ThinkingLevels) != len(want) {
		t.Fatalf("ThinkingLevels = %v, want %v", mc.ThinkingLevels, want)
	}
	for k, v := range want {
		if mc.ThinkingLevels[k] != v {
			t.Errorf("ThinkingLevels[%q] = %q, want %q", k, mc.ThinkingLevels[k], v)
		}
	}
	if mc.Compat == nil {
		t.Fatal("model Compat = nil, want parsed compat")
	}
	if mc.Compat.SupportsReasoningEffort == nil || !*mc.Compat.SupportsReasoningEffort {
		t.Errorf("model Compat.SupportsReasoningEffort = %v, want true", mc.Compat.SupportsReasoningEffort)
	}
	if mc.Compat.MaxTokensField != "max_tokens" {
		t.Errorf("model Compat.MaxTokensField = %q, want max_tokens", mc.Compat.MaxTokensField)
	}
	if mc.Compat.ToolStream == nil || !*mc.Compat.ToolStream {
		t.Errorf("model Compat.ToolStream = %v, want true", mc.Compat.ToolStream)
	}
}

// The full compat surface parses on a compat-family instance.
func TestLoadParsesAllCompatFields(t *testing.T) {
	src := `
[instances.gw]
type = "glm"

[instances.gw.compat]
thinking_format = "deepseek"
supports_reasoning_effort = true
max_tokens_field = "max_completion_tokens"
tool_stream = true
supports_store = true
supports_developer_role = true
supports_usage_in_streaming = false
requires_tool_result_name = true
requires_assistant_after_tool_result = true
requires_thinking_as_text = true
requires_reasoning_content_on_assistant = true
cache_control_format = "anthropic"
lock_temperature = true
lock_top_p = true
lock_frequency_penalty = true
lock_presence_penalty = true
tool_choice_auto_only = true
max_stop_sequences = 1
strip_empty_content = true
no_json_schema = true
translate_max_to_xhigh = true
finish_reason_map = { sensitive = "content_filter" }
`
	cfg, err := Load([]byte(src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := cfg.Instances[0].Compat
	if c == nil {
		t.Fatal("Compat = nil")
	}
	boolChecks := []struct {
		name string
		got  *bool
		want bool
	}{
		{"SupportsReasoningEffort", c.SupportsReasoningEffort, true},
		{"ToolStream", c.ToolStream, true},
		{"SupportsStore", c.SupportsStore, true},
		{"SupportsDeveloperRole", c.SupportsDeveloperRole, true},
		{"SupportsUsageInStreaming", c.SupportsUsageInStreaming, false},
		{"RequiresToolResultName", c.RequiresToolResultName, true},
		{"RequiresAssistantAfterToolResult", c.RequiresAssistantAfterToolResult, true},
		{"RequiresThinkingAsText", c.RequiresThinkingAsText, true},
		{"RequiresReasoningContentOnAssistant", c.RequiresReasoningContentOnAssistant, true},
		{"LockTemperature", c.LockTemperature, true},
		{"LockTopP", c.LockTopP, true},
		{"LockFrequencyPenalty", c.LockFrequencyPenalty, true},
		{"LockPresencePenalty", c.LockPresencePenalty, true},
		{"ToolChoiceAutoOnly", c.ToolChoiceAutoOnly, true},
		{"StripEmptyContent", c.StripEmptyContent, true},
		{"NoJSONSchema", c.NoJSONSchema, true},
		{"TranslateMaxToXHigh", c.TranslateMaxToXHigh, true},
	}
	for _, bc := range boolChecks {
		if bc.got == nil || *bc.got != bc.want {
			t.Errorf("%s = %v, want %v", bc.name, bc.got, bc.want)
		}
	}
	if c.ThinkingFormat != "deepseek" {
		t.Errorf("ThinkingFormat = %q, want deepseek", c.ThinkingFormat)
	}
	if c.MaxTokensField != "max_completion_tokens" {
		t.Errorf("MaxTokensField = %q, want max_completion_tokens", c.MaxTokensField)
	}
	if c.CacheControlFormat != "anthropic" {
		t.Errorf("CacheControlFormat = %q, want anthropic", c.CacheControlFormat)
	}
	if c.MaxStopSequences == nil || *c.MaxStopSequences != 1 {
		t.Errorf("MaxStopSequences = %v, want 1", c.MaxStopSequences)
	}
	if c.FinishReasonMap["sensitive"] != "content_filter" {
		t.Errorf("FinishReasonMap = %v, want sensitive→content_filter", c.FinishReasonMap)
	}
}

// thinking_levels keys are normalized: uppercase folds down, "max" aliases to
// "xhigh" (serf's rank table treats them as one tier).
func TestLoadNormalizesThinkingLevelKeys(t *testing.T) {
	src := `
[instances.gw]
type = "glm"

[instances.gw.models."glm-x"]
thinking_levels = { LOW = "high", max = "max" }
`
	cfg, err := Load([]byte(src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	mc := cfg.Instances[0].Models["glm-x"]
	if got := mc.ThinkingLevels["low"]; got != "high" {
		t.Errorf("ThinkingLevels[low] = %q, want high (case-folded)", got)
	}
	if got := mc.ThinkingLevels["xhigh"]; got != "max" {
		t.Errorf("ThinkingLevels[xhigh] = %q, want max (max key aliases xhigh)", got)
	}
	if _, ok := mc.ThinkingLevels["max"]; ok {
		t.Error("ThinkingLevels kept raw 'max' key; want normalized away")
	}
}

func TestLoadRejectsInvalidCompatAndModels(t *testing.T) {
	cases := []struct {
		name    string
		src     string
		wantErr string
	}{
		{
			name: "unknown thinking_format",
			src: `
[instances.gw]
type = "glm"
[instances.gw.compat]
thinking_format = "banana"
`,
			wantErr: "thinking_format",
		},
		{
			name: "unknown max_tokens_field",
			src: `
[instances.gw]
type = "glm"
[instances.gw.compat]
max_tokens_field = "tokens"
`,
			wantErr: "max_tokens_field",
		},
		{
			name: "unknown cache_control_format",
			src: `
[instances.gw]
type = "glm"
[instances.gw.compat]
cache_control_format = "openai"
`,
			wantErr: "cache_control_format",
		},
		{
			name: "negative max_stop_sequences",
			src: `
[instances.gw]
type = "glm"
[instances.gw.compat]
max_stop_sequences = -1
`,
			wantErr: "max_stop_sequences",
		},
		{
			name: "unknown thinking level key",
			src: `
[instances.gw]
type = "glm"
[instances.gw.models."m"]
thinking_levels = { turbo = "high" }
`,
			wantErr: "thinking_levels",
		},
		{
			name: "off level key rejected",
			src: `
[instances.gw]
type = "glm"
[instances.gw.models."m"]
thinking_levels = { off = "none" }
`,
			wantErr: "off",
		},
		{
			name: "empty thinking level value",
			src: `
[instances.gw]
type = "glm"
[instances.gw.models."m"]
thinking_levels = { low = "" }
`,
			wantErr: "thinking_levels",
		},
		{
			name: "compat on anthropic instance",
			src: `
[instances.claude]
type = "anthropic"
[instances.claude.compat]
thinking_format = "zai"
`,
			wantErr: "compat",
		},
		{
			name: "models on openai responses instance",
			src: `
[instances.oai]
type = "openai"
api_style = "responses"
[instances.oai.models."gpt-x"]
context_window = 100
`,
			wantErr: "models",
		},
		{
			name: "negative context_window",
			src: `
[instances.gw]
type = "glm"
[instances.gw.models."m"]
context_window = -5
`,
			wantErr: "context_window",
		},
		{
			name: "negative max_output_tokens",
			src: `
[instances.gw]
type = "glm"
[instances.gw.models."m"]
max_output_tokens = -5
`,
			wantErr: "max_output_tokens",
		},
		{
			name: "empty model id",
			src: `
[instances.gw]
type = "glm"
[instances.gw.models.""]
context_window = 100
`,
			wantErr: "model",
		},
		{
			name: "max and xhigh keys collide",
			src: `
[instances.gw]
type = "glm"
[instances.gw.models."m"]
thinking_levels = { max = "max", xhigh = "ultra" }
`,
			wantErr: "duplicate",
		},
		{
			name: "case-folded duplicate level keys collide",
			src: `
[instances.gw]
type = "glm"
[instances.gw.models."m"]
thinking_levels = { LOW = "high", low = "medium" }
`,
			wantErr: "duplicate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load([]byte(tc.src))
			if err == nil {
				t.Fatalf("Load succeeded, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// Compat and models are accepted for every openai-compat-family type.
func TestLoadAcceptsCompatForCompatFamily(t *testing.T) {
	for _, typ := range []string{"kimi", "glm", "openrouter", "ollama"} {
		src := `
[instances.gw]
type = "` + typ + `"
[instances.gw.compat]
thinking_format = "openai"
[instances.gw.models."m"]
context_window = 1000
`
		if _, err := Load([]byte(src)); err != nil {
			t.Errorf("type %s: Load: %v", typ, err)
		}
	}
	// openai requires chat-completions style.
	src := `
[instances.gw]
type = "openai"
api_style = "chat-completions"
[instances.gw.compat]
thinking_format = "openai"
`
	if _, err := Load([]byte(src)); err != nil {
		t.Errorf("openai chat-completions: Load: %v", err)
	}
}

func TestMarshalRoundTripsCompatAndModels(t *testing.T) {
	cfg, err := Load([]byte(modelsFixtureToml))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	cfg2, err := Load(data)
	if err != nil {
		t.Fatalf("Load(Marshal): %v\n---\n%s", err, data)
	}
	in := cfg.Instances[0]
	out := cfg2.Instances[0]
	if out.Compat == nil || out.Compat.ThinkingFormat != in.Compat.ThinkingFormat {
		t.Errorf("round-trip lost instance compat: %+v", out.Compat)
	}
	if out.Compat.SupportsReasoningEffort == nil || *out.Compat.SupportsReasoningEffort {
		t.Errorf("round-trip lost supports_reasoning_effort=false: %+v", out.Compat)
	}
	mcIn := in.Models["glm-5.2-nvfp4"]
	mcOut, ok := out.Models["glm-5.2-nvfp4"]
	if !ok {
		t.Fatalf("round-trip lost model entry; got %v", out.Models)
	}
	if mcOut.ContextWindow != mcIn.ContextWindow {
		t.Errorf("round-trip ContextWindow = %d, want %d", mcOut.ContextWindow, mcIn.ContextWindow)
	}
	if mcOut.MaxOutputTokens != mcIn.MaxOutputTokens {
		t.Errorf("round-trip MaxOutputTokens = %d, want %d", mcOut.MaxOutputTokens, mcIn.MaxOutputTokens)
	}
	if mcOut.Reasoning == nil || !*mcOut.Reasoning {
		t.Errorf("round-trip Reasoning = %v, want true", mcOut.Reasoning)
	}
	if len(mcOut.ThinkingLevels) != len(mcIn.ThinkingLevels) {
		t.Errorf("round-trip ThinkingLevels = %v, want %v", mcOut.ThinkingLevels, mcIn.ThinkingLevels)
	}
	for k, v := range mcIn.ThinkingLevels {
		if mcOut.ThinkingLevels[k] != v {
			t.Errorf("round-trip ThinkingLevels[%q] = %q, want %q", k, mcOut.ThinkingLevels[k], v)
		}
	}
	if mcOut.Compat == nil || mcOut.Compat.MaxTokensField != "max_tokens" {
		t.Errorf("round-trip lost model compat: %+v", mcOut.Compat)
	}
	if mcOut.Compat.ToolStream == nil || !*mcOut.Compat.ToolStream {
		t.Errorf("round-trip lost tool_stream: %+v", mcOut.Compat)
	}
	// api_key round-trips through Marshal (the on-disk scrub/restore guard
	// lives in WriteFile, not here).
	if out.APIKey != in.APIKey {
		t.Errorf("round-trip APIKey = %q, want %q", out.APIKey, in.APIKey)
	}
}

func TestResolveAPIKey(t *testing.T) {
	t.Setenv("SERF_TEST_KEY_A", "sk-alpha")
	t.Setenv("SERF_TEST_KEY_B", "beta")
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr string
	}{
		{name: "literal", in: "sk-plain", want: "sk-plain"},
		{name: "empty", in: "", want: ""},
		{name: "bare var", in: "$SERF_TEST_KEY_A", want: "sk-alpha"},
		{name: "braced var", in: "${SERF_TEST_KEY_A}", want: "sk-alpha"},
		{name: "concatenated", in: "pre-${SERF_TEST_KEY_B}-post", want: "pre-beta-post"},
		{name: "escaped dollar", in: "pa$$word", want: "pa$word"},
		{name: "missing var", in: "$SERF_TEST_KEY_MISSING", wantErr: "SERF_TEST_KEY_MISSING"},
		{name: "trailing lone dollar", in: "abc$", want: "abc$"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAPIKey(tc.in)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveAPIKey(%q) = %q, want error containing %q", tc.in, got, tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not mention %q", err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveAPIKey(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ResolveAPIKey(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// A rewrite (hub edit/set-default/remove flows) must not destroy a
// hand-authored api_key: WriteFile restores whatever the on-disk file already
// carried, while struct-held keys (possibly injected from the credentials
// store) are scrubbed and never written.
func TestWriteFile_PreservesOnDiskAPIKeyAndScrubsInjected(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/providers.toml"
	src := `
default = "lunaroute"
[instances.lunaroute]
type = "openai"
api_style = "chat-completions"
base_url = "https://gw.example.com/v1"
api_key = "$LUNAROUTE_API_KEY"

[instances.literal]
type = "glm"
api_key = "sk-hand-authored"
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, exists, err := LoadFile(path)
	if err != nil || !exists {
		t.Fatalf("LoadFile: %v exists=%v", err, exists)
	}
	// Simulate a credentials-store injection on a mutated copy plus an edit.
	for i := range cfg.Instances {
		if cfg.Instances[i].Name == "lunaroute" {
			cfg.Instances[i].BaseURL = "https://gw2.example.com/v1"
		}
		if cfg.Instances[i].Name == "literal" {
			cfg.Instances[i].APIKey = "sk-INJECTED-FROM-CRED-STORE"
		}
	}
	if err := WriteFile(path, cfg); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	out, _ := os.ReadFile(path)
	text := string(out)
	if !strings.Contains(text, `api_key = "$LUNAROUTE_API_KEY"`) {
		t.Errorf("rewrite dropped the hand-authored $ENV api_key:\n%s", text)
	}
	if !strings.Contains(text, `api_key = "sk-hand-authored"`) {
		t.Errorf("rewrite dropped the hand-authored literal api_key:\n%s", text)
	}
	if strings.Contains(text, "sk-INJECTED-FROM-CRED-STORE") {
		t.Errorf("rewrite leaked a struct-held (injected) api_key:\n%s", text)
	}
	if !strings.Contains(text, `base_url = "https://gw2.example.com/v1"`) {
		t.Errorf("rewrite lost the edit itself:\n%s", text)
	}
	// The rewritten file must still load.
	if _, _, err := LoadFile(path); err != nil {
		t.Fatalf("rewritten file fails LoadFile: %v", err)
	}
}

// Explicitly-empty compat tables are overrides (clear the inherited value),
// distinct from absent fields (inherit). BurntSushi decodes `x = {}` to a
// non-nil empty map, so the distinction survives parsing.
func TestLoad_ExplicitEmptyCompatMapsSurvive(t *testing.T) {
	src := `
[instances.gw]
type = "glm"
[instances.gw.compat]
finish_reason_map = {}
chat_template_kwargs = {}
`
	cfg, err := Load([]byte(src))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c := cfg.Instances[0].Compat
	if c.FinishReasonMap == nil {
		t.Error("explicit empty finish_reason_map decoded to nil (indistinguishable from absent)")
	}
	if c.ChatTemplateKwargs == nil {
		t.Error("explicit empty chat_template_kwargs decoded to nil")
	}
	// And they round-trip through Marshal.
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	cfg2, err := Load(data)
	if err != nil {
		t.Fatalf("Load(Marshal): %v\n%s", err, data)
	}
	c2 := cfg2.Instances[0].Compat
	if c2 == nil || c2.FinishReasonMap == nil || c2.ChatTemplateKwargs == nil {
		t.Errorf("explicit empty maps lost in round-trip:\n%s", data)
	}
}
