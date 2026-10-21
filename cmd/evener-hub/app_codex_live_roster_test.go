package hub

import (
	"testing"

	authopenai "primeradiant.com/evener/auth/openai"
	"primeradiant.com/evener/auth/openai/oaitest"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// TestCredentialGateDefersLiveOnlyCodexModelToTheChild pins the resume
// preflight: the hub's registry may not hold a live listing for a model the
// backend serves (a provider added after startup, or a failed prefetch), but
// the child applies the instance's live roster before resolving the persisted
// model. The gate must judge the instance's credential and let the child own
// the model verdict, instead of refusing the resume before the child runs.
func TestCredentialGateDefersLiveOnlyCodexModelToTheChild(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	clearProviderKeysFromEnvironment(t)

	dir := t.TempDir()
	stateDir := t.TempDir()
	store, err := credentials.LoadStore(dir + "/credentials.toml")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	if err := authopenai.SaveAuth(stateDir, "work", makeOAuthRecord("work", "work@example.com")); err != nil {
		t.Fatalf("SaveAuth: %v", err)
	}
	reg := newProbeRegistry(t, stateDir, store, nil, map[string]registry.Provider{
		"work": {Base: "openai-codex"},
	})

	if err := validateProviderCredentials("work", "gpt-reserve", reg); err != nil {
		t.Fatalf("the resume gate refused a Codex model the child can resolve from the live roster: %v", err)
	}
}

// TestCredentialGateStillRefusesUnsignedCodexModel keeps the credential half
// of the gate: deferring the model verdict must not launch an instance with no
// OAuth record at all.
func TestCredentialGateStillRefusesUnsignedCodexModel(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	clearProviderKeysFromEnvironment(t)

	dir := t.TempDir()
	store, err := credentials.LoadStore(dir + "/credentials.toml")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	reg := newProbeRegistry(t, t.TempDir(), store, nil, map[string]registry.Provider{
		"work": {Base: "openai-codex"},
	})

	if err := validateProviderCredentials("work", "gpt-reserve", reg); err == nil {
		t.Fatal("the resume gate accepted an unsigned Codex instance")
	}
}

// TestCredentialGateIgnoresDefaultRowOverrideForUnlistedCodexModel pins the
// fallback's transport: an unlisted Codex model has no row, so it inherits the
// provider's OAuth shape. A default row that overrides auth to none describes
// a bare-name launch, not this model, and must not wave it through unsigned.
func TestCredentialGateIgnoresDefaultRowOverrideForUnlistedCodexModel(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	clearProviderKeysFromEnvironment(t)

	dir := t.TempDir()
	store, err := credentials.LoadStore(dir + "/credentials.toml")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	reg := newProbeRegistry(t, t.TempDir(), store, nil, map[string]registry.Provider{
		"work": {
			Base:         "openai-codex",
			DefaultModel: "default-model",
			Models: map[string]registry.Model{
				"default-model": {Transport: &registry.Transport{Auth: registry.AuthNone}},
			},
		},
	})

	if err := validateProviderCredentials("work", "gpt-reserve", reg); err == nil {
		t.Fatal("the resume gate judged an unlisted Codex model by its default row's auth override")
	}
}

// TestCredentialGateHonorsGlobOverrideForUnlistedCodexModel pins the other
// side: an unlisted model's matching glob does describe it, so a glob that
// sets auth to none makes the launch credentialless even without an OAuth
// record. The fallback must replay the glob at transport depth.
func TestCredentialGateHonorsGlobOverrideForUnlistedCodexModel(t *testing.T) {
	oaitest.IsolateOpenAIAuth(t)
	clearProviderKeysFromEnvironment(t)

	dir := t.TempDir()
	store, err := credentials.LoadStore(dir + "/credentials.toml")
	if err != nil {
		t.Fatalf("LoadStore: %v", err)
	}
	reg := newProbeRegistry(t, t.TempDir(), store, nil, map[string]registry.Provider{
		"work": {
			Base: "openai-codex",
			Models: map[string]registry.Model{
				"gpt-reserve*": {Transport: &registry.Transport{Auth: registry.AuthNone}},
			},
		},
	})

	if err := validateProviderCredentials("work", "gpt-reserve", reg); err != nil {
		t.Fatalf("the resume gate ignored a matching glob's auth override: %v", err)
	}
}
