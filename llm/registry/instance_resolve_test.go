package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func cutoverRegistry(t *testing.T, env map[string]string, instances map[string]Provider) *Registry {
	t.Helper()
	r, err := Load(
		WithOffline(true), WithoutCache(), WithNoUserLayer(), WithStateRoot(t.TempDir()),
		WithEnv(func(k string) (string, bool) { v, ok := env[k]; return v, ok }),
		WithInstances(instances),
	)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return r
}

func TestResolveInstanceCarriesTransportCredentialAndProviderCaps(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"GROQ_API_KEY": "gk"}, nil)
	res, err := r.ResolveInstance("groq")
	if err != nil {
		t.Fatalf("ResolveInstance: %v", err)
	}
	if res.Instance != "groq" || res.ProviderID != "groq" || res.Protocol != ProtocolOpenAIChat {
		t.Fatalf("identity: %+v", res)
	}
	if res.ModelID != "" || res.WireID != "" {
		t.Fatalf("model-less resolve must leave ModelID/WireID empty: %q %q", res.ModelID, res.WireID)
	}
	if res.Transport.BaseURL != "https://api.groq.com/openai/v1" || res.Transport.ModelsEndpoint == "" {
		t.Fatalf("transport: %+v", res.Transport)
	}
	if res.Credential.Value != "gk" || res.Credential.Source != "env:GROQ_API_KEY" {
		t.Fatalf("credential: %+v", res.Credential)
	}
	if res.DefaultModel == "" || res.CheapModel == "" {
		t.Fatalf("groq is on the implicit list and must carry default_model and cheap_model: %+v", res)
	}
}

func TestResolveInstanceUnknownNamesAvailableInstances(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"GROQ_API_KEY": "gk"}, nil)
	_, err := r.ResolveInstance("nope")
	if err == nil || !strings.Contains(err.Error(), `unknown instance "nope"`) || !strings.Contains(err.Error(), "groq") {
		t.Fatalf("want unknown-instance error naming groq, got %v", err)
	}
}

func TestResolveCarriesDefaultAndCheapModel(t *testing.T) {
	r := cutoverRegistry(t, nil, map[string]Provider{"work": {Base: "openai", APIKey: "k", DefaultModel: "gpt-5.5", CheapModel: "gpt-4.1-nano", Transport: Transport{BaseURL: "https://gw.example.com/v1"}}})
	res, err := r.Resolve("work/gpt-5.5")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.DefaultModel != "gpt-5.5" || res.CheapModel != "gpt-4.1-nano" {
		t.Fatalf("default/cheap: %q %q", res.DefaultModel, res.CheapModel)
	}
	if res.Synthesized {
		t.Fatal("gpt-5.5 is a catalog row on the openai base")
	}
	if made, err := r.Resolve("work/never-heard-of-it"); err != nil || !made.Synthesized {
		t.Fatalf("an unknown id resolves as synthesized (spec §7.3): %v %+v", err, made.Synthesized)
	}
}

func TestInstanceViewCarriesBaseURLVarsAndDefaultModel(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"AWS_BEARER_TOKEN_BEDROCK": "tok"}, map[string]Provider{
		"bedrock": {Base: "amazon-bedrock", Transport: Transport{Vars: map[string]string{"AWS_REGION": "us-east-1"}}},
	})
	inst, ok := r.Instance("bedrock")
	if !ok {
		t.Fatal("bedrock must be an instance")
	}
	if inst.Vars["AWS_REGION"] != "us-east-1" {
		t.Fatalf("vars: %+v", inst.Vars)
	}
	if !strings.Contains(inst.BaseURL, "us-east-1") {
		t.Fatalf("base url must be resolved with the var: %q", inst.BaseURL)
	}
	if inst.DefaultModel == "" {
		t.Fatalf("amazon-bedrock carries a curated default_model: %+v", inst)
	}
	if _, ok := r.Instance("nope"); ok {
		t.Fatal("unknown name must not be an instance")
	}
	if r.StateRoot() == "" {
		t.Fatal("StateRoot must be the load-time state root")
	}
}

func TestReasoningDisabled(t *testing.T) {
	if (Caps{}).ReasoningDisabled() {
		t.Fatal("nil is not disabled")
	}
	if (Caps{Reasoning: new(true)}).ReasoningDisabled() {
		t.Fatal("true is not disabled")
	}
	if !(Caps{Reasoning: new(false)}).ReasoningDisabled() {
		t.Fatal("false is disabled")
	}
}

func TestStrayOAuthRecords(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "auth"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"openai.json", "openai-codex.json", "work.json", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(root, "auth", name), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r, err := Load(WithOffline(true), WithoutCache(), WithNoUserLayer(), WithStateRoot(root),
		WithEnv(func(string) (string, bool) { return "", false }),
		WithInstances(map[string]Provider{"work": {Base: "openai", APIKey: "k", Transport: Transport{BaseURL: "https://gw.example.com/v1"}}}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := r.StrayOAuthRecords()
	if len(got) != 2 {
		t.Fatalf("want 2 notices (openai, work), got %d: %v", len(got), got)
	}
	for _, want := range []string{
		`"openai" is not an instance on the Codex transport`, "evener openai logout --instance openai",
		`"work" is not an instance on the Codex transport`, "evener openai logout --instance work",
	} {
		if !strings.Contains(strings.Join(got, "\n"), want) {
			t.Fatalf("missing %q in %v", want, got)
		}
	}
}
