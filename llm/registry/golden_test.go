package registry

import (
	"bytes"
	"encoding/json"
	"flag"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/golden files")

const goldenConfig = `
default = "anthropic"

[providers."groq-responses"]
base = "groq"
protocol = "openai-responses"

[providers.work]
base = "openai"
base_url = "https://gw.example.com/v1"
protocol = "openai-chat"
surface = "generic"
headers = { "X-Portkey-Provider" = "openai" }
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.work.fields]
stream_options = false
[providers.work.models."glm-5.2-nvfp4"]
context_window = 1048576
max_output_tokens = 131072
effort_values = ["high", "max"]
thinking_format = "zai"

[providers.local]
base = "openai-compatible"
base_url = "http://localhost:8080/v1"
auth = "none"

[providers.bedrock]
base = "amazon-bedrock"
[providers.bedrock.vars]
"AWS_REGION" = "us-east-1"

[providers.vertex]
base = "google-vertex-anthropic"
[providers.vertex.vars]
"GOOGLE_VERTEX_PROJECT" = "my-project"
"GOOGLE_VERTEX_LOCATION" = "global"

[providers.azure]
[providers.azure.vars]
"AZURE_RESOURCE_NAME" = "contoso-prod"
[providers.azure.models."gpt55-prod"]
alias_of = "gpt-5.5"
[providers.azure.models."claude-prod"]
alias_of = "claude-opus-4-5"

[providers.orclaude]
base = "openrouter"
protocol = "anthropic"
[providers.orclaude.models."minimax/*"]
surface = "anthropic"

[providers.ollama.models."qwen3*"]
context_window = 40960
`

// goldenSecrets are the credential values of the golden environment; the
// test asserts none of them ever appears in a serialized Resolved record.
var goldenSecrets = map[string]string{
	"ANTHROPIC_API_KEY": "SECRET-anthropic", "OPENAI_API_KEY": "SECRET-openai", "GROQ_API_KEY": "SECRET-groq",
	"OPENROUTER_API_KEY": "SECRET-openrouter", "KIMI_API_KEY": "SECRET-kimi", "MINIMAX_API_KEY": "SECRET-minimax",
	"MOONSHOT_API_KEY": "SECRET-moonshot", "AZURE_API_KEY": "SECRET-azure", "AWS_BEARER_TOKEN_BEDROCK": "SECRET-bedrock",
	"PORTKEY_KEY": "SECRET-portkey", "XAI_API_KEY": "SECRET-xai",
}

var goldenEnv = map[string]string{
	"OPENAI_ORG_ID": "org-golden", "GOOGLE_VERTEX_PROJECT": "my-project", "GOOGLE_VERTEX_LOCATION": "global", "OLLAMA_HOST": "localhost",
}

type goldenView struct {
	Resolved
	CredentialSource string   `json:"credential_source"`
	PrunedFields     []string `json:"pruned_fields"`
}

func goldenRegistry(t *testing.T, extraEnv map[string]string) *Registry {
	t.Helper()
	home := t.TempDir()
	adc := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	_ = os.MkdirAll(filepath.Dir(adc), 0o700)
	_ = os.WriteFile(adc, []byte("{}"), 0o600)
	state := t.TempDir()
	_ = os.MkdirAll(filepath.Join(state, "auth"), 0o700)
	_ = os.WriteFile(oauthRecordPath(state, "openai-codex"), []byte("{}"), 0o600)
	env := map[string]string{"HOME": home}
	maps.Copy(env, goldenEnv)
	maps.Copy(env, goldenSecrets)
	maps.Copy(env, extraEnv)
	r := fixtureLoad(t, env, goldenConfig, WithStateRoot(state))
	r.ApplyLive("ollama", []Model{{ID: "llama3:8b", Caps: Caps{ContextWindow: new(8192)}}, {ID: "qwen3:8b"}})
	return r
}

func prunedFields(c Caps) []string {
	var out []string
	for k, v := range c.Fields {
		if !v {
			out = append(out, k)
		}
	}
	slices.Sort(out)
	return out
}

func sp(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func ip(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func bp(v *bool) string {
	if v == nil {
		return "nil"
	}
	if *v {
		return "true"
	}
	return "false"
}

func TestGoldenResolved(t *testing.T) {
	cases := []struct {
		name  string
		ref   string
		env   map[string]string
		check func(t *testing.T, res Resolved)
	}{
		{"groq-chat", "groq/openai/gpt-oss-120b", nil, func(t *testing.T, res Resolved) {
			if res.Protocol != ProtocolOpenAIChat || res.Transport.Endpoint != "/chat/completions" || res.Surface != SurfaceGeneric {
				t.Errorf("%+v", res)
			}
		}},
		{"groq-responses", "groq-responses/openai/gpt-oss-120b", nil, func(t *testing.T, res Resolved) {
			if res.Protocol != ProtocolOpenAIResponses || res.Transport.Endpoint != "/responses" || res.Caps.Fields["store"] || res.Caps.Fields["include"] || res.Caps.StrictTools != nil {
				t.Errorf("groq responses must stay at the baseline: %+v", res.Caps.Fields)
			}
		}},
		{"openai-gpt-5.5", "openai/gpt-5.5", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ImageDetail) != "original" || sp(res.Caps.ReasoningSummary) != "detailed" || !res.Caps.Fields["prompt_cache_retention"] || sp(res.Caps.MaxTokensField) != "max_completion_tokens" || res.Headers["OpenAI-Organization"] != "org-golden" {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"openai-gpt-5.6", "openai/gpt-5.6", nil, func(t *testing.T, res Resolved) {
			if bp(res.Caps.ThinkingAlwaysOn) != "true" || sp(res.Caps.ImageDetail) != "omit" || res.Caps.Fields["prompt_cache_retention"] || !res.Caps.Fields["prompt_cache_key"] {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"openai-gpt-5.5-proxy", "openai/gpt-5.5", map[string]string{"OPENAI_BASE_URL": "https://proxy.example/v1"}, func(t *testing.T, res Resolved) {
			if res.Transport.BaseURL != "https://proxy.example/v1" || res.Credential.Source != "env:OPENAI_API_KEY" || res.Credential.Value != goldenSecrets["OPENAI_API_KEY"] {
				t.Errorf("override must keep the inherited credential: %+v %+v", res.Transport, res.Credential)
			}
		}},
		{"openai-codex-gpt-5.6", "openai-codex/gpt-5.6", nil, func(t *testing.T, res Resolved) {
			if res.WireID != "gpt-5.6-sol" || res.Transport.Auth != AuthOAuthOpenAICodex || res.Credential.Source != "oauth" || bp(res.Caps.Sampling) != "false" || ip(res.Caps.ContextWindow) != 272000 || res.Caps.Cost == nil || res.Transport.Body["text.verbosity"] != "low" {
				t.Errorf("%+v", res)
			}
			for _, k := range []string{"temperature", "top_p", "max_output_tokens", "previous_response_id", "truncation"} {
				if res.Caps.Fields[k] {
					t.Errorf("codex off-list %s must stay off", k)
				}
			}
			if _, ok := res.Headers["OpenAI-Organization"]; ok || len(res.Headers) != 0 {
				t.Errorf("codex header set must be empty at this layer (the authenticator adds its own): %v", res.Headers)
			}
		}},
		{"anthropic-sonnet-4-5", "anthropic/claude-sonnet-4-5", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "budget" || res.Caps.ThinkingAlwaysOn != nil || ip(res.Caps.ContextWindow) != 200000 {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"anthropic-sonnet-4-5-1m", "anthropic/claude-sonnet-4-5[1m]", nil, func(t *testing.T, res Resolved) {
			// GA 1M window: the row sends no beta header (see the overlay).
			if res.WireID != "claude-sonnet-4-5" || ip(res.Caps.ContextWindow) != 1000000 || res.Headers["anthropic-beta"] != "" || sp(res.Caps.ThinkingShape) != "budget" {
				t.Errorf("%+v", res)
			}
		}},
		{"anthropic-opus-4-6-1m-unknown", "anthropic/claude-opus-4-6[1m]", nil, func(t *testing.T, res Resolved) {
			if !strings.Contains(strings.Join(res.Warnings, ";"), "model not in catalog") || res.WireID != "claude-opus-4-6[1m]" {
				t.Errorf("%+v", res)
			}
		}},
		{"anthropic-haiku-4-5", "anthropic/claude-haiku-4-5", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "budget" {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"anthropic-opus-4-6", "anthropic/claude-opus-4-6", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "adaptive" || bp(res.Caps.ThinkingAlwaysOn) != "true" || res.Caps.ThinkingDisplay != nil {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"anthropic-opus-4-7", "anthropic/claude-opus-4-7", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "adaptive" || sp(res.Caps.ThinkingDisplay) != "summarized" || bp(res.Caps.Sampling) != "false" {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"anthropic-opus-4-5", "anthropic/claude-opus-4-5", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "budget+effort" || res.Caps.ThinkingAlwaysOn != nil || res.Caps.Sampling != nil {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"anthropic-opus-4-5-1m", "anthropic/claude-opus-4-5[1m]", nil, func(t *testing.T, res Resolved) {
			if res.Caps.Sampling != nil || sp(res.Caps.ThinkingShape) != "budget+effort" || ip(res.Caps.ContextWindow) != 1000000 {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"anthropic-opus-5", "anthropic/claude-opus-5", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "adaptive" || sp(res.Caps.ThinkingDisplay) != "summarized" || !reflect.DeepEqual(res.Caps.EffortValues, []string{"low", "medium", "high", "xhigh", "max"}) {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"anthropic-mythos-5", "anthropic/claude-mythos-5", nil, func(t *testing.T, res Resolved) {
			if res.Transport.BaseURL != "https://api.anthropic.com/v1" || sp(res.Caps.ThinkingDisplay) != "summarized" || bp(res.Caps.Sampling) != "false" || res.Caps.Cost == nil {
				t.Errorf("%+v", res)
			}
		}},
		{"anthropic-some-new-model", "anthropic/some-new-model", nil, func(t *testing.T, res Resolved) {
			if res.Surface != SurfaceAnthropic || sp(res.Caps.ThinkingShape) != "adaptive" || sp(res.Caps.ThinkingDisplay) != "summarized" || res.Caps.ContextWindow != nil {
				t.Errorf("%+v", res)
			}
		}},
		{"azure-gpt55-prod", "azure/gpt55-prod", nil, func(t *testing.T, res Resolved) {
			if res.WireID != "gpt55-prod" || res.Protocol != ProtocolOpenAIResponses || res.Transport.BaseURL != "https://contoso-prod.openai.azure.com/openai/v1" || res.Transport.AuthHeader != "api-key" || ip(res.Caps.ContextWindow) == 0 {
				t.Errorf("%+v", res)
			}
		}},
		{"azure-claude-prod", "azure/claude-prod", nil, func(t *testing.T, res Resolved) {
			if res.Protocol != ProtocolAnthropic || res.Transport.BaseURL != "https://contoso-prod.services.ai.azure.com/anthropic/v1" || sp(res.Caps.ThinkingShape) != "budget+effort" {
				t.Errorf("%+v", res)
			}
		}},
		{"azure-claude-opus-4-5", "azure/claude-opus-4-5", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "budget+effort" || res.Transport.AuthHeader != "api-key" || res.Protocol != ProtocolAnthropic {
				t.Errorf("%+v", res)
			}
		}},
		{"azure-llama", "azure/llama-3.3-70b-instruct", nil, func(t *testing.T, res Resolved) {
			if res.Surface != SurfaceGeneric {
				t.Errorf("surface = %q", res.Surface)
			}
		}},
		{"bedrock-sonnet-5", "bedrock/anthropic.claude-sonnet-5", nil, func(t *testing.T, res Resolved) {
			if bp(res.Caps.StructuredOutput) != "false" || sp(res.Caps.ThinkingDisplay) != "summarized" || res.Credential.Source != "env:AWS_BEARER_TOKEN_BEDROCK" {
				t.Errorf("%+v", res)
			}
		}},
		{"bedrock-gpt-5.5", "bedrock/openai.gpt-5.5", nil, func(t *testing.T, res Resolved) {
			if res.Transport.Preset != PresetBedrockMantleOpenAI || bp(res.Caps.StructuredOutput) == "false" || res.Transport.Auth != AuthBearer {
				t.Errorf("%+v", res)
			}
		}},
		{"bedrock-global-opus-5", "bedrock/global.anthropic.claude-opus-5", nil, func(t *testing.T, res Resolved) {
			if res.WireID != "global.anthropic.claude-opus-5" || res.Provenance["model"] != "row:global.anthropic.claude-opus-5" {
				t.Errorf("%+v", res)
			}
		}},
		{"bedrock-new-model", "bedrock/anthropic.claude-new-model", nil, func(t *testing.T, res Resolved) {
			if bp(res.Caps.StructuredOutput) != "false" || bp(res.Caps.WebSearch) != "false" || sp(res.Caps.ThinkingShape) != "adaptive" {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"vertex-opus-5", "vertex/claude-opus-5", nil, func(t *testing.T, res Resolved) {
			if res.Transport.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || res.Transport.BaseURL != "https://aiplatform.googleapis.com/v1/projects/my-project/locations/global" || res.Credential.Source != "adc" {
				t.Errorf("%+v", res)
			}
		}},
		{"google-vertex-opus-5", "google-vertex/claude-opus-5", nil, func(t *testing.T, res Resolved) {
			if res.Transport.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || res.Protocol != ProtocolAnthropic {
				t.Errorf("%+v", res.Transport)
			}
		}},
		{"openrouter-opus-5", "openrouter/anthropic/claude-opus-5", nil, func(t *testing.T, res Resolved) {
			if res.Surface != SurfaceAnthropic || sp(res.Caps.CacheControl) != "anthropic" || sp(res.Caps.ThinkingFormat) != "openrouter" {
				t.Errorf("%+v", res)
			}
		}},
		{"openrouter-opus-4.6", "openrouter/anthropic/claude-opus-4.6", nil, func(t *testing.T, res Resolved) {
			if res.Caps.ThinkingAlwaysOn != nil || res.Caps.ThinkingShape != nil {
				t.Errorf("OpenAI protocols never derive always-on: %+v", res.Caps)
			}
		}},
		{"openrouter-sonnet-4.5", "openrouter/anthropic/claude-sonnet-4.5", nil, func(t *testing.T, res Resolved) {
			if !reflect.DeepEqual(res.Caps.ReasoningControls, []string{"toggle"}) {
				t.Errorf("toggle-only stays as listed; the openrouter dialect sends the effort regardless: %v", res.Caps.ReasoningControls)
			}
		}},
		{"orclaude-minimax", "orclaude/minimax/minimax-m2.7", nil, func(t *testing.T, res Resolved) {
			if res.Transport.Endpoint != "/messages" || res.Credential.Source != "env:OPENROUTER_API_KEY" || res.Surface != SurfaceAnthropic {
				t.Errorf("%+v", res)
			}
		}},
		{"openrouter-minimax", "openrouter/minimax/minimax-m2.7", nil, func(t *testing.T, res Resolved) {
			if bp(res.Caps.ThinkingAlwaysOn) != "true" || sp(res.Caps.ReasoningField) != "reasoning_details" || !reflect.DeepEqual(res.Caps.ReasoningControls, []string{"toggle"}) {
				t.Errorf("%+v", res.Caps)
			}
		}},
		{"openrouter-deepseek-r1", "openrouter/deepseek/deepseek-r1", nil, func(t *testing.T, res Resolved) {
			if !slices.Contains(res.Caps.ReasoningControls, "effort") {
				t.Errorf("effort must pass through: %v", res.Caps.ReasoningControls)
			}
		}},
		{"kimi-for-coding", "kimi-for-coding/kimi-for-coding", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "budget" || res.Surface != SurfaceAnthropic || !slices.Contains(res.Caps.ReasoningControls, "effort") {
				t.Errorf("%+v", res)
			}
		}},
		{"kimi-k3", "kimi-for-coding/k3", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "budget+effort" || res.Surface != SurfaceAnthropic {
				t.Errorf("%+v", res)
			}
		}},
		{"minimax-m3", "minimax/MiniMax-M3", nil, func(t *testing.T, res Resolved) {
			if sp(res.Caps.ThinkingShape) != "budget" || res.Surface != SurfaceAnthropic {
				t.Errorf("%+v", res)
			}
		}},
		{"local-whatever-claude", "local/whatever-claude", nil, func(t *testing.T, res Resolved) {
			if res.Caps.ThinkingShape != nil || res.Surface != SurfaceGeneric || res.Credential.Source != "none" || len(res.Warnings) == 0 {
				t.Errorf("%+v", res)
			}
		}},
		{"local-whatever", "local/whatever", nil, func(t *testing.T, res Resolved) {
			if ip(res.Caps.ContextWindow) != 131072 || res.Transport.Auth != AuthNone {
				t.Errorf("%+v", res)
			}
		}},
		{"work-glm", "work/glm-5.2-nvfp4", nil, func(t *testing.T, res Resolved) {
			if !slices.Contains(res.Caps.ReasoningControls, "effort") || res.Surface != SurfaceGeneric || res.Caps.Fields["stream_options"] || res.Credential.Source != "credential_headers" || sp(res.Caps.ThinkingFormat) != "zai" {
				t.Errorf("%+v", res)
			}
		}},
		{"moonshotai-kimi-k2.5", "moonshotai/kimi-k2.5", nil, func(t *testing.T, res Resolved) {
			if res.Caps.MaxOutputTokens != nil || res.Provenance["MaxOutputTokens"] != "derived" {
				t.Errorf("junk cap must be cleared: %v %v", res.Caps.MaxOutputTokens, res.Provenance["MaxOutputTokens"])
			}
		}},
		{"ollama-llama3", "ollama/llama3:8b", nil, func(t *testing.T, res Resolved) {
			if res.Transport.BaseURL != "http://localhost:11434/v1" || ip(res.Caps.ContextWindow) != 8192 || res.Transport.Auth != AuthOptionalBearer || strings.Contains(strings.Join(res.Warnings, ";"), "no credential") {
				t.Errorf("%+v", res)
			}
		}},
		{"ollama-llama3-ipv6", "ollama/llama3:8b", map[string]string{"OLLAMA_HOST": "::1", "OLLAMA_API_KEY": "SECRET-ollama"}, func(t *testing.T, res Resolved) {
			if res.Transport.BaseURL != "http://[::1]:11434/v1" || res.Credential.Source != "env:OLLAMA_API_KEY" {
				t.Errorf("%+v", res)
			}
		}},
		{"ollama-llama3-base-url", "ollama/llama3:8b", map[string]string{"OLLAMA_BASE_URL": "http://proxy.example/ollama/v1"}, func(t *testing.T, res Resolved) {
			if res.Transport.BaseURL != "http://proxy.example/ollama/v1" {
				t.Errorf("%+v", res.Transport)
			}
		}},
		{"ollama-qwen3", "ollama/qwen3:8b", nil, func(t *testing.T, res Resolved) {
			if ip(res.Caps.ContextWindow) != 40960 || res.Provenance["ContextWindow"] != "config/glob:qwen3*" {
				t.Errorf("%+v", res.Provenance)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := goldenRegistry(t, c.env)
			res, err := r.Resolve(c.ref)
			if err != nil {
				t.Fatalf("Resolve(%q): %v", c.ref, err)
			}
			c.check(t, res)
			view := goldenView{Resolved: res, CredentialSource: res.Credential.Source, PrunedFields: prunedFields(res.Caps)}
			got, err := json.MarshalIndent(view, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')
			for name, secret := range goldenSecrets {
				if bytes.Contains(got, []byte(secret)) {
					t.Fatalf("%s: the serialized record contains the value of %s", c.ref, name)
				}
			}
			for name, value := range c.env {
				if isKeyVar(name) && bytes.Contains(got, []byte(value)) {
					t.Fatalf("%s: the serialized record contains the value of %s", c.ref, name)
				}
			}
			path := filepath.Join("testdata", "golden", c.name+".json")
			if *updateGolden {
				_ = os.MkdirAll(filepath.Dir(path), 0o755)
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing golden %s (run: go test ./llm/registry -run TestGoldenResolved -update)", path)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s differs from golden %s (run with -update after confirming the change is intended)\n--- got ---\n%s", c.ref, path, got)
			}
		})
	}
}
