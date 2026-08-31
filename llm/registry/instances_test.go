package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type fakeCreds map[string]string

func (f fakeCreds) Lookup(name string) (string, bool) { v, ok := f[name]; return v, ok }

func instanceNames(r *Registry) []string {
	var out []string
	for _, i := range r.Instances() {
		out = append(out, i.Name)
	}
	return out
}

func TestInstances_ImplicitFromEnv(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"XAI_API_KEY": "k"}, "")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"xai", "ollama"}) {
		t.Fatalf("instances = %v", got)
	}
	name, _, err := r.DefaultInstance()
	if err != nil || name != "xai" {
		t.Fatalf("default = %q, %v", name, err)
	}
	r = fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "k"}, "")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"openai", "ollama"}) {
		t.Fatalf("OPENAI_API_KEY alone must not conjure openai-codex: %v", got)
	}
	r = fixtureLoad(t, map[string]string{"GITHUB_TOKEN": "t", "HF_TOKEN": "t", "TOGETHERAI_API_KEY": "t"}, "")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"ollama"}) {
		t.Fatalf("non-implicit providers and undocumented aliases must not become instances: %v", got)
	}
}

func TestInstances_PseudoProviderNeedsBaseURL(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"OPENAI_COMPATIBLE_BASE_URL": "http://localhost:8080/v1"}, "")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"ollama", "openai-compatible"}) {
		t.Fatalf("instances = %v", got)
	}
	if _, _, err := r.DefaultInstance(); err == nil || !strings.Contains(err.Error(), "ollama") || !strings.Contains(err.Error(), "openai-compatible") {
		t.Fatalf("no default model anywhere must name the instances: %v", err)
	}
	r = fixtureLoad(t, nil, "")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"ollama"}) {
		t.Fatalf("unset base URL must yield no pseudo-provider instance: %v", got)
	}
}

func TestInstances_GCPADCAndOAuth(t *testing.T) {
	home := t.TempDir()
	adc := filepath.Join(home, ".config", "gcloud", "application_default_credentials.json")
	if err := os.MkdirAll(filepath.Dir(adc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adc, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := fixtureLoad(t, map[string]string{"HOME": home}, "")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"ollama"}) {
		t.Fatalf("the ADC file alone makes no Vertex instance: %v", got)
	}
	r = fixtureLoad(t, map[string]string{"HOME": home, "GOOGLE_VERTEX_PROJECT": "p", "GOOGLE_VERTEX_LOCATION": "global"}, "")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"google-vertex-anthropic", "google-vertex", "ollama"}) {
		t.Fatalf("instances = %v", got)
	}
	if name, _, _ := r.DefaultInstance(); name != "google-vertex-anthropic" {
		t.Fatalf("default = %q", name)
	}
	state := t.TempDir()
	if err := os.MkdirAll(filepath.Join(state, "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(oauthRecordPath(state, "openai-codex"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	r = fixtureLoad(t, map[string]string{"OPENAI_API_KEY": "k"}, "", WithStateRoot(state))
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"openai-codex", "openai", "ollama"}) {
		t.Fatalf("instances = %v", got)
	}
	if name, _, _ := r.DefaultInstance(); name != "openai-codex" {
		t.Fatalf("stored OAuth must beat the API key: %q", name)
	}
}

func TestInstances_RankingAndShadowing(t *testing.T) {
	env := map[string]string{"ANTHROPIC_API_KEY": "a", "GROQ_API_KEY": "g"}
	r := fixtureLoad(t, env, "[providers.groq]\nprotocol = \"openai-responses\"\n")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"anthropic", "groq", "ollama"}) {
		t.Fatalf("instances = %v", got)
	}
	insts := r.Instances()
	if insts[1].Implicit || insts[1].Protocol != ProtocolOpenAIResponses || !insts[0].Implicit || !insts[0].Default {
		t.Fatalf("shadowing entry must be explicit and keep its rank: %+v", insts)
	}
	r = fixtureLoad(t, env, "[providers.work]\nbase = \"openai\"\nbase_url = \"https://gw/v1\"\napi_key_env = [\"GW_KEY\"]\n")
	if got := instanceNames(r); !reflect.DeepEqual(got, []string{"anthropic", "groq", "ollama", "work"}) {
		t.Fatalf("custom entries rank after default_order: %v", got)
	}
	r = fixtureLoad(t, env, "[providers.ollama]\ndefault_model = \"llama3:8b\"\n")
	if name, _, _ := r.DefaultInstance(); name != "anthropic" {
		t.Fatalf("anthropic must outrank an explicit ollama with a default model: %q", name)
	}
	r = fixtureLoad(t, nil, "[providers.ollama]\ndefault_model = \"llama3:8b\"\n")
	if name, _, _ := r.DefaultInstance(); name != "ollama" {
		t.Fatalf("ollama wins when it is the sole candidate: %q", name)
	}
	r = fixtureLoad(t, map[string]string{"GEMINI_API_KEY": "g", "OPENAI_API_KEY": "o"}, "")
	if name, _, _ := r.DefaultInstance(); name != "openai" {
		t.Fatalf("§14.1: GEMINI + OPENAI now defaults to openai, got %q", name)
	}
}

func TestInstances_DefaultKey(t *testing.T) {
	r := fixtureLoad(t, map[string]string{"ANTHROPIC_API_KEY": "a"}, "default = \"groq\"\n")
	name, warns, err := r.DefaultInstance()
	if err != nil || name != "anthropic" || len(warns) == 0 || !strings.Contains(warns[0], "groq") {
		t.Fatalf("credential-less implicit default must warn and fall through: %q %v %v", name, warns, err)
	}
	r = fixtureLoad(t, map[string]string{"ANTHROPIC_API_KEY": "a"}, "default = \"azure\"\n")
	if name, warns, _ := r.DefaultInstance(); name != "anthropic" || len(warns) == 0 {
		t.Fatalf("hidden implicit default must warn and fall through: %q %v", name, warns)
	}
	r = fixtureLoad(t, map[string]string{"ANTHROPIC_API_KEY": "a", "GROQ_API_KEY": "g"}, "default = \"groq\"\n")
	if name, _, err := r.DefaultInstance(); err != nil || name != "groq" {
		t.Fatalf("explicit default: %q %v", name, err)
	}
	data, _ := os.ReadFile("testdata/models.dev.sample.json")
	path := filepath.Join(t.TempDir(), "providers.toml")
	_ = os.WriteFile(path, []byte("default = \"huggingface\"\n"), 0o600)
	if _, err := Load(WithSnapshot(data), WithEnv(mapEnv(nil)), WithConfigPath(path), WithStateRoot(t.TempDir())); err == nil || !strings.Contains(err.Error(), "huggingface") {
		t.Fatalf("a non-implicit registry id as default is a load error: %v", err)
	}
	r = fixtureLoad(t, nil, "")
	if _, _, err := r.DefaultInstance(); err == nil || !strings.Contains(err.Error(), "ollama") {
		t.Fatalf("only ollama, no default model: %v", err)
	}
	// An invalid OLLAMA_HOST hides ollama, the one provider that is an
	// instance with no credential, leaving no instance at all.
	r = fixtureLoad(t, map[string]string{"OLLAMA_HOST": "ftp://bad"}, "")
	if _, _, err := r.DefaultInstance(); err == nil || !strings.Contains(err.Error(), "no default instance") {
		t.Fatalf("no instance at all: %v", err)
	}
}

func TestCredential_Order(t *testing.T) {
	cfg := `
[providers.lit]
base = "openai"
api_key = "literal-key"
[providers.envref]
base = "openai"
api_key = "$MY_KEY"
[providers.hdr]
base = "openai"
base_url = "https://gw/v1"
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.stored]
base = "openai"
base_url = "https://gw/v1"
[providers.work]
base = "openai"
base_url = "https://gw/v1"
[providers.work2]
base = "openai"
base_url = "https://gw/v1"
api_key_env = ["GW_KEY"]
[providers.gw]
base = "openai"
base_url = "https://gw/v1"
[providers.anthropic]
base_url = "https://gw/v1"
[providers.same]
base = "openai"
base_url = "https://api.openai.com/v1"
[providers.mine]
base = "openai"
[providers.bedrock]
base = "amazon-bedrock"
[providers.bedrock.vars]
"AWS_REGION" = "us-east-1"
[providers.viaproxy]
base = "openai"
base_url = "https://proxy/v1"
`
	env := map[string]string{"OPENAI_API_KEY": "sk-openai", "PORTKEY_KEY": "pk", "GW_KEY": "gw", "GW_API_KEY": "gw2", "ANTHROPIC_API_KEY": "sk-ant", "AWS_BEARER_TOKEN_BEDROCK": "bt", "OPENAI_BASE_URL": "https://proxy/v1"}
	r := fixtureLoad(t, env, cfg, WithCredentials(fakeCreds{"stored": "from-store"}))
	want := map[string]Credential{
		"lit":       {Value: "literal-key", Source: "api_key"},
		"envref":    {Value: "", Source: "none"},
		"hdr":       {Value: "Bearer pk", Source: "credential_headers"},
		"stored":    {Value: "from-store", Source: "store"},
		"work":      {Value: "", Source: "none"},
		"work2":     {Value: "gw", Source: "env:GW_KEY"},
		"gw":        {Value: "gw2", Source: "env:GW_API_KEY"},
		"anthropic": {Value: "", Source: "none"},
		"same":      {Value: "sk-openai", Source: "env:OPENAI_API_KEY"},
		"mine":      {Value: "sk-openai", Source: "env:OPENAI_API_KEY"},
		"bedrock":   {Value: "bt", Source: "env:AWS_BEARER_TOKEN_BEDROCK"},
		"viaproxy":  {Value: "sk-openai", Source: "env:OPENAI_API_KEY"},
	}
	for name, w := range want {
		got, warns := r.credential(r.explicit[name])
		if got != w {
			t.Errorf("%s: credential = %+v, want %+v", name, got, w)
		}
		if w.Source == "none" && len(warns) == 0 {
			t.Errorf("%s: missing 'no credential' warning", name)
		}
		if w.Source != "none" && len(warns) != 0 {
			t.Errorf("%s: unexpected warnings %v", name, warns)
		}
	}
	if _, warns := r.credential(r.explicit["envref"]); !strings.Contains(strings.Join(warns, " "), "MY_KEY unset") {
		t.Fatalf("unset $VAR must be named: %v", warns)
	}
	if got, warns := r.credential(r.curated["ollama"]); got.Source != "none" || len(warns) != 0 {
		t.Fatalf("optional-bearer without a key must not warn: %+v %v", got, warns)
	}
	r = fixtureLoad(t, map[string]string{"OLLAMA_API_KEY": "ok"}, "")
	if got, _ := r.credential(r.curated["ollama"]); got.Source != "env:OLLAMA_API_KEY" {
		t.Fatalf("optional-bearer with a key: %+v", got)
	}
}

func TestCredential_ValueNeverSerializes(t *testing.T) {
	raw, err := json.Marshal(Credential{Value: "x", Source: "store"})
	if err != nil || strings.Contains(string(raw), "x") {
		t.Fatalf("a serialized credential must carry the source only: %s %v", raw, err)
	}
}

func TestEnvVarName(t *testing.T) {
	if envVarName("kimi-for-coding") != "KIMI_FOR_CODING" || envVarName("zai-coding-plan") != "ZAI_CODING_PLAN" || envVarName("work") != "WORK" {
		t.Fatal("envVarName wrong")
	}
}
