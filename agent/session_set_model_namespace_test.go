package agent

import (
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/execsupport/valueexpr"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestSetModel_NamespacedModelDoesNotMintAnotherInstance pins the classifier:
// an OpenRouter id like "anthropic/claude-opus-5" is a model on the current
// instance, even though its first segment names another instance. Priming must
// not resolve that other instance, or a cold credential command would run for
// a switch that never touches it.
func TestSetModel_NamespacedModelDoesNotMintAnotherInstance(t *testing.T) {
	oldRunCommand := valueexpr.RunCommand
	runs := 0
	valueexpr.RunCommand = func(string) (string, error) {
		runs++
		return "minted", nil
	}
	t.Cleanup(func() { valueexpr.RunCommand = oldRunCommand })

	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"openrouter": {
				Base:     "openai-compatible",
				Protocol: registry.ProtocolOpenAIChat,
				Models: map[string]registry.Model{
					"anthropic/claude-opus-5": {},
				},
			},
			"anthropic": {Base: "anthropic", APIKey: "$(anthropic-mint)"},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	client.Register(newFakeEnumerableAdapter("openrouter", "anthropic/claude-opus-5"))
	for _, inst := range r.Instances() {
		if inst.Name != "openrouter" {
			client.Register(&fakeAdapter{name: inst.Name})
		}
	}
	profile, err := provider.Resolve(r, "openrouter/anthropic/claude-opus-5")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}

	sess := newSession(t,
		withClient(client),
		withProfile(profile),
		withConfig(SessionConfig{
			MaxSubagentDepth: 1,
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
			testOnly: testConfig{skipGitSnapshot: true},
		}),
	)
	if err := sess.SetModel("anthropic/claude-opus-5"); err != nil {
		t.Fatalf("SetModel on a namespaced model: %v", err)
	}
	if runs != 0 {
		t.Fatalf("the switch minted %d credential command(s) from another instance", runs)
	}
	if got := sess.currentProfile(); got.ID() != "openrouter" || got.Model() != "anthropic/claude-opus-5" {
		t.Fatalf("profile = %s/%s, want openrouter/anthropic/claude-opus-5", got.ID(), got.Model())
	}
}
