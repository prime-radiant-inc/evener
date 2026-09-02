package registry

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const specExampleConfig = `
default = "groq"

[providers.groq]
api_key  = "$GROQ_API_KEY"
protocol = "openai-responses"

[providers.work]
base     = "openai"
base_url = "https://gw.example.com/v1"
protocol = "openai-chat"
surface  = "generic"
headers  = { "X-Portkey-Provider" = "openai" }
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.work.fields]
stream_options = false
[providers.work.models."glm-5.2-nvfp4"]
context_window    = 1048576
max_output_tokens = 131072
effort_values     = ["high", "max"]
thinking_format   = "zai"

[providers.local]
base     = "openai-compatible"
base_url = "http://localhost:8080/v1"
auth     = "none"

[providers.bedrock]
base = "amazon-bedrock"
[providers.bedrock.vars]
"AWS_REGION" = "us-east-1"

[providers.vertex]
base = "google-vertex-anthropic"
[providers.vertex.vars]
"GOOGLE_VERTEX_PROJECT"  = "my-project"
"GOOGLE_VERTEX_LOCATION" = "global"

[models."*gemini-3*"]
multimodal_tool_results = true
`

func TestParseConfig_SpecExample(t *testing.T) {
	l, err := ParseConfig([]byte(specExampleConfig))
	if err != nil {
		t.Fatal(err)
	}
	if l.Tag != LayerConfig || l.Default != "groq" {
		t.Fatalf("tag/default: %q %q", l.Tag, l.Default)
	}
	work := l.Providers["work"]
	if work.ID != "work" || work.Base != "openai" || work.Protocol != ProtocolOpenAIChat || work.Surface != SurfaceGeneric {
		t.Fatalf("work: %+v", work)
	}
	if work.Transport.BaseURL != "https://gw.example.com/v1" || work.Headers["X-Portkey-Provider"] != "openai" || work.CredentialHeaders["Authorization"] != "Bearer $PORTKEY_KEY" {
		t.Fatalf("work transport/headers: %+v", work)
	}
	if v, ok := work.Caps.Fields["stream_options"]; !ok || v {
		t.Fatalf("work fields: %v", work.Caps.Fields)
	}
	row := work.Models["glm-5.2-nvfp4"]
	if row.ID != "glm-5.2-nvfp4" || *row.Caps.ContextWindow != 1048576 || *row.Caps.MaxOutputTokens != 131072 || !reflect.DeepEqual(row.Caps.EffortValues, []string{"high", "max"}) || *row.Caps.ThinkingFormat != "zai" {
		t.Fatalf("row: %+v", row)
	}
	if l.Providers["local"].Transport.Auth != AuthNone {
		t.Fatalf("local auth: %+v", l.Providers["local"].Transport)
	}
	if l.Providers["bedrock"].Transport.Vars["AWS_REGION"] != "us-east-1" || l.Providers["vertex"].Transport.Vars["GOOGLE_VERTEX_LOCATION"] != "global" {
		t.Fatal("vars not decoded")
	}
	if g, ok := l.TopGlobs["*gemini-3*"]; !ok || g.Caps.MultimodalToolResults == nil || !*g.Caps.MultimodalToolResults {
		t.Fatalf("top-level glob: %+v", l.TopGlobs)
	}
	if l.Providers["groq"].APIKeyEnv != nil {
		t.Fatal("absent api_key_env must stay nil")
	}
}

func TestParseConfig_ExplicitEmptyAPIKeyEnv(t *testing.T) {
	l, err := ParseConfig([]byte("[providers.x]\napi_key_env = []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l.Providers["x"].APIKeyEnv == nil || len(l.Providers["x"].APIKeyEnv) != 0 {
		t.Fatalf("api_key_env = [] must be a non-nil empty slice, got %#v", l.Providers["x"].APIKeyEnv)
	}
}

func TestParseConfig_ThinkingDisplayValues(t *testing.T) {
	l, err := ParseConfig([]byte("[providers.x.models.m]\nthinking_display = \"\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	row := l.Providers["x"].Models["m"]
	if row.Caps.ThinkingDisplay == nil || *row.Caps.ThinkingDisplay != "" {
		t.Fatalf("thinking_display = \"\": %+v", row.Caps.ThinkingDisplay)
	}

	l, err = ParseConfig([]byte("[providers.x.models.m]\nthinking_display = \"summarized\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	row = l.Providers["x"].Models["m"]
	if row.Caps.ThinkingDisplay == nil || *row.Caps.ThinkingDisplay != "summarized" {
		t.Fatalf("thinking_display = \"summarized\": %+v", row.Caps.ThinkingDisplay)
	}
}

func TestParseConfig_Rejects(t *testing.T) {
	cases := map[string]string{
		"unknown key":              "[providers.x]\nthinking_levels = { a = \"b\" }\n",
		"curated-only implicit":    "[providers.x]\nimplicit = true\n",
		"curated-only name":        "[providers.x]\nname = \"X\"\n",
		"curated-only doc":         "[providers.x]\ndoc = \"https://x\"\n",
		"curated-only order":       "default_order = [\"x\"]\n[providers.x]\n",
		"curated-only transports":  "[transports.t]\nauth = \"bearer\"\n",
		"uppercase name":           "[providers.Work]\n",
		"slash in name":            "[providers.\"a/b\"]\n",
		"bad protocol":             "[providers.x]\nprotocol = \"grpc\"\n",
		"bad surface":              "[providers.x]\nsurface = \"claude\"\n",
		"bad auth":                 "[providers.x]\nauth = \"sigv4\"\n",
		"bad host rule":            "[providers.x]\nhost_rule = \"magic\"\n",
		"bad thinking_format":      "[providers.x]\nthinking_format = \"claude\"\n",
		"bad thinking_shape":       "[providers.x.models.m]\nthinking_shape = \"effort\"\n",
		"bad thinking_display":     "[providers.x.models.m]\nthinking_display = \"verbose\"\n",
		"bad max_tokens_field":     "[providers.x]\nmax_tokens_field = \"max_len\"\n",
		"bad cache_control":        "[providers.x]\ncache_control = \"openai\"\n",
		"bad reasoning_field":      "[providers.x]\nreasoning_field = \"thoughts\"\n",
		"bad image_detail":         "[providers.x]\nimage_detail = \"medium\"\n",
		"bad reasoning_summary":    "[providers.x]\nreasoning_summary = \"full\"\n",
		"bad reasoning control":    "[providers.x.models.m]\nreasoning_controls = [\"levels\"]\n",
		"effort off":               "[providers.x.models.m]\neffort_values = [\"off\"]\n",
		"effort empty entry":       "[providers.x.models.m]\neffort_values = [\"\"]\n",
		"default effort typo":      "[providers.x]\ndefault_effort = \"ultra\"\n",
		"default effort typo row":  "[providers.x.models.m]\ndefault_effort = \"ultra\"\n",
		"default effort alias":     "[providers.x.models.m]\ndefault_effort = \"off\"\n",
		"default effort casing":    "[providers.x.models.m]\ndefault_effort = \"High\"\n",
		"top-level exact model":    "[models.\"gpt-5\"]\ncontext_window = 1\n",
		"unterminated env ref":     "[providers.x]\napi_key = \"${OPEN\"\n",
		"bad env name in header":   "[providers.x]\ncredential_headers = { \"Authorization\" = \"Bearer ${1X}\" }\n",
		"preset inside transports": "[transports.t]\ntransport = \"other\"\n",
		"protocol on glob row":     "[providers.x.models.\"g*\"]\nprotocol = \"anthropic\"\n",
		"preset on glob row":       "[providers.x.models.\"g*\"]\ntransport = \"vertex-anthropic\"\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(src))
			if err == nil {
				t.Fatalf("expected an error for %s", name)
			}
			if errors.Is(err, ErrOldSchema) {
				t.Fatalf("must not be reported as the old schema: %v", err)
			}
		})
	}
}

func TestParseConfig_TokenCapsMustBePositiveIntegers(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		field string
	}{
		{name: "context_window zero", src: "[providers.x.models.m]\ncontext_window = 0\n", field: "context_window"},
		{name: "max_input_tokens zero", src: "[providers.x.models.m]\nmax_input_tokens = 0\n", field: "max_input_tokens"},
		{name: "max_output_tokens zero", src: "[providers.x.models.m]\nmax_output_tokens = 0\n", field: "max_output_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tc.src))
			if err == nil {
				t.Fatalf("expected an error for %s", tc.field)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.field) || !strings.Contains(msg, "positive") {
				t.Fatalf("error = %q, want a field-specific positive-integer error", msg)
			}
		})
	}
}

func TestParseConfig_UnknownKeyNamesIt(t *testing.T) {
	_, err := ParseConfig([]byte("[providers.x]\nthinking_levels = { a = \"b\" }\n"))
	if err == nil || !strings.Contains(err.Error(), "thinking_levels") {
		t.Fatalf("error must name the key: %v", err)
	}
}

// TestParseConfig_ChatTemplateKwargsNestedValueParses pins a BurntSushi/toml
// limitation the typo guard must not misreport: decoding a table into
// map[string]any never marks a key nested inside that value as decoded (the
// toml package's own doc for Undecoded says so), so without an exemption a
// legitimate nested chat_template_kwargs value reads as an "unknown key".
// The second half confirms the exemption is narrow: a real typo elsewhere
// in the same document is still caught.
func TestParseConfig_ChatTemplateKwargsNestedValueParses(t *testing.T) {
	l, err := ParseConfig([]byte("[providers.x.chat_template_kwargs]\nenable_thinking = true\n[providers.x.chat_template_kwargs.options]\nmode = \"fast\"\n"))
	if err != nil {
		t.Fatalf("a nested chat_template_kwargs value must parse, not read as an unknown key: %v", err)
	}
	want := map[string]any{"enable_thinking": true, "options": map[string]any{"mode": "fast"}}
	if !reflect.DeepEqual(l.Providers["x"].Caps.ChatTemplateKwargs, want) {
		t.Fatalf("chat_template_kwargs: got %+v want %+v", l.Providers["x"].Caps.ChatTemplateKwargs, want)
	}
	_, err = ParseConfig([]byte("[providers.x]\ntypo_key = 1\n[providers.x.chat_template_kwargs]\nenable_thinking = true\n"))
	if err == nil || !strings.Contains(err.Error(), "typo_key") {
		t.Fatalf("a real typo must still be rejected even alongside chat_template_kwargs: %v", err)
	}
}

// TestParseConfig_ChatTemplateKwargsExemptionIsPositionScoped pins that the
// Undecoded() typo-guard exemption for chat_template_kwargs applies only
// where Caps actually lives — providers.<name>, providers.<name>.models.
// <id>, and the top-level models.<glob> — not to any key path that merely
// contains the segment "chat_template_kwargs" somewhere. A typo'd
// intermediate table name, or a bare top-level chat_template_kwargs table
// that no schema owns, must still produce the unknown-key error (spec §10).
func TestParseConfig_ChatTemplateKwargsExemptionIsPositionScoped(t *testing.T) {
	cases := []struct {
		name  string
		src   string
		check func(t *testing.T, l *Layer, err error)
	}{
		{
			"typo'd intermediate table before chat_template_kwargs still errors",
			"[providers.x.nonexistent_table.chat_template_kwargs]\nmode = \"a\"\n",
			func(t *testing.T, l *Layer, err error) {
				if err == nil || !strings.Contains(err.Error(), "nonexistent_table") {
					t.Fatalf("want an unknown-key error naming nonexistent_table, got %v", err)
				}
			},
		},
		{
			"bare top-level chat_template_kwargs table errors",
			"[chat_template_kwargs]\nmode = \"a\"\n",
			func(t *testing.T, l *Layer, err error) {
				if err == nil {
					t.Fatal("want an unknown-key error, got nil")
				}
			},
		},
		{
			"model row chat_template_kwargs is accepted and decoded",
			"[providers.x.models.\"m\".chat_template_kwargs.options]\nmode = \"a\"\n",
			func(t *testing.T, l *Layer, err error) {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				want := map[string]any{"options": map[string]any{"mode": "a"}}
				got := l.Providers["x"].Models["m"].Caps.ChatTemplateKwargs
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("chat_template_kwargs: got %+v want %+v", got, want)
				}
			},
		},
		{
			"top-level glob row chat_template_kwargs is accepted and decoded",
			"[models.\"g*\".chat_template_kwargs]\nmode = \"a\"\n",
			func(t *testing.T, l *Layer, err error) {
				if err != nil {
					t.Fatalf("want no error, got %v", err)
				}
				want := map[string]any{"mode": "a"}
				got := l.TopGlobs["g*"].Caps.ChatTemplateKwargs
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("chat_template_kwargs: got %+v want %+v", got, want)
				}
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l, err := ParseConfig([]byte(c.src))
			c.check(t, l, err)
		})
	}
}

func TestParseConfig_OldSchema(t *testing.T) {
	for _, src := range []string{
		"[instances.openai]\ntype = \"openai\"\n",
		"[providers.openai]\ntype = \"openai\"\n",
		"[providers.openai]\napi_style = \"responses\"\n",
		"[providers.openai]\nquirks = \"x\"\n",
		"[providers.openai]\ncompat = { a = 1 }\n",
	} {
		_, err := ParseConfig([]byte(src))
		if !errors.Is(err, ErrOldSchema) {
			t.Fatalf("%q: want ErrOldSchema, got %v", src, err)
		}
		if !strings.Contains(err.Error(), "§14.1") {
			t.Fatalf("old-schema error must point at the spec: %v", err)
		}
	}
}

func TestParseOverlay_CuratedKeys(t *testing.T) {
	src := `
default_order = ["a", "b"]
[transports.t]
auth = "gcp-adc"
endpoint = "/x/{model}:rawPredict"
body = { anthropic_version = "vertex-2023-10-16" }
[providers.a]
implicit = true
name = "A"
doc = "https://a"
transport = "t"
[providers.b]
implicit = true
[providers.c]
`
	l, err := ParseOverlay([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if l.Tag != LayerOverlay || !reflect.DeepEqual(l.DefaultOrder, []string{"a", "b"}) {
		t.Fatalf("overlay: %+v", l)
	}
	tr := l.Transports["t"]
	if tr.Auth != AuthGCPADC || tr.Endpoint != "/x/{model}:rawPredict" || tr.Body["anthropic_version"] != "vertex-2023-10-16" {
		t.Fatalf("transport: %+v", tr)
	}
	a := l.Providers["a"]
	if a.Implicit == nil || !*a.Implicit || a.Name != "A" || a.Doc != "https://a" || a.Transport.Preset != "t" {
		t.Fatalf("provider a: %+v", a)
	}
	if l.Providers["c"].Implicit != nil {
		t.Fatal("implicit must stay nil when unset")
	}
}

func TestParseOverlay_DefaultOrderMustBeImplicit(t *testing.T) {
	_, err := ParseOverlay([]byte("default_order = [\"a\"]\n[providers.a]\n"))
	if err == nil || !strings.Contains(err.Error(), "default_order") {
		t.Fatalf("want default_order error, got %v", err)
	}
	_, err = ParseOverlay([]byte("default_order = [\"zzz\"]\n[providers.a]\nimplicit = true\n"))
	if err == nil {
		t.Fatal("unknown default_order entry must error")
	}
}

func TestParseConfig_ModelRowKeysAndTransport(t *testing.T) {
	src := `
[providers.azure.models."claude-prod"]
alias_of = "claude-opus-4-5"
wire_id  = "claude-prod-deploy"
family   = "claude-opus"
protocol = "anthropic"
surface  = "anthropic"
headers  = { "anthropic-beta" = "x" }
base_url = "https://{AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1"
[providers.azure.models."gpt-5*"]
reasoning_summary = "detailed"
`
	l, err := ParseConfig([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	row := l.Providers["azure"].Models["claude-prod"]
	if row.AliasOf != "claude-opus-4-5" || row.WireID != "claude-prod-deploy" || row.Family != "claude-opus" || row.Protocol != ProtocolAnthropic || row.Surface != SurfaceAnthropic || row.Headers["anthropic-beta"] != "x" {
		t.Fatalf("row: %+v", row)
	}
	if row.Transport == nil || row.Transport.BaseURL != "https://{AZURE_RESOURCE_NAME}.services.ai.azure.com/anthropic/v1" {
		t.Fatalf("row transport: %+v", row.Transport)
	}
	glob := l.Providers["azure"].Models["gpt-5*"]
	if glob.Transport != nil || *glob.Caps.ReasoningSummary != "detailed" {
		t.Fatalf("glob row: %+v", glob)
	}
	if l.Providers["azure"].Models["gpt-5*"].ID != "gpt-5*" {
		t.Fatal("row ID must equal its key, globs included")
	}
}

// Every level the session vocabulary accepts is a legal default_effort, on a
// provider row and a model row alike; a typo is a parse refusal, so it can
// never reach a provider the way an unvalidated one would (the clamp passes an
// unrankable level through untouched).
func TestParseConfig_DefaultEffortVocabulary(t *testing.T) {
	for _, level := range []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"} {
		l, err := ParseConfig([]byte("[providers.x]\ndefault_effort = \"" + level + "\"\n[providers.x.models.m]\ndefault_effort = \"" + level + "\"\n"))
		if err != nil {
			t.Fatalf("default_effort = %q: %v", level, err)
		}
		if got := l.Providers["x"].Caps.DefaultEffort; got == nil || *got != level {
			t.Fatalf("provider default_effort = %v, want %q", got, level)
		}
		if got := l.Providers["x"].Models["m"].Caps.DefaultEffort; got == nil || *got != level {
			t.Fatalf("row default_effort = %v, want %q", got, level)
		}
	}
	_, err := ParseConfig([]byte("[providers.x]\ndefault_effort = \"ultra\"\n"))
	if err == nil || !strings.Contains(err.Error(), "default_effort") || !strings.Contains(err.Error(), "xhigh") {
		t.Fatalf("a typo must be refused by name and list the vocabulary: %v", err)
	}
}

func TestParseConfig_SchemaTwoAccepted(t *testing.T) {
	if _, err := ParseConfig([]byte("schema = 2\n\n[providers.openai]\n")); err != nil {
		t.Fatalf("schema = 2 must parse: %v", err)
	}
}

func TestParseConfig_SchemaOneIsOldSchema(t *testing.T) {
	_, err := ParseConfig([]byte("schema = 1\n\n[providers.openai]\n"))
	if !errors.Is(err, ErrOldSchema) {
		t.Fatalf("schema = 1: want ErrOldSchema, got %v", err)
	}
	if !strings.Contains(err.Error(), "§14.1") {
		t.Fatalf("old-schema error must point at the spec: %v", err)
	}
}

func TestParseConfig_UnsupportedSchemaNamesIt(t *testing.T) {
	_, err := ParseConfig([]byte("schema = 3\n"))
	if err == nil || !strings.Contains(err.Error(), "unsupported schema 3") || !strings.Contains(err.Error(), "2") {
		t.Fatalf("schema = 3 must name the unsupported version and the supported one: %v", err)
	}
}

func TestParseConfig_OldSchemaErrorNamesMigrate(t *testing.T) {
	_, err := ParseConfig([]byte("[instances.kimi]\ntype = \"kimi\"\n"))
	if !errors.Is(err, ErrOldSchema) {
		t.Fatalf("want ErrOldSchema, got %v", err)
	}
	if !strings.Contains(err.Error(), "evener migrate") {
		t.Fatalf("old-schema error must point at evener migrate: %v", err)
	}
}
