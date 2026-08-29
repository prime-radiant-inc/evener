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
		"bad max_tokens_field":     "[providers.x]\nmax_tokens_field = \"max_len\"\n",
		"bad cache_control":        "[providers.x]\ncache_control = \"openai\"\n",
		"bad reasoning_field":      "[providers.x]\nreasoning_field = \"thoughts\"\n",
		"bad image_detail":         "[providers.x]\nimage_detail = \"medium\"\n",
		"bad reasoning_summary":    "[providers.x]\nreasoning_summary = \"full\"\n",
		"bad reasoning control":    "[providers.x.models.m]\nreasoning_controls = [\"levels\"]\n",
		"effort off":               "[providers.x.models.m]\neffort_values = [\"off\"]\n",
		"effort empty entry":       "[providers.x.models.m]\neffort_values = [\"\"]\n",
		"top-level exact model":    "[models.\"gpt-5\"]\ncontext_window = 1\n",
		"unterminated env ref":     "[providers.x]\napi_key = \"${OPEN\"\n",
		"bad env name in header":   "[providers.x]\ncredential_headers = { \"Authorization\" = \"Bearer ${1X}\" }\n",
		"preset inside transports": "[transports.t]\ntransport = \"other\"\n",
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

func TestParseConfig_UnknownKeyNamesIt(t *testing.T) {
	_, err := ParseConfig([]byte("[providers.x]\nthinking_levels = { a = \"b\" }\n"))
	if err == nil || !strings.Contains(err.Error(), "thinking_levels") {
		t.Fatalf("error must name the key: %v", err)
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
