package hub

import (
	"slices"
	"testing"

	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/internal/credentials"
	"primeradiant.com/evener/llm/registry"
)

// effortRegistry is pricingRegistry's shape for the reasoning facts: the
// curated openai-codex rows, offline, uncached, no user layer, so a past
// session's ProfileID/Model resolve gpt-6-luna's ladder at facts depth.
func effortRegistry(tb testing.TB) *hubcore.ProviderRegistry {
	tb.Helper()
	stateRoot := tb.TempDir()
	holder := hubcore.NewProviderRegistry(func(extra ...registry.Option) (*registry.Registry, *credentials.Store, error) {
		r, err := registry.Load(append([]registry.Option{
			registry.WithOffline(true), registry.WithoutCache(), registry.WithNoUserLayer(),
			registry.WithStateRoot(stateRoot),
			registry.WithEnv(func(string) (string, bool) { return "", false }),
			registry.WithInstances(map[string]registry.Provider{
				"openai-codex": {Base: "openai-codex", APIKey: "test"},
				// A custom-gateway row that states no reasoning verdict: it
				// resolves, but Caps.Reasoning stays nil.
				"gateway": {Base: "openai", APIKey: "test", Models: map[string]registry.Model{
					"house-model": {},
				}},
			}),
		}, extra...)...)
		return r, nil, err
	})
	if err := holder.Reload(); err != nil {
		tb.Fatalf("registry: %v", err)
	}
	return holder
}

// TestPastEntryThread_CarriesReasoningFacts proves a cold session's read
// carries the live envelope's reasoning trio — current effort from the
// persisted Config, ladder and supportsReasoning from the registry row the
// session's own ProfileID/Model resolve to — so the web footer's effort
// control renders beside the model picker without a daemon. Mirrors
// server/appwire_runtime.go's TestAppThread_CarriesReasoningInfoFromLiveSessionState
// for the daemonless read path.
func TestPastEntryThread_CarriesReasoningFacts(t *testing.T) {
	entry := hubcore.PastEntry{
		ID:       "01EFFORT",
		StateDir: t.TempDir(),
		Meta: schema.SessionMeta{
			ID:        "01EFFORT",
			ProfileID: "openai-codex",
			Model:     "gpt-6-luna",
			Config:    schema.ConfigSnapshot{ReasoningEffort: "high"},
		},
	}

	thread := requirePastEntryThread(t, hubcore.WebConfig{Registry: effortRegistry(t)}, entry, false)

	if thread.Evener.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want the persisted Config value high", thread.Evener.ReasoningEffort)
	}
	if !slices.Equal(thread.Evener.ReasoningEffortLevels, []string{"low", "medium", "high", "xhigh", "max"}) {
		t.Fatalf("ReasoningEffortLevels = %v, want gpt-6-luna's curated ladder", thread.Evener.ReasoningEffortLevels)
	}
	if !thread.Evener.SupportsReasoning {
		t.Fatal("SupportsReasoning = false, want true for gpt-6-luna")
	}
}

// TestPastEntryThread_ReasoningFactsUnspecifiedModel pins a resolved row that
// states no reasoning verdict: the live profile answers SupportsReasoning with
// !Caps.ReasoningDisabled(), so a nil verdict is supported, not unsupported. A
// cold read that answered false would hide the effort picker the live session
// offers, and the model's empty ladder is what earns the web's fallback ladder.
func TestPastEntryThread_ReasoningFactsUnspecifiedModel(t *testing.T) {
	entry := hubcore.PastEntry{
		ID:       "01EFFORT",
		StateDir: t.TempDir(),
		Meta: schema.SessionMeta{
			ID:        "01EFFORT",
			ProfileID: "gateway",
			Model:     "house-model",
			Config:    schema.ConfigSnapshot{ReasoningEffort: "high"},
		},
	}

	thread := requirePastEntryThread(t, hubcore.WebConfig{Registry: effortRegistry(t)}, entry, false)

	if len(thread.Evener.ReasoningEffortLevels) != 0 {
		t.Fatalf("ReasoningEffortLevels = %v, want none: the row names no ladder", thread.Evener.ReasoningEffortLevels)
	}
	if !thread.Evener.SupportsReasoning {
		t.Fatal("SupportsReasoning = false for an unspecified row, want true: the live profile only disables reasoning on an explicit false")
	}
}

// TestPastEntryThread_ReasoningFactsHonestWithoutRegistry pins the
// no-registry read: an unresolvable row must not fabricate a ladder or claim
// support. The current effort still reports — it is the session's own
// persisted setting, a fact of the saved state, not a registry lookup.
func TestPastEntryThread_ReasoningFactsHonestWithoutRegistry(t *testing.T) {
	entry := hubcore.PastEntry{
		ID:       "01EFFORT",
		StateDir: t.TempDir(),
		Meta: schema.SessionMeta{
			ID:     "01EFFORT",
			Config: schema.ConfigSnapshot{ReasoningEffort: "high"},
		},
	}

	thread := requirePastEntryThread(t, hubcore.WebConfig{}, entry, false)

	if thread.Evener.ReasoningEffort != "high" {
		t.Fatalf("ReasoningEffort = %q, want the persisted Config value even with no registry", thread.Evener.ReasoningEffort)
	}
	if len(thread.Evener.ReasoningEffortLevels) != 0 {
		t.Fatalf("ReasoningEffortLevels = %v, want none: no registry means no honest ladder", thread.Evener.ReasoningEffortLevels)
	}
	if thread.Evener.SupportsReasoning {
		t.Fatal("SupportsReasoning = true with no registry, want false: nothing resolves the claim")
	}
}
