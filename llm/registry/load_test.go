package registry

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fixtureLoad loads the 40-provider fixture with the real curated overlay,
// no user layer unless config is non-empty, and the given environment.
func fixtureLoad(t *testing.T, env map[string]string, config string, extra ...Option) *Registry {
	t.Helper()
	data, err := os.ReadFile("testdata/models.dev.sample.json")
	if err != nil {
		t.Fatal(err)
	}
	opts := []Option{WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir())}
	if config == "" {
		opts = append(opts, WithNoUserLayer())
	} else {
		path := filepath.Join(t.TempDir(), "providers.toml")
		if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
			t.Fatal(err)
		}
		opts = append(opts, WithConfigPath(path))
	}
	opts = append(opts, extra...)
	r, err := Load(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func mapEnv(env map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) { v, ok := env[name]; return v, ok }
}

// overlayWith appends extra TOML to the embedded curated overlay: a custom
// overlay must keep the transport presets the converter's rows reference.
func overlayWith(extra string) []byte {
	return []byte(string(EmbeddedOverlay()) + "\n" + extra)
}

func layerTags(rec *record) []string {
	out := make([]string, 0, len(rec.layers))
	for _, l := range rec.layers {
		out = append(out, l.tag+":"+l.owner)
	}
	return out
}

func TestLoad_CuratedBaseChainAndInheritModelsFalse(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	codex := r.curated["openai-codex"]
	if codex == nil {
		t.Fatal("openai-codex missing")
	}
	if got := layerTags(codex); !reflect.DeepEqual(got, []string{"snapshot:openai", "overlay:openai", "overlay:openai-codex"}) {
		t.Fatalf("codex layers = %v", got)
	}
	h := codex.head
	if h.Protocol != ProtocolOpenAIResponses || h.Transport.Auth != AuthOAuthOpenAICodex || h.Transport.BaseURL != "{BASE_URL}" || h.Transport.Vars["BASE_URL"] != "https://chatgpt.com/backend-api/codex" || h.Transport.ModelsEndpoint != "/models?client_version=0.0.0" {
		t.Fatalf("codex head: %+v", h.Transport)
	}
	if h.Transport.CountTokensEndpoint != EndpointUnsupported {
		t.Fatalf("codex must override the inherited count-tokens endpoint, got %q", h.Transport.CountTokensEndpoint)
	}
	if _, ok := h.Models["gpt-5.5"]; ok {
		t.Fatal("inherit_models = false must drop the base's rows")
	}
	if _, ok := h.Models["gpt-5.6"]; !ok || h.Models["gpt-5.6"].WireID != "gpt-5.6-sol" {
		t.Fatalf("codex rows: %v", h.Models["gpt-5.6"])
	}
	for _, l := range codex.layers[:2] {
		if l.rows != nil {
			t.Fatal("base layers must carry no rows after inherit_models = false")
		}
	}
	if codex.providerID != "openai-codex" || codex.head.APIKeyEnv == nil || len(codex.head.APIKeyEnv) != 0 {
		t.Fatalf("providerID=%q apiKeyEnv=%#v", codex.providerID, codex.head.APIKeyEnv)
	}
	openai := r.curated["openai"]
	if openai.head.Transport.CountTokensEndpoint != "/responses/input_tokens" || openai.head.Headers["OpenAI-Organization"] != "$OPENAI_ORG_ID" {
		t.Fatalf("openai head: %+v", openai.head)
	}
}

func TestLoad_ExplicitInstances(t *testing.T) {
	cfg := `
[providers.groq]
protocol = "openai-responses"
[providers.work]
base = "openai"
protocol = "openai-chat"
base_url = "https://gw.example.com/v1"
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.openai]
base = "openai-codex"
[providers.mycodex]
base = "openai-codex"
`
	r := fixtureLoad(t, nil, cfg)
	groq := r.explicit["groq"]
	if groq.providerID != "groq" || groq.head.Protocol != ProtocolOpenAIResponses || !reflect.DeepEqual(layerTags(groq), []string{"snapshot:groq", "overlay:groq", "config:groq"}) {
		t.Fatalf("groq: %q %q %v", groq.providerID, groq.head.Protocol, layerTags(groq))
	}
	work := r.explicit["work"]
	if work.providerID != "openai" || work.head.Protocol != ProtocolOpenAIChat || work.head.Transport.Auth != AuthBearer {
		t.Fatalf("work: %+v", work.head)
	}
	if work.head.Transport.CountTokensEndpoint != "" || work.head.Transport.BaseURL != "https://gw.example.com/v1" {
		t.Fatalf("cross-protocol instance must drop the base's endpoint fields: %+v", work.head.Transport)
	}
	if !work.layers[len(work.layers)-1].resetFields {
		t.Fatal("cross-protocol layer must reset inherited Fields")
	}
	if r.explicit["openai"].providerID != "openai-codex" || r.explicit["openai"].head.Transport.Auth != AuthOAuthOpenAICodex {
		t.Fatal("an explicit base must beat the name match")
	}
	if r.explicit["mycodex"].providerID != "openai-codex" {
		t.Fatalf("mycodex providerID = %q", r.explicit["mycodex"].providerID)
	}
	if _, ok := r.explicit["work"].userVars["BASE_URL"]; ok {
		t.Fatal("no user vars were set")
	}
}

func TestLoad_PresetExpansion(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	va := r.curated["google-vertex-anthropic"].head.Transport
	if va.Preset != PresetVertexAnthropic || va.Auth != AuthGCPADC || va.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || va.Body["anthropic_version"] != "vertex-2023-10-16" || va.HostRule != HostRuleVertexLocation {
		t.Fatalf("vertex-anthropic transport: %+v", va)
	}
	row := r.curated["google-vertex"].head.Models["claude-opus-5"]
	if row.Transport == nil || row.Transport.Endpoint != "/publishers/anthropic/models/{model}:rawPredict" || row.Protocol != ProtocolAnthropic {
		t.Fatalf("vertex claude row must expand the converter's preset: %+v", row)
	}
	mantle := r.curated["amazon-bedrock"].head.Models["openai.gpt-oss-120b"]
	if mantle.Transport == nil || mantle.Transport.Auth != AuthBearer || mantle.Transport.BaseURL != "https://bedrock-mantle.{AWS_REGION}.api.aws/v1" || mantle.Transport.ModelsEndpoint != "/models" {
		t.Fatalf("mantle row: %+v", mantle.Transport)
	}
}

func TestLoad_Errors(t *testing.T) {
	cases := map[string]string{
		"unknown base":              "[providers.x]\nbase = \"nope\"\n",
		"unknown preset":            "[providers.x]\nbase = \"openai\"\ntransport = \"nope\"\n",
		"no protocol":               "[providers.x]\nbase_url = \"https://x/v1\"\n",
		"no base url":               "[providers.x]\nprotocol = \"openai-chat\"\n",
		"dangling alias":            "[providers.anthropic.models.\"mine\"]\nalias_of = \"claude-nope\"\n",
		"alias of alias":            "[providers.anthropic.models.\"mine\"]\nalias_of = \"claude-sonnet-4-5[1m]\"\n",
		"cross-provider unknown":    "[providers.anthropic.models.\"mine\"]\nalias_of = \"nope/claude-opus-5\"\n",
		"cross-provider glob alias": "[providers.anthropic.models.\"mine\"]\nalias_of = \"openai/gpt-5*\"\n",
		"fields key wrong protocol": "[providers.groq.fields]\ninclude = true\n",
		"row fields wrong protocol": "[providers.groq.models.\"llama-3.3-70b-versatile\"]\nfields = { include = true }\n",
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "providers.toml")
			if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
			data, _ := os.ReadFile("testdata/models.dev.sample.json")
			_, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithConfigPath(path), WithStateRoot(t.TempDir()))
			if err == nil {
				t.Fatalf("expected error for %s", name)
			}
			if strings.Contains(name, "fields") && !strings.Contains(err.Error(), "include") {
				t.Fatalf("fields error must name the key: %v", err)
			}
		})
	}
	// Inherited fields from a base on another protocol are ignored, not
	// errors — even when the instance is named after a registry id in its own
	// base chain (openai-codex inherits openai's Responses-only fields).
	fixtureLoad(t, nil, "[providers.work]\nbase = \"openai\"\nprotocol = \"openai-chat\"\nbase_url = \"https://gw/v1\"\n")
	fixtureLoad(t, nil, "[providers.openai]\nbase = \"openai-codex\"\nprotocol = \"anthropic\"\nbase_url = \"https://gw/v1\"\n")
}

func TestLoad_OverlayBaseCycleAndCuratedDanglingAlias(t *testing.T) {
	data, _ := os.ReadFile("testdata/models.dev.sample.json")
	_, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()),
		WithOverlay(overlayWith("[providers.a]\nbase = \"b\"\n[providers.b]\nbase = \"a\"\n")))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want base cycle error, got %v", err)
	}
	r, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()),
		WithOverlay(overlayWith("[providers.anthropic.models.\"gone\"]\nalias_of = \"claude-nope\"\n")))
	if err != nil {
		t.Fatal(err)
	}
	row := r.curated["anthropic"].head.Models["gone"]
	if !row.Hidden {
		t.Fatal("a curated dangling alias must degrade to a hidden row")
	}
	if !strings.Contains(strings.Join(r.Warnings(), "\n"), "dangling alias") {
		t.Fatalf("warnings = %v", r.Warnings())
	}
	// An instance that inherits the curated dangling alias must still load.
	path := filepath.Join(t.TempDir(), "providers.toml")
	if err := os.WriteFile(path, []byte("[providers.myclaude]\nbase = \"anthropic\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithConfigPath(path), WithStateRoot(t.TempDir()),
		WithOverlay(overlayWith("[providers.anthropic.models.\"gone\"]\nalias_of = \"claude-nope\"\n")))
	if err != nil {
		t.Fatalf("an inherited curated dangling alias must not fail the load: %v", err)
	}
	if !r.explicit["myclaude"].head.Models["gone"].Hidden {
		t.Fatal("the inherited dangling row must be hidden on the instance too")
	}
}

func TestLoad_HiddenAgainstEnvironment(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	for id, want := range map[string]bool{
		"azure": true, "amazon-bedrock": true, "google-vertex": true, "google-vertex-anthropic": true,
		"cohere": true, "deepinfra": true, "openai-compatible": true,
		"ollama": false, "openai": false, "anthropic": false, "groq": false,
	} {
		p, ok := r.Provider(id)
		if !ok {
			t.Fatalf("%s missing", id)
		}
		if p.Hidden != want {
			t.Errorf("%s Hidden = %v, want %v", id, p.Hidden, want)
		}
	}
	r = fixtureLoad(t, map[string]string{"AZURE_RESOURCE_NAME": "contoso", "AWS_REGION": "us-east-1", "GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global", "OPENAI_COMPATIBLE_BASE_URL": "http://localhost:8080/v1"}, "")
	for _, id := range []string{"azure", "amazon-bedrock", "google-vertex", "google-vertex-anthropic", "openai-compatible"} {
		if p, _ := r.Provider(id); p.Hidden {
			t.Errorf("%s must be visible with its variables set", id)
		}
	}
	url, missing, _ := r.resolveBaseURL(r.curated["amazon-bedrock"], r.curated["amazon-bedrock"].head.Transport)
	if url != "https://bedrock-mantle.us-east-1.api.aws/anthropic/v1" || len(missing) != 0 {
		t.Fatalf("bedrock url = %q missing = %v", url, missing)
	}
	url, _, _ = r.resolveBaseURL(r.curated["google-vertex-anthropic"], r.curated["google-vertex-anthropic"].head.Transport)
	if url != "https://aiplatform.googleapis.com/v1/projects/p/locations/global" {
		t.Fatalf("vertex url = %q", url)
	}
}

func TestLoad_UserVarsBeatEnvBeatCurated(t *testing.T) {
	cfg := "[providers.bedrock]\nbase = \"amazon-bedrock\"\n[providers.bedrock.vars]\n\"AWS_REGION\" = \"eu-west-1\"\n[providers.mine]\nbase = \"openai\"\n"
	r := fixtureLoad(t, map[string]string{"AWS_REGION": "us-east-1", "OPENAI_BASE_URL": "https://proxy.example/v1"}, cfg)
	url, _, _ := r.resolveBaseURL(r.explicit["bedrock"], r.explicit["bedrock"].head.Transport)
	if url != "https://bedrock-mantle.eu-west-1.api.aws/anthropic/v1" {
		t.Fatalf("user vars must win: %q", url)
	}
	url, _, _ = r.resolveBaseURL(r.explicit["mine"], r.explicit["mine"].head.Transport)
	if url != "https://proxy.example/v1" {
		t.Fatalf("env must beat the curated default: %q", url)
	}
	url, _, _ = r.resolveBaseURL(r.curated["anthropic"], r.curated["anthropic"].head.Transport)
	if url != "https://api.anthropic.com/v1" {
		t.Fatalf("curated default: %q", url)
	}
}

func TestLoad_UnresolvedUserVarWarnsAndStops(t *testing.T) {
	cfg := "[providers.bedrock]\nbase = \"amazon-bedrock\"\n[providers.bedrock.vars]\n\"AWS_REGION\" = \"$AWS_REGION_PROD\"\n"
	r := fixtureLoad(t, map[string]string{"AWS_REGION": "us-east-1", "AWS_BEARER_TOKEN_BEDROCK": "bt"}, cfg)
	rec := r.explicit["bedrock"]
	url, missing, warns := r.resolveBaseURL(rec, rec.head.Transport)
	if !strings.Contains(url, "{AWS_REGION}") || !slices.Contains(missing, "AWS_REGION") {
		t.Fatalf("an unset user var must not fall through to the environment: %q %v", url, missing)
	}
	if !slices.Contains(warns, "unresolved variable AWS_REGION_PROD") {
		t.Fatalf("resolveBaseURL warnings = %v", warns)
	}
	res, err := r.Resolve("bedrock/anthropic.claude-sonnet-5")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Warnings, "unresolved variable AWS_REGION_PROD") {
		t.Fatalf("Resolve warnings = %v", res.Warnings)
	}
}

func TestLoad_OllamaHostRule(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"OLLAMA_HOST": "::1"}, "")
	url, _, _ := r.resolveBaseURL(r.curated["ollama"], r.curated["ollama"].head.Transport)
	if url != "http://[::1]:11434/v1" {
		t.Fatalf("OLLAMA_HOST: %q", url)
	}
	r = fixtureLoad(t, map[string]string{"OLLAMA_HOST": "::1", "OLLAMA_BASE_URL": "http://proxy/ollama/v1"}, "")
	url, _, _ = r.resolveBaseURL(r.curated["ollama"], r.curated["ollama"].head.Transport)
	if url != "http://proxy/ollama/v1" {
		t.Fatalf("OLLAMA_BASE_URL must win: %q", url)
	}
	r = fixtureLoad(t, nil, "[providers.ollama]\nbase_url = \"http://gpu:11434/v1\"\n")
	url, _, _ = r.resolveBaseURL(r.explicit["ollama"], r.explicit["ollama"].head.Transport)
	if url != "http://gpu:11434/v1" {
		t.Fatalf("a literal base_url bypasses the host rule: %q", url)
	}
	r = fixtureLoad(t, map[string]string{"OLLAMA_HOST": "ftp://bad"}, "")
	if _, _, warns := r.resolveBaseURL(r.curated["ollama"], r.curated["ollama"].head.Transport); len(warns) == 0 {
		t.Fatal("an invalid OLLAMA_HOST must warn")
	}
}

func TestLoad_RowHidden(t *testing.T) {
	r := fixtureLoad(t, nil, "")
	rows := r.curated["amazon-bedrock"].head.Models
	if !rows["global.openai.gpt-5.6-sol"].Hidden || rows["openai.gpt-oss-120b"].Hidden || rows["anthropic.claude-opus-5"].Hidden {
		t.Fatal("bedrock row hiding wrong")
	}
	data, _ := os.ReadFile("testdata/models.dev.sample.json")
	ov := overlayWith("[providers.\"amazon-bedrock\".models.\"global.openai.gpt-5.6-sol\"]\ntransport = \"bedrock-mantle-openai\"\nprotocol = \"openai-responses\"\n")
	r2, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithNoUserLayer(), WithStateRoot(t.TempDir()), WithOverlay(ov))
	if err != nil {
		t.Fatal(err)
	}
	if r2.curated["amazon-bedrock"].head.Models["global.openai.gpt-5.6-sol"].Hidden {
		t.Fatal("a layer that supplies a transport must un-hide the row")
	}
}

func TestLoad_UserLayerTriState(t *testing.T) {
	data, _ := os.ReadFile("testdata/models.dev.sample.json")
	dir := t.TempDir()
	env := map[string]string{"XDG_CONFIG_HOME": dir, "EVENER_PROVIDERS_CONFIG": ""}
	r, err := Load(WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir()))
	if err != nil || !strings.Contains(r.UserLayerNote(), "EVENER_PROVIDERS_CONFIG is empty") {
		t.Fatalf("empty variable: err=%v note=%q", err, r.UserLayerNote())
	}
	delete(env, "EVENER_PROVIDERS_CONFIG")
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir()))
	if err != nil || !strings.Contains(r.UserLayerNote(), filepath.Join(dir, "evener", "providers.toml")) {
		t.Fatalf("default path: err=%v note=%q", err, r.UserLayerNote())
	}
	path := filepath.Join(dir, "custom.toml")
	if err := os.WriteFile(path, []byte("[providers.groq]\nprotocol = \"openai-responses\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	env["EVENER_PROVIDERS_CONFIG"] = path
	r, err = Load(WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir()))
	if err != nil || r.explicit["groq"] == nil {
		t.Fatalf("explicit path: err=%v", err)
	}
	if err := os.WriteFile(path, []byte("[instances.openai]\ntype = \"openai\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(WithSnapshot(data), WithEnv(mapEnv(env)), WithStateRoot(t.TempDir())); !errors.Is(err, ErrOldSchema) {
		t.Fatalf("old schema must surface as ErrOldSchema: %v", err)
	}
}

func TestLoad_WithInstances(t *testing.T) {
	r := fixtureLoad(t, nil, "", WithInstances(map[string]Provider{
		"tiny": {Base: "openai", Protocol: ProtocolOpenAIChat, Transport: Transport{BaseURL: "http://127.0.0.1:1/v1"}},
	}))
	tiny := r.explicit["tiny"]
	if tiny == nil || tiny.providerID != "openai" || tiny.head.Protocol != ProtocolOpenAIChat || !tiny.injected {
		t.Fatalf("tiny: %+v", tiny)
	}
}
