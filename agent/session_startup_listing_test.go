package agent

import (
	"testing"

	"primeradiant.com/evener/agent/provider"
	"primeradiant.com/evener/llm"
	"primeradiant.com/evener/llm/registry"
)

// TestNewSessionReusesLauncherListing pins the launch path's single fetch: the
// launcher has already listed the instance and resolved the model, so session
// construction must use that listing for its metadata and membership checks.
// A second fetch could return empty and clear the live rows the resolve
// applied, rejecting a model the provider serves.
func TestNewSessionReusesLauncherListing(t *testing.T) {
	const protocolID = "test-proto-agent-startup-listing"
	proto := stagedCodexProtocol(protocolID)

	r, err := registry.Load(
		registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
		registry.WithStateRoot(t.TempDir()),
		registry.WithEnv(func(string) (string, bool) { return "", false }),
		registry.WithInstances(map[string]registry.Provider{
			"codex-plan": {Base: "openai-codex", Protocol: protocolID},
		}),
	)
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	client := llm.NewClient(llm.WithRegistry(r))
	for _, inst := range r.Instances() {
		if inst.Name != "codex-plan" {
			client.Register(&fakeAdapter{name: inst.Name})
		}
	}
	// The launcher's initial resolve applied this listing.
	r.ApplyLive("codex-plan", []registry.Model{{ID: "gpt-reserve"}})
	profile, err := provider.Resolve(r, "codex-plan/gpt-reserve")
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	res, err := r.Resolve("codex-plan/gpt-reserve")
	if err != nil {
		t.Fatalf("resolve row: %v", err)
	}
	primed := &StartupModelListing{
		Instance: "codex-plan",
		Listing:  llm.ModelListing{Live: true, Usable: true, Models: []registry.Resolved{res}},
	}

	sess := newSession(t,
		withClient(client),
		withProfile(profile),
		withConfig(SessionConfig{
			MaxSubagentDepth:    1,
			StartupModelListing: primed,
			ResolveProfile: func(ref string) (*provider.Profile, error) {
				return provider.Resolve(r, ref)
			},
			testOnly: testConfig{skipGitSnapshot: true},
		}),
	)
	if got := proto.callCount("codex-plan"); got != 0 {
		t.Fatalf("session startup listed the instance %d times, want the launcher listing reused", got)
	}
	if got := sess.currentProfile(); got.Model() != "gpt-reserve" {
		t.Fatalf("session model = %q, want gpt-reserve", got.Model())
	}
}
