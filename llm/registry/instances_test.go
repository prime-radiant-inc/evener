package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// TestInstances_ShadowedEnvVar covers the shadow relation TestCredential_Order
// does not: an environment variable that is SET but loses to a
// higher-precedence source (api_key, credential_headers, or store, spec
// §10), which today's ActiveSource cannot express because it only ever names
// the winner (issue #712). Instance.ShadowedEnvVar names that variable, or
// is empty when nothing shadows it — including when an env source is itself
// what wins, or when no candidate is set at all.
func TestInstances_ShadowedEnvVar(t *testing.T) {
	cfg := `
[providers.lit]
base = "openai"
api_key = "literal-key"
[providers.hdr]
base = "openai"
base_url = "https://gw/v1"
api_key_env = ["HDR_KEY"]
credential_headers = { "Authorization" = "Bearer $PORTKEY_KEY" }
[providers.stored]
base = "openai"
base_url = "https://gw/v1"
api_key_env = ["STORED_KEY"]
[providers.envwins]
base = "openai"
base_url = "https://gw/v1"
api_key_env = ["ENV_KEY"]
[providers.nothing]
base = "openai"
base_url = "https://gw/v1"
api_key_env = ["UNSET_KEY"]
[providers.apiref]
base = "openai"
base_url = "https://gw/v1"
api_key = "$APIREF_KEY"
api_key_env = ["APIREF_KEY"]
`
	env := map[string]string{
		"OPENAI_API_KEY": "sk-openai", "PORTKEY_KEY": "pk",
		"HDR_KEY": "hdr-val", "STORED_KEY": "stored-shadow-val", "ENV_KEY": "env-val",
		"APIREF_KEY": "apiref-val",
	}
	r := fixtureLoad(t, env, cfg, WithCredentials(fakeCreds{"stored": "from-store"}))
	want := map[string]struct {
		source string
		shadow string
	}{
		"lit":     {"api_key", "OPENAI_API_KEY"}, // no base_url: inherits openai's APIKeyEnv, which is set but loses to the literal key
		"hdr":     {"credential_headers", "HDR_KEY"},
		"stored":  {"store", "STORED_KEY"},
		"envwins": {"env:ENV_KEY", ""}, // the env source is itself the winner, not a shadow
		"nothing": {"none", ""},        // UNSET_KEY is unset: no candidate to shadow with
		// api_key is itself a $VAR reference to APIREF_KEY, which is ALSO
		// listed in api_key_env: that name is what resolved the credential,
		// not a loser, even though it is also a candidate (PR #758 review).
		"apiref": {"api_key", ""},
	}
	for name, w := range want {
		inst, ok := r.Instance(name)
		if !ok {
			t.Fatalf("%s: not an instance", name)
		}
		if inst.CredentialSource != w.source {
			t.Fatalf("%s: credential source = %q, want %q", name, inst.CredentialSource, w.source)
		}
		if inst.ShadowedEnvVar != w.shadow {
			t.Errorf("%s: ShadowedEnvVar = %q, want %q", name, inst.ShadowedEnvVar, w.shadow)
		}
	}
}

// TestInstances_ShadowedEnvVarSkipsAnUnresolvedHigherPrecedenceScheme covers
// the other half of the PR #758 review: oauth-openai-codex and gcp-adc are
// terminal branches in credential (spec §10) - when neither resolves,
// credential returns "none" without ever consulting api_key_env, so a
// candidate env var that happens to be set must not be reported as shadowed
// by a scheme that never looked at it ("WORK_API_KEY set but shadowed by
// none" would be a lie: none is not a competing precedence source here, it
// is this instance's entire resolution never reaching the env candidates at
// all). google-vertex resolves through gcp-adc; with no ADC file or
// GOOGLE_APPLICATION_CREDENTIALS reachable, it stays unresolved.
func TestInstances_ShadowedEnvVarSkipsAnUnresolvedHigherPrecedenceScheme(t *testing.T) {
	cfg := `
[providers.myvertex]
base = "google-vertex"
api_key_env = ["FAKE_VERTEX_KEY"]
`
	r := fixtureLoad(t, map[string]string{"FAKE_VERTEX_KEY": "set-but-irrelevant"}, cfg)
	rec := r.explicit["myvertex"]
	if rec.head.Transport.Auth != AuthGCPADC {
		t.Fatalf("myvertex must inherit gcp-adc from google-vertex, got %q", rec.head.Transport.Auth)
	}
	cred, _ := r.credential(rec)
	if cred.Source != "none" {
		t.Fatalf("gcp-adc with no ADC reachable must resolve none, got %q", cred.Source)
	}
	if got := r.shadowedEnvVar(rec, cred); got != "" {
		t.Fatalf("an unresolved gcp-adc scheme must never report a shadow, got %q", got)
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

const vertexUserInstanceToml = `
[providers.vertex]
base = "google-vertex"
[providers.vertex.vars]
"GOOGLE_VERTEX_PROJECT" = "my-project"
"GOOGLE_VERTEX_LOCATION" = "global"
`

// noADCEnv is an environment where adcAvailable is false: an empty HOME and
// no GOOGLE_APPLICATION_CREDENTIALS.
func noADCEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"HOME": t.TempDir()}
}

func TestCredential_GCPADCPrefersStoredJSON(t *testing.T) {
	const stored = `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`
	r := fixtureLoad(t, noADCEnv(t), vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": stored}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.CredentialSource != "store" || len(inst.Warnings) != 0 {
		t.Fatalf("instance = %+v ok=%v; want source store with no warnings", inst, ok)
	}
	res, err := r.Resolve("vertex/gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "store" || res.Credential.Value != stored {
		t.Fatalf("credential = %+v", res.Credential)
	}
}

func TestCredential_GCPADCStoreSourceNeverReportsShadowedEnvVar(t *testing.T) {
	const stored = `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`
	env := noADCEnv(t)
	env["VERTEX_API_KEY"] = "placeholder"
	r := fixtureLoad(t, env, vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": stored}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.ShadowedEnvVar != "" {
		t.Fatalf("instance = %+v ok=%v; want no shadowed env var (gcp-adc never consults api_key_env)", inst, ok)
	}
	res, err := r.Resolve("vertex/gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if res.ShadowedEnvVar != "" {
		t.Fatalf("resolved.ShadowedEnvVar = %q, want empty", res.ShadowedEnvVar)
	}
}

func TestCredential_GCPADCWithoutStoreOrFileIsNoneAndNamesTheRemedies(t *testing.T) {
	r := fixtureLoad(t, noADCEnv(t), vertexUserInstanceToml, WithCredentials(fakeCreds{}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.CredentialSource != "none" {
		t.Fatalf("instance = %+v ok=%v", inst, ok)
	}
	joined := strings.Join(inst.Warnings, "; ")
	for _, want := range []string{"gcloud auth application-default login", "GOOGLE_APPLICATION_CREDENTIALS", "credential JSON"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
}

// writeFakeADCFile writes an ADC file under env's HOME so adcAvailable(env)
// reports true, the same technique golden_test.go's goldenRegistry uses.
func writeFakeADCFile(t *testing.T, env map[string]string) {
	t.Helper()
	adc := filepath.Join(env["HOME"], ".config", "gcloud", "application_default_credentials.json")
	if err := os.MkdirAll(filepath.Dir(adc), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(adc, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCredential_GCPADCIgnoresNonJSONStoreValue_WithADC(t *testing.T) {
	env := noADCEnv(t)
	writeFakeADCFile(t, env)
	r := fixtureLoad(t, env, vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": "AQ.legacy-key-not-json"}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.CredentialSource != "adc" {
		t.Fatalf("instance = %+v ok=%v; want source adc (the stale store value must not shadow it)", inst, ok)
	}
	if joined := strings.Join(inst.Warnings, "; "); !strings.Contains(joined, "not a credential JSON") {
		t.Fatalf("warnings = %q, want it to name the ignored store value", joined)
	}
	res, err := r.Resolve("vertex/gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "adc" || res.Credential.Value != "" {
		t.Fatalf("credential = %+v, want the adc source with no value", res.Credential)
	}
}

func TestCredential_GCPADCIgnoresNonJSONStoreValue_WithoutADC(t *testing.T) {
	r := fixtureLoad(t, noADCEnv(t), vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": "AQ.legacy-key-not-json"}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.CredentialSource != "none" {
		t.Fatalf("instance = %+v ok=%v; want source none", inst, ok)
	}
	joined := strings.Join(inst.Warnings, "; ")
	for _, want := range []string{"not a credential JSON", "gcloud auth application-default login"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
}

// TestCredential_GCPADCIgnoresUnsupportedStoreJSON_WithADC covers a stored
// value that IS valid JSON but not a type the gcp-adc scheme can mint a
// token from (external_account, say): it must not shadow a working ADC
// file, so it falls through to adc with a warning naming both problems
// (roborev round 3, F1).
func TestCredential_GCPADCIgnoresUnsupportedStoreJSON_WithADC(t *testing.T) {
	env := noADCEnv(t)
	writeFakeADCFile(t, env)
	r := fixtureLoad(t, env, vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": `{"type":"external_account","audience":"x"}`}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.CredentialSource != "adc" {
		t.Fatalf("instance = %+v ok=%v; want source adc (the unsupported store value must not shadow it)", inst, ok)
	}
	joined := strings.Join(inst.Warnings, "; ")
	for _, want := range []string{"not supported", "not a credential JSON"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
	res, err := r.Resolve("vertex/gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "adc" || res.Credential.Value != "" {
		t.Fatalf("credential = %+v, want the adc source with no value", res.Credential)
	}
}

// TestCredential_GCPADCIgnoresTypeOnlyStoreJSON_WithADC covers a stored value
// whose type is allowed but which carries no key material: Google's parser
// accepts it and fails only at the first request, so the gate refuses it here
// and a working ADC file still wins (roborev round 6, F1).
func TestCredential_GCPADCIgnoresTypeOnlyStoreJSON_WithADC(t *testing.T) {
	env := noADCEnv(t)
	writeFakeADCFile(t, env)
	r := fixtureLoad(t, env, vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": `{"type":"service_account"}`}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.CredentialSource != "adc" {
		t.Fatalf("instance = %+v ok=%v; want source adc (a store value with no key material must not shadow it)", inst, ok)
	}
	joined := strings.Join(inst.Warnings, "; ")
	for _, want := range []string{"missing client_email, private_key", "not a credential JSON"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
	res, err := r.Resolve("vertex/gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "adc" || res.Credential.Value != "" {
		t.Fatalf("credential = %+v, want the adc source with no value", res.Credential)
	}
}

// TestCredential_GCPADCIgnoresUnusableKeyStoreJSON_WithADC covers a stored
// service_account whose private_key is not key material Google's signer can
// parse: it would fail only at the first request, so the gate refuses it here
// and a working ADC file still wins (roborev round 11, F1).
func TestCredential_GCPADCIgnoresUnusableKeyStoreJSON_WithADC(t *testing.T) {
	env := noADCEnv(t)
	writeFakeADCFile(t, env)
	r := fixtureLoad(t, env, vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": `{"type":"service_account","client_email":"sa@example.iam.gserviceaccount.com","private_key":"not-a-real-key"}`}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.CredentialSource != "adc" {
		t.Fatalf("instance = %+v ok=%v; want source adc (a store value whose key will not parse must not shadow it)", inst, ok)
	}
	joined := strings.Join(inst.Warnings, "; ")
	for _, want := range []string{"unusable private_key", "not a credential JSON"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
	res, err := r.Resolve("vertex/gemini-2.5-flash")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "adc" || res.Credential.Value != "" {
		t.Fatalf("credential = %+v, want the adc source with no value", res.Credential)
	}
}

func TestCredential_GCPADCIgnoresUnsupportedStoreJSON_WithoutADC(t *testing.T) {
	r := fixtureLoad(t, noADCEnv(t), vertexUserInstanceToml, WithCredentials(fakeCreds{"vertex": `{"type":"external_account","audience":"x"}`}))
	inst, ok := r.Instance("vertex")
	if !ok || inst.CredentialSource != "none" {
		t.Fatalf("instance = %+v ok=%v; want source none", inst, ok)
	}
	joined := strings.Join(inst.Warnings, "; ")
	for _, want := range []string{"not supported", "not a credential JSON", "gcloud auth application-default login"} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q lack %q", joined, want)
		}
	}
}

func TestImplicitGoogleVertexExistsWithStoredJSONAndNoADCFile(t *testing.T) {
	env := noADCEnv(t)
	env["GOOGLE_VERTEX_PROJECT"], env["GOOGLE_VERTEX_LOCATION"] = "my-project", "global"
	without := fixtureLoad(t, env, "")
	if slices.Contains(instanceNames(without), "google-vertex") {
		t.Fatal("google-vertex exists with neither an ADC file nor a store entry")
	}
	with := fixtureLoad(t, env, "", WithCredentials(fakeCreds{"google-vertex": `{"type":"authorized_user","client_id":"a","client_secret":"b","refresh_token":"c"}`}))
	if !slices.Contains(instanceNames(with), "google-vertex") {
		t.Fatalf("a store entry did not make google-vertex exist: %v", instanceNames(with))
	}
}
