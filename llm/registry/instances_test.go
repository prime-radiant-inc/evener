package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/valueexpr"
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
[providers.hdrgap]
base = "openai"
base_url = "https://gw/v1"
credential_headers = { "Authorization" = "Bearer $PORTKEY_MISSING" }
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
		"lit": {Value: "literal-key", Source: "api_key"},
		// An authored layer whose variables are unset is terminal, and it says
		// so: "none" alone would be indistinguishable from an instance that
		// authors no credential at all, and the difference decides whether a key
		// a writer stores under the name is ever sent (AuthoredLayer).
		"envref":    {Value: "", Source: "none", AuthoredLayer: "api_key"},
		"hdr":       {Value: "Bearer pk", Source: "credential_headers"},
		"hdrgap":    {Value: "", Source: "none", AuthoredLayer: "credential_headers"},
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
		rec := r.explicit[name]
		got, warns := r.credential(rec, r.listingTransport(rec))
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
	rec := r.explicit["envref"]
	if _, warns := r.credential(rec, r.listingTransport(rec)); !strings.Contains(strings.Join(warns, " "), "MY_KEY unset") {
		t.Fatalf("unset $VAR must be named: %v", warns)
	}
	curated := r.curated["ollama"]
	if got, warns := r.credential(curated, r.listingTransport(curated)); got.Source != "none" || len(warns) != 0 {
		t.Fatalf("optional-bearer without a key must not warn: %+v %v", got, warns)
	}
	r = fixtureLoad(t, map[string]string{"OLLAMA_API_KEY": "ok"}, "")
	curated = r.curated["ollama"]
	if got, _ := r.credential(curated, r.listingTransport(curated)); got.Source != "env:OLLAMA_API_KEY" {
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
	cred, _ := r.credential(rec, r.listingTransport(rec))
	if cred.Source != "none" {
		t.Fatalf("gcp-adc with no ADC reachable must resolve none, got %q", cred.Source)
	}
	if got := r.shadowedEnvVar(rec, rec.head.Transport, cred); got != "" {
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

// TestProviderRenameLeavesInstance pins the rule the hub's rename note reads:
// after a rename moves the user's authored entry, stored credential and OAuth
// record away, old curated id still resolves an instance only when the curated
// provider itself re-derives from what the rename cannot move.
func TestProviderRenameLeavesInstance(t *testing.T) {
	// baseEnv keeps the curated providers addressable (so none of these cases
	// is refused as hidden) while ADC stays absent.
	baseEnv := func(t *testing.T) map[string]string {
		t.Helper()
		env := noADCEnv(t)
		env["GOOGLE_VERTEX_PROJECT"] = "p"
		env["GOOGLE_VERTEX_LOCATION"] = "global"
		env["OLLAMA_HOST"] = "localhost"
		return env
	}
	t.Run("set environment variable re-derives", func(t *testing.T) {
		env := baseEnv(t)
		env["GOOGLE_VERTEX_API_KEY"] = "gk"
		r := fixtureLoad(t, env, "")
		if !r.ProviderRenameLeavesInstance("google-vertex-express") {
			t.Fatal("google-vertex-express: want true, the curated provider reads GOOGLE_VERTEX_API_KEY which is set")
		}
	})
	t.Run("unset environment variable leaves nothing", func(t *testing.T) {
		r := fixtureLoad(t, baseEnv(t), "")
		if r.ProviderRenameLeavesInstance("google-vertex-express") {
			t.Fatal("google-vertex-express: want false, the curated provider holds no other credential")
		}
	})
	t.Run("keyless curated provider re-derives", func(t *testing.T) {
		r := fixtureLoad(t, baseEnv(t), "")
		if !r.ProviderRenameLeavesInstance("ollama") {
			t.Fatal("ollama: want true, the optional-bearer scheme needs no credential")
		}
	})
	t.Run("non-implicit curated provider leaves nothing", func(t *testing.T) {
		// azure-cognitive-services is curated but not implicit: with its
		// resource name and key variable both set it is addressable and its
		// api_key_env is satisfied, yet computeInstances derives no row for it,
		// so a rename leaves nothing behind.
		env := baseEnv(t)
		env["AZURE_COGNITIVE_SERVICES_RESOURCE_NAME"] = "r"
		env["AZURE_COGNITIVE_SERVICES_API_KEY"] = "k"
		r := fixtureLoad(t, env, "")
		if r.ProviderRenameLeavesInstance("azure-cognitive-services") {
			t.Fatal("azure-cognitive-services: want false, the curated provider is not implicit so no row re-derives")
		}
	})
	t.Run("implicit provider with a terminal inline expression leaves nothing", func(t *testing.T) {
		// The inline api_key is present, so credential() stops there even
		// though RENAME_INLINE_KEY is unset; the set RENAME_INLINE_ENV must not
		// be reached, and a row that resolves nothing leaves nothing behind.
		env := baseEnv(t)
		env["RENAME_INLINE_ENV"] = "k"
		r := fixtureLoad(t, env, "", WithOverlay(overlayWith(`
[providers."rename-inline"]
implicit = true
protocol = "openai-chat"
auth = "bearer"
api_key = "$RENAME_INLINE_KEY"
api_key_env = ["RENAME_INLINE_ENV"]
base_url = "https://rename-inline.example.test/v1"
`)))
		if r.ProviderRenameLeavesInstance("rename-inline") {
			t.Fatal("rename-inline: want false, the present api_key is terminal and its variable is unset, so the row resolves nothing")
		}
	})
	t.Run("oauth record is the credential the rename moves", func(t *testing.T) {
		r := fixtureLoad(t, baseEnv(t), "")
		if r.ProviderRenameLeavesInstance("openai-codex") {
			t.Fatal("openai-codex: want false, the only credential is the record the rename moves")
		}
	})
	t.Run("gcp-adc with the ADC file re-derives", func(t *testing.T) {
		env := baseEnv(t)
		writeFakeADCFile(t, env)
		r := fixtureLoad(t, env, "")
		if !r.ProviderRenameLeavesInstance("google-vertex") {
			t.Fatal("google-vertex: want true with ADC present")
		}
	})
	t.Run("gcp-adc without the ADC file leaves nothing", func(t *testing.T) {
		r := fixtureLoad(t, baseEnv(t), "")
		if r.ProviderRenameLeavesInstance("google-vertex") {
			t.Fatal("google-vertex: want false with no ADC, so the stored-JSON false positive is gone")
		}
	})
	t.Run("hidden curated provider resolves no instance", func(t *testing.T) {
		r := fixtureLoad(t, baseEnv(t), "")
		if r.ProviderRenameLeavesInstance("openai-compatible") {
			t.Fatal("openai-compatible: want false, its base URL variable is unset so it is hidden")
		}
	})
	t.Run("non-curated id leaves nothing", func(t *testing.T) {
		r := fixtureLoad(t, baseEnv(t), "")
		if r.ProviderRenameLeavesInstance("work") {
			t.Fatal("work: want false, freeing a non-curated name recreates nothing")
		}
	})
}

// A credential whose api_key is a command expression resolves like any other
// inline credential: the command mints the value, the shared evaluator caches
// it across resolutions, and a failing command behaves like an unset variable
// — warning plus no credential, retried on the next resolution.
func TestCredentialFromCommandExpression(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"bearer\"\n" +
		"api_key = '''$(get-gateway-token)'''\n" +
		"[providers.gw.models.\"house-model\"]\n"

	runs := 0
	valueexpr.RunCommand = func(string) (string, error) { runs++; return "minted-token", nil }
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Value != "minted-token" || res.Credential.Source != "api_key" {
		t.Fatalf("credential = %+v; want minted-token from api_key", res.Credential)
	}
	// Resolve again: the load-time derivation and every resolution share the
	// evaluator's cache, so one command run serves them all.
	if _, err := r.Resolve("gw/house-model"); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("executor ran %d times; want 1 (shared cache)", runs)
	}

	valueexpr.ResetForTest()
	valueexpr.RunCommand = func(string) (string, error) {
		return "", errors.New("command exited with status 1: session expired")
	}
	failing := fixtureLoad(t, nil, config)
	res, err = failing.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "none" {
		t.Fatalf("credential source = %q; want none", res.Credential.Source)
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), "no credential (command expression failed: command exited with status 1: session expired)") {
		t.Fatalf("warnings = %v; want the command failure wording", res.Warnings)
	}
}

// An api_key or Authorization value that expands to empty (an empty
// ${VAR:-} default) is not a present credential: it resolves as none with a
// warning, never as a credential whose value is the empty string.
func TestCredentialEmptyExpansionIsNoCredential(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config string
	}{
		{
			name: "api_key",
			config: "[providers.gw]\n" +
				"base = \"openai-compatible\"\n" +
				"base_url = \"https://gw.internal.example/v1\"\n" +
				"protocol = \"openai-chat\"\n" +
				"auth = \"bearer\"\n" +
				"api_key = '''${MISSING:-}'''\n" +
				"[providers.gw.models.\"house-model\"]\n",
		},
		{
			name: "authorization",
			config: "[providers.gw]\n" +
				"base = \"openai-compatible\"\n" +
				"base_url = \"https://gw.internal.example/v1\"\n" +
				"protocol = \"openai-chat\"\n" +
				"auth = \"header\"\n" +
				"auth_header = \"Authorization\"\n" +
				"credential_headers = { \"Authorization\" = '''${MISSING:-}''' }\n" +
				"[providers.gw.models.\"house-model\"]\n",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := fixtureLoad(t, nil, tt.config)
			res, err := r.Resolve("gw/house-model")
			if err != nil {
				t.Fatal(err)
			}
			if res.Credential.Source != "none" || res.Credential.Value != "" {
				t.Fatalf("credential = %+v; want none with no value", res.Credential)
			}
			if !strings.Contains(strings.Join(res.Warnings, ";"), "expands to an empty value") {
				t.Fatalf("warnings = %v; want the empty-expansion warning", res.Warnings)
			}
		})
	}
}

// A command expression in a credential header behaves like an unset
// reference: the header drops out of the resolution with a warning naming it,
// so an auth failure has a local explanation.
func TestCredentialHeaderCommandExpressionWarns(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	valueexpr.RunCommand = func(string) (string, error) {
		return "", errors.New("command timed out")
	}
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-Gateway-Key\"\n" +
		"credential_headers = { \"X-Gateway-Key\" = '''$(get-gateway-token)''' }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.CredentialHeaders["X-Gateway-Key"]; ok {
		t.Fatal("a failed command expression must drop the credential header")
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), `credential header "X-Gateway-Key": command expression failed: command timed out`) {
		t.Fatalf("warnings = %v; want the header failure wording", res.Warnings)
	}
}

// A credential header that expands to empty (an empty ${VAR:-} default) is
// not a present header: it drops out of the map with a warning naming it,
// never sent on the wire as an empty-valued header.
func TestCredentialHeaderEmptyExpansionWarns(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-Gateway-Key\"\n" +
		"credential_headers = { \"X-Gateway-Key\" = '''${MISSING:-}''' }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.CredentialHeaders["X-Gateway-Key"]; ok {
		t.Fatal("an empty expansion must drop the credential header")
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), `credential header "X-Gateway-Key": expands to an empty value`) {
		t.Fatalf("warnings = %v; want the empty-expansion warning naming the header", res.Warnings)
	}
}

// A failing Authorization header is one condition and one warning: the
// header loop that builds the map reports it naming the header, and the
// credential path's parallel "no credential" reason is suppressed on the
// resolution path. The listing path, which builds no header map, keeps the
// credential-form warning (pinned in the test below).
func TestResolveAuthHeaderFailureWarnsOnce(t *testing.T) {
	for _, tt := range []struct{ name, value, want string }{
		{"unset reference", "$MISSING", `credential header "Authorization": MISSING unset`},
		{"empty default", "${MISSING:-}", `credential header "Authorization": expands to an empty value`},
		{"scheme-word default", "${MISSING:-Bearer}", `credential header "Authorization": expands to nothing but an auth scheme word`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := "[providers.gw]\n" +
				"base = \"openai-compatible\"\n" +
				"base_url = \"https://gw.internal.example/v1\"\n" +
				"protocol = \"openai-chat\"\n" +
				"auth = \"header\"\n" +
				"auth_header = \"Authorization\"\n" +
				"credential_headers = { \"Authorization\" = '''" + tt.value + "''' }\n" +
				"[providers.gw.models.\"house-model\"]\n"
			r := fixtureLoad(t, nil, config)
			res, err := r.Resolve("gw/house-model")
			if err != nil {
				t.Fatal(err)
			}
			if res.Credential.Source != "none" {
				t.Fatalf("credential = %+v; want none", res.Credential)
			}
			if len(res.Warnings) != 1 {
				t.Fatalf("warnings = %v; want exactly one, naming the header", res.Warnings)
			}
			if res.Warnings[0] != tt.want {
				t.Fatalf("warning = %q; want %q", res.Warnings[0], tt.want)
			}
		})
	}
}

// The listing path builds no header map, so there the credential-form
// warning is the only report of a failing Authorization header.
func TestListingKeepsAuthHeaderCredentialWarning(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"Authorization\"\n" +
		"credential_headers = { \"Authorization\" = '''$MISSING''' }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	for _, inst := range r.Instances() {
		if inst.Name != "gw" {
			continue
		}
		if len(inst.Warnings) != 1 || inst.Warnings[0] != "no credential (MISSING unset)" {
			t.Fatalf("listing warnings = %v; want the single credential-form warning", inst.Warnings)
		}
		return
	}
	t.Fatal("the gw instance is missing from the listing")
}

// The Authorization header's command expression runs once per resolution:
// the one expansion feeds both the credential and the credential-header map,
// so the two can never disagree, and a failing command is not retried (and
// not raced) within the same resolution.
func TestAuthorizationCommandRunsOncePerResolution(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "", errors.New("command timed out")
	}
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"Authorization\"\n" +
		"credential_headers = { \"Authorization\" = '''$(flaky)''' }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	if _, err := r.Resolve("gw/house-model"); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("the Authorization command ran %d time(s) in one resolution; want 1", runs)
	}
}

// Header names are case-insensitive on the wire, so an Authorization header
// written in any case is still the Authorization: the credential and the
// header map must read the same entry, and the one shared expansion must
// reach both.
func TestAuthorizationHeaderLookupCaseInsensitive(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "minted-token", nil
	}
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"Authorization\"\n" +
		"credential_headers = { \"authorization\" = '''$(mint-auth)''' }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "credential_headers" || res.Credential.Value != "minted-token" {
		t.Fatalf("credential = %+v; want the minted value from the lowercase Authorization header", res.Credential)
	}
	if got := res.CredentialHeaders["authorization"]; got != "minted-token" {
		t.Fatalf("credential header map carries %q; want the minted value under the author's case", got)
	}
	if runs != 1 {
		t.Fatalf("executor ran %d times; want 1 (one shared expansion)", runs)
	}
}

// Authored defaults that fill in nothing but scheme words carry no
// credential: the authoring boundary admits a scheme word only standing
// ahead of material, so an expansion whose only material is scheme words —
// the bare "${KEY:-Bearer}", or "Bearer ${KEY:-Basic}" — resolves as no
// credential with a warning, never as a present one.
func TestCredentialSchemeWordOnlyExpansionIsNoCredential(t *testing.T) {
	for _, value := range []string{`${MISSING:-Bearer}`, `Bearer ${MISSING:-Basic}`} {
		t.Run(value, func(t *testing.T) {
			config := "[providers.gw]\n" +
				"base = \"openai-compatible\"\n" +
				"base_url = \"https://gw.internal.example/v1\"\n" +
				"protocol = \"openai-chat\"\n" +
				"auth = \"header\"\n" +
				"auth_header = \"Authorization\"\n" +
				"credential_headers = { \"Authorization\" = '''" + value + "''' }\n" +
				"[providers.gw.models.\"house-model\"]\n"
			r := fixtureLoad(t, nil, config)
			res, err := r.Resolve("gw/house-model")
			if err != nil {
				t.Fatal(err)
			}
			if res.Credential.Source != "none" || res.Credential.Value != "" {
				t.Fatalf("credential = %+v; want none: the default is only scheme words", res.Credential)
			}
			if _, ok := res.CredentialHeaders["Authorization"]; ok {
				t.Fatal("the header map carries the scheme-word-only expansion; want it dropped")
			}
			if !strings.Contains(strings.Join(res.Warnings, ";"), "nothing but an auth scheme word") {
				t.Fatalf("warnings = %v; want the scheme-word warning", res.Warnings)
			}
		})
	}
}

// The same rule holds for api_key: a default that fills in a bare scheme
// word is authored placeholder text, not key material, so it resolves as
// no credential with a warning. A literal api_key stays a credential —
// hand-typed text is the author's own material, exactly like any secret.
func TestAPIKeySchemeWordOnlyExpansionIsNoCredential(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"bearer\"\n" +
		"api_key = '''${MISSING:-Bearer}'''\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "none" || res.Credential.Value != "" {
		t.Fatalf("credential = %+v; want none: the default is only a scheme word", res.Credential)
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), "api_key expands to nothing but an auth scheme word") {
		t.Fatalf("warnings = %v; want the api_key scheme-word warning", res.Warnings)
	}

	// A literal scheme word is the author's own material and stays a
	// credential: the guard judges authored defaults, never hand-typed text.
	const literal = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"bearer\"\n" +
		"api_key = \"Bearer\"\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r = fixtureLoad(t, nil, literal)
	res, err = r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "api_key" || res.Credential.Value != "Bearer" {
		t.Fatalf("credential = %+v; want the literal api_key kept", res.Credential)
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("warnings = %v; the literal is trusted without warnings", res.Warnings)
	}

	// Multiple scheme words are still no material: a default that assembles
	// "Bearer Basic" authors words all the way down, never a key.
	const multiWord = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"bearer\"\n" +
		"api_key = \"Bearer ${MISSING:-Basic}\"\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r = fixtureLoad(t, nil, multiWord)
	res, err = r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "none" || res.Credential.Value != "" {
		t.Fatalf("credential = %+v; want none: the default is only scheme words", res.Credential)
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), "api_key expands to nothing but an auth scheme word") {
		t.Fatalf("warnings = %v; want the api_key scheme-word warning", res.Warnings)
	}
}

// The credential follows the scheme's own auth header: a header-auth
// instance reads its credential from the auth_header entry — case-
// insensitively, like every header name — and shares one expansion with
// the header map, so the resolve, the header map, and the listing all see
// the same source. Before this, only an Authorization entry could carry
// the credential, so a working custom header resolved as no credential and
// the probe path skipped the instance.
func TestCustomAuthHeaderCarriesTheCredential(t *testing.T) {
	for _, tt := range []struct{ name, authHeader, mapKey string }{
		{"exact case", "X-Api-Key", "X-Api-Key"},
		{"case-insensitive authoring", "x-api-key", "X-Api-Key"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			valueexpr.ResetForTest()
			t.Cleanup(valueexpr.ResetForTest)
			runs := 0
			valueexpr.RunCommand = func(string) (string, error) {
				runs++
				return "minted-key", nil
			}
			config := "[providers.gw]\n" +
				"base = \"openai-compatible\"\n" +
				"base_url = \"https://gw.internal.example/v1\"\n" +
				"protocol = \"openai-chat\"\n" +
				"auth = \"header\"\n" +
				"auth_header = \"" + tt.authHeader + "\"\n" +
				"credential_headers = { \"" + tt.mapKey + "\" = '''$(get-key)''' }\n" +
				"[providers.gw.models.\"house-model\"]\n"
			r := fixtureLoad(t, nil, config)
			res, err := r.Resolve("gw/house-model")
			if err != nil {
				t.Fatal(err)
			}
			if res.Credential.Source != "credential_headers" || res.Credential.Value != "minted-key" {
				t.Fatalf("credential = %+v; want the minted value from the custom auth header", res.Credential)
			}
			if got := res.CredentialHeaders[tt.mapKey]; got != "minted-key" {
				t.Fatalf("credential header map carries %q; want the minted value", got)
			}
			if runs != 1 {
				t.Fatalf("executor ran %d times; want 1 (one shared expansion)", runs)
			}
			// The listing reports the same source the resolution does.
			for _, inst := range r.Instances() {
				if inst.Name != "gw" {
					continue
				}
				if inst.CredentialSource != "credential_headers" {
					t.Fatalf("listing credential source = %q; want credential_headers", inst.CredentialSource)
				}
				return
			}
			t.Fatal("the gw instance is missing from the listing")
		})
	}
}

// A custom auth header's $VAR expression consumed the variable, so the
// listing must not report that variable as shadowed: consumedEnvVars reads
// the entry the scheme actually sends, and the config's own header won.
func TestCustomAuthHeaderConsumedEnvVarNotShadowed(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-Api-Key\"\n" +
		"api_key_env = [\"GW_KEY\"]\n" +
		"credential_headers = { \"X-Api-Key\" = \"$GW_KEY\" }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, map[string]string{"GW_KEY": "k-1"}, config)
	for _, inst := range r.Instances() {
		if inst.Name != "gw" {
			continue
		}
		if inst.CredentialSource != "credential_headers" {
			t.Fatalf("credential source = %q; want credential_headers", inst.CredentialSource)
		}
		if inst.ShadowedEnvVar != "" {
			t.Fatalf("shadowed env var = %q; the header consumed GW_KEY, so nothing was shadowed", inst.ShadowedEnvVar)
		}
		return
	}
	t.Fatal("the gw instance is missing from the listing")
}

// The credential follows the row-merged transport: a model row may override
// auth_header, and the row a launch resolves decides which header carries
// the credential. The resolve reads the same transport it returns, and the
// listing reads the default row's — the launch a bare instance name makes.
// With no default row the listing stays provider-level, its long-standing
// approximation, pinned here.
func TestRowAuthHeaderOverrideCarriesTheCredential(t *testing.T) {
	const base = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-Provider-Key\"\n" +
		"credential_headers = { \"Authorization\" = \"$K\" }\n"
	const overrideRow = "[providers.gw.models.\"house-model\"]\n" +
		"auth_header = \"Authorization\"\n"
	const plainRow = "[providers.gw.models.\"house-model\"]\n"
	for _, tt := range []struct {
		name        string
		config      string
		wantResolve string
		wantListing string
	}{
		{"default row overrides", base + "default_model = \"house-model\"\n" + overrideRow, "credential_headers", "credential_headers"},
		{"no default row", base + overrideRow, "credential_headers", "none"},
		{"no override at all", base + plainRow, "none", "none"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := fixtureLoad(t, map[string]string{"K": "gk"}, tt.config)
			res, err := r.Resolve("gw/house-model")
			if err != nil {
				t.Fatal(err)
			}
			if res.Credential.Source != tt.wantResolve {
				t.Fatalf("resolve credential = %+v; want source %s", res.Credential, tt.wantResolve)
			}
			if tt.wantResolve == "credential_headers" {
				if res.Credential.Value != "gk" {
					t.Fatalf("credential value = %q; want the expanded key", res.Credential.Value)
				}
				if got := res.CredentialHeaders["Authorization"]; got != "gk" {
					t.Fatalf("credential header map carries %q; want the expanded key", got)
				}
			}
			for _, inst := range r.Instances() {
				if inst.Name != "gw" {
					continue
				}
				if inst.CredentialSource != tt.wantListing {
					t.Fatalf("listing credential source = %q; want %s", inst.CredentialSource, tt.wantListing)
				}
				return
			}
			t.Fatal("the gw instance is missing from the listing")
		})
	}
}

// The row's auth override selects the scheme the credential resolves under:
// a default row overriding auth to none makes the instance's no-credential
// quiet — optional schemes warn nothing — where the provider-level header
// scheme would have warned.
func TestRowAuthOverrideSelectsTheScheme(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-K\"\n" +
		"credential_headers = { \"X-K\" = \"$MISSING\" }\n" +
		"default_model = \"house-model\"\n" +
		"[providers.gw.models.\"house-model\"]\n" +
		"auth = \"none\"\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "none" {
		t.Fatalf("credential = %+v; want none", res.Credential)
	}
	if strings.Contains(strings.Join(res.Warnings, ";"), "no credential") {
		t.Fatalf("warnings = %v; the row's none scheme needs no credential warning", res.Warnings)
	}
	for _, inst := range r.Instances() {
		if inst.Name != "gw" {
			continue
		}
		if inst.Auth != "none" {
			t.Fatalf("listing auth = %q; the row's none override is the scheme the bare-name launch signs with", inst.Auth)
		}
		if strings.Contains(strings.Join(inst.Warnings, ";"), "no credential") {
			t.Fatalf("listing warnings = %v; the row's none scheme is quiet about the missing credential", inst.Warnings)
		}
		return
	}
	t.Fatal("the gw instance is missing from the listing")
}

// The listing's transport resolves the default model the way the child
// does: a default matched by a glob row takes the glob row's overrides —
// auth included — not the provider-level shape the exact-row lookup falls
// back to. The resume gate judges this listing, so its credential verdict
// must describe the default model's own resolution.
func TestListingJudgesTheGlobResolvedDefaultModel(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-K\"\n" +
		"credential_headers = { \"X-K\" = \"$MISSING\" }\n" +
		"default_model = \"house-model\"\n" +
		"[providers.gw.models.\"house-*\"]\n" +
		"auth = \"none\"\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Transport.Auth != "none" {
		t.Fatalf("resolve auth = %q; the glob row's none override must apply to the child", res.Transport.Auth)
	}
	for _, inst := range r.Instances() {
		if inst.Name != "gw" {
			continue
		}
		if inst.Auth != "none" {
			t.Fatalf("listing auth = %q; the listing must judge the same glob-resolved shape the child resolves", inst.Auth)
		}
		if strings.Contains(strings.Join(inst.Warnings, ";"), "no credential") {
			t.Fatalf("listing warnings = %v; the glob row's none scheme is quiet about the missing credential", inst.Warnings)
		}
		return
	}
	t.Fatal("the gw instance is missing from the listing")
}

// The listing describes one launch — the bare-name one — so the endpoint
// it prints is the one that launch contacts: a default row overriding
// base_url moves the listing's URL with it, the same way the row's auth
// scheme already moves the listing's Auth. Mixing the provider-level URL
// with the row's scheme describes a launch nothing makes.
func TestListingBaseURLFollowsTheRowOverride(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-K\"\n" +
		"credential_headers = { \"X-K\" = \"$K\" }\n" +
		"default_model = \"house-model\"\n" +
		"[providers.gw.models.\"house-model\"]\n" +
		"base_url = \"https://row.internal.example/v1\"\n"
	r := fixtureLoad(t, map[string]string{"K": "k-1"}, config)
	for _, inst := range r.Instances() {
		if inst.Name != "gw" {
			continue
		}
		if inst.BaseURL != "https://row.internal.example/v1" {
			t.Fatalf("listing base URL = %q; want the default row's override, the endpoint the bare-name launch contacts", inst.BaseURL)
		}
		return
	}
	t.Fatal("the gw instance is missing from the listing")
}

// The model-less resolve reports the shadowed variable against the same
// transport it resolved the credential with — the provider-level one — not
// the default row's merged shape, which may name a different header.
func TestResolveInstanceShadowedVarFollowsRowlessTransport(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-A\"\n" +
		"api_key_env = [\"V\"]\n" +
		"credential_headers = { \"X-A\" = \"$V\" }\n" +
		"default_model = \"house-model\"\n" +
		"[providers.gw.models.\"house-model\"]\n" +
		"auth_header = \"X-B\"\n"
	r := fixtureLoad(t, map[string]string{"V": "v-1"}, config)
	res, err := r.ResolveInstance("gw")
	if err != nil {
		t.Fatalf("ResolveInstance: %v", err)
	}
	if res.Credential.Source != "credential_headers" {
		t.Fatalf("credential = %+v; want the header the row-less transport names", res.Credential)
	}
	if res.ShadowedEnvVar != "" {
		t.Fatalf("shadowed env var = %q; X-A consumed V, so nothing was shadowed", res.ShadowedEnvVar)
	}
}

// Multiple failing credential headers warn in a deterministic order: the
// header loop walks the keys sorted, so the same config resolves to the
// same warning sequence every time instead of Go's random map order.
func TestCredentialHeaderWarningsSortedByKey(t *testing.T) {
	pairs := make([]string, 0, 6)
	for _, k := range []string{"X-C", "X-A", "X-D", "X-B", "X-F", "X-E"} {
		pairs = append(pairs, fmt.Sprintf("%q = %q", k, "$"+strings.TrimPrefix(k, "X-")))
	}
	config := "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"bearer\"\n" +
		"api_key = \"k\"\n" +
		"credential_headers = { " + strings.Join(pairs, ", ") + " }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "api_key" {
		t.Fatalf("credential = %+v; want the api_key", res.Credential)
	}
	want := []string{
		`credential header "X-A": A unset`,
		`credential header "X-B": B unset`,
		`credential header "X-C": C unset`,
		`credential header "X-D": D unset`,
		`credential header "X-E": E unset`,
		`credential header "X-F": F unset`,
	}
	if !reflect.DeepEqual(res.Warnings, want) {
		t.Fatalf("warnings out of order or wrong:\n got %v\nwant %v", res.Warnings, want)
	}
}

// The listing resolves every instance's credential, so it must not expand
// an Authorization header the credential will never use: a provider whose
// api_key outranks the header must not mint (or stall on) its command
// until a resolution actually reads the header for the wire.
func TestCredentialListingSkipsUnusedHeaderCommands(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	flakyRuns := 0
	stableRuns := 0
	valueexpr.RunCommand = func(cmd string) (string, error) {
		if cmd == "flaky" {
			flakyRuns++
			return "", errors.New("command timed out")
		}
		stableRuns++
		return "stable-token", nil
	}
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"bearer\"\n" +
		"api_key = '''$(stable-mint)'''\n" +
		"credential_headers = { \"Authorization\" = '''$(flaky)''' }\n" +
		"default_model = \"house-model\"\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	for _, inst := range r.Instances() {
		if inst.Name == "gw" && inst.CredentialSource != "api_key" {
			t.Fatalf("listing credential source = %q; want api_key", inst.CredentialSource)
		}
	}
	if flakyRuns != 0 {
		t.Fatalf("the listing ran the unused Authorization command %d time(s); want 0", flakyRuns)
	}
	if stableRuns != 0 {
		t.Fatalf("the listing ran the api_key command %d time(s); the pane displays the credential's presence, the child alone executes it", stableRuns)
	}
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "api_key" || res.Credential.Value != "stable-token" {
		t.Fatalf("credential = %+v; want the api_key mint", res.Credential)
	}
	if flakyRuns != 1 {
		t.Fatalf("the Authorization command ran %d time(s) across listing+resolution; want 1 (the resolution reads it for the wire)", flakyRuns)
	}
}

// An alias row seeds from its target's facts and transport; the target's
// own credential commands are not the alias row's to run — resolving the
// alias must not execute a credential the launch it is resolving never
// sends.
func TestAliasResolutionSkipsTargetCommandCredentials(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	const config = "[providers.mine]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://mine.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"bearer\"\n" +
		"api_key = \"sk-mine\"\n" +
		"[providers.mine.models.\"house-model\"]\n" +
		"alias_of = \"tgt/tgt-model\"\n"
	// The target rides in the curated overlay, not the user layer: an
	// overlay provider is not an instance, so nothing but the alias replay
	// itself can resolve its credential.
	overlay := overlayWith("[providers.tgt]\n" +
		"implicit = true\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://tgt.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"bearer\"\n" +
		"api_key = '''$(tgt-mint)'''\n" +
		"[providers.tgt.models.\"tgt-model\"]\n")
	r := fixtureLoad(t, nil, config, WithOverlay(overlay))
	if _, err := r.Resolve("mine/house-model"); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("alias resolution ran the target's credential command %d time(s); the alias seeds facts and transport, not the target's credential", runs)
	}
}

// The hub always wires a credentials store, so the fingerprint's store arm
// must be terminal only on a hit: a store that holds no entry for the
// instance falls through to the environment candidates, whose rotation the
// identity has to follow.
func TestAuthFingerprintRotatesEnvValuesWithStoreWired(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"http://127.0.0.1:9/v1\"\n" +
		"api_key_env = [\"ROT_KEY\"]\n"
	mk := func(t *testing.T, key string) *Registry {
		t.Helper()
		return fixtureLoad(t, map[string]string{"ROT_KEY": key}, config, WithCredentials(fakeCreds{}))
	}
	first, ok := mk(t, "sk-aaaa").AuthFingerprint("gw")
	if !ok {
		t.Fatal("no fingerprint for gw")
	}
	second, _ := mk(t, "sk-bbbb").AuthFingerprint("gw")
	if first == second {
		t.Fatal("fingerprint unchanged across an env rotation with the store wired; a store miss must reach the env candidates")
	}
}

// A mixed credential value — an environment reference and a command
// together — is rotation-sensitive in its environment half: the command
// piece contributes its authored text (the mint rotates with the TTL),
// but a rotated env value changes the effective credential and must
// change the fingerprint, or the previous credential's cached live rows
// survive the rotation.
func TestAuthFingerprintRotatesMixedValues(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"http://127.0.0.1:9/v1\"\n" +
		"api_key = '''$ROT_KEY-$(gw-mint)'''\n"
	mk := func(t *testing.T, key string) *Registry {
		t.Helper()
		return fixtureLoad(t, map[string]string{"ROT_KEY": key}, config, WithCredentials(fakeCreds{}))
	}
	first, ok := mk(t, "sk-aaaa").AuthFingerprint("gw")
	if !ok {
		t.Fatal("no fingerprint for gw")
	}
	second, _ := mk(t, "sk-bbbb").AuthFingerprint("gw")
	if first == second {
		t.Fatal("fingerprint unchanged across an env rotation inside a mixed value; the env half must stay rotation-sensitive")
	}
	if runs != 0 {
		t.Fatalf("fingerprinting executed the command %d time(s)", runs)
	}
}

// A set-but-empty environment value is the `:-` case (spec §10.1): the
// effective credential is the default, so the fingerprint hashes the
// default the expansion uses. Hashing the empty value instead would hide
// a default rotation behind an identical fingerprint and churn an
// unset-to-empty transition that changes no credential.
func TestAuthFingerprintRotatesEmptySetDefaults(t *testing.T) {
	mk := func(t *testing.T, env map[string]string, def string) (string, bool) {
		t.Helper()
		config := "[providers.gw]\n" +
			"base = \"openai-compatible\"\n" +
			"base_url = \"http://127.0.0.1:9/v1\"\n" +
			"api_key = '''${ROT_KEY:-" + def + "}'''\n"
		return fixtureLoad(t, env, config).AuthFingerprint("gw")
	}
	setEmpty, ok := mk(t, map[string]string{"ROT_KEY": ""}, "sk-old")
	if !ok {
		t.Fatal("no fingerprint for gw")
	}
	rotated, _ := mk(t, map[string]string{"ROT_KEY": ""}, "sk-new")
	if setEmpty == rotated {
		t.Fatal("fingerprint unchanged across a default rotation under a set-but-empty env value; the effective credential changed")
	}
	unset, _ := mk(t, nil, "sk-old")
	if unset != setEmpty {
		t.Fatal("fingerprint differs between an unset env value and a set-but-empty one with the same default; the effective credential is the same")
	}
}

// The listing fetch resolves through the default row and sends its
// headers with the request, so the fingerprint — the identity's
// request-and-credential digest — must rotate when those row headers
// do, or cached live rows survive a change to the request that fetched
// them.
func TestAuthFingerprintRotatesListingRowHeaders(t *testing.T) {
	mk := func(t *testing.T, rowHeaders string) (string, bool) {
		t.Helper()
		config := "[providers.gw]\n" +
			"base = \"anthropic\"\n" +
			"api_key_env = [\"WORK_KEY\"]\n" +
			"default_model = \"claude-opus-4-5\"\n" +
			"[providers.gw.models.\"claude-opus-4-5\"]\n" +
			"[providers.gw.models.\"claude-opus-4-5\".headers]\n" +
			rowHeaders
		return fixtureLoad(t, map[string]string{"WORK_KEY": "sk-test"}, config).AuthFingerprint("gw")
	}
	plain, ok := mk(t, "\"X-Beta\" = \"false\"\n")
	if !ok {
		t.Fatal("no fingerprint for gw")
	}
	rotated, _ := mk(t, "\"X-Beta\" = \"true\"\n")
	if plain == rotated {
		t.Fatal("fingerprint unchanged across a default-row header rotation; the listing request changed")
	}
}

// A matching glob row merges into the effective row at resolve time, so
// the listing request carries its headers too: a glob-header rotation is
// as much a change to what the cached live rows came through as an exact
// row's is. The fingerprint hashes the merged request shape, not the
// exact row alone.
func TestAuthFingerprintRotatesGlobRowHeaders(t *testing.T) {
	mk := func(t *testing.T, globHeaders string) (string, bool) {
		t.Helper()
		config := "[providers.gw]\n" +
			"base = \"anthropic\"\n" +
			"api_key_env = [\"WORK_KEY\"]\n" +
			"default_model = \"claude-opus-4-5\"\n" +
			"[providers.gw.models.\"*claude-opus*\"]\n" +
			globHeaders +
			"[providers.gw.models.\"claude-opus-4-5\"]\n"
		return fixtureLoad(t, map[string]string{"WORK_KEY": "sk-test"}, config).AuthFingerprint("gw")
	}
	plain, ok := mk(t, "[providers.gw.models.\"*claude-opus*\".headers]\n\"X-Beta\" = \"false\"\n")
	if !ok {
		t.Fatal("no fingerprint for gw")
	}
	rotated, _ := mk(t, "[providers.gw.models.\"*claude-opus*\".headers]\n\"X-Beta\" = \"true\"\n")
	if plain == rotated {
		t.Fatal("fingerprint unchanged across a glob-row header rotation; the listing request changed")
	}
}

// A row can pin its own auth scheme, and the listing resolves every row
// at full depth through that row's own transport: the mint predicate
// must see what the rows would execute, not only the provider-level
// scheme, or an automatic view mints through the override.
func TestLaunchMintsCoversRowAuthOverride(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"http://127.0.0.1:9/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"none\"\n" +
		"api_key = '''$(gw-mint)'''\n" +
		"[providers.gw.models.\"house-model\"]\n" +
		"auth = \"bearer\"\n"
	r := fixtureLoad(t, nil, config)
	if !r.LaunchMintsCredentialCommand("gw") {
		t.Fatal("the mint predicate missed the row's auth override: the listing's full-depth row resolution executes the api_key command under the row's bearer scheme")
	}
}

// A cached live row resolves at full depth like a catalog row: the
// listing (resolveListing) resolves every id the registry knows — exact
// rows plus the cached live ids — and a top-level glob can pin a live
// row's scheme onto one that sends api_key. The predicate's row scan
// must cover the live ids too, or the hub's next prefetch spends the
// mint the predicate just called safe.
func TestLaunchMintsCoversLiveRowAuthOverride(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"http://127.0.0.1:9/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"none\"\n" +
		"api_key = '''$(gw-mint)'''\n" +
		"[models.\"*house*\"]\n" +
		"auth = \"bearer\"\n"
	r := fixtureLoad(t, nil, config)
	r.ApplyLive("gw", []Model{{ID: "house-live-1"}})
	if !r.LaunchMintsCredentialCommand("gw") {
		t.Fatal("the mint predicate missed the cached live row's glob-pinned scheme: the listing's full-depth row resolution executes the api_key command under it")
	}
}

// A live listing can return an id the registry has never seen, and a
// glob — top-level or provider-scoped — can pin that unseen row onto a
// scheme that sends api_key. The predicate cannot name ids it has not
// seen, so an auth-bearing glob means it cannot vouch for the listing's
// rows: the command key must count as mintable.
func TestLaunchMintsTreatsAuthBearingGlobsAsReachable(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"http://127.0.0.1:9/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"none\"\n" +
		"api_key = '''$(gw-mint)'''\n" +
		"[models.\"*future*\"]\n" +
		"auth = \"bearer\"\n"
	r := fixtureLoad(t, nil, config)
	if !r.LaunchMintsCredentialCommand("gw") {
		t.Fatal("the mint predicate vouched for rows it cannot name: a glob can pin an unseen live id onto a scheme that sends the api_key command")
	}
}

// The conservative glob rule reads the auth field alone: a glob that
// pins transport fields but no auth — or only caps — cannot flip a
// scheme, so the none-scheme command key stays safe to skip.
func TestLaunchMintsIgnoresAuthlessGlobs(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"http://127.0.0.1:9/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"none\"\n" +
		"api_key = '''$(gw-mint)'''\n" +
		"[providers.gw.models.\"house-model\"]\n" +
		"[models.\"*caps*\"]\n" +
		"base_url = \"http://127.0.0.1:10/v1\"\n"
	r := fixtureLoad(t, nil, config)
	if r.LaunchMintsCredentialCommand("gw") {
		t.Fatal("the mint predicate refused a none-scheme command key over a glob that cannot change any row's scheme")
	}
}

// The default row's headers hash as wire material, verbatim: the
// values are already resolved, and re-parsing them as authored
// $-expression text shreds a value that itself contains '$' — an
// embedded unset ref contributes nothing, so two different wire
// values hash alike and a header rotation keeps publishing stale rows
// under the old value's identity.
func TestAuthFingerprintRowHeaderWireValue(t *testing.T) {
	mk := func(t *testing.T, org string) string {
		t.Helper()
		const config = "[providers.gw]\n" +
			"base = \"openai-compatible\"\n" +
			"base_url = \"http://127.0.0.1:9/v1\"\n" +
			"protocol = \"openai-chat\"\n" +
			"auth = \"none\"\n" +
			"default_model = \"house-model\"\n" +
			"[providers.gw.headers]\n" +
			"X-Org = \"$ORG\"\n" +
			"[providers.gw.models.\"house-model\"]\n"
		fp, _ := fixtureLoad(t, map[string]string{"ORG": org}, config).AuthFingerprint("gw")
		return fp
	}
	if mk(t, "a $B") == mk(t, "a $C") {
		t.Fatal("the fingerprint hashed two different wire header values alike: a header rotation would not rotate the identity")
	}
}

// The none scheme never sends a credential, so its resolution never
// expands the api_key slot: a command there is authored for a scheme the
// instance does not use, and running it would spend a mint the wire never
// carries.
func TestAuthNoneNeverExpandsAPIKey(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"none\"\n" +
		"api_key = '''$(none-mint)'''\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "none" {
		t.Fatalf("credential = %+v; want none", res.Credential)
	}
	if runs != 0 {
		t.Fatalf("the none scheme executed the api_key command %d time(s); it never sends a credential", runs)
	}
}

// Credential-header names are case-insensitive on the wire, so two that
// differ only by case would collide into one header while resolution picks
// a single entry for the credential: the load refuses the pair.
func TestCredentialHeaderCaseVariantsRefusedAtLoad(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"credential_headers = { \"Authorization\" = '''$A''', \"authorization\" = '''$B''' }\n"
	if _, err := ParseConfig([]byte(config)); err == nil || !strings.Contains(err.Error(), "differ only by case") {
		t.Fatalf("ParseConfig err = %v; want the case-variant refusal", err)
	}
}

// Byte order sorts case variants apart when an unrelated name lands
// between them, so adjacent-pair comparison alone cannot be the check: the
// guard keys on the folded name, wherever sorting puts it.
func TestCredentialHeaderCaseVariantsWithInterveningNameRefusedAtLoad(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"credential_headers = { \"Authorization\" = '''$A''', \"X-Key\" = '''$B''', \"authorization\" = '''$C''' }\n"
	if _, err := ParseConfig([]byte(config)); err == nil || !strings.Contains(err.Error(), "differ only by case") {
		t.Fatalf("ParseConfig err = %v; want the case-variant refusal", err)
	}
}

// A minted token is data, never judged by its shape: an all-letters command
// output is a credential, not a scheme word, so the no-material rule reads
// the authored pieces (literals and reference defaults), never the
// expanded text.
func TestMintedLettersOnlyTokenIsACredential(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	valueexpr.RunCommand = func(string) (string, error) { return "abcdeftoken", nil }
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"Authorization\"\n" +
		"credential_headers = { \"Authorization\" = '''$(mint-auth)''' }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if res.Credential.Source != "credential_headers" || res.Credential.Value != "abcdeftoken" {
		t.Fatalf("credential = %+v; want the minted letters-only token", res.Credential)
	}
	if got := res.CredentialHeaders["Authorization"]; got != "abcdeftoken" {
		t.Fatalf("credential header map carries %q; want the minted token", got)
	}
}

// The no-material rule is the same for every credential header, not just
// Authorization: a gateway key whose only authored material is a bare
// scheme word carries no credential and drops with a warning.
func TestGenericCredentialHeaderSchemeWordDefaultDrops(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-Gateway-Key\"\n" +
		"credential_headers = { \"X-Gateway-Key\" = '''${MISSING:-Bearer}''' }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.CredentialHeaders["X-Gateway-Key"]; ok {
		t.Fatal("the scheme-word-only gateway key is on the wire; want it dropped")
	}
	if !strings.Contains(strings.Join(res.Warnings, ";"), "auth scheme word") {
		t.Fatalf("warnings = %v; want the scheme-word warning", res.Warnings)
	}
}

// A literal credential-header value is the author's own key material,
// trusted exactly like an api_key literal: the no-material rule judges
// only reference defaults, never literals or minted output, so an
// all-letters literal key stays on the wire.
func TestLiteralCredentialHeaderValueStays(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-compatible\"\n" +
		"base_url = \"https://gw.internal.example/v1\"\n" +
		"protocol = \"openai-chat\"\n" +
		"auth = \"header\"\n" +
		"auth_header = \"X-Api-Key\"\n" +
		"credential_headers = { \"X-Api-Key\" = \"abcdef\" }\n" +
		"[providers.gw.models.\"house-model\"]\n"
	r := fixtureLoad(t, nil, config)
	res, err := r.Resolve("gw/house-model")
	if err != nil {
		t.Fatal(err)
	}
	if got := res.CredentialHeaders["X-Api-Key"]; got != "abcdef" {
		t.Fatalf("credential header map carries %q; want the literal key kept", got)
	}
}
