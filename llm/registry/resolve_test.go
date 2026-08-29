package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func mustResolve(t *testing.T, r *Registry, ref string) Resolved {
	t.Helper()
	res, err := r.Resolve(ref)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", ref, err)
	}
	return res
}

func hasWarning(res Resolved, substr string) bool {
	return strings.Contains(strings.Join(res.Warnings, "\n"), substr)
}

func TestParseRef(t *testing.T) {
	if ParseRef("groq/openai/gpt-oss-120b") != (Ref{Instance: "groq", Model: "openai/gpt-oss-120b"}) || ParseRef("gpt-5.5") != (Ref{Model: "gpt-5.5"}) {
		t.Fatal("ParseRef splits on the first slash only")
	}
}

func TestResolve_LookupSteps(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"AWS_REGION": "us-east-1", "GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global"}, "")
	res := mustResolve(t, r, "anthropic/claude-opus-4-5")
	if res.Provenance["model"] != "row:claude-opus-4-5" || res.WireID != "claude-opus-4-5" || res.ModelID != "claude-opus-4-5" {
		t.Fatalf("exact: %+v", res.Provenance["model"])
	}
	res = mustResolve(t, r, "amazon-bedrock/global.anthropic.claude-haiku-4-5")
	if res.Provenance["model"] != "region:anthropic.claude-haiku-4-5" || res.WireID != "global.anthropic.claude-haiku-4-5" || *res.Caps.ContextWindow != 200000 {
		t.Fatalf("region: %+v ctx=%v", res.Provenance["model"], res.Caps.ContextWindow)
	}
	res = mustResolve(t, r, "anthropic/claude-opus-4-5-20260101")
	if res.Provenance["model"] != "dated:claude-opus-4-5" || res.WireID != "claude-opus-4-5-20260101" {
		t.Fatalf("dated: %+v", res.Provenance["model"])
	}
	res = mustResolve(t, r, "google-vertex/claude-opus-5@20260101")
	if res.Provenance["model"] != "dated:claude-opus-5" || res.WireID != "claude-opus-5@20260101" || res.Protocol != ProtocolAnthropic {
		t.Fatalf("vertex dated: %+v", res)
	}
	res = mustResolve(t, r, "anthropic/claude-opus-4-6[1m]")
	if res.Provenance["model"] != "synthesized" || !hasWarning(res, "model not in catalog") || res.WireID != "claude-opus-4-6[1m]" || res.Caps.ContextWindow != nil {
		t.Fatalf("synthesized: %+v", res)
	}
	if _, err := r.Resolve("openai-codex/gpt-5.9"); err == nil || !strings.Contains(err.Error(), "gpt-5.6-sol") {
		t.Fatalf("codex unknown id must error naming the allowlist: %v", err)
	}
	r.ApplyLive("ollama", []Model{{ID: "llama3:8b", Caps: Caps{ContextWindow: new(8192)}}, {ID: "qwen3:8b"}})
	res = mustResolve(t, r, "ollama/llama3:8b")
	if res.Provenance["model"] != "live" || *res.Caps.ContextWindow != 8192 || res.Provenance["ContextWindow"] != "live" {
		t.Fatalf("live: %+v", res.Provenance)
	}
	res = mustResolve(t, r, "ollama/qwen3:8b")
	if *res.Caps.ContextWindow != 131072 || res.Provenance["ContextWindow"] != "overlay/provider" {
		t.Fatalf("live row without a window keeps the provider default: %v %v", res.Caps.ContextWindow, res.Provenance["ContextWindow"])
	}
	r = fixtureLoad(t, nil, "[providers.ollama.models.\"qwen3*\"]\ncontext_window = 40960\n")
	r.ApplyLive("ollama", []Model{{ID: "qwen3:8b", Caps: Caps{ContextWindow: new(8192)}}})
	res = mustResolve(t, r, "ollama/qwen3:8b")
	if *res.Caps.ContextWindow != 40960 || res.Provenance["ContextWindow"] != "config/glob:qwen3*" {
		t.Fatalf("a user glob beats live and reaches a live-only id: %v %v", res.Caps.ContextWindow, res.Provenance)
	}
}

const orderOverlay = `
[models."*"]
max_output_tokens = 10
[providers.x]
implicit = true
protocol = "openai-chat"
base_url = "https://x/v1"
context_window = 1000
[providers.x.models."a*"]
context_window = 2000
[providers.x.models."ab*"]
context_window = 3000
[providers.x.models."abc"]
context_window = 4000
[providers.x.models."m"]
context_window = 1000
`

func orderLoad(t *testing.T, config string) *Registry {
	t.Helper()
	data, _ := os.ReadFile("testdata/models.dev.sample.json")
	opts := []Option{WithSnapshot(data), WithEnv(mapEnv(nil)), WithStateRoot(t.TempDir()), WithOverlay(overlayWith(orderOverlay))}
	if config == "" {
		opts = append(opts, WithNoUserLayer())
	} else {
		path := filepath.Join(t.TempDir(), "providers.toml")
		_ = os.WriteFile(path, []byte(config), 0o600)
		opts = append(opts, WithConfigPath(path))
	}
	r, err := Load(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestResolve_LayerOrder(t *testing.T) {
	r := orderLoad(t, "[providers.x]\nmax_output_tokens = 20\n")
	for ref, want := range map[string]struct {
		ctx  int
		prov string
	}{
		"x/abc": {4000, "overlay/row"},
		"x/abd": {3000, "overlay/glob:ab*"},
		"x/az":  {2000, "overlay/glob:a*"},
		"x/zzz": {1000, "overlay/provider"},
	} {
		res := mustResolve(t, r, ref)
		if *res.Caps.ContextWindow != want.ctx || res.Provenance["ContextWindow"] != want.prov {
			t.Errorf("%s: ctx=%d prov=%q", ref, *res.Caps.ContextWindow, res.Provenance["ContextWindow"])
		}
		if *res.Caps.MaxOutputTokens != 20 || res.Provenance["MaxOutputTokens"] != "config/provider" {
			t.Errorf("%s: a later layer wins regardless of level: %v %q", ref, *res.Caps.MaxOutputTokens, res.Provenance["MaxOutputTokens"])
		}
	}
	res := mustResolve(t, orderLoad(t, ""), "x/zzz")
	if *res.Caps.MaxOutputTokens != 10 || res.Provenance["MaxOutputTokens"] != "overlay/glob:*" {
		t.Fatalf("top-level glob reaches a synthesized id: %v %q", *res.Caps.MaxOutputTokens, res.Provenance["MaxOutputTokens"])
	}
}

func TestResolve_LiveSitsBetweenOverlayAndConfig(t *testing.T) {
	r := orderLoad(t, "")
	r.ApplyLive("x", []Model{{ID: "m", Caps: Caps{ContextWindow: new(5000)}}})
	if res := mustResolve(t, r, "x/m"); *res.Caps.ContextWindow != 5000 || res.Provenance["ContextWindow"] != "live" {
		t.Fatalf("live must beat the curated fact: %+v", res.Provenance)
	}
	r = orderLoad(t, "[providers.x.models.\"m\"]\ncontext_window = 7000\n")
	r.ApplyLive("x", []Model{{ID: "m", Caps: Caps{ContextWindow: new(5000)}}})
	if res := mustResolve(t, r, "x/m"); *res.Caps.ContextWindow != 7000 || res.Provenance["ContextWindow"] != "config/row" {
		t.Fatalf("live must never beat the user layer: %+v", res.Provenance)
	}
}

func TestResolve_AliasSeeding(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"AZURE_RESOURCE_NAME": "contoso"}, "[providers.azure.models.\"claude-prod\"]\nalias_of = \"claude-opus-4-5\"\n")
	res := mustResolve(t, r, "anthropic/claude-sonnet-4-5[1m]")
	if *res.Caps.ContextWindow != 1000000 || res.Provenance["ContextWindow"] != "overlay/row" || *res.Caps.MaxOutputTokens != 64000 || res.Provenance["MaxOutputTokens"] != "alias" {
		t.Fatalf("[1m]: %v %v", res.Caps.ContextWindow, res.Provenance)
	}
	if res.WireID != "claude-sonnet-4-5" || res.Surface != SurfaceAnthropic || res.Model.Family != "claude-sonnet" || res.Headers["anthropic-beta"] != "context-1m-2025-08-07" || *res.Caps.ThinkingShape != "budget" || res.Caps.ThinkingAlwaysOn != nil || res.Caps.Sampling != nil {
		t.Fatalf("[1m] row: %+v", res)
	}
	res = mustResolve(t, r, "openai-codex/gpt-5.6")
	if res.WireID != "gpt-5.6-sol" || res.Caps.Sampling == nil || *res.Caps.Sampling || *res.Caps.ContextWindow != 272000 || res.Caps.Cost == nil || len(res.Caps.EffortValues) != 5 {
		t.Fatalf("codex gpt-5.6: %+v", res.Caps)
	}
	if res.Caps.Fields["temperature"] || res.Caps.Fields["max_output_tokens"] || !res.Caps.Fields["prompt_cache_key"] || !res.Caps.Fields["store"] || !res.Caps.Fields["metadata"] {
		t.Fatalf("codex fields: %v", res.Caps.Fields)
	}
	if res.Transport.Auth != AuthOAuthOpenAICodex || res.Transport.BaseURL != "https://chatgpt.com/backend-api/codex" || res.Transport.Body["reasoning.context"] != "all_turns" || !*res.Caps.ResponsesLite {
		t.Fatalf("codex transport: %+v", res.Transport)
	}
	if _, ok := res.Headers["OpenAI-Organization"]; ok {
		t.Fatal("codex must not carry the platform org header")
	}
	res = mustResolve(t, r, "anthropic/claude-mythos-5")
	if res.Transport.BaseURL != "https://api.anthropic.com/v1" || res.Protocol != ProtocolAnthropic || *res.Caps.ContextWindow != 1000000 || res.Caps.Cost.Input != 10 || *res.Caps.Sampling || res.Model.Family != "claude-mythos" || *res.Caps.ThinkingShape != "adaptive" || *res.Caps.ThinkingDisplay != "summarized" {
		t.Fatalf("mythos-5: %+v", res)
	}
	res = mustResolve(t, r, "azure/claude-prod")
	if res.Protocol != ProtocolAnthropic || res.Transport.BaseURL != "https://contoso.services.ai.azure.com/anthropic/v1" || res.Transport.Endpoint != "/messages" || res.Transport.AuthHeader != "api-key" || res.WireID != "claude-prod" || *res.Caps.ThinkingShape != "budget+effort" {
		t.Fatalf("azure/claude-prod: %+v", res)
	}
	if _, ok := res.Caps.Fields["store"]; ok {
		t.Fatal("a cross-protocol row must not inherit the provider's Fields")
	}
	if _, ok := res.Caps.Fields["stop_sequences"]; !ok {
		t.Fatal("a cross-protocol row starts from its own protocol's baseline")
	}
}

func TestResolve_TransportAssembly(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global", "AWS_REGION": "us-east-1"}, "")
	res := mustResolve(t, r, "google-vertex-anthropic/claude-opus-5")
	if res.Transport.BaseURL != "https://aiplatform.googleapis.com/v1/projects/p/locations/global" || res.Transport.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || res.Transport.StreamEndpoint != "/publishers/anthropic/models/{model}:streamRawPredict" || res.Transport.Body["anthropic_version"] != "vertex-2023-10-16" || res.Transport.ModelsEndpoint != EndpointUnsupported {
		t.Fatalf("vertex anthropic: %+v", res.Transport)
	}
	if res := mustResolve(t, r, "google-vertex/gemini-2.5-flash"); res.Transport.Endpoint != "/publishers/google/models/{model}:generateContent" || res.Protocol != ProtocolGoogle {
		t.Fatalf("vertex gemini: %+v", res.Transport)
	}
	if res := mustResolve(t, r, "google-vertex/claude-opus-5"); res.Transport.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || res.Protocol != ProtocolAnthropic {
		t.Fatalf("vertex claude under google-vertex: %+v", res.Transport)
	}
	if res := mustResolve(t, r, "openai/gpt-5.5"); res.Transport.Endpoint != "/responses" || res.Transport.StreamEndpoint != "/responses" || res.Transport.ModelsEndpoint != "/models" || res.Transport.CountTokensEndpoint != "/responses/input_tokens" || res.Transport.BaseURL != "https://api.openai.com/v1" {
		t.Fatalf("openai: %+v", res.Transport)
	}
	if res := mustResolve(t, r, "groq/llama-3.3-70b-versatile"); res.Transport.Endpoint != "/chat/completions" || res.Transport.CountTokensEndpoint != EndpointUnsupported || res.Transport.BaseURL != "https://api.groq.com/openai/v1" {
		t.Fatalf("groq: %+v", res.Transport)
	}
	res = mustResolve(t, r, "amazon-bedrock/openai.gpt-5.5")
	if res.Transport.BaseURL != "https://bedrock-mantle.us-east-1.api.aws/openai/v1" || res.Transport.Auth != AuthBearer || res.Transport.Endpoint != "/responses" || res.Caps.StructuredOutput == nil || !*res.Caps.StructuredOutput {
		t.Fatalf("mantle: %+v %v", res.Transport, res.Caps.StructuredOutput)
	}
	res = mustResolve(t, r, "amazon-bedrock/anthropic.claude-sonnet-5")
	if *res.Caps.StructuredOutput || *res.Caps.WebSearch || *res.Caps.ThinkingDisplay != "summarized" || res.Transport.BaseURL != "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1" || res.Transport.AuthHeader != "x-api-key" {
		t.Fatalf("bedrock claude: %+v", res)
	}
	res = mustResolve(t, r, "amazon-bedrock/anthropic.claude-new-model")
	if !hasWarning(res, "model not in catalog") || *res.Caps.StructuredOutput || *res.Caps.ThinkingShape != "adaptive" || res.Surface != SurfaceAnthropic {
		t.Fatalf("bedrock synthesized: %+v", res)
	}
	r = fixtureLoad(t, nil, "")
	res = mustResolve(t, r, "azure/gpt-5.5")
	if !strings.Contains(res.Transport.BaseURL, "{AZURE_RESOURCE_NAME}") || !hasWarning(res, "unresolved variable AZURE_RESOURCE_NAME") || !hasWarning(res, "no credential") {
		t.Fatalf("azure without vars: %+v", res)
	}
	r = fixtureLoad(t, map[string]string{"GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "europe-west1"}, "")
	if res := mustResolve(t, r, "google-vertex-anthropic/claude-opus-5"); !hasWarning(res, "regional") || res.Transport.BaseURL != "https://europe-west1-aiplatform.googleapis.com/v1/projects/p/locations/europe-west1" {
		t.Fatalf("regional vertex: %+v", res)
	}
	if res := mustResolve(t, r, "google-vertex-anthropic/claude-sonnet-4-6"); hasWarning(res, "regional") {
		t.Fatal("Sonnet 4.6 and earlier are fine on regional endpoints")
	}
}

func TestResolve_HeadersAndCredential(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "sk", "OPENAI_ORG_ID": "org-1"}, "")
	res := mustResolve(t, r, "openai/gpt-5.5")
	if res.Headers["OpenAI-Organization"] != "org-1" || res.Credential != (Credential{Value: "sk", Source: "env:OPENAI_API_KEY"}) || hasWarning(res, "no credential") {
		t.Fatalf("openai: %+v %+v", res.Headers, res.Credential)
	}
	if _, ok := res.Headers["OpenAI-Project"]; ok {
		t.Fatal("an unset $VAR drops the header")
	}
	r = fixtureLoad(t, nil, "")
	res = mustResolve(t, r, "openai/gpt-5.5")
	if res.Credential.Source != "none" || !hasWarning(res, "no credential") {
		t.Fatalf("credential-less resolve must succeed with a warning: %+v", res.Credential)
	}
	res = mustResolve(t, r, "kimi-for-coding/k3")
	if res.Headers["User-Agent"] != "claude-cli/2.1.177 (external, cli)" || res.Surface != SurfaceAnthropic || *res.Caps.ThinkingShape != "budget+effort" {
		t.Fatalf("kimi k3: %+v", res)
	}
	res = mustResolve(t, r, "minimax/MiniMax-M3")
	if *res.Caps.ThinkingShape != "budget" || res.Surface != SurfaceAnthropic || res.ModelID != "MiniMax-M3" {
		t.Fatalf("minimax: %+v", res)
	}
	r = fixtureLoad(t, map[string]string{"PORTKEY_KEY": "pk"}, "[providers.gw]\nbase = \"openai\"\nbase_url = \"https://gw/v1\"\ncredential_headers = { \"Authorization\" = \"Bearer $PORTKEY_KEY\", \"X-Portkey-Key\" = \"$PORTKEY_KEY\" }\n")
	res = mustResolve(t, r, "gw/gpt-5.5")
	if res.CredentialHeaders["X-Portkey-Key"] != "pk" || res.Credential.Value != "Bearer pk" || res.Credential.Source != "credential_headers" {
		t.Fatalf("credential headers: %+v %+v", res.CredentialHeaders, res.Credential)
	}
}

func TestResolve_CrossProtocolInstances(t *testing.T) {
	cfg := `
[providers.work]
base = "openai"
protocol = "openai-chat"
base_url = "https://gw/v1"
[providers.orclaude]
base = "openrouter"
protocol = "anthropic"
[providers.orclaude.models."minimax/*"]
surface = "anthropic"
`
	r := fixtureLoad(t, map[string]string{"OPENROUTER_API_KEY": "or"}, cfg)
	res := mustResolve(t, r, "work/gpt-5.5")
	if res.Protocol != ProtocolOpenAIChat || res.Transport.Endpoint != "/chat/completions" || res.Transport.CountTokensEndpoint != EndpointUnsupported || res.Transport.Auth != AuthBearer {
		t.Fatalf("work: %+v", res.Transport)
	}
	if _, ok := res.Caps.Fields["include"]; ok {
		t.Fatal("responses-only keys must not leak onto a chat instance")
	}
	if res.Caps.Fields["store"] || !res.Caps.Fields["stream_options"] || *res.Caps.MaxTokensField != "max_completion_tokens" {
		t.Fatalf("work fields: %v max_tokens_field=%v", res.Caps.Fields, res.Caps.MaxTokensField)
	}
	res = mustResolve(t, r, "orclaude/minimax/minimax-m2.7")
	if res.Transport.Endpoint != "/messages" || res.Transport.BaseURL != "https://openrouter.ai/api/v1" || res.Credential.Source != "env:OPENROUTER_API_KEY" || res.Surface != SurfaceAnthropic || res.WireID != "minimax/minimax-m2.7" {
		t.Fatalf("orclaude: %+v", res)
	}
}

func TestFindModel(t *testing.T) {
	state := t.TempDir()
	_ = os.MkdirAll(filepath.Join(state, "auth"), 0o700)
	_ = os.WriteFile(oauthRecordPath(state, "openai-codex"), []byte("{}"), 0o600)
	r := fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "k", "ANTHROPIC_API_KEY": "a"}, "", WithStateRoot(state))
	if got := r.FindModel("gpt-5.6"); !reflect.DeepEqual(got, []Ref{{"openai-codex", "gpt-5.6"}, {"openai", "gpt-5.6"}}) {
		t.Fatalf("FindModel(gpt-5.6) = %v", got)
	}
	if got := r.FindModel("claude-opus-5"); !reflect.DeepEqual(got, []Ref{{"anthropic", "claude-opus-5"}}) {
		t.Fatalf("FindModel(claude-opus-5) = %v", got)
	}
	if got := r.FindModel("nope"); len(got) != 0 {
		t.Fatalf("FindModel(nope) = %v", got)
	}
	r.ApplyLive("ollama", []Model{{ID: "llama3:8b"}})
	if got := r.FindModel("llama3:8b"); !reflect.DeepEqual(got, []Ref{{"ollama", "llama3:8b"}}) {
		t.Fatalf("FindModel(llama3:8b) = %v", got)
	}
}

func TestApplyLive_FactsOnlyAndNonChatDropped(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	shape := "adaptive"
	r.ApplyLive("openrouter", []Model{
		{ID: "whisper-large", Caps: Caps{Tools: new(true)}},
		{ID: "anthropic/claude-opus-4.6", Caps: Caps{Tools: new(true), ContextWindow: new(1000000), ThinkingShape: &shape, ThinkingAlwaysOn: new(false)}},
		{ID: "minimax/minimax-m3", Caps: Caps{ThinkingAlwaysOn: new(true)}},
	})
	live := r.LiveModels("openrouter")
	if len(live) != 2 {
		t.Fatalf("non-chat ids must be dropped: %v", live)
	}
	res := mustResolve(t, r, "openrouter/anthropic/claude-opus-4.6")
	if res.Provenance["ContextWindow"] != "live" || res.Caps.ThinkingShape != nil || res.Caps.ThinkingAlwaysOn != nil {
		t.Fatalf("live must carry only advertised facts: %+v", res.Provenance)
	}
	res = mustResolve(t, r, "openrouter/minimax/minimax-m3")
	if res.Caps.ThinkingAlwaysOn == nil || !*res.Caps.ThinkingAlwaysOn {
		t.Fatal("mandatory reasoning reaches ThinkingAlwaysOn")
	}
	ids, err := r.ModelIDs("openrouter")
	if err != nil || !strings.Contains(strings.Join(ids, ","), "minimax/minimax-m3") {
		t.Fatalf("ModelIDs must include live ids: %v %v", ids, err)
	}
}

var provRe = regexp.MustCompile(`^((snapshot|cache|overlay|config)/(provider|row|glob:.+)|live|alias|derived)$`)

func TestResolve_ProvenanceNamesRealLayers(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"AWS_REGION": "us-east-1"}, "[providers.groq]\ncontext_window = 9000\n")
	for _, ref := range []string{"anthropic/claude-sonnet-4-5[1m]", "openai-codex/gpt-5.6", "amazon-bedrock/anthropic.claude-sonnet-5", "groq/llama-3.3-70b-versatile", "groq/brand-new"} {
		res := mustResolve(t, r, ref)
		for k, v := range res.Provenance {
			if k == "model" {
				continue
			}
			if !provRe.MatchString(v) {
				t.Errorf("%s: %s = %q is not a real layer", ref, k, v)
			}
		}
		if ref == "groq/brand-new" && (res.Provenance["ContextWindow"] != "config/provider" || *res.Caps.ContextWindow != 9000) {
			t.Errorf("instance-wide context_window must rewrite every row: %v", res.Provenance)
		}
	}
}

func TestResolve_DefaultInstanceAndErrors(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "k"}, "")
	if res := mustResolve(t, r, "gpt-5.5"); res.Instance != "openai" {
		t.Fatalf("bare id → default instance: %q", res.Instance)
	}
	if _, err := r.Resolve("nope/x"); err == nil || !strings.Contains(err.Error(), "unknown instance") || !strings.Contains(err.Error(), "openai") {
		t.Fatalf("unknown instance must list the available ones: %v", err)
	}
	if _, err := r.Resolve("huggingface/x"); err == nil {
		t.Fatal("a non-implicit registry id is not an instance without a [providers.huggingface] entry")
	}
}
