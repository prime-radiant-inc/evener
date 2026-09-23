package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"primeradiant.com/evener/internal/valueexpr"
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

// TestResolveInstancePurgesFieldsProvenanceOnProtocolReset pins parity with
// resolveOn (resolve.go): a layer that changes the record's protocol
// mid-chain must purge any "Fields.*" provenance already recorded by an
// earlier layer, not just clear caps.Fields itself. "work" inherits from
// openai (protocol openai-responses, whose overlay layer sets several
// provider-level Fields with recorded provenance) and overrides the
// protocol to openai-chat, the same cross-protocol fixture load_test.go's
// TestLoad_ExplicitInstances uses to pin resetFields itself.
func TestResolveInstancePurgesFieldsProvenanceOnProtocolReset(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"OPENAI_API_KEY": "k"}, map[string]Provider{
		"work": {Base: "openai", Protocol: ProtocolOpenAIChat},
	})
	res, err := r.ResolveInstance("work")
	if err != nil {
		t.Fatalf("ResolveInstance: %v", err)
	}
	for k := range res.Provenance {
		if strings.HasPrefix(k, "Fields.") {
			t.Fatalf("cross-protocol reset must purge stale Fields provenance, found %q: %+v", k, res.Provenance)
		}
	}
}

// TestResolveInstanceCarriesHiddenProviderWarning pins parity with
// resolveOn: a hidden record (here, an injected instance whose base_url
// template variable is never set) must explain itself through
// ResolveInstance the same way a model-based Resolve already does.
func TestResolveInstanceCarriesHiddenProviderWarning(t *testing.T) {
	r := cutoverRegistry(t, nil, map[string]Provider{"nobedrockregion": {Base: "amazon-bedrock"}})
	res, err := r.ResolveInstance("nobedrockregion")
	if err != nil {
		t.Fatalf("ResolveInstance: %v", err)
	}
	want := "hidden: provider has no resolvable base URL or protocol"
	if !strings.Contains(strings.Join(res.Warnings, "\n"), want) {
		t.Fatalf("hidden instance must explain itself: %v", res.Warnings)
	}
}

// TestResolveInstanceCarriesConverterNotes pins parity with resolveOn: a
// record's converter notes (spec: "protocol unverified" and similar,
// populated by the models.dev conversion in modelsdev.go) must ride
// through ResolveInstance's Warnings the same way they ride through
// Resolve's. Setting the unexported notes field directly is the simplest
// way to construct "a record whose converter notes are non-empty" — notes
// is ordinary Provider input data (like DefaultModel, which other tests in
// this file already set directly), just usually populated by the
// converter instead of a literal.
func TestResolveInstanceCarriesConverterNotes(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"OPENAI_API_KEY": "k"}, map[string]Provider{
		"noted": {Base: "openai", notes: []string{"protocol unverified: unknown npm test-sdk"}},
	})
	res, err := r.ResolveInstance("noted")
	if err != nil {
		t.Fatalf("ResolveInstance: %v", err)
	}
	want := "protocol unverified: unknown npm test-sdk"
	if !strings.Contains(strings.Join(res.Warnings, "\n"), want) {
		t.Fatalf("converter notes must ride through to Warnings: %v", res.Warnings)
	}
}

// TestResolveInstanceCarriesWebSearchDisabledWarning pins parity with
// resolveOn: a record that is not reaching its provider's
// first-party endpoint must explain a stripped web_search through
// ResolveInstance the same way a model-based Resolve already does
// (gateWebSearch, resolve.go, is the one function both call), and an
// explicit web_search on the instance's own entry must suppress the
// warning identically on both paths. It also pins that the strip lands as
// an explicit false at this call site too, not nil: every protocol
// adapter's own gate treats caps.WebSearch == nil as permissive, so a
// caller reading Resolved straight from ResolveInstance (not through
// profile.SupportsWebSearch's BoolValue, which happens to collapse nil and
// false alike) must still see an unambiguous deny.
func TestResolveInstanceCarriesWebSearchDisabledWarning(t *testing.T) {
	r := cutoverRegistry(t, map[string]string{"OPENAI_API_KEY": "k"}, map[string]Provider{
		"gw":      {Base: "openai", Transport: Transport{BaseURL: "https://gw.example.com/v1"}},
		"optedin": {Base: "openai", Transport: Transport{BaseURL: "https://gw.example.com/v1"}, Caps: Caps{WebSearch: new(true)}},
	})
	want := "web_search disabled: this endpoint is not openai's first-party API (set web_search = true on the instance to opt back in)"

	instRes, err := r.ResolveInstance("gw")
	if err != nil {
		t.Fatalf("ResolveInstance(gw): %v", err)
	}
	if !strings.Contains(strings.Join(instRes.Warnings, "\n"), want) {
		t.Fatalf("ResolveInstance must explain a stripped web_search: %v", instRes.Warnings)
	}
	if instRes.Caps.WebSearch == nil || *instRes.Caps.WebSearch {
		t.Fatalf("ResolveInstance must strip to an explicit false, not nil (nil is fail-open at the adapter layer): %v", bp(instRes.Caps.WebSearch))
	}
	modelRes, err := r.Resolve("gw/gpt-5.5")
	if err != nil {
		t.Fatalf("Resolve(gw/gpt-5.5): %v", err)
	}
	if !strings.Contains(strings.Join(modelRes.Warnings, "\n"), want) {
		t.Fatalf("resolveOn must carry the identical warning ResolveInstance does: %v", modelRes.Warnings)
	}
	if modelRes.Caps.WebSearch == nil || *modelRes.Caps.WebSearch {
		t.Fatalf("resolveOn must strip to an explicit false too, identically: %v", bp(modelRes.Caps.WebSearch))
	}

	instOK, err := r.ResolveInstance("optedin")
	if err != nil {
		t.Fatalf("ResolveInstance(optedin): %v", err)
	}
	if strings.Contains(strings.Join(instOK.Warnings, "\n"), "web_search disabled") {
		t.Fatalf("ResolveInstance: an explicit web_search = true must suppress the warning: %v", instOK.Warnings)
	}
	if instOK.Caps.WebSearch == nil || !*instOK.Caps.WebSearch {
		t.Fatalf("ResolveInstance: the explicit opt-in must survive as a non-nil true, not merely avoid the warning: %v", bp(instOK.Caps.WebSearch))
	}
	modelOK, err := r.Resolve("optedin/gpt-5.5")
	if err != nil {
		t.Fatalf("Resolve(optedin/gpt-5.5): %v", err)
	}
	if strings.Contains(strings.Join(modelOK.Warnings, "\n"), "web_search disabled") {
		t.Fatalf("resolveOn: an explicit web_search = true must suppress the warning too: %v", modelOK.Warnings)
	}
}

// ResolveInstancePresence is the depth the hub's automatic views resolve
// at (spec §10.1): every field those views read must match a full
// resolve, with no wire map built and no command executed, so the depth
// can never silently lose a field.
func TestResolveInstancePresenceMatchesFullDepth(t *testing.T) {
	// An explicit instance with an environment credential: presence keeps
	// the source label the full resolve would report, without the value.
	const cfgEnv = "[providers.work]\nbase = \"anthropic\"\napi_key_env = [\"WORK_KEY\"]\n"
	full, err := fixtureLoad(t, map[string]string{"WORK_KEY": "sk-test"}, cfgEnv).ResolveInstance("work")
	if err != nil {
		t.Fatal(err)
	}
	presence, err := fixtureLoad(t, map[string]string{"WORK_KEY": "sk-test"}, cfgEnv).ResolveInstancePresence("work")
	if err != nil {
		t.Fatal(err)
	}
	if presence.Credential.Source != full.Credential.Source {
		t.Fatalf("presence source = %q, want the full resolve's %q", presence.Credential.Source, full.Credential.Source)
	}
	if presence.Credential.Value != full.Credential.Value {
		t.Fatalf("presence credential material = %q, want the full resolve's (label parity, same reading as the listing judgment)", full.Credential.Value)
	}
	if presence.Protocol != full.Protocol || presence.Surface != full.Surface {
		t.Fatalf("presence protocol/surface drifted: %q/%q want %q/%q", presence.Protocol, presence.Surface, full.Protocol, full.Surface)
	}
	if presence.Transport.Auth != full.Transport.Auth || presence.Transport.BaseURL != full.Transport.BaseURL {
		t.Fatal("presence transport drifted from the full resolve")
	}
	if presence.ShadowedEnvVar != full.ShadowedEnvVar {
		t.Fatalf("presence shadowed env var = %q, want %q", presence.ShadowedEnvVar, full.ShadowedEnvVar)
	}
	if len(presence.CredentialHeaders) != 0 {
		t.Fatal("presence resolve built a credential-headers wire map; no hub view builds a request")
	}
}

// A command-bearing api_key counts as present at presence depth and keeps
// its field's label, without ever running the command: that judgment is
// the launch gate's (spec §10.1), and this is the depth the gate's
// judgment feeds.
func TestResolveInstancePresenceCountsCommandWithoutMinting(t *testing.T) {
	valueexpr.ResetForTest()
	t.Cleanup(valueexpr.ResetForTest)
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "token", nil
	}
	const cfgCmd = "[providers.gw]\nbase = \"anthropic\"\napi_key = '''$(gw-mint)'''\n"
	presence, err := fixtureLoad(t, nil, cfgCmd).ResolveInstancePresence("gw")
	if err != nil {
		t.Fatal(err)
	}
	if presence.Credential.Source != "api_key" {
		t.Fatalf("presence source = %q, want api_key: a command-bearing slot counts as present", presence.Credential.Source)
	}
	if presence.Credential.Value != "" {
		t.Fatal("presence resolve materialized the command's mint")
	}
	if runs != 0 {
		t.Fatalf("presence resolve executed the credential command %d time(s)", runs)
	}
}

// A stale default row must not break the listing and the endpoint view:
// the same fallback listingTransport already makes — the provider's own
// transport — carries them, instead of an error. A Codex-based instance
// whose default names a model the transport does not serve is the shape
// that errors today.
func TestResolveInstanceListingFallsBackWhenDefaultUnresolvable(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"openai-codex\"\n" +
		"default_model = \"gpt-not-a-real-codex-model\"\n"
	r := fixtureLoad(t, nil, config)
	listing, err := r.ResolveInstanceListing("gw")
	if err != nil {
		t.Fatalf("the listing broke on a stale default row: %v", err)
	}
	if listing.Protocol == "" || listing.Transport.BaseURL == "" {
		t.Fatalf("listing = %q/%q; want the provider transport's fallback shape", listing.Protocol, listing.Transport.BaseURL)
	}
	transport, err := r.ResolveInstanceTransport("gw")
	if err != nil {
		t.Fatalf("the endpoint view broke on a stale default row: %v", err)
	}
	if transport.Transport.BaseURL != listing.Transport.BaseURL || transport.Protocol != listing.Protocol {
		t.Fatal("the endpoint view's fallback drifted from the listing's")
	}
}

// An empty instance names the default instance, the rule Resolve applies:
// a legacy session recorded without a profile still resolves through the
// default at facts depth, or it loses every hub-side read.
func TestResolveInstanceModelFactsDefaultsEmptyInstance(t *testing.T) {
	const config = "[providers.gw]\n" +
		"base = \"anthropic\"\n" +
		"api_key_env = [\"WORK_KEY\"]\n" +
		"[providers.gw.models.\"claude-opus-4-5\"]\n"
	r := fixtureLoad(t, map[string]string{"WORK_KEY": "sk-test"}, config)
	res, err := r.ResolveInstanceModelFacts("", "claude-opus-4-5")
	if err != nil {
		t.Fatalf("facts resolve with an empty instance: %v", err)
	}
	direct, err := r.ResolveInstanceModelFacts("gw", "claude-opus-4-5")
	if err != nil {
		t.Fatal(err)
	}
	if res.Instance != direct.Instance || res.Model.ID != direct.Model.ID {
		t.Fatalf("defaulted resolve = %s/%s, want %s/%s", res.Instance, res.Model.ID, direct.Instance, direct.Model.ID)
	}
}
