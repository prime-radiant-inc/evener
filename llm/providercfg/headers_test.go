package providercfg

import (
	"strings"
	"testing"
)

func TestResolveHeaderValue(t *testing.T) {
	t.Setenv("SERF_TEST_HDR_A", "secret-token")
	cases := []struct {
		name    string
		key     string
		in      string
		want    string
		wantErr []string // substrings the error must contain
	}{
		{name: "literal", key: "X-Api-Key", in: "plain", want: "plain"},
		{name: "empty", key: "X-Api-Key", in: "", want: ""},
		{name: "bare var", key: "Authorization", in: "$SERF_TEST_HDR_A", want: "secret-token"},
		{name: "braced var", key: "Authorization", in: "Bearer ${SERF_TEST_HDR_A}", want: "Bearer secret-token"},
		{name: "escaped dollar", key: "X-Cost", in: "pa$$word", want: "pa$word"},
		{name: "missing var names header and var", key: "X-Portkey-Key", in: "$SERF_TEST_HDR_MISSING", wantErr: []string{"X-Portkey-Key", "SERF_TEST_HDR_MISSING"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveHeaderValue(tc.key, tc.in)
			if len(tc.wantErr) > 0 {
				if err == nil {
					t.Fatalf("ResolveHeaderValue(%q, %q) = %q, want error", tc.key, tc.in, got)
				}
				for _, sub := range tc.wantErr {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q does not mention %q", err.Error(), sub)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveHeaderValue(%q, %q): %v", tc.key, tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ResolveHeaderValue(%q, %q) = %q, want %q", tc.key, tc.in, got, tc.want)
			}
		})
	}
}

// TestResolveAPIKey_UnchangedBehavior guards that the shared-core refactor keeps
// ResolveAPIKey's error text keyed on "api_key".
func TestResolveAPIKey_ErrorNamesApiKey(t *testing.T) {
	_, err := ResolveAPIKey("$SERF_TEST_HDR_STILL_MISSING")
	if err == nil {
		t.Fatal("expected error for missing var")
	}
	if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("ResolveAPIKey error %q must still mention api_key", err.Error())
	}
}

func TestLoad_Headers_AnyType(t *testing.T) {
	// Headers are valid for ALL instance types, not just the compat family.
	data := []byte(`
default = "ant"
[instances.ant]
type = "anthropic"
headers = { "X-Gateway" = "portkey", "Authorization" = "$MY_KEY" }
`)
	cfg, err := Load(data)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	h := cfg.Instances[0].Headers
	if h["X-Gateway"] != "portkey" {
		t.Errorf("X-Gateway = %q, want portkey", h["X-Gateway"])
	}
	// Raw $ENV form is preserved at Load — resolution happens later.
	if h["Authorization"] != "$MY_KEY" {
		t.Errorf("Authorization = %q, want raw $MY_KEY", h["Authorization"])
	}
}

func TestLoad_Headers_EmptyName_Rejected(t *testing.T) {
	data := []byte(`
[instances.x]
type = "openai"
headers = { "" = "value" }
`)
	_, err := Load(data)
	if err == nil {
		t.Fatal("expected error for empty header name")
	}
	if !strings.Contains(err.Error(), "header name") {
		t.Errorf("error %q must mention empty header name", err.Error())
	}
}

func TestMarshal_Headers_RoundTrip(t *testing.T) {
	cfg := Config{Default: "gw", Instances: []InstanceConfig{
		{Name: "gw", Type: "anthropic", Headers: map[string]string{
			"X-Gateway":     "helicone",
			"Authorization": "$HELICONE_KEY", // raw $ENV round-trips (unlike api_key)
		}},
	}}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Unlike api_key, header raw values are written back verbatim.
	if !strings.Contains(string(data), "$HELICONE_KEY") {
		t.Fatalf("Marshal must round-trip raw header value:\n%s", data)
	}
	got, err := Load(data)
	if err != nil {
		t.Fatalf("Load(Marshal): %v\n%s", err, data)
	}
	h := got.Instances[0].Headers
	if h["X-Gateway"] != "helicone" || h["Authorization"] != "$HELICONE_KEY" {
		t.Fatalf("round-trip lost headers: %+v", h)
	}
}

func TestLoad_ThinkingFormat_ChatTemplateVariants(t *testing.T) {
	data := []byte(`
[instances.qwen]
type = "ollama"
[instances.qwen.compat]
thinking_format = "qwen-chat-template"

[instances.other]
type = "ollama"
[instances.other.compat]
thinking_format = "chat-template"
chat_template_kwargs = { enable_thinking = true, thinking_budget = 2048 }
`)
	cfg, err := Load(data)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var other *InstanceConfig
	for i := range cfg.Instances {
		if cfg.Instances[i].Name == "other" {
			other = &cfg.Instances[i]
		}
	}
	if other == nil || other.Compat == nil {
		t.Fatal("missing other instance compat")
	}
	if other.Compat.ChatTemplateKwargs["enable_thinking"] != true {
		t.Errorf("enable_thinking = %v, want true", other.Compat.ChatTemplateKwargs["enable_thinking"])
	}
}

func TestMarshal_CompatStrictAndChatTemplate_RoundTrip(t *testing.T) {
	tru := true
	cfg := Config{Default: "x", Instances: []InstanceConfig{
		{Name: "x", Type: "ollama", Compat: &CompatConfig{
			SupportsStrictMode: &tru,
			ThinkingFormat:     "chat-template",
			ChatTemplateKwargs: map[string]any{"enable_thinking": true, "thinking_budget": int64(2048)},
		}},
	}}
	data, err := Marshal(cfg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got, err := Load(data)
	if err != nil {
		t.Fatalf("Load(Marshal): %v\n%s", err, data)
	}
	c := got.Instances[0].Compat
	if c == nil || c.SupportsStrictMode == nil || !*c.SupportsStrictMode {
		t.Fatalf("round-trip lost supports_strict_mode: %+v", c)
	}
	if c.ChatTemplateKwargs["enable_thinking"] != true {
		t.Errorf("round-trip enable_thinking = %v", c.ChatTemplateKwargs["enable_thinking"])
	}
	if c.ChatTemplateKwargs["thinking_budget"] != int64(2048) {
		t.Errorf("round-trip thinking_budget = %#v, want int64(2048)", c.ChatTemplateKwargs["thinking_budget"])
	}
}

// Two spellings of the same HTTP header name must be rejected at load —
// header names are case-insensitive on the wire, and letting both through
// would leave the surviving value to map iteration order.
func TestLoad_Headers_CaseCollisionRejected(t *testing.T) {
	src := `
[instances.gw]
type = "glm"
headers = { "Authorization" = "a", "authorization" = "b" }
`
	_, err := Load([]byte(src))
	if err == nil {
		t.Fatal("Load accepted case-colliding header names")
	}
	if !strings.Contains(err.Error(), "case-insensitive") {
		t.Errorf("error %q should explain the case-insensitivity collision", err)
	}
}
